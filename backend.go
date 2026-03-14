package main

import (
	"embed"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

func parseFeed(response *http.Response, url string) (feed Feed, entries []Entry, err error) {
	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		err = fmt.Errorf("GET %v", response.Status)
		return
	}

	decoder := xml.NewDecoder(response.Body)

	// NOTE(simon): Find the first start element to determine the kind of feed we have.
	var startToken xml.StartElement
	for startToken.Name.Local == "" {
		var token xml.Token
		token, err = decoder.Token()

		if err != nil {
			return
		}

		switch token := token.(type) {
		case xml.StartElement:
			startToken = token
		}
	}

	// NOTE(simon): Parse the feed based on startToken.
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
			return
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
			return
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
		err = fmt.Errorf("Unknown feed type \"%v\"\n", startToken.Name.Local)
		return
	}

	return
}



type Config struct {
	Host            string
	Port            int
	Urls            []string
	OutputDirectory string
}

var config     Config
var allFeeds   sync.Map
var allEntries sync.Map

func jsonFromFeeds() ([]byte, error) {
	// NOTE(simon): Collect all feeds.
	var feeds []Feed
	for _, feedInstance := range allFeeds.Range {
		feed := feedInstance.(Feed)
		feeds = append(feeds, feed)
	}

	return json.Marshal(feeds)
}

func jsonFromEntries() ([]byte, error) {
	// NOTE(simon): Collect all entries.
	var entries []Entry
	for _, entryInstance := range allEntries.Range {
		entry := entryInstance.(Entry)
		entries = append(entries, entry)
	}

	return json.Marshal(entries)
}

