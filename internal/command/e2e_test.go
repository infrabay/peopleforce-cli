package command

import (
	"bytes"
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

// runCLI executes the full command tree the way main() does, capturing the
// agent-facing contract: stdout, stderr, exit code.
func runCLI(t *testing.T, srvURL string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // never read the developer's real config
	t.Setenv("PEOPLEFORCE_API_KEY", "")
	t.Setenv("PEOPLEFORCE_PROFILE", "")

	root, app := NewRoot()
	var out, errBuf bytes.Buffer
	app.Stdout, app.Stderr = &out, &errBuf
	root.SetOut(&out)
	root.SetErr(&errBuf)

	if srvURL != "" {
		args = append(args, "--api-url", srvURL, "--api-key", "test-key")
	}
	root.SetArgs(args)

	err := root.Execute()
	if err != nil {
		PrintError(&errBuf, err, app.JSONErrors())
	}
	return out.String(), errBuf.String(), CodeFor(err)
}

func TestEmployeesListWireContract(t *testing.T) {
	var gotPath, gotQuery, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotKey = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-API-KEY")
		fmt.Fprint(w, `{"data":[{"id":1,"email":"a@x.co"}],"metadata":{"page":1,"pages":1,"count":1,"items":25}}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list",
		"--status", "active",
		"--ids", "12", "--ids", "14",
		"--hired-on-gte", "2025-01-01")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotPath != "/employees" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("X-API-KEY = %q", gotKey)
	}
	// Pairs follow spec parameter order, keys verbatim, repeated keys repeat.
	want := "ids[]=12&ids[]=14&status=active&hired_on[gte]=2025-01-01"
	if gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}

	var envelope struct {
		Data []map[string]any `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout)
	}
	if len(envelope.Data) != 1 || envelope.Meta["pages"] != 1.0 {
		t.Errorf("unexpected envelope: %s", stdout)
	}
}

func TestAllPaginationLoops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			fmt.Fprint(w, `{"data":[{"id":1}],"metadata":{"page":1,"pages":3,"count":3,"items":1}}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":2}],"metadata":{"page":2,"pages":3,"count":3,"items":1}}`)
		case "3":
			fmt.Fprint(w, `{"data":[{"id":3}],"metadata":{"page":3,"pages":3,"count":3,"items":1}}`)
		default:
			t.Errorf("unexpected page %q", page)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var envelope struct {
		Data []struct{ ID int `json:"id"` } `json:"data"`
		Meta map[string]any                 `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 3 {
		t.Errorf("fetched %d items, want 3: %s", len(envelope.Data), stdout)
	}
	if envelope.Meta["fetched"] != 3.0 {
		t.Errorf("meta.fetched = %v", envelope.Meta["fetched"])
	}
	if !strings.Contains(stderr, "page 3") {
		t.Errorf("stderr should log page progress, got: %q", stderr)
	}
}

func TestTerminationReasonsUpdateHitsTypoPathVerbatim(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(body)
		fmt.Fprint(w, `{"data":{"id":5,"name":"Better offer"}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "termination", "reasons", "update", "5",
		"--set", "name=Better offer")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotMethod != "PUT" || gotPath != "/termintation_reasons/5" {
		t.Errorf("request = %s %s, want PUT /termintation_reasons/5 (typo preserved)", gotMethod, gotPath)
	}
	if gotBody != `{"name":"Better offer"}` {
		t.Errorf("body = %s", gotBody)
	}
}

func TestMultipartDocumentUpload(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "contract.pdf")
	if err := os.WriteFile(file, []byte("%PDF-fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotContentType, gotFolder, gotName, gotFilename, gotFileBody, gotPartType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(400)
			return
		}
		gotFolder = r.FormValue("document_folder_id")
		gotName = r.FormValue("name")
		f, hdr, err := r.FormFile("document")
		if err != nil {
			t.Errorf("form file: %v", err)
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		gotFilename = hdr.Filename
		gotPartType = hdr.Header.Get("Content-Type")
		b, _ := io.ReadAll(f)
		gotFileBody = string(b)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "documents", "upload", "42",
		"--document", "@"+file, "--name", "Contract", "--document-folder-id", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("content type = %q", gotContentType)
	}
	if gotFolder != "3" || gotName != "Contract" {
		t.Errorf("fields = folder %q, name %q", gotFolder, gotName)
	}
	if gotFilename != "contract.pdf" || gotFileBody != "%PDF-fake" {
		t.Errorf("file = %q (%q)", gotFilename, gotFileBody)
	}
	if gotPartType != "application/pdf" {
		t.Errorf("file part Content-Type = %q, want application/pdf", gotPartType)
	}
}

