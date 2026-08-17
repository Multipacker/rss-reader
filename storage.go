package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"Multipacker/rss-reader/internal/feedparse"
	"Multipacker/rss-reader/internal/wayback"
)

type SortOrder int
const (
	SortOrderNewestFirst SortOrder = iota
	SortOrderOldestFirst
)

type Storage struct {
	path string
	feeds sync.Map
	entries sync.Map
	snapshots []wayback.Snapshot
	snapshotPoints map[string]time.Time
	snapshotLock sync.Mutex
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

	// NOTE(simon): Load old feeds.
	feedsContent, err := os.ReadFile(filepath.Join(storage.path, "feeds.json"))
	if err == nil {
		var feeds []feedparse.Feed
		err = json.Unmarshal(feedsContent, &feeds)
		if err != nil {
			return nil, fmt.Errorf("json unmarshal feeds: %w", err)
		}
		for _, feed := range feeds {
			storage.feeds.Store(feed.Id, feed)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read file feeds: %w", err)
	}

	// NOTE(simon): Load old entries.
	entriesContent, err := os.ReadFile(filepath.Join(storage.path, "entries.json"))
	if err == nil {
		var entries []feedparse.Entry
		err = json.Unmarshal(entriesContent, &entries)
		if err != nil {
			return nil, fmt.Errorf("json unmarshal entries: %w", err)
		}
		for _, entry := range entries {
			storage.entries.Store(entry.Id, entry)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read file entries: %w", err)
	}

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

	return storage, nil
}

func (storage *Storage) storeFeed(feed feedparse.Feed, entries []feedparse.Entry) {
	// NOTE(simon): Update stores.
	// TODO(simon): Only do this if the date is newer.
	storage.feeds.Store(feed.Id, feed)

	// NOTE(simon): Update entries.
	for _, newEntry := range entries {
		updateEntry := true

		// NOTE(simon): Merge with existing entry (keep the publish date).
		if entryInstance, hasEntry := storage.entries.Load(newEntry.Id); hasEntry {
			oldEntry := entryInstance.(feedparse.Entry)

			newEntry.Published = oldEntry.Published
			updateEntry = oldEntry.Updated.Before(newEntry.Updated)
		}

		if updateEntry {
			storage.entries.Store(newEntry.Id, newEntry)
		}
	}

	// NOTE(simon): Serialize to disk.
	encodedFeeds,   feedsErr   := storage.jsonFromFeeds()
	encodedEntries, entriesErr := storage.jsonFromEntries()
	if feedsErr == nil {
		feedsErr = atomicWriteFile(filepath.Join(storage.path, "feeds.json"), encodedFeeds)
		if feedsErr == nil && entriesErr == nil {
			entriesErr = atomicWriteFile(filepath.Join(storage.path, "entries.json"), encodedEntries)
		}
	}

	if feedsErr != nil {
		log.Printf("ERROR: Could not save feeds: %v\n", feedsErr)
	}
	if entriesErr != nil {
		log.Printf("ERROR: Could not save entries: %v\n", entriesErr)
	}
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

func (storage *Storage) QueryFeeds(query string) []FeedDescription {
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

	return descriptions
}

func (storage *Storage) Feeds() []feedparse.Feed {
	// NOTE(simon): Collect all entries.
	var feeds []feedparse.Feed
	for _, feedInstance := range storage.feeds.Range {
		feed := feedInstance.(feedparse.Feed)
		feeds = append(feeds, feed)
	}

	slices.SortFunc(feeds, func (a, b feedparse.Feed) int {
		return strings.Compare(a.Title, b.Title)
	})

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

func (storage *Storage) QueryEntries(query string, sortOrder SortOrder) []EntryDescription {
	queryWords := strings.Fields(strings.ToLower(query))

	// NOTE(simon): Collect entries to descriptions.
	descriptions := []EntryDescription{}
	for _, entry := range storage.Entries() {
		var feedTitle string
		if feedInstance, ok := storage.feeds.Load(entry.Feed); ok {
			feed := feedInstance.(feedparse.Feed)
			feedTitle = feed.Title
		}

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

	return descriptions
}

func (storage *Storage) Entries() []feedparse.Entry {
	// NOTE(simon): Collect all entries.
	var entries []feedparse.Entry
	for _, entryInstance := range storage.entries.Range {
		entry := entryInstance.(feedparse.Entry)
		entries = append(entries, entry)
	}

	slices.SortFunc(entries, func (a, b feedparse.Entry) int {
		return b.Published.Compare(a.Published)
	})

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

	storage.snapshotLock.Lock()
	defer storage.snapshotLock.Unlock()
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
