package main

import (
	"context"
	"embed"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)



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
	if response.StatusCode == http.StatusOK {
		changed = true
	} else if response.StatusCode == http.StatusNotModified {
		changed = false
	}

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
			log.Printf("Failed to parse Last-Modified header from %v: '%v'\n", url, httpLastModified)
		}
	}

	// NOTE(simon): Update meta cache.
	httpMetaCache.Store(url, meta)

	return
}



type Config struct {
	Urls []string `json:"urls"`
}

type Entry struct {
	Id        string    `json:"id"`
	Feed      string    `json:"feed"`
	Title     string    `json:"title"`
	Published time.Time `json:"published"`
	Updated   time.Time `json:"updated"`
	Link      string    `json:"link"`
}

type Feed struct {
	Id          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Link        string    `json:"link"`
	Updated     time.Time `json:"updated"`
}

func parseRssDateOrNow(raw string) time.Time {
	// NOTE(simon): Early out on empty dates.
	if raw == "" {
		return time.Now()
	}

	// NOTE(simon): Trim anything before and including the first comma.
	firstComma := strings.IndexRune(raw, ',')
	if firstComma != -1 {
		raw = strings.TrimSpace(raw[firstComma + 1:])
	}

	// NOTE(simon): Try to parse a few common date formats.
	formats := []string{
		"02 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04:05 -0700",
		"02 Jan 06 15:04:05 MST",
		"02 Jan 06 15:04:05 -0700",
		"2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		"2 Jan 06 15:04:05 MST",
		"2 Jan 06 15:04:05 -0700",
	}

	for _, format := range formats {
		parsed, err := time.Parse(format, raw)
		if err != nil {
			continue
		}

		if parsed.Before(time.Now()) {
			return parsed
		}
	}

	log.Printf("ERROR: Failed to parse \"%v\" as a RSS date", raw)

	return time.Now()
}

func parseAtomDateOrNow(raw string) time.Time {
	// NOTE(simon): Early out on empty dates.
	if raw == "" {
		return time.Now()
	}

	formats := []string{
		time.RFC3339,
	}

	for _, format := range formats {
		parsed, err := time.Parse(format, raw)
		if err != nil {
			continue
		}

		if parsed.Before(time.Now()) {
			return parsed
		}
	}

	log.Printf("ERROR: Failed to parse \"%v\" as an Atom date", raw)

	return time.Now()
}

var db *pgxpool.Pool
var config Config

