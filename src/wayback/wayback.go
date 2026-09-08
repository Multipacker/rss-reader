package wayback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"Multipacker/rss-reader/src/db"
	"Multipacker/rss-reader/src/feedparse"
	"Multipacker/rss-reader/src/feeds"
	"Multipacker/rss-reader/src/jobs"
	"Multipacker/rss-reader/src/models"

	"github.com/jackc/pgx/v5"
)

const (
	TimeFormat string = "20060102150405"
)

func urlFromSnapshot(snapshot models.FeedSnapshot) string {
	return "https://web.archive.org/web/" + snapshot.Timestamp.Format(TimeFormat) + "id_/" + snapshot.Url
}

func urlFromFeed(feedUrl string, lastPollTime time.Time, resumeKey string) string {
	// NOTE(simon): Always valid so skip the error.
	requestUrl, _ := url.Parse("http://web.archive.org/cdx/search/cdx")

	// NOTE(simon): Filtering on mimetypes was problematic during testing, so
	// we avoid it. I could not get it to filter for multiple mimetypes
	// simultaneously, and only filtering for one would require us to do more
	// queries. Better to do it ourselves.
	values := url.Values{}
	values.Set("fl", "timestamp,mimetype")
	values.Add("filter", "statuscode:200")
	values.Set("showResumeKey", "true")
	values.Set("output", "json")
	values.Set("url", feedUrl)
	if !lastPollTime.IsZero() {
		values.Set("from", lastPollTime.UTC().Format(TimeFormat))
	}
	if resumeKey != "" {
		values.Set("resumeKey", resumeKey)
	}

	requestUrl.RawQuery = values.Encode()

	return requestUrl.String()
}

func querySnapshots(client *http.Client, feedUrl string, lastPollTime time.Time) ([]time.Time, error) {
	var snapshots []time.Time
	for url := urlFromFeed(feedUrl, lastPollTime, ""); url != ""; {
		response, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("failed to perform http request: %w", err)
		}
		defer response.Body.Close()

		// NOTE(simon): Parse format, just an array of records, which is an
		// array of fields.
		var records [][]string
		err = json.NewDecoder(response.Body).Decode(&records)
		if err != nil {
			return nil, fmt.Errorf("failed to parse snapshots: %w", err)
		}
		response.Body.Close()

		if len(records) == 0 {
			break
		}

		// NOTE(simon): Parse records. Skip the header describing the fields,
		// we request them in a specific order anyway.
		nextUrl := ""
		for _, line := range records[1:] {
			if len(line) == 1 {
				// NOTE(simon): Resume keys are identified by the second last
				// record being empty and the last one containing the resume
				// key. We assume that any line that only has one entry is the
				// resume key.
				nextUrl = urlFromFeed(feedUrl, lastPollTime, line[0])
			} else if len(line) == 2 {
				// NOTE(simon): Just skip entries that don't have an accepted
				// mimetype or were we cannot parse the timestamp.
				timestamp, err := time.Parse(TimeFormat, line[0])
				mimetype       := line[1]
				if err == nil && feedparse.IsAccpetedMimeType(mimetype) {
					snapshots = append(snapshots, timestamp)
				}
			}
		}

		url = nextUrl
	}

	return snapshots, nil
}

