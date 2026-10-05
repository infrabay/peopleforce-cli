package command

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func decodeNDJSON(t *testing.T, s string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid NDJSON report: %v\n%s", err, s)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestBulkUpdateReportsPerRecord(t *testing.T) {
	var paths []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		paths = append(paths, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(b))
		switch r.URL.Path {
		case "/employees/8322":
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"invalid field"}`)
		default:
			fmt.Fprintf(w, `{"data":{"id":%s,"github":"ok"}}`, strings.TrimPrefix(r.URL.Path, "/employees/"))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "updates.jsonl")
	os.WriteFile(input, []byte(
		`{"id": 8321, "set": {"github": "octocat"}}
{"id": 8322, "set": {"github": ""}}
{"id": 8323, "set": {"github": "octocat"}}
`), 0o644)

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "bulk-update", "--input", "@"+input)
	if code != ExitValidation {
		t.Errorf("exit = %d, want %d (one record failed)", code, ExitValidation)
	}
	if len(paths) != 3 || paths[0] != "PUT /employees/8321" || paths[1] != "PUT /employees/8322" || paths[2] != "PUT /employees/8323" {
		t.Errorf("requests = %v", paths)
	}
	if bodies[0] != `{"github":"octocat"}` {
		t.Errorf("body[0] = %s", bodies[0])
	}

	lines := decodeNDJSON(t, stdout)
	if len(lines) != 3 {
		t.Fatalf("report lines = %d, want 3\n%s", len(lines), stdout)
	}
	if lines[0]["ok"] != true || lines[0]["status"] != 200.0 {
		t.Errorf("line 0 = %v", lines[0])
	}
	// The updated record rides along for verification without extra GETs.
	if data, ok := lines[0]["data"].(map[string]any); !ok || data["github"] != "ok" {
		t.Errorf("line 0 data = %v", lines[0]["data"])
	}
	if lines[1]["ok"] != false || lines[1]["status"] != 422.0 {
		t.Errorf("line 1 = %v", lines[1])
	}
	if lines[2]["ok"] != true {
		t.Errorf("line 2 = %v", lines[2])
	}
	if !strings.Contains(stderr, "2 ok, 1 failed") {
		t.Errorf("stderr summary: %q", stderr)
	}
}

func TestBulkUpdateAllOKExitsZeroAndAcceptsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "updates.json")
	os.WriteFile(input, []byte(`[{"id": 1, "set": {"a": "b"}}, {"id": 2, "set": {"a": "c"}}]`), 0o644)

	stdout, _, code := runCLI(t, srv.URL, "employees", "bulk-update", "--input", "@"+input)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(decodeNDJSON(t, stdout)) != 2 {
		t.Errorf("report: %s", stdout)
	}
}

func TestBulkUpdateDryRunSendsNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "updates.jsonl")
	os.WriteFile(input, []byte(`{"id": 8321, "set": {"github": "octocat"}}`), 0o644)

	stdout, _, code := runCLI(t, srv.URL, "employees", "bulk-update", "--input", "@"+input, "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if called {
		t.Error("dry-run must not send requests")
	}
	lines := decodeNDJSON(t, stdout)
	if len(lines) != 1 || lines[0]["dry_run"] != true || lines[0]["method"] != "PUT" {
		t.Errorf("dry-run report: %s", stdout)
	}
}

func TestBulkUpdateValidatesRecords(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(input, []byte(`{"id": 1}`), 0o644)

	_, stderr, code := runCLI(t, "", "employees", "bulk-update", "--input", "@"+input)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "missing or empty") || !strings.Contains(stderr, "set") {
		t.Errorf("stderr should name the missing field: %s", stderr)
	}
}

func TestJQRawOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"id":1,"github":"octocat"}}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "get", "1", "--jq", ".data.github", "--raw")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "octocat\n" {
		t.Errorf("raw jq output = %q, want %q", stdout, "octocat\n")
	}

	// Without --raw the string stays JSON-quoted.
	stdout, _, _ = runCLI(t, srv.URL, "employees", "get", "1", "--jq", ".data.github")
	if stdout != "\"octocat\"\n" {
		t.Errorf("quoted jq output = %q", stdout)
	}
}

func TestBulkUpdateRejectsNonPositiveID(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(input, []byte(`{"id": 0, "set": {"a": "b"}}`), 0o644)

	_, stderr, code := runCLI(t, "", "employees", "bulk-update", "--input", "@"+input)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "positive integer") {
		t.Errorf("stderr: %s", stderr)
	}
}

// A transport failure is not a per-record failure: the run aborts with the
// network exit code, and the report stops at the record that failed. The
// truncated report is the contract --help states — an agent re-runs exactly
// the records that never reached stdout, so a line for a record that was
// never sent (or a "continue on error" exit 5) would double-apply updates.
func TestBulkUpdateAbortsRunOnNetworkFailure(t *testing.T) {
	// A hijacked connection carries none of net/http's usual synchronization
	// between the handler and the client, so the record of what was attempted
	// needs its own lock.
	var mu sync.Mutex
	var attempted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempted = append(attempted, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/employees/8322" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close() // no response at all: the client sees a transport error
			return
		}
		fmt.Fprintf(w, `{"data":{"id":%s,"github":"ok"}}`, strings.TrimPrefix(r.URL.Path, "/employees/"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "updates.jsonl")
	os.WriteFile(input, []byte(
		`{"id": 8321, "set": {"github": "octocat"}}
{"id": 8322, "set": {"github": "octocat"}}
{"id": 8323, "set": {"github": "hubot"}}
`), 0o644)

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "bulk-update", "--input", "@"+input)
	if code != ExitNetwork {
		t.Fatalf("exit = %d, want %d (network, not %d), stderr: %s", code, ExitNetwork, ExitValidation, stderr)
	}
	mu.Lock()
	got := append([]string(nil), attempted...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "PUT /employees/8321" || got[1] != "PUT /employees/8322" {
		t.Errorf("requests = %v, want the run to stop at the failed record", got)
	}

	lines := decodeNDJSON(t, stdout)
	if len(lines) != 2 {
		t.Fatalf("report lines = %d, want 2 (8323 was never attempted)\n%s", len(lines), stdout)
	}
	if lines[0]["id"] != 8321.0 || lines[0]["ok"] != true || lines[0]["status"] != 200.0 {
		t.Errorf("line 0 = %v, want 8321 applied", lines[0])
	}
	if lines[1]["id"] != 8322.0 || lines[1]["ok"] != false {
		t.Errorf("line 1 = %v, want 8322 failed", lines[1])
	}
	if ee, _ := lines[1]["error"].(map[string]any); ee == nil || ee["type"] != "network" {
		t.Errorf("line 1 error = %v, want type network", lines[1]["error"])
	}
	if !strings.Contains(stderr, "aborted on network error") {
		t.Errorf("stderr must say the run aborted, got: %s", stderr)
	}
}

// README and SKILL.md both teach piping records in, so the "-" arm has to
// reach the same parser as @file.
func TestBulkUpdateReadsRecordsFromStdin(t *testing.T) {
	stdin := `{"id": 8321, "set": {"github": "octocat"}}` + "\n"
	stdout, stderr, code := runWithStdin(t, "", stdin, "https://api.example.test/v3",
		"employees", "bulk-update", "--input", "-", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	lines := decodeNDJSON(t, stdout)
	if len(lines) != 1 {
		t.Fatalf("report lines = %d, want 1\n%s", len(lines), stdout)
	}
	if lines[0]["id"] != 8321.0 || lines[0]["dry_run"] != true || lines[0]["method"] != "PUT" {
		t.Errorf("preview = %v", lines[0])
	}
	if lines[0]["url"] != "https://api.example.test/v3/employees/8321" {
		t.Errorf("preview url = %v", lines[0]["url"])
	}
	if body, _ := lines[0]["body"].(map[string]any); body == nil || body["github"] != "octocat" {
		t.Errorf("preview body = %v", lines[0]["body"])
	}
	if !strings.Contains(stderr, "1 record(s), nothing sent") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestBulkUpdateInputArgumentErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(empty, []byte("  \n"), 0o644)
	badArray := filepath.Join(dir, "bad.json")
	os.WriteFile(badArray, []byte(`[{"id": 1, "set": {"a": "b"}},]`), 0o644)
	badRecord := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(badRecord, []byte(
		`{"id": 1, "set": {"a": "b"}}
{"id": 2, "set": }
`), 0o644)

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"missing file", "@" + filepath.Join(dir, "nope.jsonl"), "no such file"},
		{"path without @", "updates.jsonl", "--input expects @file"},
		{"empty file", "@" + empty, "--input is empty"},
		{"malformed array", "@" + badArray, "invalid JSON array"},
		// The record number is what an agent uses to locate the line to repair.
		{"malformed record", "@" + badRecord, "invalid JSON on record 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "", "employees", "bulk-update", "--input", tc.input)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d, stderr: %s", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr should mention %q, got: %s", tc.want, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout must stay empty on a usage error, got: %s", stdout)
			}
		})
	}
}
