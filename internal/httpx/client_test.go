package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoSendsVerbatimQueryAndAuthHeader(t *testing.T) {
	var gotRawQuery, gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		gotKey = r.Header.Get("X-API-KEY")
		gotPath = r.URL.Path
		w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "secret"}
	resp, err := c.Do(context.Background(), Request{
		Method: "GET",
		Path:   "/employees",
		Query: []QueryPair{
			{"employee_ids[]", "1"},
			{"employee_ids[]", "2"},
			{"hired_on[gte]", "2025-01-01"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	if gotKey != "secret" {
		t.Errorf("X-API-KEY = %q", gotKey)
	}
	if gotPath != "/employees" {
		t.Errorf("path = %q", gotPath)
	}
	// The wire must carry literal brackets — Go's http client must not
	// re-encode what we built.
	want := "employee_ids[]=1&employee_ids[]=2&hired_on[gte]=2025-01-01"
	if gotRawQuery != want {
		t.Errorf("raw query = %q, want %q", gotRawQuery, want)
	}
}

func TestDoRetriesOn429HonoringRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k", MaxRetries: 3}
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d after retries", resp.Status)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoReturnsLastResponseWhenRetriesExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k", MaxRetries: 1}
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.Status)
	}
}

func TestDoDeleteWithJSONBody(t *testing.T) {
	var gotBody string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(204)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	body := []byte(`{"ids":[1,2]}`)
	resp, err := c.Do(context.Background(), Request{
		Method: "DELETE", Path: "/time/timesheet_entries/bulk",
		Body: body, ContentType: "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 204 {
		t.Fatalf("status = %d", resp.Status)
	}
	if gotBody != `{"ids":[1,2]}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("content type = %q", gotContentType)
	}
}

func TestDoNetworkErrorIsTransportError(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", APIKey: "k"}
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*TransportError); !ok {
		t.Errorf("error type = %T, want *TransportError", err)
	}
}

func TestPUTRetriedOn502ButPOSTNot(t *testing.T) {
	var putCalls, postCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			if putCalls.Add(1) < 2 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write([]byte(`{"data":{}}`))
		case "POST":
			postCalls.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k", MaxRetries: 3}
	resp, err := c.Do(context.Background(), Request{Method: "PUT", Path: "/x"})
	if err != nil || resp.Status != 200 {
		t.Fatalf("PUT should retry on 502: status=%v err=%v", resp, err)
	}
	if putCalls.Load() != 2 {
		t.Errorf("PUT calls = %d, want 2", putCalls.Load())
	}

	resp, err = c.Do(context.Background(), Request{Method: "POST", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("POST status = %d", resp.Status)
	}
	if postCalls.Load() != 1 {
		t.Errorf("POST calls = %d; POST must not be retried on 502 (duplicate-write risk)", postCalls.Load())
	}
}

func TestRetryDelayHTTPDate(t *testing.T) {
	resp := &Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", time.Now().Add(5*time.Second).UTC().Format(http.TimeFormat))
	d := retryDelay(resp, 1)
	if d < 3*time.Second || d > 6*time.Second {
		t.Errorf("retryDelay for HTTP-date Retry-After = %v, want ~5s", d)
	}
	// Past date clamps to zero, not negative.
	resp.Header.Set("Retry-After", time.Now().Add(-10*time.Second).UTC().Format(http.TimeFormat))
	if d := retryDelay(resp, 1); d != 0 {
		t.Errorf("past HTTP-date should clamp to 0, got %v", d)
	}
}
