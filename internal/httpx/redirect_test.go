package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// net/http strips Authorization and Cookie across origins, but the API key
// rides in a custom header, so an open redirect used to hand a full-scope HR
// token to whatever host the redirect named.
func TestRedirectToOtherHostDropsAPIKey(t *testing.T) {
	var gotKey string
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		gotKey = r.Header.Get(authHeader)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	defer origin.Close()

	c := &Client{BaseURL: origin.URL, APIKey: "SUPER-SECRET-HR-TOKEN"}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/employees"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !reached {
		t.Fatal("redirect was not followed; the test proves nothing")
	}
	if gotKey != "" {
		t.Errorf("cross-host redirect leaked the API key: %q", gotKey)
	}
}

// Same-origin redirects are ordinary API behaviour and must keep working.
func TestRedirectSameHostKeepsAPIKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/employees" {
			http.Redirect(w, r, "/employees/", http.StatusFound)
			return
		}
		gotKey = r.Header.Get(authHeader)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "SUPER-SECRET-HR-TOKEN"}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/employees"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotKey != "SUPER-SECRET-HR-TOKEN" {
		t.Errorf("same-host redirect dropped the API key: %q", gotKey)
	}
}

// A 307/308 resends method and body, so stripping the key still handed the
// record being written — an employee's personal data, an uploaded contract —
// to the other host. Writes must stop at a cross-origin redirect.
func TestRedirectToOtherHostDoesNotResendBody(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var gotBody string
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.Write([]byte(`{"data":{}}`))
		}))
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/collect", code)
		}))

		c := &Client{BaseURL: origin.URL, APIKey: "k"}
		_, err := c.Do(context.Background(), Request{
			Method: "POST", Path: "/employees",
			Body: []byte(`{"email":"jane@example.com"}`), ContentType: "application/json",
		})
		origin.Close()
		target.Close()

		if gotBody != "" {
			t.Errorf("HTTP %d: the request body reached the other origin: %q", code, gotBody)
		}
		if err == nil || !strings.Contains(err.Error(), "another origin") {
			t.Errorf("HTTP %d: err = %v, want a refusal naming the cross-origin redirect", code, err)
		}
	}
}
