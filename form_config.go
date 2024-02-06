package main

import (
	"log"
	"reflect"
	"strings"
	"time"
)

type FormConfig struct {
	FormCode           string
	TableName          string
	ShowEntryTimeLimit *time.Duration
	ValidUserList      *[]UserEntry
	EntryLimit         int
}

func newFormConfig(form string, tableName string) FormConfig {
	return FormConfig{form, tableName, nil, nil, -1}
}

func (config FormConfig) withEntryLimit(entryLimit int) FormConfig {
	config.EntryLimit = entryLimit
	return config
}

func (config FormConfig) withLookupDays(lookupDays int) FormConfig {
	if lookupDays > 0 {
		limit := new(time.Duration)
		*limit = time.Duration(24 * float64(time.Hour) * float64(lookupDays))
		config.ShowEntryTimeLimit = limit
	} else {
		config.ShowEntryTimeLimit = nil
	}

	return config
}

func (config FormConfig) withValidUserList(userList *[]UserEntry) FormConfig {
	config.ValidUserList = userList
	return config
}

func (config FormConfig) getUserTable(race *Race) *[]*User {
	intf := reflect.Indirect(reflect.ValueOf(race))

	val := intf.FieldByName(config.TableName)
	if val.Type() != reflect.TypeOf([]*User{}) {
		log.Fatalf("field '%v' not found with valid type - %v", config.TableName, val)
	}

	return val.Addr().Interface().(*[]*User)
}

func (config FormConfig) canPerformActionForUser(user *User) bool {
	if config.ValidUserList == nil {
		return true
	}

	for _, validUser := range *config.ValidUserList {
		if strings.ToLower(user.Email) == strings.ToLower(validUser.Email) {
			return true
		}
	}

	return false
}
