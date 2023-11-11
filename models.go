package main

import (
	"encoding/csv"
	"io"
	"log"
	"os"
	"time"

	"gorm.io/gorm"
)

type User struct {
	gorm.Model
	Name        string
	Email       string
	RcRaces     []*Race `gorm:"many2many:user_rc_races;"`
	RentalRaces []*Race `gorm:"many2many:user_rental_races;"`
}

type Race struct {
	gorm.Model
	Name    string
	Date    string
	EventID *string
	RC      []*User `gorm:"many2many:user_rc_races;"`
	Renters []*User `gorm:"many2many:user_rental_races;"`
}

func (race Race) Time() time.Time {
	t, err := time.Parse(time.DateOnly, race.Date)
	if err != nil {
		panic("unable to convert date to a time object")
	}
	return t
}

func (race Race) SetTime(t time.Time) {
	race.Date = t.Format(time.DateOnly)
}

// Reads the race events input file
func readRaceEvents(file string) []*Race {
	// open file
	f, err := os.Open(file)
	if err != nil {
		return []*Race{}
	}
	defer f.Close()

	// Define the race list and loop through entries
	var races = []*Race{}
	var is_first = true

	reader := csv.NewReader(f)
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			log.Fatal(err)
		}

		if is_first {
			is_first = false
		} else {
			races = append(races, &Race{Name: rec[0], Date: rec[1]})
		}
	}

	return races
}
