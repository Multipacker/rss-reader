package models

import (
	"time"
)

type FeedSnapshot struct {
	Url       string
	Timestamp time.Time
}

type SnapshotTime struct {
	Url     string
	Updated time.Time
}
