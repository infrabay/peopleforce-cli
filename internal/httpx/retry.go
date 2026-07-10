package httpx

import (
	"math"
	"net/http"
	"strconv"
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

// retryDelay picks the wait before the next attempt. Retry-After wins in
// either RFC form (delta-seconds or HTTP-date); otherwise exponential
// backoff: 1s, 2s, 4s... capped.
func retryDelay(resp *Response, attempt int) time.Duration {
	const maxDelay = 30 * time.Second
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				return clampDelay(time.Duration(secs)*time.Second, maxDelay)
			}
			if t, err := http.ParseTime(ra); err == nil {
				return clampDelay(time.Until(t), maxDelay)
			}
		}
	}
	return clampDelay(time.Duration(math.Pow(2, float64(attempt-1)))*time.Second, maxDelay)
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
