package httpx

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// isRetryable reports whether the response is worth another attempt.
// 429 is always safe to retry (the request was not processed). Transient
// 5xx gateway errors are retried only for idempotent methods: a 502/504 can
// arrive after the backend already committed a POST, and re-sending would
// duplicate the created resource.
func isRetryable(method string, status int) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
			return true
		}
		return false
	}
	return false
}

// maxRetryAfter caps how long the client will honour a server's Retry-After.
// A delay the server asks for beyond it is not shortened (the retry would hit
// the same 429 and burn an attempt) and not obeyed (it would park the CLI for
// minutes): Do stops retrying and hands the response back instead.
const maxRetryAfter = 60 * time.Second

// maxBackoff caps the exponential schedule used when there is no Retry-After.
const maxBackoff = 30 * time.Second

// hugeRetryAfter stands in for a delta-seconds value too large to be worth
// converting; it is far above maxRetryAfter and far below Duration overflow.
const hugeRetryAfter = maxRetryAfter * 1000

// serverDelay reads the Retry-After header in either RFC form (delta-seconds
// or HTTP-date). ok is false when the header is absent or unparseable. The
// delay is NOT clamped; callers compare it to maxRetryAfter.
func serverDelay(resp *Response) (d time.Duration, ok bool) {
	if resp == nil {
		return 0, false
	}
	ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if ra == "" {
		return 0, false
	}
	if isDigits(ra) {
		// Bounds-check in seconds before converting: time.Duration(secs)*
		// time.Second overflows int64 from about 9.2e9 seconds and wraps
		// negative, which used to turn an absurd Retry-After into an immediate
		// retry. A value that does not even fit an int64 is just "very long".
		secs, err := strconv.ParseInt(ra, 10, 64)
		if err != nil || secs > int64(hugeRetryAfter/time.Second) {
			return hugeRetryAfter, true
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(ra); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		if d > hugeRetryAfter {
			d = hugeRetryAfter
		}
		return d, true
	}
	return 0, false
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// retryDelay picks the wait before the next attempt. A Retry-After up to
// maxRetryAfter is honoured exactly (Do checks serverDelay against the cap
// first and gives up beyond it); otherwise exponential backoff: 1s, 2s, 4s...
// capped at maxBackoff.
func retryDelay(resp *Response, attempt int) time.Duration {
	if d, ok := serverDelay(resp); ok {
		return clampDelay(d, maxRetryAfter)
	}
	// Cap in seconds before converting: time.Duration(math.Pow(2, 34))*Second
	// overflows int64 and wraps negative, which clamped to a 0s delay and
	// turned --max-retries 50 into a tight loop against the endpoint.
	secs := math.Pow(2, float64(attempt-1))
	if secs >= maxBackoff.Seconds() {
		return maxBackoff
	}
	return clampDelay(time.Duration(secs*float64(time.Second)), maxBackoff)
}

func clampDelay(d, max time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > max {
		return max
	}
	return d
}
