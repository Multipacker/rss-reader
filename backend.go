package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"Multipacker/rss-reader/internal/feedparse"
	"Multipacker/rss-reader/internal/wayback"
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



type HttpMeta struct {
	Etag         string
	LastModified time.Time
}

var httpMetaCache sync.Map

func pollUrl(url string) (response *http.Response, changed bool, err error) {
	// NOTE(simon): Create the request.
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}

	// NOTE(simon): Query meta information
	var meta HttpMeta
	if metaInterface, hasMeta := httpMetaCache.Load(url); hasMeta {
		meta = metaInterface.(HttpMeta)
	}

	// NOTE(simon): Add conditions from previous requests.
	if !meta.LastModified.IsZero() {
		request.Header.Add("If-Modified-Since", meta.LastModified.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	}
	if len(meta.Etag) > 0 {
		request.Header.Add("If-None-Match", meta.Etag)
	}

	response, err = (&http.Client{}).Do(request)
	if err != nil {
		return
	}

	// NOTE(simon): Has the content changed?
	changed = response.StatusCode != http.StatusNotModified

	// NOTE(simon): Get Etag header
	if httpEtag := response.Header["Etag"]; len(httpEtag) > 0 {
		// NOTE(simon): There might be multiple items due to the interface, but
		// the HTTP spec only allows one, so we use the first one.
		meta.Etag = httpEtag[0]
	}

	// NOTE(simon): Get Last-Modifed header
	if httpLastModified := response.Header["Last-Modified"]; len(httpLastModified) > 0 {
		// NOTE(simon): There might be multiple items due to the interface, but
		// the HTTP spec only allows one, so we use the first one.
		httpLastModified := httpLastModified[0]

		formats := []string{
			time.RFC1123,                   // From HTTP spec
			"Mon, 2 Jan 2006 15:04:05 MST", // Some don't zero-pad the days
		}

		// NOTE(simon): Try different time formats until one parses.
		for _, format := range formats {
			if parsed, err := time.Parse(format, httpLastModified); err == nil {
				meta.LastModified = parsed
				break
			}
		}

		// NOTE(simon): Did we parse the header?
		if meta.LastModified.IsZero() {
			log.Printf("Failed to parse Last-Modified header '%v'\n", httpLastModified)
		}
	}

	// NOTE(simon): Update meta cache.
	httpMetaCache.Store(url, meta)

	return
}



func updateFeed(url string, storage *Storage) error {
	response, changed, err := pollUrl(url)
	if err != nil {
		return fmt.Errorf("poll url: %w", err)
	}
	defer response.Body.Close()

	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %v", response.Status)
	}

	// NOTE(simon): If nothing changed, we are done!
	if !changed {
		return nil
	}

	feed, entries, err := feedparse.Parse(response.Body, url)
	if err != nil {
		return fmt.Errorf("feed parse: %w", err)
	}

	storage.storeFeed(feed, entries)

	return nil
}

func updateFeeds(storage *Storage) {
	log.Println("INFO: Updating feeds")
	beforeUpdate := time.Now()

	// NOTE(simon): Dispatch updates to all feeds.
	var wg sync.WaitGroup
	for _, feedInstance := range storage.feeds.Range {
		feed := feedInstance.(feedparse.Feed)

		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			err := updateFeed(link, storage)
			if err != nil {
				log.Println(err)
			}
		}(feed.Link)
	}

	wg.Wait()

	log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
}

func fetchWaybackEntry(storage *Storage) {
	// NOTE(simon): Pop the latest snapshot.
	var snapshot wayback.Snapshot
	storage.snapshotLock.Lock()
	snapshotCount := len(storage.snapshots)
	if snapshotCount > 0 {
		snapshot = storage.snapshots[snapshotCount - 1]
		storage.snapshots = storage.snapshots[:snapshotCount - 1]
	}
	storage.snapshotLock.Unlock()

	// NOTE(simon): No snapshots left? Quit
	if snapshot.Date == "" {
		return
	}

	log.Printf("Fetching snapshot %v@%v\n", snapshot.Url, snapshot.Date)

	response, err := wayback.FetchSnapshot(snapshot.Url, snapshot.Date)

	// NOTE(simon): We failed to fetch the entry, requeue it for later processing.
	if err != nil {
		log.Printf("ERROR %v (%v): %v\n", snapshot.Url, snapshot.Date, err)
		storage.snapshotLock.Lock()
		i, _ := slices.BinarySearchFunc(storage.snapshots, snapshot, func (a, b wayback.Snapshot) int {
			return strings.Compare(a.Date, b.Date)
		})
		storage.snapshots = slices.Insert(storage.snapshots, i, snapshot)
		storage.snapshotLock.Unlock()
		return
	}
	defer response.Body.Close()

	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		log.Printf("GET %v", response.Status)
		return
	}

	feed, entries, err := feedparse.Parse(response.Body, snapshot.Url)

	// NOTE(simon): Failing to parse historic entries cannot be recovered.
	if err != nil {
		log.Printf("ERROR %v@%v: %v\n", snapshot.Url, snapshot.Date, err)
		return
	}

	storage.storeFeed(feed, entries)

	snapshotsErr := storage.saveSnapshots()
	if snapshotsErr != nil {
		log.Print(snapshotsErr)
	}
}

