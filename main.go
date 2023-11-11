package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/forms/v1"
	"google.golang.org/api/option"
	"gorm.io/gorm"
)

type RaceItem struct {
	Item  *forms.Item
	Index int64
}

func main() {
	initTime := time.Now()
	configFile := "config.json"
	progConfig, err := readConfig(configFile)
	if err != nil {
		ProgramConfig{LastRun: time.Now()}.writeConfig(configFile)
		log.Fatalf("Unable to read config file - new config file written")
	}
	log.Printf("Last Run: %s\n", progConfig.LastRun.Format(time.DateTime))

	// Connect to the local database
	db := progConfig.openDatabase()

	// Create new race events as necessary from CSV
	createNewRaceEventsFromCSV(db, progConfig)

	// Create the Google API Context
	ctx, client := getGoogleContext(progConfig)

	// Update the forms and calendar items
	updateGoogleForm(progConfig, db, ctx, client)
	updateGoogleCalendar(progConfig, db, ctx, client)

	// Update the program time and save the resulting config file
	progConfig.LastRun = initTime
	progConfig.writeConfig(configFile)
}

func cmpResponse(a, b *forms.FormResponse) int {
	if a.CreateTime > b.CreateTime {
		return 1
	} else if a.CreateTime < b.CreateTime {
		return -1
	} else {
		return 0
	}
}

func createNewRaceEventsFromCSV(db *gorm.DB, config ProgramConfig) {
	var races = readRaceEvents(config.racesFile())
	for _, r := range races {
		var testRace Race
		err := db.Where(&Race{Name: r.Name, Date: r.Date}).First(&testRace).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				log.Printf("Record not found for %s\n", r.Name)
				db.Create(r)
			} else {
				log.Fatalf("Database error: %v", err)
			}
		}
	}
}

func getAllRaces(db *gorm.DB) []*Race {
	// Get all the races
	allRaces := []*Race{}
	result := db.Preload("RC").Preload("Renters").Find(&allRaces)
	if result.Error != nil {
		log.Fatalf("Error getting database races: %v", result.Error)
	}
	return allRaces
}

func updateGoogleForm(progConfig ProgramConfig, db *gorm.DB, ctx context.Context, client *http.Client) {
	// Create the forms service to update the form with new races
	formSrv, err := forms.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Form client: %v", err)
	}

	targetForm, err := formSrv.Forms.Get(progConfig.FormCode).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve Form client: %v", err)
	}

	var raceItem *RaceItem = nil
	questionMap := make(map[string]string)
	for i, itm := range targetForm.Items {
		questionMap[strings.ToLower(itm.Title)] = itm.QuestionItem.Question.QuestionId
		if itm.Title == "Race Dates" {
			raceItem = &RaceItem{
				Item:  itm,
				Index: int64(i),
			}
		}
	}

	// Get form responses and link user values
	responses, err := formSrv.Forms.Responses.List(progConfig.FormCode).Filter(fmt.Sprintf("timestamp > %s", progConfig.LastRun.Format(time.RFC3339))).Do()
	if err != nil {
		log.Fatalf("Unable to get forms responses: %v", err)
	}

	responseItems := responses.Responses
	slices.SortFunc(responseItems, cmpResponse)

	for _, response := range responseItems {
		targetUser := &User{
			Email: response.Answers[questionMap["email"]].TextAnswers.Answers[0].Value,
		}
		err := db.Where(targetUser).First(targetUser).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				db.Create(targetUser)
			} else {
				log.Fatalf("Database error: %v", err)
			}
		}

		targetUser.Name = response.Answers[questionMap["name"]].TextAnswers.Answers[0].Value
		action := strings.ToLower(response.Answers[questionMap["action"]].TextAnswers.Answers[0].Value)

		for _, raceQuestionText := range response.Answers[raceItem.Item.QuestionItem.Question.QuestionId].TextAnswers.Answers {
			raceName := strings.TrimSpace(strings.Split(raceQuestionText.Value, "-")[0])

			targetRace := &Race{
				Name: raceName,
			}

			err := db.Preload("RC").Where(targetRace).First(targetRace).Error
			if err != nil {
				log.Fatalf("Database error: %v", err)
			}

			listWithoutUser := []*User{}
			for _, rc := range targetRace.RC {
				if rc.ID != targetUser.ID {
					listWithoutUser = append(listWithoutUser, rc)
				}
			}

			if action == "signup" {
				listWithoutUser = append(listWithoutUser, targetUser)
			} else if action == "cancel" {
				db.Model(targetRace).Association("RC").Clear()
			} else {
				log.Fatalf("Unknown action %v", action)
			}

			log.Printf("%s %s for %s - %v\n", targetUser.Email, action, targetRace.Name, raceName)

			targetRace.RC = listWithoutUser
			db.Save(targetRace)
		}

		db.Save(&targetUser)
	}

	// Get all the races
	allRaces := getAllRaces(db)

	// Update the form items
	if raceItem == nil {
		log.Fatalf("Unable to find race dates item in form")
	}

	newOptions := []*forms.Option{}

	for _, race := range allRaces {
		if race.Time().After(time.Now()) {
			newOptions = append(newOptions, &forms.Option{
				Value: fmt.Sprintf("%s - %s", race.Name, race.Date),
			})
		}
	}

	raceItem.Item.QuestionItem.Question.ChoiceQuestion.Options = newOptions

	resp, err := formSrv.Forms.BatchUpdate(progConfig.FormCode, &forms.BatchUpdateFormRequest{
		IncludeFormInResponse: false,
		Requests: []*forms.Request{
			{
				UpdateItem: &forms.UpdateItemRequest{
					Item:       raceItem.Item,
					UpdateMask: "questionItem",
					Location:   &forms.Location{Index: raceItem.Index},
				},
			},
		},
	}).Do()

	log.Printf("Updated Races on Form: %v\n", targetForm.Info.Title)

	if err != nil {
		log.Fatalf("Unable to update form: %v", err)
	} else {
		targetForm = resp.Form
	}
}

