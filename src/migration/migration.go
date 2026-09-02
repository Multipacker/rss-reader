package migration

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/migration/migrations"
	"Multipacker/rss-reader/src/migration/types"

	"github.com/jackc/pgx/v5/pgxpool"
)

func getSortedMigrationVersions() []types.MigrationVersion {
	var allVersions []types.MigrationVersion
	for version, _ := range migrations.All {
		allVersions = append(allVersions, version)
	}
	sort.Slice(allVersions, func(i, j int) bool {
		return allVersions[i].Before(allVersions[j])
	})
	return allVersions
}

func getCurrentVersion(context context.Context, dbConnection db.Database) (types.MigrationVersion, error) {
	var currentVersion time.Time
	err := dbConnection.QueryRow(context, "SELECT version FROM Migration").Scan(&currentVersion)
	if err != nil {
		return types.MigrationVersion{}, err
	}

	return types.MigrationVersion(currentVersion), nil
}

func LatestVersion() types.MigrationVersion {
	allVersions := getSortedMigrationVersions()
	return allVersions[len(allVersions) - 1]
}

func Migrate(dbConnection *pgxpool.Pool, targetVersion types.MigrationVersion) error {
	context := context.Background()
	
	// NOTE(simon): Create the migration table.
	_, err := dbConnection.Exec(context, `
		CREATE TABLE IF NOT EXISTS Migration (
			version TIMESTAMP NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migration table: %w", err)
	}

	// NOTE(simon): Ensure there is one row in it
	var rowCount int
	err = dbConnection.QueryRow(context, "SELECT COUNT(*) FROM Migration").Scan(&rowCount)
	if err != nil {
		return err
	}
	if rowCount < 1 {
		_, err = dbConnection.Exec(context, "INSERT INTO Migration (version) VALUES ($1)", time.Time{})
		if err != nil {
			return fmt.Errorf("failed to insert initial migration row: %w", err)
		}
	}

	currentVersion, err := getCurrentVersion(context, dbConnection)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}
	if currentVersion.IsZero() {
		log.Println("This is the first time you have run database migrations.")
	} else {
		log.Printf("Current version: %v\n", currentVersion.String())
	}

	allVersions := getSortedMigrationVersions()
	if targetVersion.IsZero() {
		targetVersion = LatestVersion()
	}

	currentIndex := -1
	targetIndex  := -1
	for i, version := range allVersions {
		if currentVersion.Equal(version) {
			currentIndex = i
		}
		if targetVersion.Equal(version) {
			targetIndex = i
		}
	}

	if targetIndex < 0 {
		log.Printf("Could not find migration with version %v\n", targetVersion)
		return nil
	}

	if currentIndex < targetIndex {
		for i := currentIndex + 1; i <= targetIndex; i++ {
			version   := allVersions[i]
			migration := migrations.All[version]
			log.Printf("Applying migration %v (%v)\n", version, migration.Name())

			transaction, err := dbConnection.Begin(context)
			if err != nil {
				return fmt.Errorf("failed to start transaction: %w", err)
			}
			defer transaction.Rollback(context)

			err = migration.Up(context, transaction)
			if err != nil {
				return fmt.Errorf("failed to apply migration %v: %w\n", version, err)
			}

			_, err = transaction.Exec(context, "UPDATE Migration SET version = $1", version)
			if err != nil {
				return fmt.Errorf("failed to update version in migration table: %w\n", err)
			}

			err = transaction.Commit(context)
			if err != nil {
				return fmt.Errorf("failed to commit transaction: %w\n", err)
			}
		}
	} else if targetIndex < currentIndex {
		for i := currentIndex - 1; i >= targetIndex; i-- {
			version   := allVersions[i]
			migration := migrations.All[version]
			log.Printf("Applying migration %v (%v)\n", version, migration.Name())

			transaction, err := dbConnection.Begin(context)
			if err != nil {
				return fmt.Errorf("failed to start transaction: %w", err)
			}
			defer transaction.Rollback(context)

			err = migration.Down(context, transaction)
			if err != nil {
				return fmt.Errorf("failed to apply migration %v: %w\n", version, err)
			}

			_, err = transaction.Exec(context, "UPDATE Migration SET version = $1", version)
			if err != nil {
				return fmt.Errorf("failed to update version in migration table: %w\n", err)
			}

			err = transaction.Commit(context)
			if err != nil {
				return fmt.Errorf("failed to commit transaction: %w\n", err)
			}
		}
	} else {
		log.Println("Already migrated; nothing to do.")
	}

	return nil
}
