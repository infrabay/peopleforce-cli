package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
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
