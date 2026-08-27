package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"Multipacker/rss-reader/internal/feedparse"
	"Multipacker/rss-reader/internal/wayback"

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
	path string
	snapshots []wayback.Snapshot
	snapshotPoints map[string]time.Time
	snapshotLock sync.Mutex
	db *pgxpool.Pool
}

func createStorage(path string) (*Storage, error) {
	// NOTE(simon): Ensure that the output directory exists.
	if path != "" {
		err := os.MkdirAll(path, 0755)
		if err != nil {
			return nil, err
		}
	}

	storage := new(Storage)
	storage.path = path
	storage.snapshotPoints = make(map[string]time.Time)

	// NOTE(simon): Load old snapshots.
	snapshotsContent, err := os.ReadFile(filepath.Join(storage.path, "snapshots.json"))
	if err == nil {
		err = json.Unmarshal(snapshotsContent, &storage.snapshots)
		if err != nil {
			return nil, fmt.Errorf("json unmarshal snapshots: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read file snapshots: %w", err)
	}

	// NOTE(simon): Load old snapshot points.
	snapshotPointsContent, err := os.ReadFile(filepath.Join(storage.path, "snapshotPoints.json"))
	if err == nil {
		err = json.Unmarshal(snapshotPointsContent, &storage.snapshotPoints)
		if err != nil {
			return nil, fmt.Errorf("json unmarshal snapshot points: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read file snapshot points: %w", err)
	}

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
		log.Printf("ERROR %v\n", fmt.Errorf("storage db exec: %w", err))
	}

	return storage, nil
}

func (storage *Storage) storeFeed(feed feedparse.Feed, entries []feedparse.Entry) {
	transaction, err := storage.db.Begin(context.Background())
	if err != nil {
		log.Println(err)
	}
	defer transaction.Rollback(context.Background())

	feedSql :=  `
		INSERT INTO Feeds (externalId, title, description, url, updated) VALUES (@id, @title, @description, @url, @updated)
		ON CONFLICT (externalId) DO UPDATE SET title = @title, description = @description, url = @url, updated = @updated WHERE Feeds.updated < @updated
	`
	_, err = transaction.Exec(context.Background(), feedSql, pgx.NamedArgs{
		"id":          feed.Id,
		"title":       feed.Title,
		"description": feed.Description,
		"url":         feed.Link,
		"updated":     feed.Updated,
	})
	if err != nil {
		log.Println(err)
	}

	for _, entry := range entries {
		entrySql := `
			INSERT INTO Entries (feed, externalId, title, url, published, updated) VALUES ((SELECT id FROM Feeds WHERE externalId = @feed), @id, @title, @url, @published, @updated)
			ON CONFLICT (externalId) DO UPDATE SET title = @title, url = @url, updated = @updated WHERE Entries.updated < @updated;
		`
		_, err := transaction.Exec(context.Background(), entrySql, pgx.NamedArgs{
			"id":          entry.Id,
			"feed":        feed.Id,
			"title":       entry.Title,
			"url":         entry.Link,
			"published":   entry.Updated,
			"updated":     entry.Updated,
		})
		if err != nil {
			log.Println(err)
		}
	}
	transaction.Commit(context.Background())
}



type HighlightPart struct {
	Value     string
	Highlight bool
}

type HighlightString []HighlightPart

func highlightFromValueQuery(value string, queryWords []string) HighlightString {
	type Range struct {
		min, max int
	}

	lowerValue := strings.ToLower(value)

	// NOTE(simon): Collect matches.
	var matches []Range
	for _, queryWord := range queryWords {
		offset := 0
		for {
			start := offset + strings.Index(lowerValue[offset:], queryWord)
			if start < offset {
				break
			}

			matches = append(matches, Range{start, start + len(queryWord)})
			offset = start + len(queryWord)
		}
	}

	// NOTE(simon): Sort mathces on starting position.
	slices.SortFunc(matches, func (a, b Range) int {
		return a.min - b.min
	})

	// NOTE(simon): Build highlight string.
	var highlight HighlightString
	previousOffset := 0
	for _, match := range matches {
		if previousOffset < match.min {
			highlight = append(highlight, HighlightPart{value[previousOffset:match.min], false})
		}

		// NOTE(simon): If we have overlapping matches, keep the first one.
		if previousOffset <= match.min {
			highlight = append(highlight, HighlightPart{value[match.min:match.max], true})

			previousOffset = match.max
		}
	}
	if previousOffset != len(value) {
		highlight = append(highlight, HighlightPart{value[previousOffset:], false})
	}

	return highlight
}



type FeedDescription struct {
	Title string
	Description string
	Link string

	HighlightTitle HighlightString
}

func (storage *Storage) QueryFeeds(query string, offset int, size int) []FeedDescription {
	queryWords := strings.Fields(strings.ToLower(query))

	// NOTE(simon): Collect feeds to descriptions.
	descriptions := []FeedDescription{}
	for _, feed := range storage.Feeds() {
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

	return descriptions
}

func (storage *Storage) Feeds() []feedparse.Feed {
	query := `SELECT (id, title, description, url as link, updated) FROM Feeds ORDER BY title`
	rows, err := storage.db.Query(context.Background(), query)
	if err != nil {
		log.Println(err)
		return nil
	}

	feeds, err := pgx.CollectRows(rows, pgx.RowToStructByName[feedparse.Feed])
	if err != nil {
		log.Println(err)
		return nil
	}

	return feeds
}

func (storage *Storage) jsonFromFeeds() ([]byte, error) {
	feeds := storage.Feeds()
	return json.Marshal(feeds)
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

func (storage *Storage) QueryEntries(query string, sortOrder SortOrder, offset int, size int) []EntryDescription {
	queryWords := strings.Fields(strings.ToLower(query))

	// NOTE(simon): Collect entries to descriptions.
	descriptions := []EntryDescription{}
	for _, entry := range storage.Entries() {
		var feedTitle string
		//if feedInstance, ok := storage.feeds.Load(entry.Feed); ok {
			//feed := feedInstance.(feedparse.Feed)
			//feedTitle = feed.Title
		//}

		description := EntryDescription{
			Title: entry.Title,
			Feed: feedTitle,
			Link: entry.Link,
			Id: entry.Id,
			Published: entry.Published,
		}

		descriptions = append(descriptions, description)
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

	return descriptions
}

func (storage *Storage) Entries() []feedparse.Entry {
	query := `SELECT id, feed, title, url AS link, updated, published FROM Entries ORDER BY published`
	rows, err := storage.db.Query(context.Background(), query)
	if err != nil {
		log.Println(err)
		return nil
	}

	entries, err := pgx.CollectRows(rows, pgx.RowToStructByName[feedparse.Entry])
	if err != nil {
		log.Println(err)
		return nil
	}

	return entries
}

func (storage *Storage) jsonFromEntries() ([]byte, error) {
	entries := storage.Entries()
	return json.Marshal(entries)
}



func (storage *Storage) saveSnapshots() error {
	storage.snapshotLock.Lock()
	defer storage.snapshotLock.Unlock()

	encodedSnapshots, err := json.Marshal(storage.snapshots)
	if err != nil {
		return fmt.Errorf("json marshal snapshots: %w", err)
	}

	err = atomicWriteFile(filepath.Join(storage.path, "snapshots.json"), encodedSnapshots)
	if err != nil {
		return fmt.Errorf("atomic write file snapshots: %w", err)
	}

	encodedSnapshotPoints, err := json.Marshal(storage.snapshotPoints)
	if err != nil {
		return fmt.Errorf("json marshal snapshot points: %w", err)
	}

	err = atomicWriteFile(filepath.Join(storage.path, "snapshotPoints.json"), encodedSnapshotPoints)
	if err != nil {
		return fmt.Errorf("atomic write file snapshot points: %w", err)
	}

	return nil
}

func (storage *Storage) addSnapshots(snapshots []wayback.Snapshot) {
	slices.SortFunc(snapshots, func (a, b wayback.Snapshot) int {
		return strings.Compare(a.Date, b.Date)
	})

	storage.snapshotLock.Lock()
	defer storage.snapshotLock.Unlock()

	// NOTE(simon): Merge arrays.
	merged := []wayback.Snapshot{}
	i, j := 0, 0
	for i < len(storage.snapshots) && j < len(snapshots) {
		if storage.snapshots[i].Date < snapshots[j].Date {
			merged = append(merged, storage.snapshots[i])
			i += 1
		} else {
			merged = append(merged, snapshots[j])
			j += 1
		}
	}

	// NOTE(simon): Append remaining.
	merged = append(merged, storage.snapshots[i:]...)
	merged = append(merged, snapshots[j:]...)

	storage.snapshots = merged
}

func (storage *Storage) updateSnapshotTime(link string, time time.Time) {
	storage.snapshotLock.Lock()
	storage.snapshotPoints[link] = time
	storage.snapshotLock.Unlock()
}

func (storage *Storage) getLatestSnapshotTime(link string) time.Time {
	storage.snapshotLock.Lock()
	defer storage.snapshotLock.Unlock()
	return storage.snapshotPoints[link]
}



func atomicWriteFile(file string, data []byte) error {
	directory, _ := filepath.Split(file)

	tempFile, err := os.CreateTemp(directory, "temp-*.json")
	if err != nil {
		return fmt.Errorf("create temporary: %w", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err = tempFile.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err = tempFile.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if err = tempFile.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	err = os.Rename(tempFile.Name(), file)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}
