package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"Multipacker/rss-reader/internal/feedparse"
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

type FeedInfo struct {
	Query       string
	HasMore     bool
	NextOffset  int
	Feeds       []feedparse.Feed
}

func GetFeedInfo(storage *Storage, offset int, size int, query string) FeedInfo {
	feeds := storage.Feeds()

	if query != "" {
		var filtered []feedparse.Feed
		for _, entry := range feeds {
			matches := true
			for field := range strings.FieldsSeq(strings.ToLower(query)) {
				matches = matches && strings.Contains(strings.ToLower(entry.Title), field)
			}

			if matches {
				filtered = append(filtered, entry)
			}
		}
		feeds = filtered
	}

	size = min(size, len(feeds) - offset)

	return FeedInfo{
		Query:      query,
		Feeds:      feeds[offset:offset + size],
		NextOffset: offset + size,
		HasMore:    len(feeds) > offset + size,
	}
}

type EntryInfo struct {
	Query       string
	HasMore     bool
	NextOffset  int
	Entries     []feedparse.Entry
}

func GetEntryInfo(storage *Storage, offset int, size int, query string) EntryInfo {
	entries := storage.Entries()

	if query != "" {
		var filtered []feedparse.Entry
		for _, entry := range entries {
			matches := true
			for field := range strings.FieldsSeq(strings.ToLower(query)) {
				matches = matches && strings.Contains(strings.ToLower(entry.Title), field)
			}

			if matches {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}

	size = min(size, len(entries) - offset)

	return EntryInfo{
		Query:      query,
		Entries:    entries[offset:offset + size],
		NextOffset: offset + size,
		HasMore:    len(entries) > offset + size,
	}
}

func handleIndex(templateExecutor TemplateExecutor, storage *Storage) http.HandlerFunc {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/entries", http.StatusFound)
	})
}

func handleFeedsGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		feedInfo := GetFeedInfo(storage, 0, 20, "")
		err := templateExecutor.ExecuteTemplate(response, "feeds.gohtml", feedInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleFeedsPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func (response http.ResponseWriter, request *http.Request) {
		queryOffset := request.FormValue("offset")
		querySize := request.FormValue("size")
		query := request.FormValue("query")

		if queryOffset == "" {
			queryOffset = "0"
		}
		if querySize == "" {
			querySize = "20"
		}

		offset, _ := strconv.Atoi(queryOffset)
		size, _ := strconv.Atoi(querySize)

		feedInfo := GetFeedInfo(storage, offset, size, query)

		err := templateExecutor.ExecuteTemplate(response, "feed_items.gohtml", feedInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleEntriesGet(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		entryInfo := GetEntryInfo(storage, 0, 20, "")
		err := templateExecutor.ExecuteTemplate(response, "entries.gohtml", entryInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleEntriesPost(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
	return http.HandlerFunc(func (response http.ResponseWriter, request *http.Request) {
		queryOffset := request.FormValue("offset")
		querySize := request.FormValue("size")
		query := request.FormValue("query")

		if queryOffset == "" {
			queryOffset = "0"
		}
		if querySize == "" {
			querySize = "20"
		}

		offset, _ := strconv.Atoi(queryOffset)
		size, _ := strconv.Atoi(querySize)

		entryInfo := GetEntryInfo(storage, offset, size, query)

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
