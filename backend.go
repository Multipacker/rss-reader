package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"Multipacker/rss-reader/internal/feedparse"
	"Multipacker/rss-reader/internal/wayback"
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



var (
	allFeeds   sync.Map
	allEntries sync.Map
	allWaybackSnapshots []wayback.Snapshot
	waybackSnapshotPoints map[string]time.Time
	waybackSnapshotLock sync.Mutex
)

func jsonFromFeeds() ([]byte, error) {
	// NOTE(simon): Collect all feeds.
	var feeds []feedparse.Feed
	for _, feedInstance := range allFeeds.Range {
		feed := feedInstance.(feedparse.Feed)
		feeds = append(feeds, feed)
	}

	return json.Marshal(feeds)
}

func jsonFromEntries() ([]byte, error) {
	// NOTE(simon): Collect all entries.
	var entries []feedparse.Entry
	for _, entryInstance := range allEntries.Range {
		entry := entryInstance.(feedparse.Entry)
		entries = append(entries, entry)
	}

	return json.Marshal(entries)
}

func jsonFromSnapshots() ([]byte, error) {
	waybackSnapshotLock.Lock()
	encoded, err := json.Marshal(allWaybackSnapshots)
	waybackSnapshotLock.Unlock()

	return encoded, err
}

func jsonFromSnapshotPoints() ([]byte, error) {
	waybackSnapshotLock.Lock()
	encoded, err := json.Marshal(waybackSnapshotPoints)
	waybackSnapshotLock.Unlock()

	return encoded, err
}



type Config struct {
	Host            string
	Port            int
	Urls            []string
	OutputDirectory string
}

func storeFeed(feed feedparse.Feed, entries []feedparse.Entry, config Config) {
	// NOTE(simon): Update stores.
	// TODO(simon): Only do this if the date is newer.
	allFeeds.Store(feed.Id, feed)

	// NOTE(simon): Update entries.
	for _, newEntry := range entries {
		updateEntry := true

		// NOTE(simon): Merge with existing entry (keep the publish date).
		if entryInstance, hasEntry := allEntries.Load(newEntry.Id); hasEntry {
			oldEntry := entryInstance.(feedparse.Entry)

			newEntry.Published = oldEntry.Published
			updateEntry = oldEntry.Updated.Before(newEntry.Updated)
		}

		if updateEntry {
			allEntries.Store(newEntry.Id, newEntry)
		}
	}

	// NOTE(simon): Serialize to disk.
	encodedFeeds,   feedsErr   := jsonFromFeeds()
	encodedEntries, entriesErr := jsonFromEntries()
	if feedsErr == nil {
		feedsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "feeds.json"), encodedFeeds)
		if feedsErr == nil && entriesErr == nil {
			entriesErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "entries.json"), encodedEntries)
		}
	}

	if feedsErr != nil {
		log.Printf("ERROR: Could not save feeds: %v\n", feedsErr)
	}
	if entriesErr != nil {
		log.Printf("ERROR: Could not save entries: %v\n", entriesErr)
	}
}

func updateFeed(url string, config Config) error {
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

	storeFeed(feed, entries, config)

	return nil
}

func atomicWriteFile(file string, data []byte) (err error) {
	directory, _ := filepath.Split(file)

	tempFile, err := os.CreateTemp(directory, "temp-*.json")
	if err != nil {
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err = tempFile.Write(data); err != nil {
		return
	}
	if err = tempFile.Sync(); err != nil {
		return
	}
	if err = tempFile.Close(); err != nil {
		return
	}

	err = os.Rename(tempFile.Name(), file)

	return
}

func updateFeeds(config Config) {
	log.Println("INFO: Updating feeds")
	beforeUpdate := time.Now()

	// NOTE(simon): Dispatch updates to all feeds.
	var wg sync.WaitGroup
	for _, feedInstance := range allFeeds.Range {
		feed := feedInstance.(feedparse.Feed)

		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			err := updateFeed(link, config)
			if err != nil {
				log.Println(err)
			}
		}(feed.Link)
	}

	wg.Wait()

	log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
}

func fetchWaybackEntry(config Config) {
	// NOTE(simon): Pop the latest snapshot.
	var snapshot wayback.Snapshot
	waybackSnapshotLock.Lock()
	snapshotCount := len(allWaybackSnapshots)
	if snapshotCount > 0 {
		snapshot = allWaybackSnapshots[snapshotCount - 1]
		allWaybackSnapshots = allWaybackSnapshots[:snapshotCount - 1]
	}
	waybackSnapshotLock.Unlock()

	// NOTE(simon): No snapshots left? Quit
	if snapshot.Date == "" {
		return
	}

	log.Printf("Fetching snapshot %v@%v\n", snapshot.Url, snapshot.Date)

	response, err := wayback.FetchSnapshot(snapshot.Url, snapshot.Date)

	// NOTE(simon): We failed to fetch the entry, requeue it for later processing.
	if err != nil {
		log.Printf("ERROR %v (%v): %v\n", snapshot.Url, snapshot.Date, err)
		waybackSnapshotLock.Lock()
		i, _ := slices.BinarySearchFunc(allWaybackSnapshots, snapshot, func (a, b wayback.Snapshot) int {
			return strings.Compare(a.Date, b.Date)
		})
		allWaybackSnapshots = slices.Insert(allWaybackSnapshots, i, snapshot)
		waybackSnapshotLock.Unlock()
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

	storeFeed(feed, entries, config)

	encodedSnapshots, snapshotsErr := jsonFromSnapshots()
	if snapshotsErr == nil {
		snapshotsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "snapshots.json"), encodedSnapshots)
	}
	if snapshotsErr != nil {
		log.Printf("ERROR: Could not save snapshots: %v\n", snapshotsErr)
	}
}

