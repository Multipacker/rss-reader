package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
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

	storage.storeFeed(feed, entries)

	return nil
}

func updateFeeds(client *http.Client, storage *Storage) {
	log.Println("INFO: Updating feeds")
	beforeUpdate := time.Now()

	// NOTE(simon): Dispatch updates to all feeds.
	var wg sync.WaitGroup
	for _, feed := range storage.Feeds() {
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

func fetchWaybackEntry(client *http.Client, storage *Storage) {
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

	response, err := wayback.FetchSnapshot(client, snapshot.Url, snapshot.Date)

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

func update(client *http.Client, storage *Storage) {
	updateFeedsTick       := time.Tick(24 * time.Hour)
	fetchWaybackEntryTick := time.Tick(30 * time.Second)

	for {
		select {
		case <- updateFeedsTick:
			updateFeeds(client, storage)
		case <- fetchWaybackEntryTick:
			fetchWaybackEntry(client, storage)
		}
	}
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

	client := http.Client{
		Transport: gzhttp.Transport(http.DefaultTransport),
	}

	if true {
	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, link := range config.Urls {
		go updateFeed(&client, link, storage)
	}

	go func () {
		for _, link := range config.Urls {
			timeBeforePoll := time.Now()
			lastPollTime := storage.getLatestSnapshotTime(link)

			log.Printf("Fetching snapshots for %v\n", link)
			snapshots, err := wayback.QuerySnapshots(&client, link, lastPollTime)
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
	go update(&client, storage)
	}

	runFrontend(*reload, config, storage)
}
