// Package output renders normalized responses. The contract for agents:
// data on stdout only, JSON by default; --jq/--fields trim token usage
// in-process (no external jq dependency).
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/itchyny/gojq"

	"github.com/3bagels/peopleforce-cli/internal/envelope"
)

// Options come from global flags.
type Options struct {
	Format string   // json (default) | table | ndjson
	JQ     string   // gojq expression applied to the normalized envelope
	Fields []string // project these fields from data items
	Pretty bool     // pretty-print JSON (default true)
}

// Render writes the normalized envelope to w according to opts.
func Render(w io.Writer, n envelope.Normalized, opts Options) error {
	if opts.JQ != "" {
		return renderJQ(w, n, opts.JQ)
	}
	if len(opts.Fields) > 0 {
		var err error
		n, err = projectFields(n, opts.Fields)
		if err != nil {
			return err
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
		return fmt.Errorf("unknown output format %q (want json, table, or ndjson)", opts.Format)
	}
}

func renderJSON(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// renderNDJSON streams each data item on its own line; non-array data is a
// single line. meta is omitted (available via --output json).
func renderNDJSON(w io.Writer, n envelope.Normalized) error {
	var items []json.RawMessage
	if err := json.Unmarshal(n.Data, &items); err != nil {
		_, werr := fmt.Fprintf(w, "%s\n", n.Data)
		return werr
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(w, "%s\n", item); err != nil {
			return err
		}
	}
	return nil
}

func renderJQ(w io.Writer, n envelope.Normalized, expr string) error {
	query, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid --jq expression: %w", err)
	}
	// gojq operates on any-typed values; round-trip the envelope.
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	var input any
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return fmt.Errorf("--jq: %w", err)
		}
		line, err := gojq.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// projectFields keeps only the named fields on each data item (or on the
// single data object).
func projectFields(n envelope.Normalized, fields []string) (envelope.Normalized, error) {
	keep := func(item map[string]json.RawMessage) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(fields))
		for _, f := range fields {
			if v, ok := item[f]; ok {
				out[f] = v
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
			return n, err
		}
		return envelope.Normalized{Data: data, Meta: n.Meta}, nil
	}

	var single map[string]json.RawMessage
	if err := json.Unmarshal(n.Data, &single); err == nil {
		data, err := json.Marshal(keep(single))
		if err != nil {
			return n, err
		}
		return envelope.Normalized{Data: data, Meta: n.Meta}, nil
	}
	return n, nil // scalar/null data: nothing to project
}

// renderTable prints a minimal aligned table of top-level scalar fields —
// a convenience for humans; agents should use JSON.
func renderTable(w io.Writer, n envelope.Normalized) error {
	var items []map[string]any
	if err := json.Unmarshal(n.Data, &items); err != nil {
		var single map[string]any
		if err := json.Unmarshal(n.Data, &single); err != nil {
			_, werr := fmt.Fprintf(w, "%s\n", n.Data)
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

	if err := writeRow(cols); err != nil {
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
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}
