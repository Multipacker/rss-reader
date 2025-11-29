package main

import (
	"context"
	"encoding/xml"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Urls []string `json:"urls"`
}

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
	parsed, err := time.Parse("02 Jan 2006 15:04:05 MST", raw)
	if err == nil {
		return parsed
	}

	parsed, err = time.Parse("02 Jan 2006 15:04:05 -0700", raw)
	if err == nil {
		return parsed
	}

	parsed, err = time.Parse("02 Jan 06 15:04:05 MST", raw)
	if err == nil {
		return parsed
	}

	parsed, err = time.Parse("02 Jan 06 15:04:05 -0700", raw)
	if err == nil {
		return parsed
	}

	log.Printf("Failed to parse \"%v\" as a date", raw)

	return time.Now()
}

func parseAtomDateOrNow(raw string) time.Time {
	// NOTE(simon): Early out on empty dates.
	if raw == "" {
		return time.Now()
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return parsed
	}

	log.Printf("Failed to parse \"%v\" as a date", raw)

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
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	parsedRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[Feed])

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	encoded, err := json.Marshal(parsedRows)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

func handleEntries(w http.ResponseWriter, request *http.Request) {
	if request.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	query := `SELECT * FROM Entries ORDER BY published DESC;`
	rows, err := db.Query(request.Context(), query)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	parsedRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[Entry])

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	encoded, err := json.Marshal(parsedRows)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

func update() {
	updateDuration := 24 * 60 * 60 * 1000 * 1000 * 1000
	updateFeedsTick := time.Tick(updateDuration)

	for {
		var feeds []Feed
		var entries[]Entry

		beforeFetch := time.Now()
		for _, url := range config.Urls {
			log.Println(url)
			if resp, err := http.Get(url); err == nil {
				defer resp.Body.Close()

				if resp.StatusCode != 200 {
					log.Printf("Get %s: %s", url, resp.Status)
					continue
				}

				decoder := xml.NewDecoder(resp.Body)

				// NOTE(simon): Find the first start element to determine the kind of feed we have.
				var startToken xml.StartElement
				for startToken.Name.Local == "" {
					token, err := decoder.Token()

					if err != nil {
						log.Println(err)
						break
					}

					switch token := token.(type) {
					case xml.StartElement:
						startToken = token
					}
				}

				switch startToken.Name.Local {
				case "rss":
					var rssFeed RssFeed

					// NOTE(simon): Attempt to parse the feed.
					if err := decoder.DecodeElement(&rssFeed, &startToken); err != nil {
						log.Println(err)
						continue
					}

					// NOTE(simon): Restructure to our internal format.
					var feed Feed
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
						entry.Feed = feed.Id
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

					feeds = append(feeds, feed)
				case "feed":
					var atomFeed AtomFeed

					// NOTE(simon): Attempt to parse the feed.
					if err := decoder.DecodeElement(&atomFeed, &startToken); err != nil {
						log.Println(err)
						continue
					}

					// NOTE(simon): Restructure to our internal format.
					var feed Feed
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

					feeds = append(feeds, feed)
				default:
					log.Printf("Unknown feed type \"%v\"\n", startToken.Name.Local)
				}
			} else {
				log.Print(err)
			}
		}
		log.Printf("Downloading feeds: %s\n", time.Since(beforeFetch))

		beforeInserts := time.Now()
		batch := &pgx.Batch{}
		for _, feed := range feeds {
			feedQuery := `
				INSERT INTO Feeds VALUES (@id, @title, @description, @link, @updated)
				ON CONFLICT (id) DO
				UPDATE SET title = @title, description = @description, link = @link, updated = @updated
				WHERE Feeds.updated < @updated
			`
			args := pgx.NamedArgs{
				"id":          feed.Id,
				"title":       feed.Title,
				"description": feed.Description,
				"link":        feed.Link,
				"updated":     feed.Updated,
			}
			batch.Queue(feedQuery, args)

		}
		for _, entry := range entries {
			entryQuery := `
				INSERT INTO Entries VALUES (@id, @feed, @title, @published, @updated, @link)
				ON CONFLICT (id, feed) DO
				UPDATE SET title = @title, updated = @updated, link = @link
				WHERE Entries.updated < @updated
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

		results := db.SendBatch(context.Background(), batch)
		if err := results.Close(); err != nil {
			fmt.Println(err)
		}
		log.Printf("Updating store: %s\n", time.Since(beforeInserts))

		// NOTE(simon): Wait for next update tick.
		<- updateFeedsTick
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

	// NOTE(simon): Create tables if they don't already exist
	_, err = db.Exec(
		context.Background(),
		`
		CREATE TABLE IF NOT EXISTS Feeds (
			id          TEXT NOT NULL PRIMARY KEY,
			title       TEXT NOT NULL,
			description TEXT NOT NULL,
			link        TEXT NOT NULL,
			updated     TIMESTAMP WITH TIME ZONE NOT NULL
		)
		`,
	)
	if err != nil {
		log.Fatalf("Failed to create 'Feeds' table: %v\n", err)
	}
	_, err = db.Exec(
		context.Background(),
		`
		CREATE TABLE IF NOT EXISTS Entries (
			id        TEXT NOT NULL,
			feed      TEXT NOT NULL,
			title     TEXT NOT NULL,
			published TIMESTAMP WITH TIME ZONE NOT NULL,
			updated   TIMESTAMP WITH TIME ZONE NOT NULL,
			link      TEXT NOT NULL,
			PRIMARY KEY (id, feed)
		)
		`,
	)
	if err != nil {
		log.Fatalf("Failed to create 'Entries' table: %v\n", err)
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
	log.Printf("Serving on http://%s", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
}
