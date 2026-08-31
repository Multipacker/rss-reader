package website

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:sql
var sqlFiles embed.FS

type SortOrder int
const (
	SortOrderNewestFirst SortOrder = iota
	SortOrderOldestFirst
)

type Storage struct {
	db *pgxpool.Pool
}



func createStorage() (*Storage, error) {
	storage := new(Storage)

	// NOTE(simon): Open database connection
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}
	storage.db = pool

	init, err := fs.ReadFile(sqlFiles, "sql/init.sql")
	if err != nil {
		return nil, fmt.Errorf("fs read file: %w", err)
	}

	_, err = storage.db.Exec(context.Background(), string(init))
	if err != nil {
		return nil, fmt.Errorf("storage db exec: %w", err)
	}

	return storage, nil
}

func (storage *Storage) storeFeed(context context.Context, feed feedparse.Feed, entries []feedparse.Entry) error {
	return storage.storeFeedSnapshot(context, time.Time{}, feed, entries)
}

func (storage *Storage) storeFeedSnapshot(context context.Context, snapshotTime time.Time, feed feedparse.Feed, entries []feedparse.Entry) error {
	transaction, err := storage.db.Begin(context)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer transaction.Rollback(context)

	batch := pgx.Batch{}
	if !snapshotTime.IsZero() {
		batch.Queue(
			"DELETE FROM UnfetchedSnapshots WHERE url = @url AND timestamp = @timestamp",
			pgx.NamedArgs{
				"url":       feed.Link,
				"timestamp": snapshotTime,
			},
		)
	}

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



type FeedDescription struct {
	Title string
	Description string
	Link string

	HighlightTitle HighlightString
}

func (storage *Storage) QueryFeeds(context context.Context, query string, offset int, size int) ([]FeedDescription, error) {
	queryWords := strings.Fields(strings.ToLower(query))

	feeds, err := storage.Feeds(context)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feeds: %w", err)
	}

	// NOTE(simon): Collect feeds to descriptions.
	descriptions := []FeedDescription{}
	for _, feed := range feeds {
		description := FeedDescription{
			Title: feed.Title,
			Description: feed.Description,
			Link: feed.Link,
		}

		descriptions = append(descriptions, description)
	}

	for i, description := range descriptions {
		description.HighlightTitle = highlightFromValueQuery(description.Title, queryWords)
		descriptions[i] = description
	}

	// NOTE(simon): Filter results.
	filterOffset := 0
	for _, description := range descriptions {
		titleMatches := 0
		for _, match := range description.HighlightTitle {
			if match.Highlight {
				titleMatches++
			}
		}

		if titleMatches >= len(queryWords) {
			descriptions[filterOffset] = description
			filterOffset++
		}
	}
	descriptions = descriptions[:filterOffset]

	// NOTE(simon): Sort the result.
	slices.SortFunc(descriptions, func(a, b FeedDescription) int {
		result := 0

		if result == 0 {
			result = len(b.HighlightTitle) - len(a.HighlightTitle)
		}

		if result == 0 {
			result = strings.Compare(a.Title, b.Title)
		}

		return result
	})

	// NOTE(simon): Limit to query range.
	descriptions = descriptions[offset:min(offset + size, len(descriptions))]

	return descriptions, nil
}

func (storage *Storage) Feeds(context context.Context) ([]feedparse.Feed, error) {
	feeds, err := db.Query[feedparse.Feed](context, storage.db, "SELECT (id, title, description, url as link, updated) FROM Feeds ORDER BY title")
	if err != nil {
		return nil, fmt.Errorf("failed to query feeds: %w", err)
	}

	return feeds, nil
}



type EntryDescription struct {
	Title string
	Feed  string
	Link  string
	Id    string
	Published time.Time

	HighlightTitle HighlightString
	HighlightFeed  HighlightString
}

