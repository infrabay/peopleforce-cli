// Package output renders normalized responses. The contract for agents:
// data on stdout only, JSON by default; --jq/--fields trim token usage
// in-process (no external jq dependency).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/infrabay/peopleforce-cli/internal/envelope"
)

// Formats lists the accepted --output values, in the order they are offered to
// users. It is the one place the set is spelled out: the flag help, the
// up-front validation and every "unknown format" message derive from it, so a
// format cannot end up accepted by one and unknown to another.
var Formats = []string{"json", "table", "ndjson"}

// ValidFormat reports whether name is an accepted --output value. The empty
// string is NOT accepted: Render treats an unset Format as the json default
// for library callers, but a CLI user who typed `--output ""` made a mistake
// and silently rendering json would hide it.
func ValidFormat(name string) bool {
	return slices.Contains(Formats, name)
}

// FormatList renders Formats as an English list ("a, b, or c") for help and
// error text.
func FormatList() string {
	switch len(Formats) {
	case 0, 1:
		return strings.Join(Formats, "")
	case 2:
		return Formats[0] + " or " + Formats[1]
	default:
		return strings.Join(Formats[:len(Formats)-1], ", ") + ", or " + Formats[len(Formats)-1]
	}
}

// Options come from global flags.
type Options struct {
	Format string    // one of Formats; empty means the json default
	JQ     string    // gojq expression applied to the normalized envelope
	Raw    bool      // with JQ: print string results without JSON quotes (jq -r)
	Fields []string  // project these fields from data items
	Pretty bool      // pretty-print JSON (default true)
	Warn   io.Writer // non-fatal diagnostics (stderr); nil silences them
}

// Render writes the normalized envelope to w according to opts. When both
// JQ and Fields are set, JQ wins and Fields is ignored.
func Render(w io.Writer, n envelope.Normalized, opts Options) error {
	if opts.JQ != "" {
		return renderJQ(w, n, opts.JQ, opts.Raw)
	}
	if len(opts.Fields) > 0 {
		var unmatched []string
		var err error
		n, unmatched, err = projectFields(n, opts.Fields)
		if err != nil {
			return err
		}
		// Empty objects are indistinguishable from "the API returned records
		// with no data", and a wrong field name is the likeliest --fields
		// mistake. Warn rather than fail: the request already happened, and a
		// projection typo must not discard a completed mutation's response.
		if len(unmatched) > 0 && opts.Warn != nil {
			fmt.Fprintf(opts.Warn, "warning: --fields matched no data: %s\n", strings.Join(unmatched, ", "))
		}
	}
	switch opts.Format {
	case "", "json":
		return renderJSON(w, n, opts.Pretty)
	case "ndjson":
		return renderNDJSON(w, n)
	case "table":
		return renderTable(w, n)
	default:
		// Unreachable through the CLI: validateOutputOptions rejects the value
		// before any request is sent. It still guards direct callers, and a
		// format added to Formats but not to the switch above.
		return fmt.Errorf("unknown output format %q (want %s)", opts.Format, FormatList())
	}
}

func renderJSON(w io.Writer, v any, pretty bool) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(EscapeC1(buf.Bytes()))
	return err
}

// EscapeC1 rewrites every U+0080-U+009F code point in encoded JSON as a \u00XX
// escape. encoding/json and gojq escape only bytes below 0x20, so a C1 control
// from API data (U+009B is a single-byte CSI in xterm-family terminals) would
// otherwise reach the terminal verbatim. In UTF-8 those code points are exactly
// the two-byte sequences C2 80..C2 9F, a lead byte C2 can only be followed by
// one continuation byte, and JSON permits non-ASCII only inside strings, so the
// rewrite stays valid JSON and decodes to the original value.
func EscapeC1(b []byte) []byte {
	if !bytes.Contains(b, []byte{0xc2}) {
		return b
	}
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); i++ {
		if b[i] == 0xc2 && i+1 < len(b) && b[i+1] >= 0x80 && b[i+1] <= 0x9f {
			out = append(out, fmt.Sprintf("\\u%04x", b[i+1])...)
			i++
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// renderNDJSON streams each data item on its own line; non-array data is a
// single line. meta is omitted (available via --output json).
func renderNDJSON(w io.Writer, n envelope.Normalized) error {
	// The API may hand back pretty-printed records; one record per line is the
	// whole point of the format, so every item is compacted first.
	writeLine := func(raw []byte) error {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			buf.Reset()
			buf.Write(raw)
		}
		buf.WriteByte('\n')
		_, err := w.Write(EscapeC1(buf.Bytes()))
		return err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(n.Data, &items); err != nil {
		return writeLine(n.Data)
	}
	for _, item := range items {
		if err := writeLine(item); err != nil {
			return err
		}
	}
	return nil
}

func renderJQ(w io.Writer, n envelope.Normalized, expr string, raw bool) error {
	query, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid --jq expression: %w", err)
	}
	// gojq operates on any-typed values; round-trip the envelope. UseNumber
	// keeps integers beyond 2^53 exact (a float64 decode turned
	// 9007199254740993 into ...992); gojq turns json.Number into an exact int,
	// a *big.Int, or a float64 for decimals.
	encoded, err := json.Marshal(n)
	if err != nil {
		return err
	}
	var input any
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		return err
	}
	// Buffer until the iterator finishes: writing incrementally left the
	// results produced before a mid-expression error on stdout next to the
	// error on stderr, so "stdout carries data only" stopped holding.
	var buf bytes.Buffer
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return fmt.Errorf("--jq: %w", err)
		}
		if s, isString := v.(string); raw && isString {
			// --raw prints the string unquoted, so control bytes from the API
			// would reach the terminal verbatim.
			fmt.Fprintln(&buf, sanitizeRaw(s))
			continue
		}
		line, err := gojq.Marshal(v)
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "%s\n", EscapeC1(line))
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// Table cells and --jq --raw print server-supplied strings verbatim, and some
// of those strings are attacker-influenced — a candidate's name arrives
// through a public job application — so an embedded ESC sequence could erase
// lines, hide a row, or rewrite the window title of whoever reads the output.
// JSON output needs no stripping for C0 (encoding/json escapes those) but not
// for C1, which EscapeC1 rewrites as \u00XX in every JSON writer.
//
// sanitizeRaw is the --jq --raw variant. Newline and tab are ordinary content
// on that path (real jq -r prints them, and agents extract multi-line fields
// through it), so only the escape-capable bytes are removed.
func sanitizeRaw(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if isTerminalControl(r) {
			return -1
		}
		return r
	}, s)
}