func updateFeed(url string) {
	response, changed, err := pollUrl(url)
	if err != nil {
		log.Printf("ERROR %v: %v\n", url, err)
		return
	}
	defer response.Body.Close()

	// NOTE(simon): If nothing changed, we are done!
	if !changed {
		return
	}

	newFeed, newEntries, err := parseFeed(response, url)
	if err != nil {
		log.Printf("ERROR %v: %v\n", url, err)
		return
	}

	// NOTE(simon): If nothing changed, we are done!
	if !changed {
		return
	}

	// NOTE(simon): Update stores.
	allFeeds.Store(newFeed.Id, newFeed)

	// NOTE(simon): Update entries.
	for _, newEntry := range newEntries {
		updateEntry := true

		// NOTE(simon): Merge with existing entry (keep the publish date).
		if entryInstance, hasEntry := allEntries.Load(newEntry.Id); hasEntry {
			oldEntry := entryInstance.(Entry)

			newEntry.Published = oldEntry.Published
			updateEntry = oldEntry.Updated.Before(newEntry.Updated)
		}

		if updateEntry {
			allEntries.Store(newEntry.Id, newEntry)
		}
	}
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

func update() {
	updateFeedsTick := time.Tick(24 * time.Hour)

	for range updateFeedsTick {
		log.Println("INFO: Updating feeds")
		beforeUpdate := time.Now()

		// NOTE(simon): Dispatch updates to all feeds.
		var wg sync.WaitGroup
		for _, feedInstance := range allFeeds.Range {
			feed := feedInstance.(Feed)

			wg.Add(1)
			go func(link string) {
				defer wg.Done()
				updateFeed(link)
			}(feed.Link)
		}

		wg.Wait()

		// NOTE(simon): Serialize to disk.
		encodedFeeds,   feedsErr   := jsonFromFeeds()
		encodedEntries, entriesErr := jsonFromEntries()
		if feedsErr == nil {
			feedsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "feeds.json"), encodedFeeds)
			if feedsErr == nil && entriesErr == nil {
				feedsErr = atomicWriteFile(filepath.Join(config.OutputDirectory, "entries.json"), encodedEntries)
			}
		}

		if feedsErr != nil {
			log.Printf("ERROR: Could not save feeds: %v\n", feedsErr)
		}
		if entriesErr != nil {
			log.Printf("ERROR: Could not save entries: %v\n", entriesErr)
		}

		log.Printf("INFO: Feed updated finished: %s\n", time.Since(beforeUpdate))
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

func readConfig() {
	configContent, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(configContent, &config); err != nil {
		log.Fatal(err)
	}

	// Validate and set defaults.
	if config.Port == 0 {
		config.Port = 8080
	}
}

func main() {
	// NOTE(simon): Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// feedUrl := "https://fgiesen.wordpress.com/feed/"
	// feedUrl := "https://nullprogram.com/feed/"
	feedUrl := "https://probablydance.com/feed/"
	dates, err := queryWaybackSnapshotDates(feedUrl)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Found %v snapshots\n", len(dates))
	entries := make(map[string]Entry)
	for _, date := range dates {
		log.Printf("Fetching snapshot %v\n", date)
		response, err := fetchWaybackFeed(feedUrl, date)
		if err != nil {
			log.Println(err)
			continue
		}

		_, newEntries, err := parseFeed(response, feedUrl)
		if err != nil {
			log.Println(err)
			continue
		}
		for _, entry := range newEntries {
			entries[entry.Id] = entry
		}
	}
	log.Println(entries)
	return

	// NOTE(simon): Parse command line arguments.
	reload := flag.Bool("reload", false, "reload static files on page refresh")
	flag.Parse()

	readConfig()

	// NOTE(simon): Ensure that the output directory exists.
	if config.OutputDirectory != "" {
		if err := os.MkdirAll(config.OutputDirectory, 0755); err != nil {
			log.Fatal(err)
		}
	}

	// NOTE(simon): Load old feeds and entries.
	if feedsContent, err := os.ReadFile(filepath.Join(config.OutputDirectory, "feeds.json")); err == nil {
		var feeds []Feed
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
		var entries []Entry
		if err := json.Unmarshal(entriesContent, &entries); err != nil {
			log.Fatal(err)
		}

		for _, entry := range entries {
			allEntries.Store(entry.Id, entry)
		}
	} else if _, ok := err.(*os.PathError); !ok {
		log.Fatal(err)
	}

	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, link := range config.Urls {
		go updateFeed(link)
	}

	// NOTE(simon): Start feed update process.
	go update()

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

func fetchWaybackFeed(feedUrl, date string) (response *http.Response, err error) {
	// TODO(simon): Set user agent
	// TODO(simon): Honor 429 Too Many Requests and Retry-After
	response, err = http.Get("https://web.archive.org/web/" + date + "id_/" + feedUrl)
	return
}

func queryWaybackSnapshotDates(feedUrl string) (dates []string, err error) {
	// NOTE(simon): Always valid so skip the error.
	requestUrl, _ := url.Parse("http://web.archive.org/cdx/search/cdx")

	// NOTE(simon): Filtering on mimetypes was problematic during testing, so
	// we avoid it. I could not get it to filter for multiple mimetypes
	// simultaneously, and only filtering for one would require us to do more
	// queries. Better to do it ourselves.
	query := url.Values{}
	query.Set("fl", "timestamp,mimetype")
	query.Add("filter", "statuscode:200")
	query.Set("showResumeKey", "true")
	query.Set("output", "json")
	query.Set("url", feedUrl)

	exponentialBackoffBase  := 1 * time.Minute
	exponentialBackoffTries := 0

	for {
		// NOTE(simon): Setup request with custom headers.
		requestUrl.RawQuery = query.Encode()

		var request *http.Request
		request, err = http.NewRequest("GET", requestUrl.String(), nil)
		if err != nil {
			return
		}

		request.Header.Set("User-Agent", "SilverFeed/1.0")

		// NOTE(simon): Issue request with query.
		var response *http.Response
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()

		// NOTE(simon): Are we fetching too quickly? Take a break.
		if response.StatusCode == http.StatusTooManyRequests {
			response.Body.Close()

			var waitDuration time.Duration
			var retryErr error

			// NOTE(simon): Try to parse the Retry-After header.
			if retryAfter := response.Header["Retry-After"]; len(retryAfter) > 0 {
				retryAfter := retryAfter[0]

				// NOTE(simon): Try to parse it as a duration.
				waitDuration, retryErr = time.ParseDuration(retryAfter + "s")

				if retryErr != nil {
					// NOTE(simon): Try to parse it as a specific date.
					retryDate, retryErr := http.ParseTime(retryAfter)
					if retryErr == nil {
						waitDuration = time.Until(retryDate)
					}
				}
			} else {
				retryErr = fmt.Errorf("Header is not present")
			}

			// NOTE(simon): We somehow failed to parse the date, use exponential backoff.
			if retryErr != nil {
				// NOTE(simon): Arbitrary decision to abort after 5 failed attempts of exponential backoff.
				if exponentialBackoffTries > 5 {
					err = fmt.Errorf("Received %v after %v attempts of exponential backoff", response.Status, exponentialBackoffTries)
					return
				}

				waitDuration = exponentialBackoffBase * (1 << exponentialBackoffTries)
				exponentialBackoffTries += 1
				log.Printf("Received %v but failed to parse Retry-After header: %v. Using exponential backoff of %v.\n", response.Status, retryErr, waitDuration)
			} else {
				log.Printf("Received %v, waiting %v.\n", response.Status, waitDuration)
			}

			// NOTE(simon): Wait and then retry the same request again.
			time.Sleep(waitDuration)
			continue
		} else if response.StatusCode != http.StatusOK {
			err = fmt.Errorf("GET %v", response.Status)
			return
		}

		// NOTE(simon): We got a response! Reset backoff time in the hopes of faster answers.
		exponentialBackoffTries = 0

		// NOTE(simon): Parse format, just an array of records, which is an
		// array of fields.
		var records [][]string
		err = json.NewDecoder(response.Body).Decode(&records)
		if err != nil {
			return
		}
		response.Body.Close()

		recordCount := len(records)

		// NOTE(simon): We need at least two lines to continue: The header, and
		// at least one record.
		if recordCount < 2 {
			break
		}

		// NOTE(simon): Parse resume key if we have one. It is identified by
		// the second last record being empty and the last one containing the
		// resume key.
		resumeKey := ""
		hasResumeKey := len(records[recordCount - 2]) == 0 && len(records[recordCount - 1]) == 1
		if hasResumeKey {
			resumeKey = records[recordCount - 1][0]
			recordCount -= 2
		}

		// NOTE(simon): Parse records, the first line has a header with field
		// names, skip it and the footer with the resume key.
		for _, line := range records[1:recordCount] {
			// NOTE(simon): We expect two items per line.
			if len(line) < 2 {
				continue
			}

			date     := line[0]
			mimetype := line[1]

			// NOTE(simon): Do we have a valid mimetype?
			if strings.Contains(mimetype, "application/xml") || strings.Contains(mimetype, "application/rss") {
				dates = append(dates, date)
			}
		}

		// NOTE(simon): Update query paramters if we have a resume key,
		// otherwise we are done.
		if resumeKey != "" {
			query.Set("resumeKey", resumeKey)
		} else {
			break
		}
	}

	return
}
