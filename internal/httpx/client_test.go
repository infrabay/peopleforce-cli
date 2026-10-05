package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// hijackAfterStatus serves a 201 whose body is cut short: the status line and
// a Content-Length promising more than is ever sent, then the connection dies.
func hijackAfterStatus(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buf, "HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"id\":")
		buf.Flush()
	}))
}

// The status line arrived, so the server answered; only the body is lost. That
// must not look like a plain network failure, or a committed POST gets re-run.
func TestDoBodyReadFailureAfterStatusIsResponseReadError(t *testing.T) {
	srv := hijackAfterStatus(t)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	_, err := c.Do(context.Background(), Request{Method: "POST", Path: "/departments", Body: []byte(`{}`), ContentType: "application/json"})
	var rre *ResponseReadError
	if !errors.As(err, &rre) {
		t.Fatalf("error = %T %v, want *ResponseReadError", err, err)
	}
	if rre.Status != 201 || rre.Method != "POST" {
		t.Errorf("status/method = %d/%s, want 201/POST", rre.Status, rre.Method)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("underlying error = %v, want unexpected EOF", rre.Err)
	}
	var te *TransportError
	if errors.As(err, &te) {
		t.Error("must not be a TransportError")
	}
}

func TestDoOversizedBodyIsResponseReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		chunk := []byte(strings.Repeat("x", 1<<20))
		for i := 0; i < int(maxResponseBytes>>20)+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	_, err := c.Do(context.Background(), Request{Method: "POST", Path: "/x"})
	var rre *ResponseReadError
	if !errors.As(err, &rre) || rre.Status != 201 {
		t.Fatalf("error = %T %v, want *ResponseReadError with status 201", err, err)
	}
}

// Retry-After beyond the cap: no early retry, no sleep, the 429 comes back and
// the note names what the server asked for.
func TestDoStopsRetryingWhenRetryAfterExceedsCap(t *testing.T) {
	for _, v := range []string{"120", "9223372037"} {
		t.Run(v, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", v)
				w.WriteHeader(429)
			}))
			defer srv.Close()

			var notes []string
			c := &Client{BaseURL: srv.URL, APIKey: "k", MaxRetries: 3,
				Logf: func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) }}
			start := time.Now()
			resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != 429 {
				t.Errorf("status = %d, want the 429 handed back", resp.Status)
			}
			if calls.Load() != 1 {
				t.Errorf("calls = %d, want 1 (no retry)", calls.Load())
			}
			if time.Since(start) > 5*time.Second {
				t.Error("must not sleep")
			}
			if !strings.Contains(strings.Join(notes, "\n"), "Retry-After: "+v) {
				t.Errorf("notes %q do not mention the requested delay", notes)
			}
		})
	}
}
