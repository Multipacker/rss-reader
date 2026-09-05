package website

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"Multipacker/rss-reader/src/db"
)

type EntryDescription struct {
	Title string
	Feed  string
	Link  string
	Id    string
	Published time.Time

	HighlightTitle HighlightString
	HighlightFeed  HighlightString
}

func queryEntries(context context.Context, dbConnection db.Database, query string, sortOrder SortOrder, offset int, size int) ([]EntryDescription, error) {
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

func HandleEntriesGet(templateExecutor TemplateExecutor) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "entries.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func HandleEntriesPost(templateExecutor TemplateExecutor, dbConnection db.Database) http.Handler {
	type EntryInfo struct {
		Query string
		Order string

		HasMore  bool
		NextPage int
		Entries  []EntryDescription
	}

	sortOrderMap := map[string]SortOrder{
		"newest": SortOrderNewestFirst,
		"oldest": SortOrderOldestFirst,
	}

	return http.HandlerFunc(func (response http.ResponseWriter, request *http.Request) {
		formPage  := request.FormValue("page")
		formOrder := request.FormValue("order")
		formQuery := request.FormValue("query")

		page, _ := strconv.Atoi(formPage)
		sortOrder := sortOrderMap[formOrder]
		query := formQuery

		pageSize := 10
		offset := page * pageSize

		entries, err := queryEntries(request.Context(), dbConnection, query, sortOrder, offset, pageSize)
		if err != nil {
			// TODO(simon): Render error page
			log.Println(err)
		}

		entryInfo := EntryInfo{
			Query:    formQuery,
			Order:    formOrder,
			Entries:  entries,
			NextPage: page + 1,
			HasMore:  pageSize == len(entries),
		}

		err = templateExecutor.ExecuteTemplate(response, "entry_items.gohtml", entryInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}
