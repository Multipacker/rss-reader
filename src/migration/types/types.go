package types

import (
	"context"
	"time"

	"Multipacker/rss-reader/src/db"
)

type MigrationVersion time.Time

type Migration interface {
	Version() MigrationVersion
	Name() string
	Description() string
	Up(context context.Context, dbConnection db.Database) error
	Down(context context.Context, dbConnection db.Database) error
}

func (version MigrationVersion) String() string {
	return time.Time(version).Format(time.RFC3339)
}

func (version MigrationVersion) IsZero() bool {
	return time.Time(version).IsZero()
}

func (version MigrationVersion) After(other MigrationVersion) bool {
	return time.Time(version).After(time.Time(other))
}

func (version MigrationVersion) Before(other MigrationVersion) bool {
	return time.Time(version).Before(time.Time(other))
}

func (version MigrationVersion) Equal(other MigrationVersion) bool {
	return time.Time(version).Equal(time.Time(other))
}
