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

// A run that dies partway used to discard every page already transferred and
// give no way back in: --all and --page were mutually exclusive, so resuming
// meant starting from page 1.
func TestAllPaginationFailureReportsResumePoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprint(w, `{"data":[{"id":1}],"metadata":{"page":1,"pages":9}}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":2}],"metadata":{"page":2,"pages":9}}`)
		default:
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"boom"}`)
		}
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	for _, want := range []string{"pages 1-2 fetched", "2 items", "--all --page 3"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should mention %q, got: %s", want, stderr)
		}
	}
}

// --page is now the resume point for --all rather than a conflicting flag.
func TestAllPaginationResumesFromPage(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		seen = append(seen, p)
		switch p {
		case "3":
			fmt.Fprint(w, `{"data":[{"id":3}],"metadata":{"page":3,"pages":4}}`)
		case "4":
			fmt.Fprint(w, `{"data":[{"id":4}],"metadata":{"page":4,"pages":4}}`)
		default:
			t.Errorf("must not refetch page %s", p)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--page", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var env struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 || env.Data[0].ID != 3 {
		t.Errorf("want only pages 3-4, got %s", stdout)
	}
	if len(seen) != 2 {
		t.Errorf("requested pages %v, want exactly [3 4]", seen)
	}
}

// --max-pages caps how many pages this run fetches, not the page number it
// may reach — otherwise resuming at page 12 with the default cap of 20 would
// stop after 9 pages.
func TestAllPaginationMaxPagesCountsFetchesNotPageNumbers(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		seen = append(seen, p)
		fmt.Fprintf(w, `{"data":[{"id":%s}],"metadata":{"page":%s,"pages":99}}`, p, p)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--page", "10", "--max-pages", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if len(seen) != 3 {
		t.Errorf("fetched pages %v, want 3 pages starting at 10", seen)
	}
	if seen[0] != "10" || seen[2] != "12" {
		t.Errorf("pages %v, want 10..12", seen)
	}
}

func TestAllPaginationRejectsNonPositiveStartPage(t *testing.T) {
	_, stderr, code := runCLI(t, "http://127.0.0.1:1", "employees", "list", "--all", "--page", "0")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--page must be 1 or greater") {
		t.Errorf("stderr = %s", stderr)
	}
}

// --page with --all used to be rejected outright. It is now the resume point
// for an interrupted run, and --dry-run must preview the page it would start
// from rather than page 1.
func TestAllWithPageStartsThereInDryRun(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "employees", "list", "--all", "--page", "2", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var preview struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatalf("dry-run is not JSON: %v (%s)", err, stdout)
	}
	// The page param belongs to the pagination loop, not the base query, so it
	// must not be duplicated into the previewed URL.
	if strings.Count(preview.URL, "page=") > 1 {
		t.Errorf("url has more than one page param: %s", preview.URL)
	}
}