func TestExitCodesFromHTTPStatuses(t *testing.T) {
	tests := []struct {
		status   int
		wantCode int
		wantType string
	}{
		{401, ExitAuth, "auth"},
		{404, ExitNotFound, "not_found"},
		{422, ExitValidation, "validation"},
		{500, ExitServer, "server"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, `{"message":"nope"}`)
			}))
			defer srv.Close()

			_, stderr, code := runCLI(t, srv.URL, "employees", "get", "1")
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			var e struct {
				Error struct {
					Type   string `json:"type"`
					Status int    `json:"status"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(stderr), &e); err != nil {
				t.Fatalf("stderr is not structured JSON: %q", stderr)
			}
			if e.Error.Type != tt.wantType || e.Error.Status != tt.status {
				t.Errorf("error = %+v", e.Error)
			}
		})
	}
}

func TestRateLimitExhaustionExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	_, _, code := runCLI(t, srv.URL, "employees", "get", "1", "--max-retries", "1")
	if code != ExitRateLimit {
		t.Errorf("exit = %d, want %d", code, ExitRateLimit)
	}
}

func TestDestructiveGuardWithoutYes(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	// Test processes have no TTY on stdin, so the guard must hard-fail.
	_, stderr, code := runCLI(t, srv.URL, "teams", "delete", "9")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if called {
		t.Error("request must not be sent without --yes")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("stderr should mention --yes: %q", stderr)
	}

	// With --yes it goes through.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv2.Close()
	_, stderr, code = runCLI(t, srv2.URL, "teams", "delete", "9", "--yes")
	if code != 0 {
		t.Errorf("exit = %d with --yes, stderr: %s", code, stderr)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "departments", "create",
		"--set", "name=Engineering", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if called {
		t.Error("dry-run must not send a request")
	}
	var preview struct {
		DryRun bool           `json:"dry_run"`
		Method string         `json:"method"`
		URL    string         `json:"url"`
		Body   map[string]any `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.Method != "POST" || preview.Body["name"] != "Engineering" {
		t.Errorf("preview = %s", stdout)
	}
	if strings.Contains(stdout, "test-key") {
		t.Error("dry-run output must not contain the API key")
	}
}

func TestJQAndFieldsProjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":1,"email":"a@x.co","big":"blob"},{"id":2,"email":"b@x.co","big":"blob"}],"metadata":{"page":1,"pages":1,"count":2,"items":25}}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--jq", ".data[].email")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "\"a@x.co\"\n\"b@x.co\"\n" {
		t.Errorf("jq output = %q", stdout)
	}

	stdout, _, code = runCLI(t, srv.URL, "employees", "list", "--fields", "id,email")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(stdout, "blob") {
		t.Errorf("--fields must drop unselected fields: %s", stdout)
	}
}

func TestAPICallEscapeHatch(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "api", "call", "GET", "/job_levels?page=2")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotPath != "/job_levels" || gotQuery != "page=2" {
		t.Errorf("request = %s?%s", gotPath, gotQuery)
	}
}

func TestBulkErrorsMapToValidationExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"records":[{"id":1}],"errors":[{"index":1,"message":"bad"}]}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "api", "call", "POST", "/time/timesheet_entries/bulk",
		"--set", "x=1")
	if code != ExitValidation {
		t.Errorf("exit = %d, want %d", code, ExitValidation)
	}
	if !strings.Contains(stdout, `"errors"`) {
		t.Errorf("stdout should surface errors in meta: %s", stdout)
	}
}

func TestMissingAPIKeyIsAuthExit(t *testing.T) {
	_, stderr, code := runCLI(t, "", "employees", "get", "1")
	if code != ExitAuth {
		t.Errorf("exit = %d, want %d; stderr: %s", code, ExitAuth, stderr)
	}
}
