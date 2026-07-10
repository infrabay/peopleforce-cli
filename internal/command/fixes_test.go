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

	"github.com/3bagels/peopleforce-cli/internal/config"
)

// Regression tests for the confirmed review findings.

func TestMultipartRequiredSatisfiedViaSet(t *testing.T) {
	var gotName, gotFolder string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotName = r.FormValue("name")
		gotFolder = r.FormValue("document_folder_id")
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "documents", "upload", "42",
		"--set", "name=Contract", "--set", "document_folder_id=3", "--set", "url=https://x.co/d.pdf")
	if code != 0 {
		t.Fatalf("required fields via --set must be accepted; exit = %d, stderr: %s", code, stderr)
	}
	if gotName != "Contract" || gotFolder != "3" {
		t.Errorf("fields = %q, %q", gotName, gotFolder)
	}
}

func TestMultipartInputMergedNotIgnored(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "body.json")
	os.WriteFile(input, []byte(`{"name":"FromInput","document_folder_id":7,"url":"https://x.co/d.pdf"}`), 0o644)

	var gotName, gotFolder string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		gotName = r.FormValue("name")
		gotFolder = r.FormValue("document_folder_id")
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	// --set overrides --input per the documented precedence.
	_, stderr, code := runCLI(t, srv.URL, "employees", "documents", "upload", "42",
		"--input", "@"+input, "--set", "name=Overridden")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotName != "Overridden" || gotFolder != "7" {
		t.Errorf("merged fields = name %q, folder %q", gotName, gotFolder)
	}
}

func TestRepeatableJSONBodyFlag(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "teams", "create",
		"--name", "Core", "--team-lead-id", "5",
		"--user-ids", "1", "--user-ids", "2")
	if code != 0 {
		t.Fatalf("repeatable JSON body flag must work; exit = %d, stderr: %s", code, stderr)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatal(err)
	}
	arr, ok := body["user_ids[]"].([]any)
	if !ok || len(arr) != 2 || arr[0] != 1.0 || arr[1] != 2.0 {
		t.Errorf("user_ids[] = %v (body: %s)", body["user_ids[]"], gotBody)
	}
}

func TestEmptyPathParamRejected(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "get", "")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if called {
		t.Error("request must not be sent with an empty path param")
	}
	if !strings.Contains(stderr, "must not be empty") {
		t.Errorf("stderr: %s", stderr)
	}
}

func TestUnknownSubcommandOfGroupIsUsageError(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "employees", "frobnicate")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d; stdout: %s", code, ExitUsage, stdout)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("stderr should name the unknown command: %s", stderr)
	}
}

func TestInvalidJQFailsBeforeRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, _, code := runCLI(t, srv.URL, "employees", "list", "--jq", ".data[")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if called {
		t.Error("invalid --jq must fail before any request is sent")
	}

	_, _, code = runCLI(t, srv.URL, "employees", "list", "--output", "xml")
	if code != ExitUsage {
		t.Errorf("exit = %d for bad --output, want %d", code, ExitUsage)
	}
	if called {
		t.Error("invalid --output must fail before any request is sent")
	}
}

func TestAllPaginationEmptyResultIsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"metadata":{"page":1,"pages":0,"count":0,"items":25}}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(envelope.Data)) != "[]" {
		t.Errorf("--all with zero results must emit [], got %s", envelope.Data)
	}
}

func TestAuthLoginHonorsProfileEnv(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("PEOPLEFORCE_PROFILE", "staging")
	t.Setenv("PEOPLEFORCE_API_KEY", "")

	root, app := NewRoot()
	var out, errBuf strings.Builder
	app.Stdout, app.Stderr = &out, &errBuf
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "login", "--api-key", "stg-key"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["staging"].APIKey != "stg-key" {
		t.Errorf("key must land in the PEOPLEFORCE_PROFILE profile; got profiles: %+v", cfg.Profiles)
	}
}

func TestAPICallSanitizesQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "api", "call", "GET", "/employees?search=John Doe&ids[]=1&x=100%")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotQuery != "search=John%20Doe&ids[]=1&x=100%25" {
		t.Errorf("sanitized query = %q", gotQuery)
	}
}

func TestDryRunHonorsAPIURLWithoutKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PEOPLEFORCE_API_KEY", "")
	t.Setenv("PEOPLEFORCE_PROFILE", "")

	root, app := NewRoot()
	var out, errBuf strings.Builder
	app.Stdout, app.Stderr = &out, &errBuf
	root.SetOut(&out)
	root.SetErr(&errBuf)
	// No --api-key: dry-run must still respect --api-url.
	root.SetArgs([]string{"departments", "create", "--set", "name=X",
		"--api-url", "https://staging.example.com/api", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run without key failed: %v", err)
	}
	if !strings.Contains(out.String(), "https://staging.example.com/api/departments") {
		t.Errorf("dry-run URL must honor --api-url: %s", out.String())
	}
}