// sanitizeCell is the table variant: a record is one line and tab separates
// the columns, so layout-breaking whitespace cannot survive verbatim. It
// becomes a space rather than being deleted — deleting it would silently glue
// the surrounding words together.
func sanitizeCell(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t', '\r':
			return ' '
		}
		if isTerminalControl(r) {
			return -1
		}
		return r
	}, s)
}

func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// projectFields keeps only the named fields on each data item (or on the
// single data object). unmatched lists the requested fields that appeared on
// no item at all — almost always a wrong field name for the endpoint.
func projectFields(n envelope.Normalized, fields []string) (_ envelope.Normalized, unmatched []string, _ error) {
	// null data (a 204, say) has nothing to project; unmarshalling it into a
	// slice would succeed and turn "nothing" into "an empty list".
	if t := bytes.TrimSpace(n.Data); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return n, nil, nil
	}
	hit := make(map[string]bool, len(fields))
	keep := func(item map[string]json.RawMessage) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(fields))
		for _, f := range fields {
			if v, ok := item[f]; ok {
				out[f] = v
				hit[f] = true
			}
		}
		return out
	}
	missed := func() []string {
		var out []string
		for _, f := range fields {
			if !hit[f] {
				out = append(out, f)
			}
		}
		return out
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(n.Data, &items); err == nil {
		projected := make([]map[string]json.RawMessage, len(items))
		for i, item := range items {
			projected[i] = keep(item)
		}
		data, err := json.Marshal(projected)
		if err != nil {
			return n, nil, err
		}
		return envelope.Normalized{Data: data, Meta: n.Meta}, missed(), nil
	}

	var single map[string]json.RawMessage
	if err := json.Unmarshal(n.Data, &single); err == nil {
		data, err := json.Marshal(keep(single))
		if err != nil {
			return n, nil, err
		}
		return envelope.Normalized{Data: data, Meta: n.Meta}, missed(), nil
	}
	return n, nil, nil // scalar/null data: nothing to project
}

// renderTable prints a minimal aligned table of top-level scalar fields —
// a convenience for humans; agents should use JSON.
func renderTable(w io.Writer, n envelope.Normalized) error {
	// UseNumber so integers beyond 2^53 print exactly.
	decode := func(v any) error {
		dec := json.NewDecoder(bytes.NewReader(n.Data))
		dec.UseNumber()
		return dec.Decode(v)
	}
	var items []map[string]any
	if err := decode(&items); err != nil {
		var single map[string]any
		if err := decode(&single); err != nil {
			_, werr := fmt.Fprintf(w, "%s\n", sanitizeRaw(string(n.Data)))
			return werr
		}
		items = []map[string]any{single}
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "(no results)")
		return err
	}

	cols := scalarColumns(items)
	writeRow := func(cells []string) error {
		for i, c := range cells {
			if i > 0 {
				if _, err := fmt.Fprint(w, "\t"); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprint(w, c); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(w)
		return err
	}

	// Column names are API-supplied too (custom fields carry user-set keys).
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = sanitizeCell(c)
	}
	if err := writeRow(headers); err != nil {
		return err
	}
	for _, item := range items {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = formatCell(item[c])
		}
		if err := writeRow(cells); err != nil {
			return err
		}
	}
	return nil
}

// scalarColumns returns the union of scalar-valued keys across items,
// id-first then alphabetical.
func scalarColumns(items []map[string]any) []string {
	seen := map[string]bool{}
	for _, item := range items {
		for k, v := range item {
			switch v.(type) {
			case map[string]any, []any:
			default:
				seen[k] = true
			}
		}
	}
	cols := make([]string, 0, len(seen))
	for k := range seen {
		if k != "id" {
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	if seen["id"] {
		cols = append([]string{"id"}, cols...)
	}
	return cols
}

func formatCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return sanitizeCell(x)
	case json.Number:
		lit := x.String()
		if !strings.ContainsAny(lit, ".eE") {
			return lit // an integer literal prints exactly, whatever its size
		}
		f, err := x.Float64()
		if err != nil {
			return sanitizeCell(lit)
		}
		return formatCell(f)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return sanitizeCell(fmt.Sprintf("%v", x))
	}
}

// SanitizeText prepares a human-readable string for the terminal: C0 controls
// other than newline and tab, DEL and the C1 range are removed, exactly as the
// --raw path does.
func SanitizeText(s string) string { return sanitizeRaw(s) }
