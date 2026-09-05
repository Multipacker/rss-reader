package website

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/models"
)

type SortOrder int
const (
	SortOrderNewestFirst SortOrder = iota
	SortOrderOldestFirst
)

type FeedDescription struct {
	Title string
	Description string
	Link string

	HighlightTitle HighlightString
}

func queryFeeds(context context.Context, dbConnection db.Database, query string, offset int, size int) ([]FeedDescription, error) {
	queryWords := strings.Fields(strings.ToLower(query))

	feeds, err := db.Query[models.Feed](context, dbConnection, "SELECT * FROM Feeds")
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

func HandleFeedsGet(templateExecutor TemplateExecutor) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "feeds.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func HandleFeedsPost(templateExecutor TemplateExecutor, dbConnection db.Database) http.Handler {
	type FeedInfo struct {
		Query string

		HasMore  bool
		NextPage int
		Feeds    []FeedDescription
	}

	return http.HandlerFunc(func (response http.ResponseWriter, request *http.Request) {
		formPage := request.FormValue("page")
		formQuery := request.FormValue("query")

		page, _ := strconv.Atoi(formPage)
		query := formQuery

		pageSize := 10
		offset := page * pageSize

		feeds, err := queryFeeds(request.Context(), dbConnection, query, offset, pageSize)
		if err != nil {
			// TODO(simon): Render error page
			log.Println(err)
		}

		feedInfo := FeedInfo{
			Query:    query,
			Feeds:    feeds,
			NextPage: page + 1,
			HasMore:  pageSize == len(feeds),
		}

		err = templateExecutor.ExecuteTemplate(response, "feed_items.gohtml", feedInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}
