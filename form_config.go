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
	EmailList          *[]string
}

func newFormConfig(form string, tableName string) FormConfig {
	return FormConfig{form, tableName, nil, nil}
}

func (config *FormConfig) setDurationDays(days int) {
	if days > 0 {
		limit := new(time.Duration)
		*limit = time.Duration(24 * float64(time.Hour) * float64(days))
		config.ShowEntryTimeLimit = limit
	} else {
		config.ShowEntryTimeLimit = nil
	}
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
	if config.EmailList == nil {
		return true
	}

	for _, email := range *config.EmailList {
		if strings.ToLower(user.Email) == strings.ToLower(email) {
			return true
		}
	}

	return false
}
