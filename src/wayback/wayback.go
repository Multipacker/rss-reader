package wayback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/feeds"
	"Multipacker/rss-reader/src/httphelpers"
	"Multipacker/rss-reader/src/jobs"
	"Multipacker/rss-reader/src/models"

	"github.com/jackc/pgx/v5"
)

const (
	TimeFormat string = "20060102150405"
)

func makeURL(snapshot models.FeedSnapshot) string {
	return "https://web.archive.org/web/" + snapshot.Timestamp.Format(TimeFormat) + "id_/" + snapshot.Url
}

func QuerySnapshots(client *http.Client, feedUrl string, lastPollTime time.Time) ([]time.Time, error) {
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
	if !lastPollTime.IsZero() {
		query.Set("from", lastPollTime.UTC().Format(TimeFormat))
	}

	var snapshots []time.Time
	for {
		// NOTE(simon): Setup request with custom headers.
		requestUrl.RawQuery = query.Encode()

		response, err := httphelpers.GetWithRetry(client, requestUrl.String())
		if err != nil {
			return nil, fmt.Errorf("failed to perform http request: %v", err)
		}
		defer response.Body.Close()

		// NOTE(simon): Parse format, just an array of records, which is an
		// array of fields.
		var records [][]string
		err = json.NewDecoder(response.Body).Decode(&records)
		if err != nil {
			return nil, fmt.Errorf("failed to parse snapshots: %v", err)
		}
		response.Body.Close()

		// NOTE(simon): We need at least two lines to continue: the header, and
		// at least one record.
		if len(records) < 2 {
			break
		}

		// NOTE(simon): Skip the header describing the fields, we request them
		// in a specific order anyway.
		records = records[1:]

		// NOTE(simon): Parse resume key if we have one. It is identified by
		// the second last record being empty and the last one containing the
		// resume key.
		resumeKey := ""
		if len(records) >= 2 {
			footer := records[len(records) - 2:]
			if len(footer[0]) == 0 && len(footer[1]) == 1 {
				resumeKey = footer[1][0]
				records = records[:len(records) - 2]
			}
		}

		// NOTE(simon): Parse records
		for _, line := range records {
			// NOTE(simon): We expect two items per line.
			if len(line) < 2 {
				continue
			}

			timestamp, err := time.Parse(TimeFormat, line[0])
			mimetype       := line[1]
			if err != nil {
				return nil, fmt.Errorf("failed to parse snapshot time: %w", err)
			}

			// NOTE(simon): Do we have a valid mimetype?
			mimeType, mimeSubtype, _ := strings.Cut(mimetype, "/")
			hasValidType := slices.ContainsFunc([]string{ "application", "text" }, func(accepted string) bool {
				return strings.Contains(mimeType, accepted)
			})
			hasValidSubtype := slices.ContainsFunc([]string{ "atom", "rss", "xml" }, func(accepted string) bool {
				return strings.Contains(mimeSubtype, accepted)
			})
			if hasValidType && hasValidSubtype {
				snapshots = append(snapshots, timestamp)
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

	return snapshots, nil
}

func fetchWaybackEntry(client *http.Client, context context.Context, dbConnection db.Database) (models.FeedSnapshot, error) {
	// NOTE(simon): Fetch the latest snapshot.
	snapshot, err := db.QueryOne[models.FeedSnapshot](
		context,
		dbConnection,
		"SELECT url, timestamp FROM UnfetchedSnapshots ORDER BY timestamp DESC LIMIT 1",
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.FeedSnapshot{}, nil
	}
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to get latest snapshot: %w", err)
	}

	response, err := client.Get(makeURL(snapshot))
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to fetch snapshot %v: %w", snapshot.Url, err)
	}

	defer response.Body.Close()

	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		return models.FeedSnapshot{}, fmt.Errorf("failed to fetch snapshot %v: %v", snapshot.Url, response.Status)
	}

	feed, entries, err := feedparse.Parse(response.Body, snapshot.Url)
	if err != nil {
		// TODO(simon): Failing to parse historic entries cannot be recovered, delete snapshot.
		return models.FeedSnapshot{}, fmt.Errorf("failed to parse feed %v: %w", snapshot.Url, err)
	}

	transaction, err := dbConnection.Begin(context)
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer transaction.Rollback(context)
	_, err = transaction.Exec(
		context,
		"DELETE FROM UnfetchedSnapshots WHERE url = @url AND timestamp = @timestamp",
		pgx.NamedArgs{
			"url":       snapshot.Url,
			"timestamp": snapshot.Timestamp,
		},
	)
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to remove snapshot: %w", err)
	}
	feeds.StoreFeed(context, dbConnection, feed, entries)

	err = feeds.StoreFeed(context, dbConnection, feed, entries)
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to store %v: %w", snapshot.Url, err)
	}

	err = transaction.Commit(context)
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to commit transaction: %v", err)
	}

	return snapshot, nil
}

func FetchWaybackEntriesJob(client *http.Client, dbConnection db.Database) *jobs.Job {
	job := jobs.New("fetch wayback entries")
	go func() {
		defer job.Finish()

		tick := time.Tick(30 * time.Second)

		for {
			select {
			case <-tick:
				snapshot, err := fetchWaybackEntry(client, job.Context, dbConnection)
				if err != nil {
					job.Logger.Println(err)
				} else if !snapshot.Timestamp.IsZero() {
					job.Logger.Printf("Fetched %v\n", makeURL(snapshot))
				}
			case <-job.Canceled():
				return
			}
		}
	}()
	return job
}
