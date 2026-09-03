package httphelpers

import (
	"io"
	"net/http"
	"bytes"
	"slices"
	"time"
)

type RoundTripperFunc func(request *http.Request) (*http.Response, error)

func (roundTripper RoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if roundTripper == nil {
		return http.DefaultTransport.RoundTrip(request)
	}
	return roundTripper(request)
}

func UserAgentRoundTripper(userAgent string, next http.RoundTripper) http.RoundTripper {
	return RoundTripperFunc(func (request *http.Request) (*http.Response, error) {
		if request.Header.Get("User-Agent") == "" {
			request = request.Clone(request.Context())
			request.Header.Set("User-Agent", userAgent)
		}

		return next.RoundTrip(request)
	})
}

func RetryRoundTripper(maxRetries int, baseDuration time.Duration, next http.RoundTripper) http.RoundTripper {
	retryStatusCodes := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		// NOTE(simon): Grab context and body from the request.
		context := request.Context()
		// TODO(simon): Use request.GetBody if available.
		body := request.Body
		if body == nil {
			body = http.NoBody
		}

		// NOTE(simon): Read all of the body so we can send more requests.
		requestBody, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			return nil, err
		}

		var response *http.Response
		for attempt := 0; attempt <= maxRetries; attempt++ {
			request.Body = io.NopCloser(bytes.NewBuffer(requestBody))

			response, err = next.RoundTrip(request)
			if err != nil {
				return nil, err
			}

			// NOTE(simon): Not retrieable? Exit.
			if !slices.Contains(retryStatusCodes, response.StatusCode) {
				return response, nil
			}

			if attempt < maxRetries {
				response.Body.Close()

				// NOTE(simon): Default to exponential backoff.
				waitDuration := baseDuration * (1 << attempt)

				// NOTE(simon): Try to parse Retry-After as a duration.
				if retryDuration, err := time.ParseDuration(response.Header.Get("Retry-After") + "s"); err == nil {
					waitDuration = retryDuration
				}

				// NOTE(simon): Try to parse Retry-After as a specific date.
				if retryDate, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
					waitDuration = time.Until(retryDate)
				}

				timer := time.NewTimer(waitDuration)
				select {
				case <-timer.C:
					continue
				case <-context.Done():
					return nil, context.Err()
				}
			}
		}

		return response, err
	})
}