func updateFeed(url string) {
	resp, changed, err := pollUrl(url)
	if err != nil {
		log.Printf("ERROR %v: %v\n", url, err)
		return
	}
	defer resp.Body.Close()

	// NOTE(simon): If nothing changed, we are done!
	if !changed {
		return
	}

	// NOTE(simon): On a bad response we just skip this URL.
	if resp.StatusCode != http.StatusOK {
		log.Printf("ERROR %v: GET %s\n", url, resp.Status)
		return
	}

	decoder := xml.NewDecoder(resp.Body)

	// NOTE(simon): Find the first start element to determine the kind of feed we have.
	var startToken xml.StartElement
	for err == nil && startToken.Name.Local == "" {
		var token xml.Token
		token, err = decoder.Token()

		if err == nil {
			switch token := token.(type) {
			case xml.StartElement:
				startToken = token
			}
		}
	}

	var feed    Feed
	var entries []Entry

	switch startToken.Name.Local {
	case "rss":
		type RssItem struct {
			XMLName xml.Name `xml:"item"`
			Title   string   `xml:"title"`
			Link    string   `xml:"link"`
			Guid    string   `xml:"guid"`
			PubDate string   `xml:"pubDate"`
		}

		type RssLink struct {
			XMLName  xml.Name `xml:"link"`
			Href     string   `xml:"href,attr"`
			Rel      string   `xml:"rel,attr"`
			Chardata string   `xml:",chardata"`
		}

		type RssFeed struct {
			XMLName       xml.Name  `xml:"rss"`
			Title         string    `xml:"channel>title"`
			Description   string    `xml:"channel>description"`
			LastBuildDate string    `xml:"channel>lastBuildDate"`
			Links         []RssLink `xml:"channel>link"`
			Items         []RssItem `xml:"channel>item"`
		}

		// NOTE(simon): Attempt to parse the feed.
		var rssFeed RssFeed
		err = decoder.DecodeElement(&rssFeed, &startToken)
		if err != nil {
			rssFeed = RssFeed{}
		}

		// NOTE(simon): Restructure to our internal format.
		feed.Title       = rssFeed.Title
		feed.Description = rssFeed.Description
		feed.Updated     = parseRssDateOrNow(rssFeed.LastBuildDate)
		feed.Link        = url
		for _, link := range rssFeed.Links {
			if link.Rel == "self" {
				feed.Link = link.Href
				break
			}
		}
		feed.Id = feed.Link

		for _, item := range rssFeed.Items {
			var entry Entry
			entry.Feed  = feed.Id
			entry.Title = item.Title
			entry.Link  = item.Link
			if item.Guid != "" {
				entry.Id = item.Guid
			} else {
				entry.Id = entry.Link
			}
			entry.Published = parseRssDateOrNow(item.PubDate)
			entry.Updated   = entry.Published

			entries = append(entries, entry)
		}
	case "feed":
		type AtomLink struct {
			XMLName  xml.Name `xml:"link"`
			Href     string   `xml:"href,attr"`
			Rel      string   `xml:"rel,attr"`
			Chardata string   `xml:",chardata"`

		}

		type AtomEntry struct {
			XMLName   xml.Name   `xml:"entry"`
			Title     string     `xml:"title"`
			Id        string     `xml:"id"`
			Published string     `xml:"published"`
			Updated   string     `xml:"updated"`
			Links     []AtomLink `xml:"link"`
		}

		type AtomFeed struct {
			XMLName  xml.Name    `xml:"feed"`
			Title    string      `xml:"title"`
			Subtitle string      `xml:"subtitle"`
			Id       string      `xml:"id"`
			Links    []AtomLink  `xml:"link"`
			Updated  string      `xml:"updated"`
			Entries  []AtomEntry `xml:"entry"`
		}

		// NOTE(simon): Attempt to parse the feed.
		var atomFeed AtomFeed
		err = decoder.DecodeElement(&atomFeed, &startToken)
		if err != nil {
			atomFeed = AtomFeed{}
		}

		// NOTE(simon): Restructure to our internal format.
		feed.Title       = atomFeed.Title
		feed.Description = atomFeed.Subtitle
		feed.Id          = atomFeed.Id
		feed.Link        = url
		for _, link := range atomFeed.Links {
			if link.Rel == "self" {
				feed.Link = link.Href
				break
			}
		}
		feed.Updated = parseAtomDateOrNow(atomFeed.Updated)

		for _, atomEntry := range atomFeed.Entries {
			var entry Entry
			entry.Feed = feed.Id
			entry.Title = atomEntry.Title
			entry.Id    = atomEntry.Id
			for _, link := range atomEntry.Links {
				if link.Rel == "alternate" || link.Rel == "" {
					entry.Link = link.Href
					break
				}
			}

			entry.Updated = parseAtomDateOrNow(atomEntry.Updated)
			if atomEntry.Published == "" {
				entry.Published = entry.Updated
			} else {
				entry.Published = parseAtomDateOrNow(atomEntry.Published)
			}

			entries = append(entries, entry)
		}
	default:
		if err == nil {
			err = fmt.Errorf("Unknown feed type \"%v\"\n", startToken.Name.Local)
		}
	}

	// NOTE(simon): Update database.
	if err == nil {
		batch := &pgx.Batch{}
		feedQuery := `
			INSERT INTO Feeds VALUES (@id, @title, @description, @link, @updated)
			ON CONFLICT (id) DO
			UPDATE SET title = @title, description = @description, link = @link, updated = @updated
			WHERE Feeds.updated < @updated;
		`
		args := pgx.NamedArgs{
			"id":          feed.Id,
			"title":       feed.Title,
			"description": feed.Description,
			"link":        feed.Link,
			"updated":     feed.Updated,
		}
		batch.Queue(feedQuery, args)

		for _, entry := range entries {
			entryQuery := `
				INSERT INTO Entries VALUES (@id, @feed, @title, @published, @updated, @link)
				ON CONFLICT (id, feed) DO
				UPDATE SET title = @title, updated = @updated, link = @link
				WHERE Entries.updated < @updated;
			`
			args := pgx.NamedArgs{
				"id":        entry.Id,
				"feed":      entry.Feed,
				"title":     entry.Title,
				"published": entry.Published,
				"updated":   entry.Updated,
				"link":      entry.Link,
			}
			batch.Queue(entryQuery, args)
		}

		// NOTE(simon): Execute batch inserts.
		results := db.SendBatch(context.Background(), batch)
		err = results.Close()
	}

	// NOTE(simon): Common logging.
	if err != nil {
		log.Printf("ERROR %v: %v\n", url, err)
	}
}

