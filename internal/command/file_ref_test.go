package command

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readParts returns the multipart parts a handler received, keyed by field
// name, along with the filename of any file part.
func readParts(t *testing.T, r *http.Request) (values map[string][]string, filenames map[string]string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content-type: %v", err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	values, filenames = map[string][]string{}, map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		b, _ := io.ReadAll(p)
		values[p.FormName()] = append(values[p.FormName()], string(b))
		if fn := p.FileName(); fn != "" {
			filenames[p.FormName()] = fn
		}
	}
	return values, filenames
}

// @path used to be expanded for values arriving from --input and --set, so a
// body an agent assembled from untrusted content could read any local file.
func TestFileRefFromDataChannelsIsRejected(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	cases := []struct {
		name string
		args []string
	}{
		{"--input", []string{"employees", "documents", "upload", "42", "--input",
			fmt.Sprintf(`{"name":"Contract","document_folder_id":3,"document":"@%s"}`, secret)}},
		{"--set", []string{"employees", "documents", "upload", "42",
			"--name", "Contract", "--document-folder-id", "3", "--set", "document=@" + secret}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sent = false
			_, stderr, code := runCLI(t, srv.URL, tt.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (usage)", code)
			}
			if sent {
				t.Error("request was sent; the file may have been uploaded")
			}
			if !strings.Contains(stderr, "only accepted from the --document flag") {
				t.Errorf("error should name the safe route, got: %s", stderr)
			}
		})
	}
}

// The typed flag is the operator's own instruction and must still upload.
func TestFileRefFromTypedFlagStillUploads(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "contract.pdf")
	if err := os.WriteFile(doc, []byte("PDF BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotValues map[string][]string
	var gotFilenames map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotValues, gotFilenames = readParts(t, r)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "employees", "documents", "upload", "42",
		"--document", "@"+doc, "--name", "Contract", "--document-folder-id", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if got := gotValues["document"]; len(got) != 1 || got[0] != "PDF BYTES" {
		t.Errorf("document part = %q, want the file contents", got)
	}
	if gotFilenames["document"] != "contract.pdf" {
		t.Errorf("filename = %q, want contract.pdf", gotFilenames["document"])
	}
}

// Repeating a --set key on a multipart op used to keep only the last value.
func TestMultipartSetRepeatsAppend(t *testing.T) {
	var gotValues map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotValues, _ = readParts(t, r)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "recruitment", "candidates", "create",
		"--full-name", "Ada", "--set", "skills[]=go", "--set", "skills[]=rust")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	got := gotValues["skills[]"]
	if len(got) != 2 || got[0] != "go" || got[1] != "rust" {
		t.Errorf("skills[] parts = %q, want [go rust]", got)
	}
}

// api call is a write path; --input used to route numbers through float64.
func TestAPICallInputPreservesLargeNumbers(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/departments",
		"--input", `{"big":12345678901234567890,"exact":10.50}`)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(gotBody), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, gotBody)
	}
	if string(got["big"]) != "12345678901234567890" {
		t.Errorf("big = %s, want 12345678901234567890", got["big"])
	}
	if string(got["exact"]) != "10.50" {
		t.Errorf("exact = %s, want 10.50", got["exact"])
	}
}

// The docs promise the --yes guard covers "deletes, employees terminate";
// api call used to check the HTTP method only.
func TestAPICallHonoursRegistryDestructive(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/employees/1/terminate",
		"--set", "effective_from=2026-01-01")
	if code != 2 {
		t.Errorf("exit = %d, want 2 without --yes", code)
	}
	if sent {
		t.Error("destructive request was sent without --yes")
	}
	if !strings.Contains(stderr, "destructive") {
		t.Errorf("stderr should explain the guard, got: %s", stderr)
	}

	sent = false
	if _, stderr, code = runCLI(t, srv.URL, "api", "call", "POST", "/employees/1/terminate",
		"--set", "effective_from=2026-01-01", "--yes"); code != 0 {
		t.Fatalf("with --yes: exit = %d, stderr: %s", code, stderr)
	}
	if !sent {
		t.Error("--yes should let the request through")
	}
}

// A non-destructive POST must not be caught by the registry lookup.
func TestAPICallNonDestructivePostIsNotGuarded(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		fmt.Fprint(w, `{"data":{"id":1}}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "api", "call", "POST", "/departments", "--set", "name=Eng")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !sent {
		t.Error("a plain POST must not require --yes")
	}
}
