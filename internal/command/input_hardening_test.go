package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/infrabay/peopleforce-cli/internal/registry"
)

// countingServer answers every request and counts them, so tests can assert
// that a usage error fired before anything was sent.
func countingServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*n++
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, n
}

// Decode stops after the first JSON value, so trailing garbage, a second
// object or `1 999` used to be dropped silently while the valid prefix was
// sent and the exit code was 0.
func TestTrailingJSONDataIsAUsageError(t *testing.T) {
	dir := t.TempDir()
	two := filepath.Join(dir, "two.jsonl")
	if err := os.WriteFile(two, []byte("{\"first_name\":\"A\"}\n{\"first_name\":\"B\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"inline garbage":  {"employees", "update", "1", "--input", `{"first_name":"Alice"} INVALID`},
		"two objects":     {"employees", "update", "1", "--input", "@" + two},
		"set :=":          {"employees", "update", "1", "--set", `amount:=1 999`},
		"api call inline": {"api", "call", "POST", "/departments", "--input", `{"name":"x"} {"name":"y"}`},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			srv, reqs := countingServer(t)
			_, stderr, code := runCLI(t, srv.URL, args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, "trailing data") {
				t.Errorf("stderr should mention trailing data: %s", stderr)
			}
			if *reqs != 0 {
				t.Errorf("%d request(s) sent, want none", *reqs)
			}
		})
	}
}

func TestBulkArrayInputRejectsSecondArray(t *testing.T) {
	srv, reqs := countingServer(t)
	in := filepath.Join(t.TempDir(), "in.json")
	body := `[{"id":1,"set":{"a":"b"}}] [{"id":2,"set":{"a":"b"}}]`
	if err := os.WriteFile(in, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, srv.URL, "employees", "bulk-update", "--input", "@"+in)
	if code != ExitUsage || !strings.Contains(stderr, "trailing data") {
		t.Errorf("exit = %d, stderr: %s", code, stderr)
	}
	if *reqs != 0 {
		t.Errorf("%d request(s) sent, want none", *reqs)
	}
}

// `null` decoded into the body map replaced it with nil and the next --set
// panicked with "assignment to entry in nil map".
func TestNonObjectInputIsAUsageErrorNotAPanic(t *testing.T) {
	for _, stdin := range []string{"null", "[1]", `"s"`, "5", "true"} {
		for _, args := range [][]string{
			{"employees", "update", "1", "--input", "-", "--set", "first_name=Alice"},
			{"api", "call", "PUT", "/employees/1", "--input", "-", "--set", "first_name=Alice"},
		} {
			t.Run(stdin+"/"+args[0], func(t *testing.T) {
				srv, reqs := countingServer(t)
				_, stderr, code := runWithStdin(t, "", stdin, srv.URL, append(args, "--api-key", "k")...)
				if code != ExitUsage {
					t.Errorf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
				}
				if *reqs != 0 {
					t.Errorf("%d request(s) sent, want none", *reqs)
				}
			})
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The PUT already succeeded, so exit 2 ("bad args, safe to re-run") lied.
func TestBulkUpdateReportWriteFailureIsExitOutput(t *testing.T) {
	srv, reqs := countingServer(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PEOPLEFORCE_API_KEY", "")
	t.Setenv("PEOPLEFORCE_PROFILE", "")
	root, app := NewRoot()
	var errBuf bytes.Buffer
	app.Stdout, app.Stderr = failingWriter{}, &errBuf
	app.Stdin = strings.NewReader(`{"id":7,"set":{"a":"b"}}`)
	root.SetOut(&errBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"employees", "bulk-update", "--input", "-", "--api-url", srv.URL, "--api-key", "k"})
	err := root.Execute()
	if *reqs != 1 {
		t.Fatalf("requests = %d, want 1", *reqs)
	}
	if code := CodeFor(err); code != ExitOutput {
		t.Fatalf("exit = %d, want %d (err: %v)", code, ExitOutput, err)
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("error should name the record id: %v", err)
	}
}

func TestAuthStatusEmitsEnvelopeOnNetworkFailure(t *testing.T) {
	t.Setenv("PEOPLEFORCE_API_URL", "http://127.0.0.1:1")
	stdout, _, code := runWithStdin(t, "", "", "", "auth", "status", "--api-key", "secret-key-123", "--max-retries", "0")
	if code != ExitNetwork {
		t.Fatalf("exit = %d, want %d", code, ExitNetwork)
	}
	if strings.Contains(stdout, "secret-key-123") {
		t.Errorf("API key leaked into the envelope: %s", stdout)
	}
	var w struct {
		Data struct {
			Authenticated bool   `json:"authenticated"`
			ProbeError    string `json:"probe_error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("no envelope on stdout: %v\n%q", err, stdout)
	}
	if w.Data.Authenticated || w.Data.ProbeError == "" {
		t.Errorf("unexpected envelope: %s", stdout)
	}
}

func TestAuthLoginDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code := runWithStdin(t, dir, "", "", "auth", "login", "--api-key", "secret-key-123", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(stdout, "secret-key-123") {
		t.Errorf("preview leaked the key: %s", stdout)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(stdout), &p); err != nil {
		t.Fatalf("preview is not JSON: %v\n%s", err, stdout)
	}
	if p["dry_run"] != true || p["api_key"] != "(set)" || p["profile"] != "default" {
		t.Errorf("unexpected preview: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "peopleforce")); err == nil {
		t.Error("dry-run created the config directory")
	}
}

// gojq.Parse accepts an undefined function; only compiling rejects it, which
// used to happen after the POST had gone out.
func TestJQUndefinedFunctionRejectedBeforeRequest(t *testing.T) {
	srv, reqs := countingServer(t)
	_, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/departments",
		"--set", "name=Eng", "--jq", "does_not_exist")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if *reqs != 0 {
		t.Errorf("%d request(s) sent, want none", *reqs)
	}
}

func TestRootHelpDocumentsExit9AndOpCount(t *testing.T) {
	stdout, _, _ := runCLI(t, "", "--help")
	if !strings.Contains(stdout, "9 the request succeeded") {
		t.Errorf("root help lacks exit code 9:\n%s", stdout)
	}
	want := fmt.Sprintf("all %d API operations", registry.Info.OpCount)
	if !strings.Contains(stdout, want) {
		t.Errorf("root help should contain %q:\n%s", want, stdout)
	}
}
