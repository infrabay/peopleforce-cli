package command

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registry.Match cleans the path, but the request target is sent verbatim
// after the base path, so a dot segment let `/../v3/employees/1/terminate`
// skip the --yes guard while a proxy resolved it into the terminate endpoint.
func TestAPICallRejectsDotSegments(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	for _, path := range []string{
		"/../v3/employees/1/terminate",
		"/%2e%2e/v3/employees/1/terminate",
		"/%2E%2E/v3/employees/1/terminate",
		"/employees/./1",
		"/employees/1/x%2f..%2fterminate",
		"/..",
	} {
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			sent = false
			args := append([]string{"api", "call", "POST", path}, extra...)
			stdout, stderr, code := runCLI(t, srv.URL, args...)
			if code != ExitUsage {
				t.Errorf("%v: exit = %d, want 2 (stderr: %s)", args, code, stderr)
			}
			if sent {
				t.Errorf("%v: request was sent", args)
			}
			if stdout != "" {
				t.Errorf("%v: unexpected stdout %q", args, stdout)
			}
		}
	}
}

// Paths that merely look similar keep working.
func TestAPICallAllowsOrdinaryPaths(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.EscapedPath())
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	for _, path := range []string{"/files/a.b", "/employees.json", "/employees/1/terminate/..x", "/employees?q=../x"} {
		if _, stderr, code := runCLI(t, srv.URL, "api", "call", "GET", path); code != 0 {
			t.Errorf("GET %s: exit = %d, stderr: %s", path, code, stderr)
		}
	}
	if len(got) != 4 {
		t.Errorf("sent %d requests, want 4: %v", len(got), got)
	}
	// Existing spellings still reach the destructive guard.
	for _, path := range []string{"/employees/1/%74erminate", "//employees/1/terminate", "/employees/1/terminate.json"} {
		if _, _, code := runCLI(t, srv.URL, "api", "call", "POST", path); code != ExitUsage {
			t.Errorf("POST %s without --yes: exit = %d, want 2", path, code)
		}
	}
}

// The preview must be the request that goes out: no --input/--set means no
// body and no Content-Type, so no "body" key.
func TestAPICallDryRunShowsBodyOnlyWhenSent(t *testing.T) {
	stdout, stderr, code := runCLI(t, "http://example.invalid", "api", "call", "POST", "/departments", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if strings.Contains(stdout, `"body"`) {
		t.Errorf("no body is sent, preview should not show one: %s", stdout)
	}
	stdout, _, _ = runCLI(t, "http://example.invalid", "api", "call", "POST", "/departments", "--input", "{}", "--dry-run")
	if strings.Contains(stdout, `"body"`) {
		t.Errorf("--input {} sends no body, preview should not show one: %s", stdout)
	}
	stdout, _, code = runCLI(t, "http://example.invalid", "api", "call", "POST", "/departments", "--set", "name=Eng", "--dry-run")
	if code != 0 || !strings.Contains(stdout, `"name": "Eng"`) {
		t.Errorf("--set body missing from preview (exit %d): %s", code, stdout)
	}
}

// README: every mutation supports --dry-run. A dry run names what it would
// write and creates nothing, not even the directories.
func TestSkillInstallDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	_, stderr, code := runCLI(t, "", "skill", "install", "--agent", "all", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	for _, dir := range []string{".claude", ".agents"} {
		want := "dry run: would install " + filepath.Join(dir, "skills", "peopleforce", "SKILL.md")
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q, got: %q", want, stderr)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run created %d entries in the project", len(entries))
	}

	// --global too, and the symlink refusal is still reported.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, stderr, code = runCLI(t, "", "skill", "install", "--global", "--dry-run"); code != 0 {
		t.Fatalf("global: exit = %d, stderr: %s", code, stderr)
	}
	if entries, _ = os.ReadDir(home); len(entries) != 0 {
		t.Errorf("global dry run created %d entries in home", len(entries))
	}

	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".claude", "skills", "peopleforce")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, stderr, code = runCLI(t, "", "skill", "install", "--dry-run"); code != ExitUsage || !strings.Contains(stderr, "symlink") {
		t.Errorf("dry run over a symlink: exit = %d, stderr: %s", code, stderr)
	}
}
