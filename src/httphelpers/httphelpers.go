package httphelpers

import (
	"errors"
	"net/http"
	"time"
)

type RoundTripperFunc func(request *http.Request) (*http.Response, error)

func (roundTripper RoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if roundTripper == nil {
		return http.DefaultTransport.RoundTrip(request)
	}
	return roundTripper(request)
}

func GetWithRetry(client *http.Client, url string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return DoWithRetry(client, request)
}

func DoWithRetry(client *http.Client, request *http.Request) (*http.Response, error) {
	exponentialBackoffBase  := 1 * time.Millisecond
	exponentialBackoffTries := 0

	for {
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}

		// NOTE(simon): Are we fetching too quickly? Take a break.
		if response.StatusCode == http.StatusTooManyRequests {
			response.Body.Close()

			// NOTE(simon): Try to parse Retry-After as a duration.
			if waitDuration, err := time.ParseDuration(response.Header.Get("Retry-After") + "s"); err == nil {
				time.Sleep(waitDuration)
				continue
			}

			// NOTE(simon): Try to parse Retry-After as a specific date.
			if retryDate, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
				time.Sleep(time.Until(retryDate))
				continue
			}

			// NOTE(simon): We somehow failed to parse the date, use exponential backoff.
			// NOTE(simon): Arbitrary decision to abort after 5 failed attempts of exponential backoff.
			if exponentialBackoffTries > 5 {
				return nil, errors.New("too many requests")
			}

			time.Sleep(exponentialBackoffBase * (1 << exponentialBackoffTries))
			exponentialBackoffTries += 1
			continue
		}

		return response, nil
	}
}