func update() {
	updateFeedsTick := time.Tick(24 * time.Hour)

	for range updateFeedsTick {
		log.Println("INFO: Updating feeds")
		beforeUpdate := time.Now()

		// NOTE(simon): Gather and dispatch all updates.
		rows, err := db.Query(context.Background(), `SELECT link FROM Feeds`)
		if err == nil {
			var link string
			var wg sync.WaitGroup
			_, err = pgx.ForEachRow(rows, []any{&link}, func() error {
				wg.Add(1)
				go func(link string) {
					defer wg.Done()
					updateFeed(link)
				}(link)
				return nil
			})
			wg.Wait()
		}

		if err != nil {
			log.Printf("ERROR: %v\n", err)
		}

		log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
	}
}

//go:embed init.sql
var dbSqlInit string
//go:embed all:static
var staticFiles embed.FS

func createDatabase() {
	// NOTE(simon): Ensure we have a version table with one entry in it
	query := `
		CREATE TABLE IF NOT EXISTS Meta (
			version INT NOT NULL PRIMARY KEY
		);
		INSERT INTO Meta(version) (SELECT -1 WHERE NOT EXISTS (SELECT * FROM Meta));
	`
	if _, err := db.Exec(context.Background(), query); err != nil {
		log.Fatal(err)
	}

	// NOTE(simon): Query the current version.
	var version int
	if err := db.QueryRow(context.Background(), "SELECT version FROM Meta").Scan(&version); err != nil {
		log.Fatal(err)
	}

	migrations := []string{
		dbSqlInit,
	}

	for i, migration := range migrations[version + 1:] {
		tx, err := db.Begin(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		defer tx.Rollback(context.Background())

		if _, err := db.Exec(context.Background(), migration); err != nil {
			log.Fatal(err)
		}

		if _, err := db.Exec(context.Background(), "UPDATE Meta SET version = $1", version + 1 + i); err != nil {
			log.Fatal(err)
		}

		if err := tx.Commit(context.Background()); err != nil {
			log.Fatal(err)
		}
	}
}

func handleFeeds(w http.ResponseWriter, request *http.Request) {
	query := `SELECT id, title, description, link, updated FROM Feeds;`
	rows, err := db.Query(request.Context(), query)

	var parsedRows []Feed
	if err == nil {
		parsedRows, err = pgx.CollectRows(rows, pgx.RowToStructByName[Feed])
	}

	var encoded []byte
	if err == nil {
		encoded, err = json.Marshal(parsedRows)
	}

	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		log.Printf("ERROR: %v\n", err)
	}
}

func handleEntries(w http.ResponseWriter, request *http.Request) {
	query := `SELECT * FROM Entries ORDER BY published DESC;`
	rows, err := db.Query(request.Context(), query)

	var parsedRows []Entry
	if err == nil {
		parsedRows, err = pgx.CollectRows(rows, pgx.RowToStructByName[Entry])
	}

	var encoded []byte
	if err == nil {
		encoded, err = json.Marshal(parsedRows)
	}

	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		log.Printf("ERROR: %v\n", err)
	}
}

func middlewareLogging(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		logger.Printf("\"%v %v %v\" \"%v\" %v\n", r.Method, r.URL.Path, r.Proto, r.UserAgent(), r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

func main() {
	// NOTE(simon): Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// NOTE(simon): Parse command line arguments.
	reload := flag.Bool("reload", false, "reload static files on page refresh")
	flag.Parse()

	// NOTE(simon): Load the configuration file.
	{
		configContent, err := os.ReadFile("config.json")
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(configContent, &config); err != nil {
			log.Fatal(err)
		}
	}

	// NOTE(simon): Connect to the database.
	var err error
	db, err = pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("Unable to create connection pool: %v\n", err)
	}
	defer db.Close()

	createDatabase()

	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, link := range config.Urls {
		go updateFeed(link)
	}

	// NOTE(simon): Start update routing in a separte goroutine.
	go update()

	// NOTE(simon): Build the address to listen on from environment variables ADDR and PORT.
	host := "localhost"
	port := "8080"
	if hostEnv, ok := os.LookupEnv("ADDR"); ok {
		host = hostEnv
	}
	if portEnv, ok := os.LookupEnv("PORT"); ok {
		port = portEnv
	}
	address := fmt.Sprintf("%s:%s", host, port)

	// NOTE(simon): Setup handler for reloading of static files
	var staticHandler http.Handler
	if *reload {
		staticHandler = http.FileServer(http.Dir("static"))
	} else {
		root, _ := fs.Sub(staticFiles, "static")
		staticHandler = http.FileServerFS(root)
	}

	// NOTE(simon): Setup and start the server.
	http.Handle("/", staticHandler)
	http.HandleFunc("GET /feeds", handleFeeds)
	http.HandleFunc("GET /entries", handleEntries)

	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, middlewareLogging(log.Default(), http.DefaultServeMux)); err != nil {
		log.Fatal(err)
	}
}
