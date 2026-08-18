package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
)



type TemplateExecutor interface {
	ExecuteTemplate(writer io.Writer, name string, data any) error
}

type DebugTemplateExecutor struct {
	Glob string
}

func (executor DebugTemplateExecutor) ExecuteTemplate(writer io.Writer, name string, data any) error {
	templates, err := template.ParseGlob(executor.Glob)
	if err != nil {
		return fmt.Errorf("parse glob: %w", err)
	}
	return templates.ExecuteTemplate(writer, name, data)
}



//go:embed all:static
var staticFiles embed.FS

//go:embed all:templates
var templateFiles embed.FS



func handleIndex(templateExecutor TemplateExecutor, storage *Storage) http.HandlerFunc {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/entries", http.StatusFound)
	})
}



func handleFeedsGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "feeds.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleFeedsPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
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

		feeds := storage.QueryFeeds(query, offset, pageSize)

		feedInfo := FeedInfo{
			Query:    query,
			Feeds:    feeds,
			NextPage: page + 1,
			HasMore:  pageSize == len(feeds),
		}

		err := templateExecutor.ExecuteTemplate(response, "feed_items.gohtml", feedInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}



func handleEntriesGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		err := templateExecutor.ExecuteTemplate(response, "entries.gohtml", nil)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleEntriesPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
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

		entries := storage.QueryEntries(query, sortOrder, offset, pageSize)

		entryInfo := EntryInfo{
			Query:    formQuery,
			Order:    formOrder,
			Entries:  entries,
			NextPage: page + 1,
			HasMore:  pageSize == len(entries),
		}

		err := templateExecutor.ExecuteTemplate(response, "entry_items.gohtml", entryInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}



func middlewareLogging(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		logger.Printf("\"%v %v %v\" \"%v\" %v\n", r.Method, r.URL.Path, r.Proto, r.UserAgent(), r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}



func runFrontend(reload bool, config Config, storage *Storage) {
	// NOTE(simon): Setup handler for reloading of static files
	var staticHandler http.Handler
	var templateExecutor TemplateExecutor
	if reload {
		staticHandler = http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
		templateExecutor = DebugTemplateExecutor{"templates/*.gohtml"}
	} else {
		staticHandler = http.FileServerFS(staticFiles)
		templateExecutor = template.Must(template.ParseFS(templateFiles, "**/*.gohtml"))
	}

	// NOTE(simon): Setup and start the server.
	http.Handle("/", handleIndex(templateExecutor, storage))
	http.Handle("/static/", staticHandler)
	http.Handle("GET /feeds", handleFeedsGet(templateExecutor, storage))
	http.Handle("POST /feeds", handleFeedsPost(templateExecutor, storage))
	http.Handle("GET /entries", handleEntriesGet(templateExecutor, storage))
	http.Handle("POST /entries", handleEntriesPost(templateExecutor, storage))

	address := fmt.Sprintf("%s:%d", config.Host, config.Port)
	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, middlewareLogging(log.Default(), http.DefaultServeMux)); err != nil {
		log.Fatal(err)
	}
}
