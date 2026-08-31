package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"Multipacker/rss-reader/internal/feedparse"
	"Multipacker/rss-reader/internal/wayback"

	"github.com/klauspost/compress/gzhttp"
)



type HttpMeta struct {
	Etag         string
	LastModified time.Time
}

var httpMetaCache sync.Map

func pollUrl(client *http.Client, url string) (response *http.Response, changed bool, err error) {
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

	// NOTE(simon): Content negotiation
	request.Header.Add("Accept", "application/rss+xml")
	request.Header.Add("Accept", "application/atom+xml")
	request.Header.Add("Accept", "application/xml")

	response, err = client.Do(request)
	if err != nil {
		return
	}

	// NOTE(simon): Has the content changed?
	changed = response.StatusCode != http.StatusNotModified

	// NOTE(simon): Get Etag header
	meta.Etag = response.Header.Get("Etag")

	// NOTE(simon): Get Last-Modifed header
	if httpLastModified := response.Header.Get("Last-Modified"); len(httpLastModified) > 0 {
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



func updateFeed(client *http.Client, url string, storage *Storage) error {
	response, changed, err := pollUrl(client, url)
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

	storage.storeFeed(context.Background(), feed, entries)

	return nil
}

func updateFeeds(client *http.Client, storage *Storage) {
	log.Println("INFO: Updating feeds")
	beforeUpdate := time.Now()

	// NOTE(simon): Dispatch updates to all feeds.
	var wg sync.WaitGroup
	for _, feed := range storage.Feeds(context.Background()) {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			err := updateFeed(client, link, storage)
			if err != nil {
				log.Println(err)
			}
		}(feed.Link)
	}

	wg.Wait()

	log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
}

func fetchWaybackEntry(client *http.Client, storage *Storage) error {
	url, snapshot, err := storage.getLatestSnapshot(context.Background())

	// NOTE(simon): No snapshots left? Quit
	if err != nil{
		return fmt.Errorf("failed to get latest snapshot: %w\n", err)
	}

	log.Printf("Fetching snapshot %v@%v\n", url, snapshot)

	response, err := wayback.FetchSnapshot(client, url, snapshot)

	// NOTE(simon): We failed to fetch the entry, requeue it for later processing.
	if err != nil {
		return fmt.Errorf("failed to fetch snapshot %v: %w", url, err)
	}
	defer response.Body.Close()

	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch snapshot %v: %v", url, response.Status)
	}

	feed, entries, err := feedparse.Parse(response.Body, url)

	// NOTE(simon): Failing to parse historic entries cannot be recovered.
	if err != nil {
		return fmt.Errorf("failed to parse feed %v: %w", url, err)
	}

	storage.storeFeedSnapshot(context.Background(), snapshot, feed, entries)
	return nil
}

func update(client *http.Client, storage *Storage) {
	updateFeedsTick       := time.Tick(24 * time.Hour)
	fetchWaybackEntryTick := time.Tick(30 * time.Second)

	for {
		select {
		case <- updateFeedsTick:
			updateFeeds(client, storage)
		case <- fetchWaybackEntryTick:
			err := fetchWaybackEntry(client, storage)
			if err != nil {
				log.Println(err)
			}
		}
	}
}



type UserFeed struct {
	Url        string
	DaysToKeep int
}

type Config struct {
	Host  string
	Port  int
	Feeds []UserFeed
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
		log.Fatal(fmt.Errorf("failed to read config: %w", err))
	}

	storage, err := createStorage()
	if err != nil {
		log.Fatal(fmt.Errorf("failed to create storage: %w", err))
	}

	client := http.Client{
		Transport: gzhttp.Transport(http.DefaultTransport),
	}

	if true {
	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, feed := range config.Feeds {
		go updateFeed(&client, feed.Url, storage)
	}

	go func () {
		for _, feed := range config.Feeds {
			timeBeforePoll := time.Now()
			lastPollTime, err := storage.getLatestSnapshotTime(context.Background(), feed.Url)
			if err != nil {
				log.Println("failed to get latest snapshot %v: %w", feed.Url, err)
				continue
			}

			log.Printf("Fetching snapshots for %v\n", feed.Url)
			snapshots, err := wayback.QuerySnapshots(&client, feed.Url, lastPollTime)
			if err != nil {
				log.Printf("failed to fetch snapshots %v: %v\n", feed.Url, err)
				continue
			}
			log.Printf("Got %v new snapthots for %v\n", len(snapshots), feed.Url)

			err = storage.addSnapshots(context.Background(), feed.Url, timeBeforePoll, snapshots)
			if err != nil {
				log.Printf("failed to add snapshots %v: %v\n", feed.Url, err)
			}
		}
	}()

	// NOTE(simon): Start feed update process.
	go update(&client, storage)
	}

	runFrontend(*reload, config, storage)
}
