package main

// TODO(simon): To-do list in order or execution
// * Save the date something got imported into the database
// * Server side search
// * Pagination

import (
	"context"
	"encoding/json"
	"encoding/xml"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func handleFeeds(w http.ResponseWriter, request *http.Request) {
	if request.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	query := `SELECT * FROM Feeds;`
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
	if request.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

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

func updateFeed(url string, etag string, lastModified time.Time) {
	var err error

	// NOTE(simon): Build request.
	var request *http.Request
	if err == nil {
		request, err = http.NewRequest("GET", url, nil)
	}
	if err == nil {
		if !lastModified.IsZero() {
			request.Header.Add("If-Modified-Since", lastModified.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
		}
		if len(etag) > 0 {
			request.Header.Add("If-None-Match", etag)
		}
	}

	var resp *http.Response
	if err == nil {
		resp, err = (&http.Client{}).Do(request)
	}

	var decoder *xml.Decoder
	if err == nil {
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			decoder = xml.NewDecoder(resp.Body)
		} else if resp.StatusCode == http.StatusNotModified {
			log.Printf("INFO %v: Already up to date\n", url)
			return
		} else {
			err = fmt.Errorf("GET %s", resp.Status)
		}
	}

	// NOTE(simon): Get headers
	var newEtag string
	var newLastModified time.Time
	if err == nil {
		// NOTE(simon): Get Etag header
		if httpEtag := resp.Header["Etag"]; len(httpEtag) > 0 {
			// NOTE(simon): There might be multiple items due to the interface, but
			// the HTTP spec only allows one, so we use the first one.
			newEtag = httpEtag[0]
		}

		// NOTE(simon): Get Last-Modifed header
		if httpLastModified := resp.Header["Last-Modified"]; len(httpLastModified) > 0 {
			// NOTE(simon): There might be multiple items due to the interface, but
			// the HTTP spec only allows one, so we use the first one.
			httpLastModified := httpLastModified[0]

			formats := []string{
				time.RFC1123,                   // From HTTP spec
				"Mon, 2 Jan 2006 15:04:05 MST", // Some don't zero-pad the days
			}

			for _, format := range formats {
				if parsed, err := time.Parse(format, httpLastModified); err == nil {
					newLastModified = parsed
					break
				}
			}

			if newLastModified.IsZero() {
				log.Printf("ERROR %v: Failed to parse Last-Modified header '%v', ignoring\n", url, httpLastModified)
			}
		}
	}

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
			INSERT INTO Feeds VALUES (@id, @title, @description, @link, @updated, @etag, @lastModified)
			ON CONFLICT (id) DO
			UPDATE SET title = @title, description = @description, link = @link, updated = @updated, etag = @etag, lastModified = @lastModified
			WHERE Feeds.updated < @updated;
		`
		args := pgx.NamedArgs{
			"id":           feed.Id,
			"title":        feed.Title,
			"description":  feed.Description,
			"link":         feed.Link,
			"updated":      feed.Updated,
			"etag":         newEtag,
			"lastModified": newLastModified,
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

		// NOTE(simon): Gather feeds and http metadata
		type FeedMeta struct {
			link         string
			etag         string
			lastModified time.Time
		}

		rows, err := db.Query(context.Background(), `SELECT link, etag, lastModified FROM Feeds`)

		var metas []FeedMeta
		if err == nil {
			metas, err = pgx.CollectRows(rows, pgx.RowToStructByName[FeedMeta])
		}

		if err != nil {
			log.Printf("ERROR: %v\n", err)
		}

		// NOTE(simon): Dispatch all updates.
		var wg sync.WaitGroup
		for _, meta := range metas {
			wg.Add(1)
			go func(meta FeedMeta) {
				defer wg.Done()
				updateFeed(meta.link, meta.etag, meta.lastModified)
			}(meta)
		}

		wg.Wait()
		log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
	}
}

//go:embed init.sql
var dbSqlInit string

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

func main() {
	// NOTE(simon): Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

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
		go updateFeed(link, "", time.Time{})
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

	// NOTE(simon): Configure the routes.
	fileServer := http.FileServer(http.Dir("static"))
	http.Handle("/", fileServer)
	http.HandleFunc("/feeds", handleFeeds)
	http.HandleFunc("/entries", handleEntries)

	// NOTE(simon): Start the server with the configured address and the handlers configured above.
	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
}
