package command

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveAllPages answers the listed pages and fails the test for any other, so
// a run that walks past an anomaly is caught as well as one that misreports it.
func serveAllPages(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Query().Get("page")]
		if !ok {
			t.Errorf("unexpected request for page %q", r.URL.Query().Get("page"))
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const page1of5 = `{"data":[{"id":1}],"metadata":{"page":1,"pages":5}}`

// A later page that is not a list used to end the run as if the list were
// over: the pages so far were presented as everything (exit 0, no markers).
func TestAllPaginationMidRunNonListIsTruncatedServerError(t *testing.T) {
	cases := map[string]string{
		"object data": `{"data":{"id":9}}`,
		"empty body":  ``,
		"html body":   `<html>bad gateway</html>`,
		"bare null":   `null`,
		"bare object": `{"id":9}`,
	}
	for name, page2 := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serveAllPages(t, map[string]string{"1": page1of5, "2": page2})
			stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
			if code != ExitServer {
				t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
			}
			env := decodeTruncated(t, stdout)
			if len(env.Data) != 1 || env.Meta["truncated"] != true || env.Meta["next_page"] != 2.0 {
				t.Errorf("want 1 item, truncated, next_page 2, got: %s", stdout)
			}
			if !strings.Contains(stderr, "page 2 returned no list (HTTP 200") {
				t.Errorf("stderr should name the page and response, got: %s", stderr)
			}
		})
	}
}

// An empty page whose own metadata says more pages exist is the same anomaly.
func TestAllPaginationEmptyPageWithMorePagesIsTruncated(t *testing.T) {
	srv := serveAllPages(t, map[string]string{"1": page1of5,
		"2": `{"data":[],"metadata":{"page":2,"pages":5}}`})
	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 1 || env.Meta["truncated"] != true || env.Meta["next_page"] != 2.0 {
		t.Errorf("want 1 item, truncated, next_page 2, got: %s", stdout)
	}
	if !strings.Contains(stderr, "page 2 is empty") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestAllPaginationEmptyLastPageEndsNormally(t *testing.T) {
	srv := serveAllPages(t, map[string]string{"1": page1of5,
		"2": `{"data":[],"metadata":{"page":2,"pages":2}}`})
	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if env := decodeTruncated(t, stdout); env.Meta["truncated"] != nil || len(env.Data) != 1 {
		t.Errorf("a run ended by metadata must not be marked: %s", stdout)
	}
}

// A replaying backend that still reports pages=5 used to end with no marker,
// which promised "everything the endpoint had" for 1 of 5 pages.
func TestAllPaginationReplayWithMetadataIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page1of5)
	}))
	defer srv.Close()
	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 1 || env.Meta["truncated"] != true || env.Meta["next_page"] != 2.0 {
		t.Errorf("want 1 item, truncated, next_page 2, got: %s", stdout)
	}
	if !strings.Contains(stderr, "ignore the page parameter") {
		t.Errorf("stderr = %s", stderr)
	}
}

// Without metadata a backend ignoring ?page= is returning everything it has.
func TestAllPaginationReplayWithoutMetadataIsComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":1}]}`)
	}))
	defer srv.Close()
	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if env := decodeTruncated(t, stdout); env.Meta["truncated"] != nil {
		t.Errorf("no metadata: must stay unmarked: %s", stdout)
	}
}

// With page 2 held as a possible replay of page 1, an anomaly on page 3 must
// resume from the held page 2, not past it, and say it was dropped.
func TestAllPaginationHeldDuplicateThenAnomalyResumesAtHeldPage(t *testing.T) {
	srv := serveAllPages(t, map[string]string{
		"1": page1of5,
		"2": `{"data":[{"id":1}],"metadata":{"page":2,"pages":5}}`,
		"3": `<html>oops</html>`,
	})
	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 1 || env.Meta["next_page"] != 2.0 {
		t.Errorf("want 1 item and next_page 2, got: %s", stdout)
	}
	if !strings.Contains(stderr, "it was dropped") {
		t.Errorf("held page drop must be reported: %s", stderr)
	}
}

func TestMaxPagesBelowOneIsUsageError(t *testing.T) {
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		for _, v := range []string{"0", "-3"} {
			args := append([]string{"employees", "list", "--all", "--max-pages=" + v}, extra...)
			_, stderr, code := runCLI(t, "http://127.0.0.1:1", args...)
			if code != ExitUsage || !strings.Contains(stderr, "--max-pages must be 1 or greater") {
				t.Errorf("%v: exit = %d, stderr: %s", args, code, stderr)
			}
		}
	}
}

func TestAllDryRunPreviewsStartPage(t *testing.T) {
	stdout, _, code := runCLI(t, "", "employees", "list", "--all", "--page", "3", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "page=3") {
		t.Errorf("exit = %d, preview should request page 3: %s", code, stdout)
	}
	stdout, _, _ = runCLI(t, "", "employees", "list", "--all", "--dry-run")
	if !strings.Contains(stdout, "page=1") {
		t.Errorf("preview should request page 1: %s", stdout)
	}
}

func TestMultipartDryRunPreviewsRepeatedFields(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "recruitment", "candidates", "create",
		"--full-name", "Jane", "--skills", "go", "--skills", "sql", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	compact := strings.Join(strings.Fields(stdout), "")
	if !strings.Contains(compact, `"skills[]":["go","sql"]`) {
		t.Errorf("repeated field should preview as an array: %s", stdout)
	}
	if !strings.Contains(compact, `"full_name":"Jane"`) {
		t.Errorf("single field should stay a string: %s", stdout)
	}
}

func fixedServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

// A 200 HTML page from a proxy used to print data: null with exit 0, which an
// agent reads as "no employees".
func TestNonJSON2xxOnReadIsServerError(t *testing.T) {
	srv := fixedServer(200, "<html>captive portal</html>")
	defer srv.Close()
	for _, args := range [][]string{
		{"employees", "list"},
		{"employees", "list", "--all"},
		{"api", "call", "GET", "/employees"},
	} {
		stdout, stderr, code := runCLI(t, srv.URL, args...)
		if code != ExitServer {
			t.Errorf("%v: exit = %d, want %d", args, code, ExitServer)
		}
		if stdout != "" {
			t.Errorf("%v: nothing may reach stdout, got %s", args, stdout)
		}
		if !strings.Contains(stderr, "HTTP 200 but the body is not JSON") || !strings.Contains(stderr, "captive portal") {
			t.Errorf("%v: stderr = %s", args, stderr)
		}
	}
}

func TestNonJSON2xxOnMutationIsOutputError(t *testing.T) {
	srv := fixedServer(201, "created ok")
	defer srv.Close()
	_, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/employees", "--input", `{"a":1}`)
	if code != ExitOutput {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitOutput, stderr)
	}
	if !strings.Contains(stderr, "do not blindly re-run") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestNonJSONExcerptIsCapped(t *testing.T) {
	srv := fixedServer(200, strings.Repeat("é", 500))
	defer srv.Close()
	_, stderr, _ := runCLI(t, srv.URL, "employees", "list")
	if n := strings.Count(stderr, "é"); n == 0 || n > 100 {
		t.Errorf("excerpt should be capped to 200 bytes (100 chars), got %d chars", n)
	}
}

func TestEmptyBody2xxIsStillFine(t *testing.T) {
	srv := fixedServer(204, "")
	defer srv.Close()
	stdout, stderr, code := runCLI(t, srv.URL, "api", "call", "DELETE", "/employees/1", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"data": null`) {
		t.Errorf("stdout = %s", stdout)
	}
}
