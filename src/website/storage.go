package website

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/feeds"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SortOrder int
const (
	SortOrderNewestFirst SortOrder = iota
	SortOrderOldestFirst
)



func createStorage() (*pgxpool.Pool, error) {
	// NOTE(simon): Open database connection
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}

	return pool, nil
}



type FeedDescription struct {
	Title string
	Description string
	Link string

	HighlightTitle HighlightString
}

func QueryFeeds(context context.Context, dbConnection db.Database, query string, offset int, size int) ([]FeedDescription, error) {
	queryWords := strings.Fields(strings.ToLower(query))

	feeds, err := feeds.Feeds(context, dbConnection)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feeds: %w", err)
	}

	// NOTE(simon): Collect feeds to descriptions.
	descriptions := []FeedDescription{}
	for _, feed := range feeds {
		description := FeedDescription{
			Title: feed.Title,
			Description: feed.Description,
			Link: feed.Url,
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




type EntryDescription struct {
	Title string
	Feed  string
	Link  string
	Id    string
	Published time.Time

	HighlightTitle HighlightString
	HighlightFeed  HighlightString
}

func QueryEntries(context context.Context, dbConnection db.Database, query string, sortOrder SortOrder, offset int, size int) ([]EntryDescription, error) {
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
	descriptions, err := db.QueryLax[EntryDescription](context, dbConnection, entryQuery)
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

func Entries(context context.Context, dbConnection db.Database) ([]feedparse.Entry, error) {
	entries, err := db.Query[feedparse.Entry](context, dbConnection, "SELECT id, feed, title, url AS link, updated, published FROM Entries")
	if err != nil {
		return nil, fmt.Errorf("failed to query entries: %w", err)
	}

	return entries, nil
}
