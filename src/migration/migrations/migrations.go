package migrations

import (
	"Multipacker/rss-reader/src/migration/types"
)

var All = make(map[types.MigrationVersion]types.Migration)

func registerMigration(migration types.Migration) {
	All[migration.Version()] = migration
}
