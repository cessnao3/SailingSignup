package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/exp/maps"
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
	forceCalendarUpdate := flag.Bool("force", false, "forces the calendar to update")
	flag.Parse()

	initTime := time.Now()
	configFile := "config.json"
	progConfig, err := readConfig(configFile)
	if err != nil {
		if _, errf := os.Stat(configFile); errf == nil {
			log.Fatalf("Unable to read config file %v - will not override existing file %v", err, configFile)
		} else {
			ProgramConfig{LastRun: time.Now()}.writeConfig(configFile)
			log.Fatalf("Unable to read config file %v - new config file written", err)
		}
	}
	log.Printf("Last Run: %s\n", progConfig.LastRun.Format(time.DateTime))

	// Connect to the local database
	db := progConfig.openDatabase()

	// Create new race events as necessary from CSV
	createNewRaceEventsFromCSV(db, progConfig)

	// Create the Google API Context
	ctx, client := getGoogleContext(progConfig)

	// Update the forms and calendar items
	validEmailList := progConfig.getValidSheetEmails(ctx, client)

	// Create users, and ensure that the name matches the spreadsheet if provided
	for _, user := range validEmailList {
		targetUser := &User{
			Email: user.Email,
		}

		err := db.Where(targetUser).First(targetUser).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				targetUser.Name = user.Name
				db.Create(targetUser)
			} else {
				log.Fatalf("Database error: %v", err)
			}
		} else {
			targetUser.Name = user.Name
		}

		db.Save(targetUser)
	}

	for _, email := range validEmailList {
		log.Printf("Found Email %v - %v", email.Email, email.Name)
	}

	forms := []FormConfig{
		newFormConfig(progConfig.FormCodeRC, "RC", 30, -1, nil),
		newFormConfig(progConfig.FormCodeRentals, "Renters", 6, 7, &validEmailList),
	}

	updatedRaces := map[string]*Race{}

	for _, f := range forms {
		if len(f.FormCode) > 0 {
			updateGoogleForm(progConfig, f, db, ctx, client, &updatedRaces)
		}
	}

	updateGoogleCalendar(progConfig, db, ctx, client, updatedRaces, *forceCalendarUpdate)

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

