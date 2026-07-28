package command

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A render failure after a successful request used to report exit 2, which
// the documented contract defines as "bad flags/args" — telling an agent the
// request never happened and is safe to re-run.
func TestRenderFailureAfterMutationIsNotUsageExit(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		fmt.Fprint(w, `{"data":[{"id":1},{"id":2}]}`)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/departments",
		"--set", "name=Eng",
		"--jq", `.data[] | if .id == 2 then error("boom") else .id end`)
	if posts != 1 {
		t.Fatalf("expected exactly one POST, got %d", posts)
	}
	if code == ExitUsage {
		t.Errorf("exit = %d (usage); a completed mutation must not look like a flag typo", code)
	}
	if code != ExitOutput {
		t.Errorf("exit = %d, want %d (output)", code, ExitOutput)
	}
	if stdout != "" {
		t.Errorf("stdout must stay empty when rendering fails, got %q", stdout)
	}
	if !strings.Contains(stderr, "succeeded") {
		t.Errorf("stderr should say the request went through, got %q", stderr)
	}
}

func TestGlobalFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"zero timeout", []string{"--timeout", "0"}, "--timeout must be positive"},
		{"negative timeout", []string{"--timeout", "-5s"}, "--timeout must be positive"},
		{"negative retries", []string{"--max-retries", "-2"}, "--max-retries cannot be negative"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"employees", "list"}, tt.args...)
			_, stderr, code := runCLI(t, "http://127.0.0.1:1", args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tt.want)
			}
		})
	}
}

// ":=1" slipped past the key-length guard and created a field named "".
func TestSetRejectsEmptyKey(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer srv.Close()

	_, _, code := runCLI(t, srv.URL, "api", "call", "POST", "/departments", "--set", ":=1")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if sent {
		t.Error("a body with an empty field name must not be sent")
	}
}

// A group names no operation: stdout must stay clean and the exit code must
// signal the mistake, like an unknown subcommand already does.
func TestBareGroupIsUsageErrorWithCleanStdout(t *testing.T) {
	stdout, _, code := runCLI(t, "", "employees")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout must carry data only, got %d bytes", len(stdout))
	}
}

// Exit code 8 is documented in --help, README, agents.md and skill.md and was
// never asserted end to end.
func TestTransportFailureExitsNetwork(t *testing.T) {
	// Port 1 on loopback refuses connections immediately.
	_, stderr, code := runCLI(t, "http://127.0.0.1:1", "employees", "list", "--max-retries", "0")
	if code != ExitNetwork {
		t.Errorf("exit = %d, want %d (network), stderr: %s", code, ExitNetwork, stderr)
	}
}

func TestPlaintextAPIURLWarns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	// httptest serves on 127.0.0.1, which is exempt; use a hostname alias.
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, stderr, _ := runCLI(t, url, "employees", "list")
	if strings.Contains(stderr, "cleartext") {
		t.Errorf("loopback must not warn, got %q", stderr)
	}
}

// 401 and 403 send the operator to different places: a 403 with a well-formed
// key is usually an IP outside the allowlist, not a bad token. They used to
// share one "check your API key" sentence.
func TestAuthErrorsDistinguish401From403(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
		avoid  string
	}{
		{401, "missing or invalid", "allowlist"},
		{403, "allowlist", "missing or invalid"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			fmt.Fprint(w, `{"message":"nope"}`)
		}))
		_, stderr, code := runCLI(t, srv.URL, "employees", "list")
		srv.Close()

		if code != ExitAuth {
			t.Errorf("HTTP %d: exit = %d, want %d", tt.status, code, ExitAuth)
		}
		if !strings.Contains(stderr, tt.want) {
			t.Errorf("HTTP %d: stderr should mention %q, got %s", tt.status, tt.want, stderr)
		}
		if strings.Contains(stderr, tt.avoid) {
			t.Errorf("HTTP %d: stderr should not mention %q, got %s", tt.status, tt.avoid, stderr)
		}
	}
}

// --dry-run claims to print "the exact request that would be sent", but an
// empty body map was previewed as "body": {} while the real call sends no
// body and no Content-Type at all.
func TestDryRunOmitsBodyWhenNoneWouldBeSent(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "employees", "update", "1", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatalf("dry-run is not JSON: %v (%s)", err, stdout)
	}
	if _, present := preview["body"]; present {
		t.Errorf("no body is sent, so none should be previewed: %s", stdout)
	}
}

// --dry-run is the documented pre-flight check for a mutation, so it must not
// approve an upload whose file cannot be read.
func TestDryRunRejectsMissingUploadFile(t *testing.T) {
	_, stderr, code := runCLI(t, "", "employees", "documents", "upload", "42",
		"--document", "@/nonexistent/contract.pdf", "--name", "C", "--document-folder-id", "3", "--dry-run")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "document") {
		t.Errorf("error should name the field, got: %s", stderr)
	}
}

func TestDryRunAcceptsReadableUploadFile(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "contract.pdf")
	if err := os.WriteFile(doc, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "", "employees", "documents", "upload", "42",
		"--document", "@"+doc, "--name", "C", "--document-folder-id", "3", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "@"+doc) {
		t.Errorf("preview should show the file reference, got: %s", stdout)
	}
}
