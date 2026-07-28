package command

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3bagels/peopleforce-cli/internal/envelope"
	"github.com/3bagels/peopleforce-cli/internal/output"
)

func formatApp(format string) *App {
	return &App{outFormat: format, timeout: 30 * time.Second}
}

// Every name the flag help advertises must survive validation and reach a
// renderer: the list and the render switch are separate declarations, and a
// format known to only one of them is the failure this list exists to prevent.
func TestDeclaredFormatsValidateAndRender(t *testing.T) {
	for _, format := range output.Formats {
		if err := formatApp(format).validateOutputOptions(); err != nil {
			t.Errorf("--output %s rejected by validation: %v", format, err)
			continue
		}
		var out bytes.Buffer
		n := envelope.Normalized{Data: json.RawMessage(`[{"id":1}]`)}
		if err := output.Render(&out, n, output.Options{Format: format}); err != nil {
			t.Errorf("--output %s validates but does not render: %v", format, err)
		}
	}

	root, _ := NewRoot()
	usage := root.PersistentFlags().Lookup("output").Usage
	for _, format := range output.Formats {
		if !strings.Contains(usage, format) {
			t.Errorf("--output help %q omits format %q", usage, format)
		}
	}
}

// Render treats an empty format as the json default, so validation must accept
// it too — `--output ""` used to be a usage error the renderer would have
// happily served.
// An explicitly empty --output is a typo, not a request for the default: the
// flag defaults to json, so the empty string can only arrive from the command
// line. Rendering json anyway would hide the mistake.
func TestEmptyOutputFormatIsRejected(t *testing.T) {
	_, stderr, code := runCLI(t, "", "employees", "list", "--output", "", "--dry-run")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "unknown output format") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestUnknownOutputFormatNamesTheValidOnes(t *testing.T) {
	err := formatApp("xml").validateOutputOptions()
	if err == nil {
		t.Fatal("expected --output xml to be rejected")
	}
	for _, format := range output.Formats {
		if !strings.Contains(err.Error(), format) {
			t.Errorf("error %q omits valid format %q", err, format)
		}
	}
}
