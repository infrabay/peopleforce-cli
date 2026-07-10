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
		`{"id": 8321, "set": {"github": "kam1kaze"}}
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
	if bodies[0] != `{"github":"kam1kaze"}` {
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
	os.WriteFile(input, []byte(`{"id": 8321, "set": {"github": "kam1kaze"}}`), 0o644)

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
		fmt.Fprint(w, `{"data":{"id":1,"github":"kam1kaze"}}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "get", "1", "--jq", ".data.github", "--raw")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "kam1kaze\n" {
		t.Errorf("raw jq output = %q, want %q", stdout, "kam1kaze\n")
	}

	// Without --raw the string stays JSON-quoted.
	stdout, _, _ = runCLI(t, srv.URL, "employees", "get", "1", "--jq", ".data.github")
	if stdout != "\"kam1kaze\"\n" {
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
