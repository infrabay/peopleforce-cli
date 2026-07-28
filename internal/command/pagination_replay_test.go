package command

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A backend can report page/pages correctly and still ignore ?page=. Replay
// detection used to run only when metadata was absent, so --all concatenated
// page 1 once per reported page and returned a silently inflated list.
func TestAllPaginationDetectsReplayDespiteMetadata(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `{"data":[{"id":1},{"id":2}],"metadata":{"page":1,"pages":5,"count":10,"items":2}}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var envelope struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout)
	}
	if len(envelope.Data) != 2 {
		t.Errorf("fetched %d items, want 2 (page 1 only): %s", len(envelope.Data), stdout)
	}
	if envelope.Meta["fetched"] != 2.0 {
		t.Errorf("meta.fetched = %v, want 2", envelope.Meta["fetched"])
	}
	if !strings.Contains(stderr, "ignore the page parameter") {
		t.Errorf("stderr should warn about the replaying backend, got: %q", stderr)
	}
	// Two consecutive replays are enough to conclude; it must not walk to pages=5.
	if hits != 3 {
		t.Errorf("made %d requests, want 3 (page 1 + two replays)", hits)
	}
}

// A held duplicate page used to be flushed unconditionally after the loop, so
// hitting --max-pages while holding one appended page 1 a second time.
func TestAllPaginationDropsUnresolvedDuplicateAtMaxPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":1},{"id":2}]}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-pages", "2")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var envelope struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout)
	}
	if len(envelope.Data) != 2 {
		t.Errorf("fetched %d items, want 2 (page 1 only, held page dropped): %s", len(envelope.Data), stdout)
	}
	if !strings.Contains(stderr, "it was dropped") {
		t.Errorf("dropping the unresolved page must be reported, got: %q", stderr)
	}
}

// A page that merely happens to repeat page 1 and is then followed by real
// data is legitimate — it must survive.
func TestAllPaginationKeepsCoincidentalDuplicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1", "2":
			fmt.Fprint(w, `{"data":[{"id":1}]}`)
		case "3":
			fmt.Fprint(w, `{"data":[{"id":3}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var envelope struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 3 {
		t.Errorf("fetched %d items, want 3 (the repeat was real data): %s", len(envelope.Data), stdout)
	}
}
