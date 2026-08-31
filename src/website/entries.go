package website

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
)

func HandleEntriesGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "entries.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func HandleEntriesPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
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

		entries, err := storage.QueryEntries(request.Context(), query, sortOrder, offset, pageSize)
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
