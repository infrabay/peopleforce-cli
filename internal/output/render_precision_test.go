package output

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A float64 decode turned 9007199254740993 into ...992 before gojq or the
// table formatter ever saw it. Decimals must come out as before.
func TestJQKeepsLargeIntegersExact(t *testing.T) {
	body := `[{"id":9007199254740993,"big":12345678901234567890,"d":0.1,"n":-9223372036854775808}]`
	cases := map[string]string{
		".data[0].id":     "9007199254740993\n",
		".data[0].big":    "12345678901234567890\n",
		".data[0].d":      "0.1\n",
		".data[0].n":      "-9223372036854775808\n",
		".data[0].id + 1": "9007199254740994\n",
	}
	for expr, want := range cases {
		var out bytes.Buffer
		if err := Render(&out, norm(body, nil), Options{JQ: expr}); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if out.String() != want {
			t.Errorf("--jq %s = %q, want %q", expr, out.String(), want)
		}
	}
}

// gojq passes an untouched json.Number through with its literal (as jq 1.7
// does), so decimals and exponents keep their value; arithmetic yields the
// ordinary float result. 1e400 used to abort the whole decode.
func TestJQDecimalsAndHugeExponentsSurvive(t *testing.T) {
	body := `[{"a":0.1,"b":1.5e3,"c":1e400}]`
	for expr, want := range map[string]string{
		".data[0].a":     "0.1\n",
		".data[0].b":     "1.5e3\n",
		".data[0].c":     "1e400\n",
		".data[0].b + 0": "1500\n",
	} {
		var out bytes.Buffer
		if err := Render(&out, norm(body, nil), Options{JQ: expr}); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if out.String() != want {
			t.Errorf("--jq %s = %q, want %q", expr, out.String(), want)
		}
	}
}

func TestTableKeepsLargeIntegersExact(t *testing.T) {
	var out bytes.Buffer
	body := `[{"id":9007199254740993,"big":12345678901234567890,"d":0.10,"e":1e2,"huge":1e400}]`
	if err := Render(&out, norm(body, nil), Options{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	// id, big, d, e, huge
	if want := "9007199254740993\t12345678901234567890\t0.1\t100\t1e400"; row != want {
		t.Errorf("row = %q, want %q", row, want)
	}
}

// The C1 range arrives as raw UTF-8 (C2 80..C2 9F); encoding/json and gojq
// leave it alone, and U+009B is a one-byte CSI in xterm-family terminals.
func TestJSONWritersEscapeC1(t *testing.T) {
	const name = "a\u009bb\u0080c\u009fd eÂé"
	body, _ := json.Marshal([]map[string]string{{"name": name}})
	for label, opts := range map[string]Options{
		"json":        {Format: "json", Pretty: true},
		"json-flat":   {Format: "json"},
		"ndjson":      {Format: "ndjson"},
		"jq":          {JQ: ".data[0]"},
		"jq-string":   {JQ: ".data[0].name"},
		"fields-json": {Format: "json", Fields: []string{"name"}},
	} {
		var out bytes.Buffer
		if err := Render(&out, norm(string(body), nil), opts); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		got := out.String()
		for _, r := range got {
			if r >= 0x80 && r <= 0x9f {
				t.Errorf("%s: raw C1 U+%04X in output %q", label, r, got)
			}
		}
		if !strings.Contains(got, `\u009b`) || !strings.Contains(got, " ") || !strings.Contains(got, "é") {
			t.Errorf("%s: want the C1 escaped and neighbours intact, got %q", label, got)
		}
	}
}

func TestEscapeC1RoundTrips(t *testing.T) {
	var all strings.Builder
	for r := rune(0x20); r < 0x300; r++ {
		all.WriteRune(r)
	}
	orig := map[string]string{"k" + all.String(): all.String()}
	enc, _ := json.Marshal(orig)
	got := EscapeC1(enc)
	for _, r := range string(got) {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("raw C1 U+%04X remained", r)
		}
	}
	var back map[string]string
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, orig) {
		t.Error("round trip changed the value")
	}
}

func TestNDJSONOneRecordPerLine(t *testing.T) {
	body := "[\n  {\n    \"id\": 1,\n    \"a\": \"x y\"\n  },\n  {\n    \"id\": 2\n  }\n]"
	var out bytes.Buffer
	if err := Render(&out, norm(body, nil), Options{Format: "ndjson"}); err != nil {
		t.Fatal(err)
	}
	if want := "{\"id\":1,\"a\":\"x y\"}\n{\"id\":2}\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := Render(&out, norm("{\n \"id\": 1\n}", nil), Options{Format: "ndjson"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"id\":1}\n" {
		t.Errorf("single object: got %q", out.String())
	}
}

func TestFieldsKeepsNullDataNull(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, norm(`null`, nil), Options{Fields: []string{"id"}}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "null" {
		t.Errorf("data = %s, want null", got.Data)
	}
}
