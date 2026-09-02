package feeds

import (
	"fmt"
	"context"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"

	"github.com/jackc/pgx/v5"
)

func StoreFeed(context context.Context, dbConnection db.Database, feed feedparse.Feed, entries []feedparse.Entry) error {
	transaction, err := dbConnection.Begin(context)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer transaction.Rollback(context)

	batch := pgx.Batch{}
	batch.Queue(
		`INSERT INTO Feeds (externalId, title, description, url, updated) VALUES (@id, @title, @description, @url, @updated)
		ON CONFLICT (externalId) DO UPDATE SET title = @title, description = @description, url = @url, updated = @updated WHERE Feeds.updated < @updated`,
		pgx.NamedArgs{
			"id":          feed.Id,
			"title":       feed.Title,
			"description": feed.Description,
			"url":         feed.Link,
			"updated":     feed.Updated,
		},
	)

	for _, entry := range entries {
		batch.Queue(
			`INSERT INTO Entries (feed, externalId, title, url, published, updated) VALUES ((SELECT id FROM Feeds WHERE externalId = @feed), @id, @title, @url, @published, @updated)
			ON CONFLICT (externalId) DO UPDATE SET title = @title, url = @url, updated = @updated WHERE Entries.updated < @updated`,
			pgx.NamedArgs{
				"id":          entry.Id,
				"feed":        feed.Id,
				"title":       entry.Title,
				"url":         entry.Link,
				"published":   entry.Published,
				"updated":     entry.Updated,
			},
		)
	}

	err = transaction.SendBatch(context, &batch).Close()
	if err != nil {
		return fmt.Errorf("failed to send batch: %v", err)
	}

	err = transaction.Commit(context)
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}