func (storage *Storage) QueryEntries(context context.Context, query string, sortOrder SortOrder, offset int, size int) ([]EntryDescription, error) {
	queryWords := strings.Fields(strings.ToLower(query))

	// NOTE(simon): Collect entries to descriptions.
	entryQuery := `
		SELECT
			Entries.title     AS title,
			Feeds.title       AS feed,
			Entries.url       As link,
			Entries.id        AS id,
			Entries.published AS published
		FROM
			Entries JOIN Feeds ON feed = Feeds.id
		ORDER BY published
	`
	descriptions, err := db.QueryLax[EntryDescription](context, storage.db, entryQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query entries: %w", err)
	}

	for i, description := range descriptions {
		description.HighlightTitle = highlightFromValueQuery(description.Title, queryWords)
		description.HighlightFeed  = highlightFromValueQuery(description.Feed,  queryWords)
		descriptions[i] = description
	}

	// NOTE(simon): Filter results.
	filterOffset := 0
	for _, description := range descriptions {
		titleMatches := 0
		for _, match := range description.HighlightTitle {
			if match.Highlight {
				titleMatches++
			}
		}

		feedMatches := 0
		for _, match := range description.HighlightFeed {
			if match.Highlight {
				feedMatches++
			}
		}

		if titleMatches >= len(queryWords) || feedMatches >= len(queryWords) {
			descriptions[filterOffset] = description
			filterOffset++
		}
	}
	descriptions = descriptions[:filterOffset]

	// NOTE(simon): Sort the result.
	slices.SortFunc(descriptions, func(a, b EntryDescription) int {
		result := 0

		if result == 0 {
			result = len(b.HighlightTitle) - len(a.HighlightTitle)
		}

		if result == 0 {
			result = len(b.HighlightFeed) - len(a.HighlightFeed)
		}

		if result == 0 {
			switch sortOrder {
			case SortOrderNewestFirst:
				result = b.Published.Compare(a.Published)
			case SortOrderOldestFirst:
				result = a.Published.Compare(b.Published)
			}
		}

		if result == 0 {
			result = strings.Compare(a.Title, b.Title)
		}

		if result == 0 {
			result = strings.Compare(a.Feed, b.Feed)
		}

		return result
	})

	// NOTE(simon): Limit to query range.
	descriptions = descriptions[offset:min(offset + size, len(descriptions))]

	return descriptions, nil
}

func (storage *Storage) Entries(context context.Context) ([]feedparse.Entry, error) {
	entries, err := db.Query[feedparse.Entry](context, storage.db, "SELECT id, feed, title, url AS link, updated, published FROM Entries")
	if err != nil {
		return nil, fmt.Errorf("failed to query entries: %w", err)
	}

	return entries, nil
}



func (storage *Storage) addSnapshots(context context.Context, url string, timestamp time.Time, snapshots []time.Time) error {
	transaction, err := storage.db.Begin(context)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer transaction.Rollback(context)

	_, err = transaction.Exec(
		context,
		`INSERT INTO SnapshotTimes VALUES (@url, @timestamp) ON CONFLICT (url) DO UPDATE SET updated = @timestamp`,
		pgx.NamedArgs{
			"url":       url,
			"timestamp": timestamp,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to update snapshot time: %w", err)
	}

	batch := pgx.Batch{}
	for _, snapshot := range snapshots {
		batch.Queue(
			"INSERT INTO UnfetchedSnapshots VALUES (@url, @timestamp) ON CONFLICT DO NOTHING",
			pgx.NamedArgs{
				"url":       url,
				"timestamp": snapshot,
			},
		)
	}

	err = transaction.SendBatch(context, &batch).Close()
	if err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}

	err = transaction.Commit(context)
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (storage *Storage) getLatestSnapshotTime(context context.Context, url string) (time.Time, error) {
	snapshot, err := db.QueryOne[models.SnapshotTime](
		context,
		storage.db,
		"SELECT url, updated FROM SnapshotTimes WHERE url = $1",
		url,
	)

	// NOTE(simon): No rows, return zero time.
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}

	if err != nil {
		return time.Time{}, fmt.Errorf("failed to query latest snapshot time: %w\n", err)
	}

	return snapshot.Updated, nil
}

func (storage *Storage) getLatestSnapshot(context context.Context) (models.FeedSnapshot, error) {
	snapshot, err := db.QueryOne[models.FeedSnapshot](
		context,
		storage.db,
		"SELECT url, timestamp FROM UnfetchedSnapshots ORDER BY timestamp DESC LIMIT 1",
	)

	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to query latest snapshot: %w\n", err)
	}

	return snapshot, nil
}