func TestRepeatableEnumValidated(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "leave", "requests", "list", "--states", "bogus")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitUsage, stderr)
	}
	if called {
		t.Error("invalid enum value must fail before the request")
	}
}

func TestAllAndPageExclusiveEvenWithDryRun(t *testing.T) {
	_, _, code := runCLI(t, "", "employees", "list", "--all", "--page", "2", "--dry-run")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

func TestPOSTNotRetriedOn502(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, _, code := runCLI(t, srv.URL, "departments", "create", "--set", "name=X", "--max-retries", "3")
	if code != ExitServer {
		t.Errorf("exit = %d, want %d", code, ExitServer)
	}
	if calls != 1 {
		t.Errorf("POST sent %d times on 502; must not be retried", calls)
	}
}

func TestAuthStatusProbe(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/calendars" {
			t.Errorf("probe path = %q, want /calendars", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer okSrv.Close()

	stdout, stderr, code := runCLI(t, okSrv.URL, "auth", "status")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var ok struct {
		Data struct {
			Authenticated bool   `json:"authenticated"`
			ProbeStatus   int    `json:"probe_status"`
			KeySource     string `json:"api_key_source"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &ok); err != nil {
		t.Fatalf("auth status output: %v\n%s", err, stdout)
	}
	if !ok.Data.Authenticated || ok.Data.ProbeStatus != 200 || ok.Data.KeySource != "flag" {
		t.Errorf("unexpected status payload: %s", stdout)
	}

	// Rejected key → exit 3, but the diagnostic payload still lands on stdout.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer badSrv.Close()

	stdout, _, code = runCLI(t, badSrv.URL, "auth", "status")
	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	if !strings.Contains(stdout, `"authenticated": false`) {
		t.Errorf("stdout should carry authenticated:false: %s", stdout)
	}
}

func TestAllDegradesToSingleFetchOnNonList(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":{"id":1,"name":"solo"}}`)
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if calls != 1 {
		t.Errorf("non-list --all must fetch once, got %d requests", calls)
	}
	if !strings.Contains(stdout, `"name": "solo"`) {
		t.Errorf("single-object envelope must pass through: %s", stdout)
	}
}

func TestAllPagesUntilEmptyWhenMetadataMissing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprint(w, `{"data":[{"id":1},{"id":2}]}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":3}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if calls != 3 {
		t.Errorf("expected 3 requests (page until empty), got %d", calls)
	}
	var env struct {
		Data []struct{ ID int `json:"id"` } `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 3 {
		t.Errorf("must collect ALL pages without metadata, got %d items: %s", len(env.Data), stdout)
	}
	if !strings.Contains(stderr, "no pagination metadata") {
		t.Errorf("stderr should note the fallback: %q", stderr)
	}
}

func TestAllStopsWhenEndpointIgnoresPageParam(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":[{"id":1},{"id":2}]}`) // same page regardless of ?page=
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	// Two consecutive replays of page 1 confirm the backend ignores ?page=.
	if calls != 3 {
		t.Errorf("expected 3 requests (confirm replay twice), got %d", calls)
	}
	var env struct {
		Data []struct{ ID int `json:"id"` } `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 {
		t.Errorf("replayed pages must not be appended, got %d items", len(env.Data))
	}
	if !strings.Contains(stderr, "replay") {
		t.Errorf("stderr should warn about the ignored page param: %q", stderr)
	}
}

// A page that coincidentally equals page 1 mid-stream is legitimate data —
// it must be kept and pagination must continue (Opus review finding).
func TestAllKeepsCoincidentallyIdenticalPage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprint(w, `{"data":[{"id":1}]}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":1}]}`) // same bytes as page 1, but real
		case "3":
			fmt.Fprint(w, `{"data":[{"id":9}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if calls != 4 {
		t.Errorf("expected 4 requests, got %d", calls)
	}
	var env struct {
		Data []struct{ ID int `json:"id"` } `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, d := range env.Data {
		ids = append(ids, d.ID)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 1 || ids[2] != 9 {
		t.Errorf("coincidentally identical page must be kept in order, got %v", ids)
	}
}

// The held page is real data when the list simply ends after it.
func TestAllFlushesHeldPageOnEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1", "2":
			fmt.Fprint(w, `{"data":[{"id":1}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "employees", "list", "--all")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var env struct {
		Data []struct{ ID int `json:"id"` } `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 {
		t.Errorf("held page must be flushed when the list ends, got %d items: %s", len(env.Data), stdout)
	}
}
