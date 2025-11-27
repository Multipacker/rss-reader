package main

import (
	"encoding/xml"
	"fmt"
	"log"
	"time"
	"strings"
	"net/http"
)

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
	Title     string
	Id        string
	Published time.Time
	Updated   time.Time
	Link      string
}

type Feed struct {
	Title       string
	Description string
	Id          string
	Link        string
	Updated     time.Time
	Entries     []Entry
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

func main() {
	// Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	urls := []string{
        "https://nullprogram.com/feed/",
        "https://fgiesen.wordpress.com/feed/",
        "https://tonsky.me/atom.xml",
        "https://xeiaso.net/blog.rss",
        "https://jakubtomsu.github.io/index.xml",
        "https://ashhhleyyy.dev/blog.rss",
        "https://caseymuratori.com/blog_atom.rss",
        "https://cbloomrants.blogspot.com/feeds/posts/default",
        "https://drewdevault.com/blog/index.xml",
        "https://www.eno-writer.com/feed/",
        "https://fabiensanglard.net/rss.xml",
        "https://acorneroftheweb.com/index.xml",
        "https://www.internalpointers.com/rss",
        "https://calabro.io/rss",
        "https://loganforman.com/rss.xml",
        "https://www.newroadoldway.com/feed.xml",
        "https://nrk.neocities.org/rss.xml",
        "https://orlp.net/blog/atom.xml",
        "https://preshing.com/feed",
        "https://probablydance.com/feed/",
        "https://rachel.cafe/feed.xml",
        "https://floooh.github.io/feed.xml",
        "https://xorvoid.com/rss.xml",
	}

	var feeds []Feed

	for _, url := range urls {
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
					entry.Title = item.Title
					entry.Link  = item.Link
					if item.Guid != "" {
						entry.Id = item.Guid
					} else {
						entry.Id = entry.Link
					}
					entry.Published = parseRssDateOrNow(item.PubDate)
					entry.Updated   = entry.Published

					feed.Entries = append(feed.Entries, entry)
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

				for _, atomEntry := range atomFeed.Entries {
					var entry Entry
					entry.Title = atomEntry.Title
					entry.Id    = atomEntry.Id
					for _, link := range atomEntry.Links {
						if link.Rel == "alternate" || link.Rel == "" {
							entry.Link = link.Href
							break
						}
					}

					entry.Published = parseAtomDateOrNow(atomEntry.Published)
					entry.Updated   = parseAtomDateOrNow(atomEntry.Updated)

					feed.Entries = append(feed.Entries, entry)
				}

				feeds = append(feeds, feed)
			default:
				log.Printf("Unknown feed type \"%v\"\n", startToken.Name.Local)
			}
		} else {
			log.Print(err)
		}
	}

	for _, feed := range feeds {
		fmt.Printf("title:       %v\n", feed.Title)
		fmt.Printf("description: %v\n", feed.Description)
		fmt.Printf("id:          %v\n", feed.Id)
		fmt.Printf("link:        %v\n", feed.Link)
		fmt.Printf("updated:     %v\n", feed.Updated)
		fmt.Printf("Entries:\n")
		for _, entry := range feed.Entries {
			fmt.Printf("\ttitle: %v\n", entry.Title)
			fmt.Printf("\t\tid:        %v\n", entry.Id)
			fmt.Printf("\t\tpublished: %v\n", entry.Published)
			fmt.Printf("\t\tupdated:   %v\n", entry.Updated)
			fmt.Printf("\t\tlink:      %v\n", entry.Link)
		}
	}

	/*
	// Build the address to listen on from environment variables ADDR and PORT.
	host := "localhost"
	port := "8080"
	if hostEnv, ok := os.LookupEnv("ADDR"); ok {
		host = hostEnv
	}
	if portEnv, ok := os.LookupEnv("PORT"); ok {
		port = portEnv
	}
	address := fmt.Sprintf("%s:%s", host, port)

	// Configure the routes.
	fileServer := http.FileServer(http.Dir("static"))
	http.Handle("/", fileServer)

	// Start the server with the configured address and the handlers configured above.
	log.Printf("Serving on http://%s", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		log.Fatal(err)
	}
	*/
}
