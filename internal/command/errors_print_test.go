package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrintErrorSanitizesHumanMessage(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, errors.New("--jq: \x1b[2Kevil\x07 \u009bname\nline2\ttab"), false)
	got := buf.String()
	if strings.ContainsAny(got, "\x1b\x07\u009b") {
		t.Errorf("control bytes reached stderr: %q", got)
	}
	if want := "error: --jq: [2Kevil name\nline2\ttab\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrintErrorJSONEscapesC1(t *testing.T) {
	var buf bytes.Buffer
	ee := &ExitError{Code: ExitValidation, Type: "validation", Message: "bad \u009b name",
		Detail: json.RawMessage(`{"m":"x` + "\u009b" + `y"}`)}
	PrintError(&buf, ee, true)
	for _, r := range buf.String() {
		if r >= 0x80 && r <= 0x9f {
			t.Errorf("raw C1 U+%04X in structured error: %q", r, buf.String())
		}
	}
	var back struct {
		Error struct{ Message string }
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || back.Error.Message != "bad \u009b name" {
		t.Errorf("round trip: %v %+v", err, back)
	}
}
