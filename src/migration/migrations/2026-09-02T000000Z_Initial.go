package migrations

import (
	"context"
	"errors"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/migration/types"
)

func init() {
	registerMigration(Initial{})
}

type Initial struct{}

func (migration Initial) Version() types.MigrationVersion {
	return types.MigrationVersion(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
}

func (migration Initial) Name() string {
	return "Initial"
}

func (migration Initial) Description() string {
	return "Creates the inital database"
}

func (migration Initial) Up(context context.Context, dbConnection db.Database) error {
	_, err := dbConnection.Exec(context, `
		CREATE TABLE Feeds (
			id           UUID      NOT NULL PRIMARY KEY DEFAULT uuidv7(),
			externalId   TEXT      NOT NULL UNIQUE,
			title        TEXT      NOT NULL,
			description  TEXT      NOT NULL,
			url          TEXT      NOT NULL UNIQUE,
			updated      TIMESTAMP NOT NULL
		);

		CREATE TABLE Entries (
			id          UUID      NOT NULL PRIMARY KEY DEFAULT uuidv7(),
			feed        UUID      NOT NULL REFERENCES Feeds(id),
			externalId  TEXT      NOT NULL UNIQUE,
			title       TEXT      NOT NULL,
			url         TEXT      NOT NULL,
			published   TIMESTAMP NOT NULL,
			updated     TIMESTAMP NOT NULL
		);

		CREATE TABLE SnapshotTimes (
			url     TEXT        NOT NULL PRIMARY KEY,
			updated TIMESTAMPTZ NOT NULL
		);

		CREATE TABLE UnfetchedSnapshots (
			url       TEXT      NOT NULL REFERENCES SnapshotTimes(url),
			timestamp TIMESTAMP NOT NULL,
			PRIMARY KEY (url, timestamp)
		);
	`)
	if err != nil {
		return err
	}

	return nil
}

func (migration Initial) Down(context context.Context, dbConnection db.Database) error {
	return errors.New("cannot undo the initial migration")
}
