package wayback

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type Snapshot struct {
	Url  string
	Date string
}

const (
	TimeFormat string = "20060102150405"
)

func FetchSnapshot(client *http.Client, url, date string) (*http.Response, error) {
	// TODO(simon): Honor 429 Too Many Requests and Retry-After

	request, err := http.NewRequest("GET", "https://web.archive.org/web/" + date + "id_/" + url, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "SilverFeed/1.0")

	response, err := client.Do(request)
	return response, err
}

func QuerySnapshots(client *http.Client, feedUrl string, lastPollTime time.Time) ([]Snapshot, error) {
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

	exponentialBackoffBase  := 1 * time.Minute
	exponentialBackoffTries := 0

	var snapshots []Snapshot
	for {
		// NOTE(simon): Setup request with custom headers.
		requestUrl.RawQuery = query.Encode()
		request, _ := http.NewRequest("GET", requestUrl.String(), nil)
		request.Header.Set("User-Agent", "SilverFeed/1.0")

		// NOTE(simon): Issue request with query.
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("http do: %v", err)
		}
		defer response.Body.Close()

		// NOTE(simon): Are we fetching too quickly? Take a break.
		if response.StatusCode == http.StatusTooManyRequests {
			response.Body.Close()

			// NOTE(simon): Try to parse Retry-After as a duration.
			waitDuration, retryErr := time.ParseDuration(response.Header.Get("Retry-After") + "s")

			// NOTE(simon): Try to parse Retry-After as a specific date.
			if retryErr != nil {
				if retryDate, retryErr := http.ParseTime(response.Header.Get("Retry-After")); retryErr == nil {
					waitDuration = time.Until(retryDate)
				}
			}

			// NOTE(simon): We somehow failed to parse the date, use exponential backoff.
			if retryErr != nil {
				// NOTE(simon): Arbitrary decision to abort after 5 failed attempts of exponential backoff.
				if exponentialBackoffTries > 5 {
					return nil, fmt.Errorf("Received %v after %v attempts of exponential backoff", response.Status, exponentialBackoffTries)
				}

				waitDuration = exponentialBackoffBase * (1 << exponentialBackoffTries)
				exponentialBackoffTries += 1
			}

			// NOTE(simon): Wait and then retry the same request again.
			time.Sleep(waitDuration)
			continue
		} else if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http do: %v", response.Status)
		}

		// NOTE(simon): We got a response! Reset backoff time in the hopes of faster answers.
		exponentialBackoffTries = 0

		// NOTE(simon): Parse format, just an array of records, which is an
		// array of fields.
		var records [][]string
		err = json.NewDecoder(response.Body).Decode(&records)
		if err != nil {
			return nil, err
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

			date     := line[0]
			mimetype := line[1]

			// NOTE(simon): Do we have a valid mimetype?
			mimeType, mimeSubtype, _ := strings.Cut(mimetype, "/")
			hasValidType := slices.ContainsFunc([]string{ "application", "text" }, func(accepted string) bool {
				return strings.Contains(mimeType, accepted)
			})
			hasValidSubtype := slices.ContainsFunc([]string{ "atom", "rss", "xml" }, func(accepted string) bool {
				return strings.Contains(mimeSubtype, accepted)
			})
			if  hasValidType && hasValidSubtype {
				snapshots = append(snapshots, Snapshot{
					Url: feedUrl,
					Date: date,
				})
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
