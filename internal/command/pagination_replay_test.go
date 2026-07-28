package command

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	srv := httptest.NewServer(http.HandlerFunc(failAfterTwoPages))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	for _, want := range []string{"2 item(s)", "truncated envelope", "meta.next_page=3", "--all --page 3"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should mention %q, got: %s", want, stderr)
		}
	}
}

// The resume advice is only honest if the pages it resumes from are actually
// handed over: emitting nothing left an agent that obeyed it with a dataset
// permanently missing pages 1..N-1.
func TestAllPaginationEmitsTruncatedEnvelopeOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(failAfterTwoPages))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 2 || env.Data[0].ID != 1 || env.Data[1].ID != 2 {
		t.Errorf("stdout must carry the pages already fetched, got: %s", stdout)
	}
	if env.Meta["truncated"] != true {
		t.Errorf("meta.truncated = %v, want true: %s", env.Meta["truncated"], stdout)
	}
	if env.Meta["next_page"] != 3.0 {
		t.Errorf("meta.next_page = %v, want 3: %s", env.Meta["next_page"], stdout)
	}
	if env.Meta["fetched"] != 2.0 {
		t.Errorf("meta.fetched = %v, want 2: %s", env.Meta["fetched"], stdout)
	}
	if !strings.Contains(stderr, "HTTP 500") {
		t.Errorf("the failure itself must still be reported on stderr, got: %s", stderr)
	}
}

// Transport failures keep their own exit code (8) while handing over the same
// truncated envelope.
func TestAllPaginationEmitsTruncatedEnvelopeOnTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close() // drop the connection mid-run
			return
		}
		fmt.Fprint(w, `{"data":[{"id":1}],"metadata":{"page":1,"pages":9}}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitNetwork {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitNetwork, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 1 || env.Meta["truncated"] != true || env.Meta["next_page"] != 2.0 {
		t.Errorf("want page 1 marked truncated at next_page 2, got: %s", stdout)
	}
}

// A held page that only the next fetch could have vindicated stays dropped
// when that fetch fails — the truncated envelope must not smuggle it in.
func TestAllPaginationFailureDropsHeldDuplicatePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1", "2":
			fmt.Fprint(w, `{"data":[{"id":1}],"metadata":{"page":1,"pages":9}}`)
		default:
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"boom"}`)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	env := decodeTruncated(t, stdout)
	if len(env.Data) != 1 {
		t.Errorf("fetched %d items, want 1 (the held replay of page 1 is dropped): %s", len(env.Data), stdout)
	}
	if env.Meta["fetched"] != 1.0 {
		t.Errorf("meta.fetched = %v, want 1", env.Meta["fetched"])
	}
	if !strings.Contains(stderr, "it was dropped") {
		t.Errorf("dropping the held page must be reported, got: %q", stderr)
	}
}

// failAfterTwoPages serves two pages of a nine-page list, then 500s.
func failAfterTwoPages(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("page") {
	case "1":
		fmt.Fprint(w, `{"data":[{"id":1}],"metadata":{"page":1,"pages":9}}`)
	case "2":
		fmt.Fprint(w, `{"data":[{"id":2}],"metadata":{"page":2,"pages":9}}`)
	default:
		w.WriteHeader(500)
		fmt.Fprint(w, `{"message":"boom"}`)
	}
}

type truncatedEnvelope struct {
	Data []struct {
		ID int `json:"id"`
	} `json:"data"`
	Meta map[string]any `json:"meta"`
}

func decodeTruncated(t *testing.T, stdout string) truncatedEnvelope {
	t.Helper()
	var env truncatedEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout)
	}
	return env
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

// The --max-pages cap is the COMMON truncation (default 20), so it must carry
// the same completeness marker as the failure path. Without it, a capped run
// is byte-indistinguishable from a complete one on stdout.
func TestAllPaginationMarksMaxPagesTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"data":[{"id":%s}],"metadata":{"page":%s,"pages":30}}`, p, p)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-pages", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var env struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Meta["truncated"] != true {
		t.Errorf("meta.truncated = %v, want true: the cap left pages unfetched", env.Meta["truncated"])
	}
	if env.Meta["next_page"] != 4.0 {
		t.Errorf("meta.next_page = %v, want 4", env.Meta["next_page"])
	}
}

// A complete run must NOT be marked, or the flag means nothing.
func TestAllPaginationCompleteRunIsNotMarkedTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"data":[{"id":%s}],"metadata":{"page":%s,"pages":2}}`, p, p)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var env struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if _, marked := env.Meta["truncated"]; marked {
		t.Errorf("a complete run must not be marked truncated: %s", stdout)
	}
}

