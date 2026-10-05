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

// Retry-After wins over the exponential schedule and is honoured up to the
// 60s cap, not the 30s backoff cap: a 429 with Retry-After: 45 retried after
// 30s would just be rejected again and burn an attempt.
func TestRetryDelayHonoursRetryAfterUpToCap(t *testing.T) {
	cases := []struct {
		form  string
		value string
		want  time.Duration
		slack time.Duration
	}{
		{"delta-seconds", "45", 45 * time.Second, 0},
		{"delta-seconds at the cap", "60", 60 * time.Second, 0},
		{"http-date", time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat), 45 * time.Second, 3 * time.Second},
	}
	for _, tt := range cases {
		t.Run(tt.form, func(t *testing.T) {
			resp := &Response{Header: http.Header{"Retry-After": []string{tt.value}}}
			got := retryDelay(resp, 1)
			if got > tt.want || got < tt.want-tt.slack {
				t.Errorf("delay = %s, want %s", got, tt.want)
			}
			if asked, ok := serverDelay(resp); !ok || asked > maxRetryAfter {
				t.Errorf("serverDelay = %s, %v; want within the cap", asked, ok)
			}
		})
	}
}

// A Retry-After beyond the cap, however it is written (including values that
// overflow time.Duration or int64), must read as "longer than the cap" and
// never as a short or zero delay.
func TestServerDelayBeyondCap(t *testing.T) {
	for _, v := range []string{
		"61", "120", "3600", "9223372037", "9223372036854775807",
		"99999999999999999999999",
		time.Now().Add(time.Hour).UTC().Format(http.TimeFormat),
	} {
		resp := &Response{Header: http.Header{"Retry-After": []string{v}}}
		asked, ok := serverDelay(resp)
		if !ok || asked <= maxRetryAfter {
			t.Errorf("Retry-After %q: serverDelay = %s, %v; want > %s", v, asked, ok, maxRetryAfter)
		}
	}
}
