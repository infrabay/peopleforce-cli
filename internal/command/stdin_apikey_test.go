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
)

// runWithStdin executes the command tree the way main() does, with a supplied
// stdin and an isolated config dir (an empty one when dir is "").
func runWithStdin(t *testing.T, dir, stdin, srvURL string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PEOPLEFORCE_API_KEY", "")
	t.Setenv("PEOPLEFORCE_PROFILE", "")

	root, app := NewRoot()
	var out, errBuf bytes.Buffer
	app.Stdout, app.Stderr = &out, &errBuf
	app.Stdin = strings.NewReader(stdin)
	root.SetOut(&out)
	root.SetErr(&errBuf)

	if srvURL != "" {
		args = append(args, "--api-url", srvURL)
	}
	root.SetArgs(args)

	err := root.Execute()
	if err != nil {
		PrintError(&errBuf, err, app.JSONErrors())
	}
	return out.String(), errBuf.String(), CodeFor(err)
}

// A key in argv is readable through /proc/<pid>/cmdline, ps snapshots and CI
// logs under `set -x`, so it must be possible to pipe it in instead.
func TestAPIKeyFromStdin(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-KEY")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	// CRLF and the trailing newline `echo`/`pass show` add must not travel
	// into the header.
	_, stderr, code := runWithStdin(t, "", "piped-key\r\n", srv.URL, "employees", "list", "--api-key", "-")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if gotKey != "piped-key" {
		t.Errorf("X-API-KEY = %q, want %q", gotKey, "piped-key")
	}
}

func TestAPIKeyFromStdinReportedAsFlagSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runWithStdin(t, "", "piped-key\n", srv.URL, "auth", "status", "--api-key", "-")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"api_key_source": "flag"`) {
		t.Errorf("stdin is still the flag channel; got: %s", stdout)
	}
	if strings.Contains(stdout, "piped-key") {
		t.Errorf("auth status must not echo the key: %s", stdout)
	}
}

func TestAPIKeyFromEmptyStdinIsUsageError(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, stderr, code := runWithStdin(t, "", "   \n", srv.URL, "employees", "list", "--api-key", "-")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d, stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "stdin is empty") {
		t.Errorf("stderr = %s", stderr)
	}
	if called {
		t.Error("an empty key must not be sent to the API")
	}
}

// Both flags want the same stream. Splitting it would silently feed the
// request body to the key header (or the reverse), so the combination is a
// usage error whichever consumer would have run first.
func TestAPIKeyStdinConflictsWithInputStdin(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, stderr, code := runWithStdin(t, "", "piped-key\n", srv.URL,
		"employees", "update", "1", "--api-key", "-", "--input", "-")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d, stderr: %s", code, ExitUsage, stderr)
	}
	for _, want := range []string{"--api-key -", "--input -"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should name %q, got: %s", want, stderr)
		}
	}
	if called {
		t.Error("the conflict must be caught before any request is sent")
	}
}

// readInput now reads App.Stdin rather than os.Stdin; `--input -` must keep
// working through it.
func TestInputFromStdinStillFeedsTheBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runWithStdin(t, "", `{"first_name":"Ada"}`, srv.URL,
		"employees", "update", "1", "--input", "-", "--api-key", "k")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(gotBody, `"first_name":"Ada"`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestAuthLoginReadsAPIKeyFromStdin(t *testing.T) {
	_, stderr, code := runWithStdin(t, "", "stdin-key\n", "", "auth", "login", "--api-key", "-")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["default"].APIKey != "stdin-key" {
		t.Errorf("saved profiles: %+v", cfg.Profiles)
	}
}

// auth status is the documented self-diagnosis command, so a config file it
// cannot parse is precisely when it must stay machine-readable.
func TestAuthStatusEmitsEnvelopeOnUnreadableConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "peopleforce"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "peopleforce", "config.toml"), []byte("this is not = = toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runWithStdin(t, dir, "", "", "auth", "status")
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero; stderr: %s", stderr)
	}
	var wrapper struct {
		Data struct {
			Authenticated bool   `json:"authenticated"`
			ConfigError   string `json:"config_error"`
			Profile       string `json:"profile"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &wrapper); err != nil {
		t.Fatalf("auth status must still emit an envelope: %v\nstdout: %q", err, stdout)
	}
	if wrapper.Data.Authenticated {
		t.Error("authenticated must be false when the config cannot be read")
	}
	if !strings.Contains(wrapper.Data.ConfigError, "config.toml") {
		t.Errorf("config_error should name the file, got %q", wrapper.Data.ConfigError)
	}
	if wrapper.Data.Profile != "default" {
		t.Errorf("profile = %q, want default", wrapper.Data.Profile)
	}
}

// A missing key with an explicitly requested profile usually means the key
// sits under a different profile, so the error has to name the one in use.
func TestMissingKeyErrorNamesRequestedProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "peopleforce"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[profiles.staging]\napi_url = \"https://staging.example.com/api\"\n"
	if err := os.WriteFile(filepath.Join(dir, "peopleforce", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runWithStdin(t, dir, "", "", "employees", "get", "1", "--profile", "staging")
	if code != ExitAuth {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, ExitAuth, stderr)
	}
	// stderr is JSON in the default output mode, so the quotes arrive escaped.
	if !strings.Contains(stderr, `profile \"staging\"`) {
		t.Errorf("stderr should name the requested profile, got: %s", stderr)
	}
}
