package httpx

import (
	"net/http"
	"testing"
	"time"
)

// The exponential schedule is the fallback whenever the server sends no
// Retry-After; every other retry test pins Retry-After, so nothing covered it.
func TestRetryDelayExponentialSchedule(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := retryDelay(nil, i+1); got != w {
			t.Errorf("attempt %d: delay = %s, want %s", i+1, got, w)
		}
	}
}

// time.Duration(math.Pow(2, 34))*time.Second overflows int64 and wrapped
// negative, which clamped to 0 and turned a high --max-retries into a tight
// loop against the endpoint.
func TestRetryDelayDoesNotOverflowAtHighAttempts(t *testing.T) {
	const maxDelay = 30 * time.Second
	for _, attempt := range []int{34, 35, 36, 40, 63, 64, 100, 1000} {
		got := retryDelay(nil, attempt)
		if got != maxDelay {
			t.Errorf("attempt %d: delay = %s, want the %s cap", attempt, got, maxDelay)
		}
	}
}

// Retry-After wins over the exponential schedule, but a server is free to
// suggest an hour; honoring that verbatim would park the CLI on a single 429.
func TestRetryDelayClampsOversizedRetryAfter(t *testing.T) {
	const maxDelay = 30 * time.Second
	cases := []struct {
		form  string
		value string
	}{
		{"delta-seconds", "3600"},
		{"http-date", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)},
	}
	for _, tt := range cases {
		t.Run(tt.form, func(t *testing.T) {
			resp := &Response{Header: http.Header{"Retry-After": []string{tt.value}}}
			if got := retryDelay(resp, 1); got != maxDelay {
				t.Errorf("delay = %s, want the %s cap", got, maxDelay)
			}
		})
	}
}