func updateGoogleCalendar(progConfig ProgramConfig, db *gorm.DB, ctx context.Context, client *http.Client) {
	// Create the calendar service to update new calendar events
	calSrv, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Calendar client: %v", err)
	}

	allRaces := getAllRaces(db)

	for _, race := range allRaces {
		eventTime := race.Time()
		eventTime = eventTime.Add(time.Duration(progConfig.eventStartOffset()))

		cdrStart := calendar.EventDateTime{DateTime: eventTime.Format(time.RFC3339)}
		cdrEnd := calendar.EventDateTime{DateTime: eventTime.Add(progConfig.eventDuration()).Format(time.RFC3339)}

		descriptionText := "No RC"

		attendees := []*calendar.EventAttendee{}
		for i, rcUser := range race.RC {
			newUser := calendar.EventAttendee{
				Email:       rcUser.Email,
				DisplayName: rcUser.Name,
			}

			attendees = append(attendees, &newUser)

			if i == 0 {
				descriptionText = fmt.Sprintf("RC: %v", rcUser.Name)
			} else {
				descriptionText = fmt.Sprintf("%v, %v", descriptionText, rcUser.Name)
			}
		}

		if race.EventID != nil {
			exisitngEvent, err := calSrv.Events.Get(progConfig.CalendarCode, *race.EventID).Do()
			if err != nil {
				log.Fatalf("Unable to get existing event: %v", err)
			}

			exisitngEvent.Start = &cdrStart
			exisitngEvent.End = &cdrEnd
			exisitngEvent.Summary = race.Name
			exisitngEvent.Description = descriptionText
			exisitngEvent.Attendees = attendees

			_, err = calSrv.Events.Update(progConfig.CalendarCode, *race.EventID, exisitngEvent).Do()
			if err != nil {
				log.Fatalf("Error updating event %v: %v", exisitngEvent.Id, err)
			} else {
				log.Printf("Updated event %v\n", race.Name)
			}
		} else {
			newEvent := calendar.Event{
				Start:       &cdrStart,
				End:         &cdrEnd,
				Summary:     race.Name,
				Attendees:   attendees,
				Description: descriptionText,
			}
			eventResult, err := calSrv.Events.Insert(progConfig.CalendarCode, &newEvent).Do()
			if err != nil {
				log.Fatalf("Unable to add calendar event: %v", err)
			}

			log.Printf("Added calendar event for %v with id %v\n", race.Name, eventResult.Id)

			race.EventID = &eventResult.Id
			db.Save(&race)
		}
	}
}