func update(config Config) {
	updateFeedsTick       := time.Tick(24 * time.Hour)
	fetchWaybackEntryTick := time.Tick(30 * time.Second)

	for {
		select {
		case <- updateFeedsTick:
			updateFeeds(config)
		case <- fetchWaybackEntryTick:
			fetchWaybackEntry(config)
		}
	}
}



//go:embed all:static
var staticFiles embed.FS

func handleFeeds(w http.ResponseWriter, request *http.Request) {
	encoded, err := jsonFromFeeds()

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

func handleEntries(w http.ResponseWriter, request *http.Request) {
	encoded, err := jsonFromEntries()

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

func middlewareLogging(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		logger.Printf("\"%v %v %v\" \"%v\" %v\n", r.Method, r.URL.Path, r.Proto, r.UserAgent(), r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
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
		log.Fatal(err)
	}

	// NOTE(simon): Ensure that the output directory exists.
	if config.OutputDirectory != "" {
		if err := os.MkdirAll(config.OutputDirectory, 0755); err != nil {
			log.Fatal(err)
		}
	}

	// NOTE(simon): Load old feeds, entries, and snapshots.
	if feedsContent, err := os.ReadFile(filepath.Join(config.OutputDirectory, "feeds.json")); err == nil {
		var feeds []feedparse.Feed
		if err := json.Unmarshal(feedsContent, &feeds); err != nil {
			log.Fatal(err)
		}

		for _, feed := range feeds {
			allFeeds.Store(feed.Id, feed)
		}
	} else if _, ok := err.(*os.PathError); !ok {
		log.Fatal(err)
	}
	if entriesContent, err := os.ReadFile(filepath.Join(config.OutputDirectory, "entries.json")); err == nil {
		var entries []feedparse.Entry
		if err := json.Unmarshal(entriesContent, &entries); err != nil {
			log.Fatal(err)
		}

		for _, entry := range entries {
			allEntries.Store(entry.Id, entry)
		}
	} else if _, ok := err.(*os.PathError); !ok {
		log.Fatal(err)
	}
	if snapshotsContent, err := os.ReadFile(filepath.Join(config.OutputDirectory, "snapshots.json")); err == nil {
		var snapshots []wayback.Snapshot
		if err := json.Unmarshal(snapshotsContent, &snapshots); err != nil {
			log.Fatal(err)
		}

		allWaybackSnapshots = snapshots
	} else if _, ok := err.(*os.PathError); !ok {
		log.Fatal(err)
	}
	if snapshotPointsContent, err := os.ReadFile(filepath.Join(config.OutputDirectory, "snapshotPoints.json")); err == nil {
		var snapshotPoints map[string]time.Time
		if err := json.Unmarshal(snapshotPointsContent, &snapshotPoints); err != nil {
			log.Fatal(err)
		}

		waybackSnapshotPoints = snapshotPoints
	} else if _, ok := err.(*os.PathError); !ok {
		log.Fatal(err)
	} else {
		waybackSnapshotPoints = make(map[string]time.Time)
	}

	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, link := range config.Urls {
		go updateFeed(link, config)
	}

	go func () {
		for _, link := range config.Urls {
			timeBeforePoll := time.Now()

			waybackSnapshotLock.Lock()
			lastPollTime := waybackSnapshotPoints[link]
			waybackSnapshotLock.Unlock()

			log.Printf("Fetching snapshots for %v\n", link)
			snapshots, err := wayback.QuerySnapshots(link, lastPollTime)
			if err != nil {
				log.Printf("ERROR %v: Failed to fetch snapshots %v\n", link, err)
				continue
			}
			log.Printf("Got %v new snapthots for %v\n", len(snapshots), link)

			slices.SortFunc(snapshots, func (a, b wayback.Snapshot) int {
				return strings.Compare(a.Date, b.Date)
			})

			waybackSnapshotLock.Lock()
			merged := []wayback.Snapshot{}
			i, j := 0, 0
			for i < len(allWaybackSnapshots) && j < len(snapshots) {
				if allWaybackSnapshots[i].Date < snapshots[j].Date {
					merged = append(merged, allWaybackSnapshots[i])
					i += 1
				} else {
					merged = append(merged, snapshots[j])
					j += 1
				}
			}

			merged = append(merged, allWaybackSnapshots[i:]...)
			merged = append(merged, snapshots[j:]...)

			allWaybackSnapshots = merged

			waybackSnapshotPoints[link] = timeBeforePoll
			waybackSnapshotLock.Unlock()
		}

		encodedSnapshots, snapshotsErr := jsonFromSnapshots()
		if snapshotsErr == nil {
			snapshotsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "snapshots.json"), encodedSnapshots)
		}
		if snapshotsErr != nil {
			log.Printf("ERROR: Could not save snapshots: %v\n", snapshotsErr)
		}

		encodedSnapshotPoints, snapshotPointsErr := jsonFromSnapshotPoints()
		if snapshotPointsErr == nil {
			snapshotPointsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "snapshotPoints.json"), encodedSnapshotPoints)
		}
		if snapshotPointsErr != nil {
			log.Printf("ERROR: Could not save snapshot points: %v\n", snapshotPointsErr)
		}
	}()

	// NOTE(simon): Start feed update process.
	go update(config)

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

	address := fmt.Sprintf("%s:%d", config.Host, config.Port)
	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, middlewareLogging(log.Default(), http.DefaultServeMux)); err != nil {
		log.Fatal(err)
	}
}
