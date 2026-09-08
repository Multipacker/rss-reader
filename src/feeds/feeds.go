package feeds

import (
	"context"
	"fmt"
	"net/http"
	"time"
	"sync"
	"log"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/jobs"
	"Multipacker/rss-reader/src/models"

	"github.com/jackc/pgx/v5"
)

func StoreFeed(context context.Context, dbConnection db.Database, feed feedparse.Feed, entries []feedparse.Entry) error {
	// NOTE(simon): Build all updates into a batch (implicit transaction).
	batch := pgx.Batch{}
	batch.Queue(
		`INSERT INTO Feeds (externalId, title, description, feedUrl, url, updated) VALUES (@id, @title, @description, @feedUrl, @url, @updated)
		ON CONFLICT (externalId) DO UPDATE SET title = @title, description = @description, feedUrl = @feedUrl, url = @url, updated = @updated WHERE Feeds.updated < @updated`,
		pgx.NamedArgs{
			"id":          feed.Id,
			"title":       feed.Title,
			"description": feed.Description,
			"feedUrl":     feed.FeedUrl,
			"url":         feed.Url,
			"updated":     feed.Updated,
		},
	)
	for _, entry := range entries {
		batch.Queue(
			`INSERT INTO Entries (feed, externalId, title, url, published, updated) VALUES ((SELECT id FROM Feeds WHERE externalId = @feed), @id, @title, @url, @published, @updated)
			ON CONFLICT (externalId) DO UPDATE SET title = @title, url = @url, updated = @updated WHERE Entries.updated < @updated`,
			pgx.NamedArgs{
				"id":          entry.Id,
				"feed":        feed.Id,
				"title":       entry.Title,
				"url":         entry.Url,
				"published":   entry.Published,
				"updated":     entry.Updated,
			},
		)
	}

	err := dbConnection.SendBatch(context, &batch).Close()
	if err != nil {
		return fmt.Errorf("failed to store feed: %v", err)
	}

	return nil
}

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

func updateFeed(client *http.Client, context context.Context, dbConnection db.Database, url string) error {
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

	err = StoreFeed(context, dbConnection, feed, entries)
	if err != nil {
		return err
	}

	return nil
}

func UpdateFeedsJob(client *http.Client, dbConnection db.Database) *jobs.Job {
	job := jobs.NewPeriodic("update feeds", 24 * time.Hour, func (job *jobs.Job) {
		job.Logger.Println("Updating feeds")
		beforeUpdate := time.Now()

		feeds, err := db.Query[models.Feed](job.Context, dbConnection, "SELECT * FROM Feeds")
		if err != nil {
			job.Logger.Printf("Failed to query feeds: %v\n", err)
			return
		}

		// NOTE(simon): Dispatch updates to all feeds.
		var wg sync.WaitGroup
		for _, feed := range feeds {
			wg.Add(1)
			go func(link string) {
				defer wg.Done()
				err := updateFeed(client, job.Context, dbConnection, link)
				if err != nil {
					job.Logger.Printf("Failed to update feed %v: %v", link, err)
				}
			}(feed.FeedUrl)
		}

		wg.Wait()

		job.Logger.Printf("Feed updated finished: %s\n", time.Since(beforeUpdate))
	})
	return job
}

func DeleteOldEntriesJob(dbConnection db.Database) *jobs.Job {
	job := jobs.NewPeriodic("remove old entries", 24 * time.Hour, func (job *jobs.Job) {
		tag, err := dbConnection.Exec(
			job.Context,
			`
			DELETE FROM Entries
			WHERE
				id IN (
					SELECT Entries.id FROM Entries JOIN Feeds ON (feed = Feeds.id)
					WHERE
						daysToKeep != 0 AND CURRENT_TIMESTAMP - published >= make_interval(days => daysToKeep)
				)
			`,
		)
		if err != nil {
			job.Logger.Printf("Failed to delete old entries: %v\n", err)
		} else {
			job.Logger.Printf("Removed %v rows\n", tag.RowsAffected())
		}
	})
	return job
}
