package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type ProgramConfig struct {
	LastRun              time.Time
	DataFolder           string
	FormCodeRC           string
	FormCodeRentals      string
	CalendarCode         string
	RaceEventDuration    int
	RaceEventStartOffset int
	TimeZoneString       string
	AllowedRenters       int
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
	return path.Join(config.DataFolder, "race_signup.sqlite")
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

func (config ProgramConfig) emailFile() string {
	return path.Join(config.DataFolder, "member_emails.txt")
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

func (config ProgramConfig) getValidEmails() []string {
	if _, err := os.Stat(config.emailFile()); err != nil {
		log.Printf("No email file provided at %v", config.emailFile())
		os.WriteFile(config.emailFile(), []byte{}, 0644)
		return []string{}
	}

	data, err := os.ReadFile(config.emailFile())
	if err != nil {
		log.Fatalf("Unable to read member file %v - %v", config.emailFile(), err)
	}
	text := string(data)
	lines := strings.Split(text, "\n")

	emails := []string{}

	for _, l := range lines {
		l = strings.TrimSpace(l)
		emails = append(emails, l)
	}

	return emails
}
