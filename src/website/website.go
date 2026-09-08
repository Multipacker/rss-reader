package website

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/feeds"
	"Multipacker/rss-reader/src/httphelpers"
	"Multipacker/rss-reader/src/jobs"
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

	dbConnection, err := db.New()
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

			parsedFeed, entries, err := feedparse.Parse(response.Body, feed.Url)
			if err != nil {
				log.Printf("feed parse %v: %v\n", feed.Url, err)
				return
			}

			err = feeds.StoreFeed(context.Background(), dbConnection, parsedFeed, entries)
			if err != nil {
				log.Println(err)
				return
			}

			_, err = dbConnection.Exec(context.Background(), "UPDATE Feeds SET daysToKeep = $1 WHERE feedUrl = $2", feed.DaysToKeep, parsedFeed.FeedUrl)
			if err != nil {
				log.Println(err)
				return
			}
		}()
	}

	var wg sync.WaitGroup

	// NOTE(simon): Start background jobs.
	wg.Add(1)
	backgroundJobs := jobs.Jobs{
		feeds.UpdateFeedsJob(&client, dbConnection),
		feeds.DeleteOldEntriesJob(dbConnection),
		wayback.FetchWaybackSnapshotsJob(&client, dbConnection),
		wayback.FetchWaybackEntriesJob(&client, dbConnection),
	}

	// NOTE(simon): Start HTTP server.
	wg.Add(1)
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.Host, config.Port),
		Handler: NewWebsiteRoutes(*reload, config, dbConnection),
	}
	go func() {
		log.Printf("Serving on http://%s\n", server.Addr)
		err := server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Server shut down unexpectedly: %v\n", err)
		}
	}()

	// NOTE(simon): Wait for SIGINT in the background and trigger graceful shut down.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		// NOTE(simon): Start shut down.
		<-signals
		log.Println("Shutting down")

		timeout := 10 * time.Second

		// NOTE(simon): Shut down background jobs.
		go func() {
			unfinished := backgroundJobs.CancelAndWait(timeout)
			if len(unfinished) == 0 {
				log.Println("Background jobs closed gracefully")
			} else {
				log.Printf("Background jobs did not finish by the deadline: %v\n", strings.Join(unfinished, ", "))
			}
			wg.Done()
		}()

		// NOTE(simon): Shut down the HTTP server.
		go func() {
			timeoutContext, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			err := server.Shutdown(timeoutContext)
			if err != nil {
				log.Printf("Server did not shut down gracefully: %v\n", err)
			}
			wg.Done()
		}()

		// NOTE(simon): Force quit.
		<-signals
		log.Printf("Forcibly killed the website, unfinished background jobs: %v\n", strings.Join(backgroundJobs.ListUnfinished(), ", "))
		os.Exit(1)
	}()

	wg.Wait()
}
