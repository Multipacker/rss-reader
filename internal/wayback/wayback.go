package wayback

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Snapshot struct {
	Url  string
	Date string
}

const (
	TimeFormat string = "20060102030405"
)

func FetchSnapshot(url, date string) (response *http.Response, err error) {
	// TODO(simon): Honor 429 Too Many Requests and Retry-After

	request, err := http.NewRequest("GET", "https://web.archive.org/web/" + date + "id_/" + url, nil)
	if err != nil {
		return
	}

	request.Header.Set("User-Agent", "SilverFeed/1.0")

	response, err = http.DefaultClient.Do(request)
	return
}

func QuerySnapshots(feedUrl string, lastPollTime time.Time) (snapshots []Snapshot, err error) {
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

	return
}