func fetchWaybackEntry(client *http.Client, context context.Context, dbConnection db.Database) (models.FeedSnapshot, error) {
	// NOTE(simon): Fetch the latest snapshot.
	snapshot, err := db.QueryOne[models.FeedSnapshot](
		context,
		dbConnection,
		"SELECT id, feedUrl AS url, timestamp FROM UnfetchedSnapshots JOIN Feeds USING (id) ORDER BY timestamp DESC LIMIT 1",
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.FeedSnapshot{}, nil
	}
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to get latest snapshot: %w", err)
	}

	response, err := client.Get(urlFromSnapshot(snapshot))
	if err != nil {
		return models.FeedSnapshot{}, fmt.Errorf("failed to fetch snapshot: %w", err)
	}

	defer response.Body.Close()

	// NOTE(simon): On a bad response we just skip this URL.
	if response.StatusCode != http.StatusOK {
		return models.FeedSnapshot{}, fmt.Errorf("failed to fetch snapshot: %w", response.Status)
	}

	feed, entries, err := feedparse.Parse(response.Body, snapshot.Url)
	if err != nil {
		// NOTE(simon): Failing to parse historic entries cannot be recovered,
		// remove the snapshot.
		_, removeErr := dbConnection.Exec(
			context,
			"DELETE FROM UnfetchedSnapshots WHERE id = $1 AND timestamp = $2",
			snapshot.Id,
			snapshot.Timestamp,
		)
		if removeErr != nil {
			return models.FeedSnapshot{}, fmt.Errorf("failed to parse feed; unable to remove snapshot: %w", err)
		} else {
			return models.FeedSnapshot{}, fmt.Errorf("failed to parse feed; removing snapshot: %w", err)
		}
	}

	err = db.Transaction(context, dbConnection, func (dbConnection db.Database) error {
		_, err := dbConnection.Exec(
			context,
			"DELETE FROM UnfetchedSnapshots WHERE id = $1 AND timestamp = $2",
			snapshot.Id,
			snapshot.Timestamp,
		)
		if err != nil {
			return fmt.Errorf("failed to remove snapshot: %w", err)
		}

		err = feeds.StoreFeed(context, dbConnection, feed, entries)
		if err != nil {
			return fmt.Errorf("failed to store feed: %w", err)
		}

		return nil
	})
	if err != nil {
		return models.FeedSnapshot{}, err
	}

	return snapshot, nil
}

func FetchWaybackEntriesJob(client *http.Client, dbConnection db.Database) *jobs.Job {
	job := jobs.NewPeriodic("fetch wayback entries", 30 * time.Second, func(job *jobs.Job) {
		snapshot, err := fetchWaybackEntry(client, job.Context, dbConnection)
		if err != nil {
			job.Logger.Printf("failed to fetch %v: %v\n", urlFromSnapshot(snapshot), err)
		} else if !snapshot.Timestamp.IsZero() {
			job.Logger.Printf("Fetched %v\n", urlFromSnapshot(snapshot))
		}
	})
	return job
}

func fetchSnapshots(client *http.Client, context context.Context, dbConnection db.Database, feed models.Feed) (int, error) {
	timeBeforePoll := time.Now()

	// NOTE(simon): Query snapshots since last query.
	snapshots, err := querySnapshots(client, feed.FeedUrl, feed.SnapshotTime)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch snapshots: %v", err)
	}

	// NOTE(simon): Build all updates into a batch (implicit transaction).
	batch := pgx.Batch{}
	batch.Queue("UPDATE Feeds SET snapshotTime = $1 WHERE id = $2", timeBeforePoll, feed.Id)
	for _, snapshot := range snapshots {
		batch.Queue("INSERT INTO UnfetchedSnapshots VALUES ($1, $2) ON CONFLICT DO NOTHING", feed.Id, snapshot)
	}

	err = dbConnection.SendBatch(context, &batch).Close()
	if err != nil {
		return 0, fmt.Errorf("failed to store snapshots: %w", err)
	}

	return len(snapshots), nil
}

func FetchWaybackSnapshotsJob(client *http.Client, dbConnection db.Database) *jobs.Job {
	job := jobs.NewPeriodic("fetch wayback snapshots", 30 * 24 * time.Hour, func(job *jobs.Job) {
		job.Logger.Println("Fetching snapshots")
		feeds, err := db.Query[models.Feed](job.Context, dbConnection, "SELECT * FROM Feeds WHERE daysToKeep = 0")
		if err != nil {
			job.Logger.Printf("Failed to fetch feeds: %v", err)
			return
		}

		for _, feed := range feeds {
			job.Logger.Printf("Fetching snapshots for %v\n", feed.FeedUrl)
			snapshotCount, err := fetchSnapshots(client, job.Context, dbConnection, feed)
			if err != nil {
				job.Logger.Printf("Failed to fetch snapshots for %v: %v\n", feed.FeedUrl, err)
			} else {
				job.Logger.Printf("Fetched %v new snapthots for %v\n", snapshotCount, feed.FeedUrl)
			}
		}
	})
	return job
}
