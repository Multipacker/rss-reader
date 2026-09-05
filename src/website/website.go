package website

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"Multipacker/rss-reader/src/feeds"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/httphelpers"
	"Multipacker/rss-reader/src/migration"
	migrationTypes "Multipacker/rss-reader/src/migration/types"
	"Multipacker/rss-reader/src/wayback"

	"github.com/klauspost/compress/gzhttp"
)

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

func Execute() {
	// NOTE(simon): Configure the logger to give more accurate timing information.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// NOTE(simon): Parse command line arguments.
	reload := flag.Bool("reload", false, "reload static files on page refresh")
	flag.Parse()

	config, err := readConfig()
	if err != nil {
		log.Fatal(fmt.Errorf("failed to read config: %w", err))
	}

	dbConnection, err := createStorage()
	if err != nil {
		log.Fatal(fmt.Errorf("failed to create storage: %w", err))
	}

	err = migration.Migrate(dbConnection, migrationTypes.MigrationVersion{})
	if err != nil {
		log.Fatal(fmt.Errorf("failed to migrate database: %w", err))
	}

	client := http.Client{
		Transport: gzhttp.Transport(httphelpers.RetryRoundTripper(5, 100 * time.Millisecond, httphelpers.UserAgentRoundTripper("SilverFeed/1.0", http.DefaultTransport))),
	}

	// NOTE(simon): Fetch initial feeds
	log.Println("Fetching feeds from config")
	for _, feed := range config.Feeds {
		go func() {
			response, err := client.Get(feed.Url)
			if err != nil {
				log.Println(err)
				return
			}
			defer response.Body.Close()

			parseFeed, entries, err := feedparse.Parse(response.Body, feed.Url)
			if err != nil {
				log.Printf("feed parse %v: %v\n", feed.Url, err)
				return
			}

			err = feeds.StoreFeed(context.Background(), dbConnection, parseFeed, entries)
			if err != nil {
				log.Println(err)
				return
			}
		}()
	}

	// NOTE(simon): Start feed update process.
	wayback.FetchWaybackSnapshotsJob(&client, dbConnection)
	feeds.UpdateFeedsJob(&client, dbConnection)
	wayback.FetchWaybackEntriesJob(&client, dbConnection)

	handler := NewWebsiteRoutes(*reload, config, dbConnection)

	address := fmt.Sprintf("%s:%d", config.Host, config.Port)
	log.Printf("INFO: Serving on http://%s", address)
	if err := http.ListenAndServe(address, handler); err != nil {
		log.Fatal(err)
	}
}
