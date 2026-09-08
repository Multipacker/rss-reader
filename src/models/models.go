package models

import (
	"time"
)

type FeedSnapshot struct {
	Id        string
	Url       string
	Timestamp time.Time
}

type SnapshotTime struct {
	Url     string
	Updated time.Time
}

type Feed struct {
	Id           string
	ExternalId   string
	Title        string
	Description  string
	FeedUrl      string
	Url          string
	Updated      time.Time
	SnapshotTime time.Time
	DaysToKeep   int
}
