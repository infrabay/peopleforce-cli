package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/infrabay/peopleforce-cli/internal/envelope"
)

func norm(data string, meta map[string]any) envelope.Normalized {
	return envelope.Normalized{Data: json.RawMessage(data), Meta: meta}
}

// API data reaches the terminal verbatim in table mode, and a candidate
// controls fields like full_name through a public job application.
func TestTableStripsTerminalControlSequences(t *testing.T) {
	var out bytes.Buffer
	// The ESC arrives \u-escaped on the wire; encoding/json decodes it to 0x1b.
	body := `[{"id":1,"first_name":"\u001b[2K\rBob\u001b]0;pwned\u0007"}]`
	if err := Render(&out, norm(body, nil), Options{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.ContainsAny(got, "\x1b\x07\r") {
		t.Errorf("control bytes reached the terminal: %q", got)
	}
	if !strings.Contains(got, "Bob") {
		t.Errorf("the printable text should survive, got %q", got)
	}
}

func TestJQRawStripsTerminalControlSequences(t *testing.T) {
	var out bytes.Buffer
	body := `[{"name":"\u001b[2KBob"}]`
	if err := Render(&out, norm(body, nil), Options{JQ: ".data[0].name", Raw: true}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control bytes reached the terminal: %q", out.String())
	}
}

// --raw is the text-extraction path: an agent reading a multi-line note field
// must get it back the way jq -r would, whitespace included, while ESC, CR
// and the C1 range still go.
func TestJQRawPreservesNewlinesAndTabs(t *testing.T) {
	var out bytes.Buffer
	body := `[{"note":"line1\nline2\tend \u001b[2K\r\u009bx"}]`
	if err := Render(&out, norm(body, nil), Options{JQ: ".data[0].note", Raw: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "line1\nline2\tend [2Kx\n"; got != want {
		t.Errorf("--jq -r output = %q, want %q", got, want)
	}
}

// A newline inside a cell must not break the row apart, and must not vanish
// either — deleting it would merge the words on either side.
func TestTableCellNewlineBecomesSpaceOnOneRow(t *testing.T) {
	var out bytes.Buffer
	body := `[{"id":1,"note":"line1\nline2"}]`
	if err := Render(&out, norm(body, nil), Options{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and a single row, got %q", out.String())
	}
	if lines[1] != "1\tline1 line2" {
		t.Errorf("row = %q, want the newline rendered as a space", lines[1])
	}
}

// JSON output was already safe; make sure sanitizing table/raw did not change it.
func TestJSONPreservesDataExactly(t *testing.T) {
	var out bytes.Buffer
	body := `[{"name":"\u001b[2KBob"}]`
	if err := Render(&out, norm(body, nil), Options{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data []struct{ Name string } `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data[0].Name != "\x1b[2KBob" {
		t.Errorf("json must not alter data, got %q", got.Data[0].Name)
	}
}

// A jq runtime error used to leave the values produced before it on stdout.
func TestJQErrorLeavesNoPartialOutput(t *testing.T) {
	var out bytes.Buffer
	body := `[{"id":1,"email":"a@x.co"},{"id":2,"email":"b@x.co"}]`
	err := Render(&out, norm(body, nil), Options{
		JQ: `.data[] | if .id == 2 then error("boom") else .email end`, Raw: true})
	if err == nil {
		t.Fatal("expected a jq error")
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty when rendering fails, got %q", out.String())
	}
}

func TestFieldsReportsNamesThatMatchNothing(t *testing.T) {
	var out, warn bytes.Buffer
	body := `[{"id":1,"email":"a@x.co"},{"id":2,"email":"b@x.co"}]`
	if err := Render(&out, norm(body, nil), Options{
		Fields: []string{"id", "nope", "alsonope"}, Warn: &warn}); err != nil {
		t.Fatal(err)
	}
	w := warn.String()
	if !strings.Contains(w, "nope") || !strings.Contains(w, "alsonope") {
		t.Errorf("both unmatched names should be reported, got %q", w)
	}
	if strings.Contains(w, "id") {
		t.Errorf("a matched field must not be reported, got %q", w)
	}
	// The data still renders — the request already happened.
	var got struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 2 || got.Data[0]["id"] != 1.0 {
		t.Errorf("projection dropped real data: %s", out.String())
	}
}

// A field absent from some items but present in others is not "unmatched".
func TestFieldsPresentOnSomeItemsIsNotReported(t *testing.T) {
	var out, warn bytes.Buffer
	body := `[{"id":1},{"id":2,"nickname":"ada"}]`
	if err := Render(&out, norm(body, nil), Options{
		Fields: []string{"nickname"}, Warn: &warn}); err != nil {
		t.Fatal(err)
	}
	if warn.Len() != 0 {
		t.Errorf("no warning expected, got %q", warn.String())
	}
}

func TestNDJSONOmitsMetaAndStreamsItems(t *testing.T) {
	var out bytes.Buffer
	body := `[{"id":1},{"id":2}]`
	if err := Render(&out, norm(body, map[string]any{"pages": 3}), Options{Format: "ndjson"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per item, got %q", out.String())
	}
	if strings.Contains(out.String(), "pages") {
		t.Errorf("ndjson must omit meta, got %q", out.String())
	}
}

func TestTableColumnsAreIDFirstThenAlphabetical(t *testing.T) {
	var out bytes.Buffer
	body := `[{"zebra":1,"id":7,"alpha":2}]`
	if err := Render(&out, norm(body, nil), Options{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	header := strings.Split(out.String(), "\n")[0]
	if header != "id\talpha\tzebra" {
		t.Errorf("header = %q, want id first then alphabetical", header)
	}
}

func TestTableFormatsNullAndWholeFloats(t *testing.T) {
	var out bytes.Buffer
	body := `[{"id":1,"salary":50000.0,"rate":1.5,"note":null}]`
	if err := Render(&out, norm(body, nil), Options{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	// id, note, rate, salary
	if row != "1\t\t1.5\t50000" {
		t.Errorf("row = %q, want whole floats as integers and null as empty", row)
	}
}

func TestUnknownFormatIsAnError(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, norm(`[]`, nil), Options{Format: "xml"}); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}