func updateGoogleForm(progConfig ProgramConfig, formConfig FormConfig, db *gorm.DB, ctx context.Context, client *http.Client, updatedRaces *map[string]*Race) {
	// Create the forms service to update the form with new races
	formSrv, err := forms.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Form client: %v", err)
	}

	targetForm, err := formSrv.Forms.Get(formConfig.FormCode).Do()
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
	responses, err := formSrv.Forms.Responses.List(formConfig.FormCode).Filter(fmt.Sprintf("timestamp > %s", progConfig.LastRun.Format(time.RFC3339))).Do()
	if err != nil {
		log.Fatalf("Unable to get forms responses: %v", err)
	}

	responseItems := responses.Responses
	slices.SortFunc(responseItems, cmpResponse)

	for _, response := range responseItems {
		userEmail := response.Answers[questionMap["email"]].TextAnswers.Answers[0].Value
		userEmail = strings.ToLower(strings.TrimSpace(userEmail))

		targetUser := &User{
			Email: userEmail,
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

			err := db.Preload(formConfig.TableName).Where(targetRace).First(targetRace).Error
			if err != nil {
				if err == gorm.ErrRecordNotFound {
					log.Printf("No record found for %v", raceName)
					continue
				} else {
					log.Fatalf("Database error: %v", err)
				}
			}

			listWithoutUser := []*User{}
			userTable := formConfig.getUserTable(targetRace)
			for _, user := range *userTable {
				if user.ID != targetUser.ID {
					listWithoutUser = append(listWithoutUser, user)
				}
			}

			if formConfig.canPerformActionForUser(targetUser) {
				if action == "signup" && (formConfig.EntryLimit < 0 || len(listWithoutUser) < formConfig.EntryLimit) {
					listWithoutUser = append(listWithoutUser, targetUser)
				} else if action == "cancel" {
					db.Model(targetRace).Association(formConfig.TableName).Clear()
				} else {
					log.Fatalf("Unknown action %v", action)
				}
			}

			if updatedRaces != nil {
				if _, exists := (*updatedRaces)[raceName]; !exists {
					(*updatedRaces)[raceName] = targetRace
				}
			}

			log.Printf("%s %s for %s - %v\n", targetUser.Email, action, targetRace.Name, raceName)

			*userTable = listWithoutUser
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

	currentTime := time.Now()

	for _, race := range allRaces {
		raceTime := race.Time(progConfig.timezone())
		validRaceTime := raceTime.After(currentTime)

		if formConfig.ShowEntryTimeLimit != nil && validRaceTime {
			validRaceTime = currentTime.After(raceTime.Add(-*formConfig.ShowEntryTimeLimit))
		}

		if validRaceTime {
			entryName := fmt.Sprintf("%s - %s", race.Name, race.Date)
			userList := *formConfig.getUserTable(race)

			if formConfig.EntryLimit >= 0 {
				entryName = fmt.Sprintf("%s (%v Remaining)", entryName, formConfig.EntryLimit-len(userList))
			} else {
				entryName = fmt.Sprintf("%s (%v So Far)", entryName, len(userList))
			}

			newOptions = append(newOptions, &forms.Option{
				Value: entryName,
			})
		}
	}

	if len(newOptions) == 0 {
		newOptions = append(newOptions, &forms.Option{Value: "No Races Available"})
	}

	raceItem.Item.QuestionItem.Question.ChoiceQuestion.Options = newOptions

	_, err = formSrv.Forms.BatchUpdate(formConfig.FormCode, &forms.BatchUpdateFormRequest{
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

	log.Printf("Updated Races on Form for %v: %v\n", formConfig.TableName, targetForm.Info.Title)

	if err != nil {
		log.Fatalf("Unable to update form: %v", err)
	}
}

func updateGoogleCalendar(progConfig ProgramConfig, db *gorm.DB, ctx context.Context, client *http.Client, updatedRaces map[string]*Race, forceCalendarUpdate bool) {
	// Create the calendar service to update new calendar events
	calSrv, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Calendar client: %v", err)
	}

	allRaces := getAllRaces(db)

	for _, race := range allRaces {
		if _, raceExists := updatedRaces[race.Name]; (!raceExists && race.EventID != nil) && !forceCalendarUpdate {
			continue
		}

		eventTime := race.Time(progConfig.timezone())
		eventTime = eventTime.Add(time.Duration(progConfig.eventStartOffset()))

		cdrStart := calendar.EventDateTime{DateTime: eventTime.Format(time.RFC3339)}
		cdrEnd := calendar.EventDateTime{DateTime: eventTime.Add(progConfig.eventDuration()).Format(time.RFC3339)}

		descriptionTextRC := "No RC"

		attendees := map[string]*calendar.EventAttendee{}
		for i, rcUser := range race.RC {
			newUser := calendar.EventAttendee{
				Email:       rcUser.Email,
				DisplayName: rcUser.Name,
			}

			attendees[newUser.Email] = &newUser

			if i == 0 {
				descriptionTextRC = fmt.Sprintf("RC: %v", rcUser.Name)
			} else {
				descriptionTextRC = fmt.Sprintf("%v, %v", descriptionTextRC, rcUser.Name)
			}
		}

		descriptionTextRental := "No Renters"
		for i, rentalUser := range race.Renters {
			newUser := calendar.EventAttendee{
				Email:       rentalUser.Email,
				DisplayName: rentalUser.Name,
			}

			attendees[newUser.Email] = &newUser

			if i == 0 {
				descriptionTextRental = fmt.Sprintf("Renters: %v", rentalUser.Name)
			} else {
				descriptionTextRental = fmt.Sprintf("%v, %v", descriptionTextRental, rentalUser.Name)
			}
		}

		descriptionText := fmt.Sprintf("%v\n%v\nRentals Remaining: %v", descriptionTextRC, descriptionTextRental, progConfig.AllowedRenters-len(race.Renters))

		if race.EventID != nil {
			exisitngEvent, err := calSrv.Events.Get(progConfig.CalendarCode, *race.EventID).Do()
			if err != nil {
				log.Fatalf("Unable to get existing event: %v", err)
			}

			exisitngEvent.Start = &cdrStart
			exisitngEvent.End = &cdrEnd
			exisitngEvent.Summary = race.Name
			exisitngEvent.Description = descriptionText
			exisitngEvent.Attendees = maps.Values(attendees)

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
				Attendees:   maps.Values(attendees),
				Description: descriptionTextRC,
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
