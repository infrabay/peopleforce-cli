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

	"github.com/3bagels/peopleforce-cli/internal/config"
	"github.com/3bagels/peopleforce-cli/internal/registry"
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
	// No operation in the current spec has a repeatable JSON body field:
	// teams create's user_ids[] was the only one, and it is skipped in
	// overrides.yaml because the backend ignores it. The code path is still
	// live for the next spec that introduces one, so drive it from a
	// synthetic op rather than dropping the coverage.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	op := &registry.Op{
		Method: "POST", Path: "/things", Command: "things create",
		Summary: "Create a thing", BodyKind: registry.BodyJSON,
		Body: []registry.BodyField{
			{Name: "member_ids[]", Flag: "member-ids", Type: registry.TypeInteger, Repeatable: true},
		},
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PEOPLEFORCE_PROFILE", "")
	root, app := NewRoot()
	var out, errBuf bytes.Buffer
	app.Stdout, app.Stderr = &out, &errBuf
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.AddCommand(newOpCommand(app, op, "synthetic"))
	root.SetArgs([]string{"synthetic", "--member-ids", "1", "--member-ids", "2",
		"--api-url", srv.URL, "--api-key", "test-key"})

	if err := root.Execute(); err != nil {
		t.Fatalf("repeatable JSON body flag must work: %v (stderr: %s)", err, errBuf.String())
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatal(err)
	}
	arr, ok := body["member_ids[]"].([]any)
	if !ok || len(arr) != 2 || arr[0] != 1.0 || arr[1] != 2.0 {
		t.Errorf("member_ids[] = %v (body: %s)", body["member_ids[]"], gotBody)
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

// auth status is the command agents run to work out why nothing works, so its
// failure branches carry as much contract weight as the happy path: the exit
// code alone has to separate "your key is wrong" from "the API is down".
func TestAuthStatusFailureBranches(t *testing.T) {
	t.Run("no key configured anywhere", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer srv.Close()

		stdout, stderr, code := runCLI(t, "", "auth", "status", "--api-url", srv.URL)
		if code != ExitAuth {
			t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitAuth, stderr)
		}
		if called {
			t.Error("with no key to test there is nothing to probe; no request may be sent")
		}
		var got struct {
			Data struct {
				Authenticated bool   `json:"authenticated"`
				KeySource     string `json:"api_key_source"`
				ProbeStatus   *int   `json:"probe_status"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("auth status output: %v\n%s", err, stdout)
		}
		if got.Data.Authenticated || got.Data.KeySource != "none" {
			t.Errorf("want authenticated:false with source none, got: %s", stdout)
		}
		if got.Data.ProbeStatus != nil {
			t.Errorf("probe_status must be absent when nothing was probed: %s", stdout)
		}
		if !strings.Contains(stderr, config.EnvAPIKey) {
			t.Errorf("stderr should say how to supply a key: %s", stderr)
		}
	})

	// A 403 is almost never the key itself, so the diagnosis has to point at
	// the allowlist and name the source the rejected key came from.
	t.Run("403 is an auth failure naming the allowlist", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		stdout, stderr, code := runCLI(t, srv.URL, "auth", "status")
		if code != ExitAuth {
			t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitAuth, stderr)
		}
		for _, want := range []string{"allowlist", "source: flag"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr should mention %q, got: %s", want, stderr)
			}
		}
		if !strings.Contains(stdout, `"probe_status": 403`) || !strings.Contains(stdout, `"authenticated": false`) {
			t.Errorf("the diagnostic payload must still land on stdout: %s", stdout)
		}
	})

	// A broken backend must not read as "your credentials are fine" (exit 0)
	// nor as "your key is bad" (exit 3) — the caller should retry, not go
	// hunting for a new key.
	t.Run("500 exits with the server code", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		stdout, stderr, code := runCLI(t, srv.URL, "auth", "status")
		if code != ExitServer {
			t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitServer, stderr)
		}
		if !strings.Contains(stdout, `"probe_status": 500`) || !strings.Contains(stdout, `"authenticated": false`) {
			t.Errorf("the diagnostic payload must still land on stdout: %s", stdout)
		}
	})
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

// A key in argv is readable by `ps` and by any CI log running under `set -x`,
// so `--api-key -` exists to keep it off the command line entirely.
func TestAPIKeyStdinDelivery(t *testing.T) {
	// echo, `pass show` and a file written on Windows deliver the same secret
	// with three different line endings; all three must produce one header.
	t.Run("newline variants deliver the same key", func(t *testing.T) {
		for _, tc := range []struct{ name, stdin string }{
			{"bare", "piped-key"},
			{"lf", "piped-key\n"},
			{"crlf", "piped-key\r\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var gotKey string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotKey = r.Header.Get("X-API-KEY")
					fmt.Fprint(w, `{"data":[]}`)
				}))
				defer srv.Close()

				_, stderr, code := runWithStdin(t, "", tc.stdin, srv.URL, "employees", "list", "--api-key", "-")
				if code != 0 {
					t.Fatalf("exit = %d, stderr: %s", code, stderr)
				}
				if gotKey != "piped-key" {
					t.Errorf("X-API-KEY = %q, want %q", gotKey, "piped-key")
				}
			})
		}
	})

	t.Run("empty stdin is a usage error", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer srv.Close()

		_, stderr, code := runWithStdin(t, "", "", srv.URL, "employees", "list", "--api-key", "-")
		if code != ExitUsage {
			t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitUsage, stderr)
		}
		if called {
			t.Error("an empty key must not reach the API as a request")
		}
	})

	// Piping the key is still the flag channel: it outranks the config file,
	// and auth status has to report the source it actually used.
	t.Run("piped key outranks the config file and reports source flag", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "peopleforce"), 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := "[profiles.default]\napi_key = \"from-config\"\n"
		if err := os.WriteFile(filepath.Join(dir, "peopleforce", "config.toml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}

		var gotKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("X-API-KEY")
			fmt.Fprint(w, `{"data":[]}`)
		}))
		defer srv.Close()

		stdout, stderr, code := runWithStdin(t, dir, "piped-key\n", srv.URL, "auth", "status", "--api-key", "-")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		if gotKey != "piped-key" {
			t.Errorf("X-API-KEY = %q; the piped key must win over the config file", gotKey)
		}
		if !strings.Contains(stdout, `"api_key_source": "flag"`) {
			t.Errorf("api_key_source must stay flag: %s", stdout)
		}
		if strings.Contains(stdout, "piped-key") {
			t.Errorf("the key must never be echoed back: %s", stdout)
		}
	})

	t.Run("auth login persists the piped key without argv exposure", func(t *testing.T) {
		const key = "login-piped-key"
		args := []string{"auth", "login", "--api-key", "-"}

		_, stderr, code := runWithStdin(t, "", key+"\n", "", args...)
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Profiles["default"].APIKey != key {
			t.Errorf("piped key must be saved to the profile; got: %+v", cfg.Profiles)
		}
		// argv is exactly what this flow exists to keep the secret out of.
		if strings.Contains(strings.Join(args, " "), key) {
			t.Errorf("the key reached the command line: %v", args)
		}
	})

	// Splitting one stream between the key and the request body would feed
	// each the other's bytes, so the combination is rejected — and because the
	// claim happens before any consumer runs, flag order cannot change it.
	t.Run("claiming stdin twice fails identically in either flag order", func(t *testing.T) {
		var messages []string
		for _, args := range [][]string{
			{"employees", "update", "1", "--api-key", "-", "--input", "-"},
			{"employees", "update", "1", "--input", "-", "--api-key", "-"},
		} {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))

			_, stderr, code := runWithStdin(t, "", "piped-key\n", srv.URL, args...)
			srv.Close()

			if code != ExitUsage {
				t.Errorf("%v: exit = %d, want %d (stderr: %s)", args, code, ExitUsage, stderr)
			}
			if called {
				t.Errorf("%v: no request may be sent once stdin is contested", args)
			}
			// The key claims the stream first, so the diagnosis reads the same
			// way however the flags were ordered — never a JSON parse error
			// from a body that the key already drained.
			if !strings.Contains(stderr, "--api-key - and --input -") {
				t.Errorf("%v: stderr must name both claimants, key first: %s", args, stderr)
			}
			messages = append(messages, stderr)
		}
		if messages[0] != messages[1] {
			t.Errorf("the conflict must not depend on flag order:\n%s\n%s", messages[0], messages[1])
		}
	})
}
