package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"golang.org/x/exp/maps"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
	"gorm.io/gorm"
)

type ProgramConfig struct {
	LastRun              time.Time
	DataFolder           string
	FormRC               ProgramConfigForm
	FormRentals          ProgramConfigForm
	CalendarCode         string
	RaceEventDuration    int
	RaceEventStartOffset int
	TimeZoneString       string
	AllowedRentersCount  int
	AllowedUsersSheetID  string
	RaceLocation         string
	RentalMembershipYear int
}

func (config ProgramConfig) eventDuration() time.Duration {
	dur, err := time.ParseDuration(fmt.Sprintf("%dh", config.RaceEventDuration))
	if err != nil {
		log.Fatalf("Unable to get duration: %v", err)
	}
	return dur
}

func (config ProgramConfig) timezone() *time.Location {
	tz, err := time.LoadLocation(config.TimeZoneString)
	if err != nil {
		log.Fatalf("Unable to get time zone: %v", err)
	}
	return tz
}

func (config ProgramConfig) eventStartOffset() time.Duration {
	dur, err := time.ParseDuration(fmt.Sprintf("%dh", config.RaceEventStartOffset))
	if err != nil {
		log.Fatalf("Unable to get duration: %v", err)
	}
	return dur
}

func (config ProgramConfig) dbFile() string {
	return path.Join(config.DataFolder, "db.sqlite")
}

func (config ProgramConfig) racesFile() string {
	return path.Join(config.DataFolder, "races.csv")
}

func (config ProgramConfig) credFile() string {
	return path.Join(config.DataFolder, "credentials.json")
}

func (config ProgramConfig) tokenFile() string {
	return path.Join(config.DataFolder, "token.json")
}

func (config ProgramConfig) openDatabase() *gorm.DB {
	// Connect to the local database
	db, err := gorm.Open(sqlite.Open(config.dbFile()), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect database: %v", err)
	}

	db.AutoMigrate(&User{})
	db.AutoMigrate(&Race{})

	return db
}

func readConfig(file string) (ProgramConfig, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return ProgramConfig{}, err
	}

	var config ProgramConfig
	err = json.Unmarshal(content, &config)
	if err != nil {
		return ProgramConfig{}, err
	}

	return config, nil
}

func (config ProgramConfig) writeConfig(file string) {
	data, err := json.Marshal(&config)
	if err != nil {
		log.Fatalf("Error during Marshal(): %v", err)
	}

	err = os.WriteFile(file, data, 0644)
	if err != nil {
		log.Fatalf("Error during WriteFile(): %v", err)
	}
}

type UserEntry struct {
	Email string
	Name  string
}

func (config ProgramConfig) getValidSheetEmails(ctx context.Context, client *http.Client) []UserEntry {
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	readRange := "A:C"
	resp, err := srv.Spreadsheets.Values.Get(config.AllowedUsersSheetID, readRange).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from sheet: %v", err)
	}

	users := map[string]UserEntry{}

	for _, row := range resp.Values {
		emails := strings.ToLower(strings.TrimSpace(row[0].(string)))
		name := strings.TrimSpace(row[1].(string))
		membership_year, err := strconv.Atoi(strings.TrimSpace(row[2].(string)))
		if err != nil {
			log.Fatalf("Unable to convert membership year for %v", row)
		}

		if membership_year < config.RentalMembershipYear {
			log.Printf("Skipping %v due to membership year %v < %v", name, membership_year, config.RentalMembershipYear)
			continue
		}

		for _, email := range strings.Split(emails, ";") {
			if len(email) == 0 || len(name) == 0 {
				log.Printf("User field empty for email '%v', '%v'", email, name)
				continue
			} else if _, exists := users[email]; !exists {
				users[email] = UserEntry{email, name}
			} else {
				log.Printf("Duplicate entry for email '%v' detected as '%v'", email, name)
			}
		}
	}

	return maps.Values(users)
}

type ProgramConfigForm struct {
	FormCode      string
	TableName     string
	PrelookupDays int
	EntryLimit    int
}

func (form ProgramConfigForm) toFormConfig(users *[]UserEntry) FormConfig {
	return newFormConfig(form.FormCode, form.TableName).withLookupDays(form.PrelookupDays).withEntryLimit(form.EntryLimit).withValidUserList(users)
}
