package website

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
)

func HandleFeedsGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "feeds.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func HandleFeedsPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
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

		feeds, err := storage.QueryFeeds(request.Context(), query, offset, pageSize)
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