// A held replay-suspect page is dropped on failure, so it is in neither the
// emitted data nor a later page: resuming past it would lose it silently.
func TestAllPaginationResumePageCoversDroppedDuplicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1", "2":
			fmt.Fprint(w, `{"data":[{"id":1}]}`)
		default:
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"boom"}`)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitServer, stderr)
	}
	var env struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	// Page 2 was held and dropped, so the caller must come back for it.
	if env.Meta["next_page"] != 2.0 {
		t.Errorf("meta.next_page = %v, want 2 (the dropped page), got envelope: %s", env.Meta["next_page"], stdout)
	}
	if !strings.Contains(stderr, "--all --page 2") {
		t.Errorf("resume advice should point at the dropped page, got: %s", stderr)
	}
}

// Under ndjson no meta is written at all, so the error must not send the
// operator looking for meta.truncated.
func TestAllPaginationTruncationNoteMatchesOutputFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(failAfterTwoPages))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "-o", "ndjson", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d", code, ExitServer)
	}
	if strings.Contains(stderr, "meta.truncated") {
		t.Errorf("ndjson carries no meta; the note must not promise it: %s", stderr)
	}
	if !strings.Contains(stderr, "exit code") {
		t.Errorf("the note should point at the exit code instead: %s", stderr)
	}
}

// Nothing was collected, so there is nothing honest to hand over: emitting an
// empty envelope here would look like a legitimately empty list.
func TestAllPaginationFirstPageFailureWritesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-retries", "0")
	if code != ExitServer {
		t.Fatalf("exit = %d, want %d", code, ExitServer)
	}
	if stdout != "" {
		t.Errorf("stdout must stay empty when no page was fetched, got: %q", stdout)
	}
	if strings.Contains(stderr, "--all --page") {
		t.Errorf("there is nothing to resume from, so no resume advice: %s", stderr)
	}
}

// realPagination serves the envelope shape the live API actually uses, with
// the counters nested under metadata.pagination. Every other --all fixture
// here uses the flat shape, which is why the metadata-driven stop condition
// went unexercised for so long.
func realPagination(pages int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		n, _ := strconv.Atoi(p)
		if n < 1 || n > pages {
			fmt.Fprintf(w, `{"data":[],"metadata":{"pagination":{"page":%s,"pages":%d,"count":%d,"items":0}}}`, p, pages, pages)
			return
		}
		fmt.Fprintf(w, `{"data":[{"id":%d}],"metadata":{"pagination":{"page":%d,"pages":%d,"count":%d,"items":1}}}`, n, n, pages, pages)
	}
}

// With the real shape the loop must stop on metadata rather than probing an
// extra empty page, and a complete run must carry no truncation marker.
func TestAllPaginationStopsOnRealMetadata(t *testing.T) {
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Query().Get("page"))
		realPagination(3)(w, r)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if len(requested) != 3 {
		t.Errorf("requested pages %v, want exactly 3 — metadata says where to stop", requested)
	}
	if strings.Contains(stderr, "no pagination metadata") {
		t.Errorf("the real envelope carries metadata; the fallback must not engage: %s", stderr)
	}
	var env struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 3 {
		t.Errorf("fetched %d items, want 3", len(env.Data))
	}
	if _, marked := env.Meta["truncated"]; marked {
		t.Errorf("a complete run must not be marked truncated: %s", stdout)
	}
	if env.Meta["pages"] != 3.0 {
		t.Errorf("meta.pages = %v, want 3 hoisted from metadata.pagination", env.Meta["pages"])
	}
}

// The cap must mark truncation against the real shape too — this is the case
// that falsely reported truncated:true on a complete list before the hoist.
func TestAllPaginationCapMarksOnlyRealTruncation(t *testing.T) {
	srv := httptest.NewServer(realPagination(2))
	defer srv.Close()

	// Cap equal to the real page count: complete, so no marker.
	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all", "--max-pages", "2")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var env struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if _, marked := env.Meta["truncated"]; marked {
		t.Errorf("cap equal to the page count is not truncation: %s", stdout)
	}

	// Cap below the real page count: genuinely truncated.
	stdout, _, code = runCLI(t, srv.URL, "employees", "list", "--all", "--max-pages", "1")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Meta["truncated"] != true || env.Meta["next_page"] != 2.0 {
		t.Errorf("want truncated with next_page 2, got: %s", stdout)
	}
}