func update(storage *Storage) {
	updateFeedsTick       := time.Tick(24 * time.Hour)
	fetchWaybackEntryTick := time.Tick(30 * time.Second)

	for {
		select {
		case <- updateFeedsTick:
			updateFeeds(storage)
		case <- fetchWaybackEntryTick:
			fetchWaybackEntry(storage)
		}
	}
}



//go:embed all:static
var staticFiles embed.FS

//go:embed all:templates
var templateFiles embed.FS

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
		entryInfo := GetEntryInfo(storage, 0, 20, "")
		err := templateExecutor.ExecuteTemplate(response, "index.gohtml", entryInfo)
		if err != nil {
			log.Println(fmt.Errorf("execute template: %w", err))
		}
	})
}

func handleFeeds(storage *Storage) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, request *http.Request) {
		encoded, err := storage.jsonFromFeeds()

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	})
}

func handleEntries(templateExecutor TemplateExecutor, storage *Storage) http.Handler {
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

		err := templateExecutor.ExecuteTemplate(response, "items.gohtml", entryInfo)
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



type Config struct {
	Host            string
	Port            int
	Urls            []string
	OutputDirectory string
}

func readConfig() (Config, error) {
	configContent, err := os.ReadFile("config.json")
	if err != nil {
		return Config{}, fmt.Errorf("read file: %w", err)
	}

	config := Config{}
	if err := json.Unmarshal(configContent, &config); err != nil {
		return Config{}, fmt.Errorf("json unmarshal: %w", err)
	}

	// Validate and set defaults.
	if config.Port == 0 {
		config.Port = 8080
	}

	return config, nil
}



func main() {
	// NOTE(simon): Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// NOTE(simon): Parse command line arguments.
	reload := flag.Bool("reload", false, "reload static files on page refresh")
	flag.Parse()

	config, err := readConfig()
	if err != nil {
		log.Fatal(fmt.Errorf("read config: %w", err))
	}

	storage, err := createStorage(config.OutputDirectory)
	if err != nil {
		log.Fatal(fmt.Errorf("create storage: %w", err))
	}

	if false {
	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, link := range config.Urls {
		go updateFeed(link, storage)
	}

	go func () {
		for _, link := range config.Urls {
			timeBeforePoll := time.Now()
			lastPollTime := storage.getLatestSnapshotTime(link)

			log.Printf("Fetching snapshots for %v\n", link)
			snapshots, err := wayback.QuerySnapshots(link, lastPollTime)
			if err != nil {
				log.Printf("ERROR %v: Failed to fetch snapshots %v\n", link, err)
				continue
			}
			log.Printf("Got %v new snapthots for %v\n", len(snapshots), link)

			storage.addSnapshots(snapshots)
			storage.updateSnapshotTime(link, timeBeforePoll)
		}

		snapshotsErr := storage.saveSnapshots()
		if snapshotsErr != nil {
			log.Print(snapshotsErr)
		}
	}()

	// NOTE(simon): Start feed update process.
	go update(storage)
	}

	// NOTE(simon): Setup handler for reloading of static files
	var staticHandler http.Handler
	var templateExecutor TemplateExecutor
	if *reload {
		staticHandler = http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
		templateExecutor = DebugTemplateExecutor{"templates/*.gohtml"}
	} else {
		staticHandler = http.FileServerFS(staticFiles)
		templateExecutor = template.Must(template.ParseFS(templateFiles, "**/*.gohtml"))
	}

	// NOTE(simon): Setup and start the server.
	http.Handle("/", handleIndex(templateExecutor, storage))
	http.Handle("/static/", staticHandler)
	http.Handle("GET /feeds", handleFeeds(storage))
	http.Handle("GET /entries", handleEntries(templateExecutor, storage))

	address := fmt.Sprintf("%s:%d", config.Host, config.Port)
	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, middlewareLogging(log.Default(), http.DefaultServeMux)); err != nil {
		log.Fatal(err)
	}
}
