package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/3bagels/peopleforce-cli/internal/httpx"
	"github.com/3bagels/peopleforce-cli/internal/registry"
)

// registerQueryFlags declares one typed flag per query parameter.
func registerQueryFlags(cmd *cobra.Command, op *registry.Op) {
	for i := range op.Query {
		q := &op.Query[i]
		usage := flagUsage(q.Description, q.WireName, q.Enum, q.Repeatable, q.Required)
		if q.Repeatable {
			cmd.Flags().StringArray(q.Flag, nil, usage)
			continue
		}
		switch q.Type {
		case registry.TypeInteger:
			cmd.Flags().Int64(q.Flag, 0, usage)
		case registry.TypeNumber:
			cmd.Flags().Float64(q.Flag, 0, usage)
		case registry.TypeBoolean:
			cmd.Flags().Bool(q.Flag, false, usage)
		default:
			cmd.Flags().String(q.Flag, "", usage)
		}
	}
}

// registerBodyFlags declares typed flags for scalar top-level body fields.
// Object/array fields are reachable via --set / --input.
func registerBodyFlags(cmd *cobra.Command, op *registry.Op) {
	for i := range op.Body {
		f := &op.Body[i]
		usage := flagUsage(f.Description, f.Name, f.Enum, f.Repeatable, f.Required)
		if f.Repeatable {
			cmd.Flags().StringArray(f.Flag, nil, usage)
			continue
		}
		switch f.Type {
		case registry.TypeInteger:
			cmd.Flags().Int64(f.Flag, 0, usage)
		case registry.TypeNumber:
			cmd.Flags().Float64(f.Flag, 0, usage)
		case registry.TypeBoolean:
			cmd.Flags().Bool(f.Flag, false, usage)
		case registry.TypeObject, registry.TypeArray:
			// no flag: complex value, use --set key:=json or --input
		case registry.TypeFile:
			cmd.Flags().String(f.Flag, "", withFileHint(usage))
		default:
			cmd.Flags().String(f.Flag, "", usage)
		}
	}
}

func withFileHint(usage string) string {
	if usage == "" {
		return "file to upload: @path/to/file"
	}
	return usage + " (@path/to/file)"
}

func flagUsage(desc, wireName string, enum []string, repeatable, required bool) string {
	u := firstSentence(desc)
	if u == "" {
		u = wireName
	}
	if len(enum) > 0 {
		u += " (one of: " + strings.Join(enum, ", ") + ")"
	}
	if repeatable {
		u += " (repeatable)"
	}
	if required {
		u += " (required)"
	}
	return u
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	if len(s) > 140 {
		return s[:140] + "…"
	}
	return s
}

// collectQuery turns changed flags into ordered wire pairs.
func collectQuery(cmd *cobra.Command, op *registry.Op) ([]httpx.QueryPair, error) {
	var pairs []httpx.QueryPair
	for i := range op.Query {
		q := &op.Query[i]
		if !cmd.Flags().Changed(q.Flag) {
			continue
		}
		if q.Repeatable {
			values, _ := cmd.Flags().GetStringArray(q.Flag)
			for _, v := range values {
				if err := validateTyped(q.Type, v, q.Flag); err != nil {
					return nil, err
				}
				if err := validateEnum(q.Enum, v, q.Flag); err != nil {
					return nil, err
				}
				pairs = append(pairs, httpx.QueryPair{Key: q.WireName, Value: v})
			}
			continue
		}
		v, err := flagValueString(cmd, q.Flag, q.Type)
		if err != nil {
			return nil, err
		}
		if err := validateEnum(q.Enum, v, q.Flag); err != nil {
			return nil, err
		}
		pairs = append(pairs, httpx.QueryPair{Key: q.WireName, Value: v})
	}
	return pairs, nil
}

func flagValueString(cmd *cobra.Command, flag string, t registry.ParamType) (string, error) {
	switch t {
	case registry.TypeInteger:
		v, err := cmd.Flags().GetInt64(flag)
		return strconv.FormatInt(v, 10), err
	case registry.TypeNumber:
		v, err := cmd.Flags().GetFloat64(flag)
		return strconv.FormatFloat(v, 'f', -1, 64), err
	case registry.TypeBoolean:
		v, err := cmd.Flags().GetBool(flag)
		return strconv.FormatBool(v), err
	default:
		return cmd.Flags().GetString(flag)
	}
}

func validateTyped(t registry.ParamType, v, flag string) error {
	switch t {
	case registry.TypeInteger:
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return usageErr("--%s: %q is not an integer", flag, v)
		}
	case registry.TypeNumber:
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return usageErr("--%s: %q is not a number", flag, v)
		}
	case registry.TypeBoolean:
		if _, err := strconv.ParseBool(v); err != nil {
			return usageErr("--%s: %q is not a boolean", flag, v)
		}
	}
	return nil
}

func validateEnum(enum []string, v, flag string) error {
	if len(enum) == 0 {
		return nil
	}
	for _, e := range enum {
		if v == e {
			return nil
		}
	}
	return usageErr("--%s: %q is not one of: %s", flag, v, strings.Join(enum, ", "))
}

// convertTyped parses a string per the declared type for JSON bodies.
func convertTyped(t registry.ParamType, v, flag string) (any, error) {
	switch t {
	case registry.TypeInteger:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, usageErr("--%s: %q is not an integer", flag, v)
		}
		return n, nil
	case registry.TypeNumber:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, usageErr("--%s: %q is not a number", flag, v)
		}
		return n, nil
	case registry.TypeBoolean:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, usageErr("--%s: %q is not a boolean", flag, v)
		}
		return b, nil
	default:
		return v, nil
	}
}

// buildJSONBody merges, in order of increasing precedence:
// --input (file/stdin/inline JSON) → typed body flags → --set entries.
func buildJSONBody(cmd *cobra.Command, op *registry.Op, inputArg string, setArgs []string) (map[string]any, error) {
	body := map[string]any{}

	if inputArg != "" {
		raw, err := readInput(inputArg)
		if err != nil {
			return nil, err
		}
		if err := unmarshalPreservingNumbers(raw, &body); err != nil {
			return nil, usageErr("--input is not a JSON object: %v", err)
		}
	}

	for i := range op.Body {
		f := &op.Body[i]
		if !cmd.Flags().Changed(f.Flag) {
			continue
		}
		if f.Repeatable {
			values, _ := cmd.Flags().GetStringArray(f.Flag)
			arr := make([]any, 0, len(values))
			for _, v := range values {
				if err := validateEnum(f.Enum, v, f.Flag); err != nil {
					return nil, err
				}
				tv, err := convertTyped(f.Type, v, f.Flag)
				if err != nil {
					return nil, err
				}
				arr = append(arr, tv)
			}
			body[f.Name] = arr
			continue
		}
		if f.Type == registry.TypeObject || f.Type == registry.TypeArray {
			continue // no flag registered
		}
		v, err := bodyFlagValue(cmd, f)
		if err != nil {
			return nil, err
		}
		body[f.Name] = v
	}

	for _, s := range setArgs {
		if err := applySet(body, s); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func bodyFlagValue(cmd *cobra.Command, f *registry.BodyField) (any, error) {
	switch f.Type {
	case registry.TypeInteger:
		return cmd.Flags().GetInt64(f.Flag)
	case registry.TypeNumber:
		return cmd.Flags().GetFloat64(f.Flag)
	case registry.TypeBoolean:
		return cmd.Flags().GetBool(f.Flag)
	case registry.TypeFile:
		v, err := cmd.Flags().GetString(f.Flag)
		if err != nil {
			return nil, err
		}
		if f.FileToDataURI {
			path, ok := strings.CutPrefix(v, "@")
			if !ok {
				return nil, usageErr("--%s expects a file reference like @photo.png", f.Flag)
			}
			uri, err := httpx.FileToDataURI(path)
			if err != nil {
				return nil, usageErr("--%s: %v", f.Flag, err)
			}
			return uri, nil
		}
		return v, nil
	default:
		v, err := cmd.Flags().GetString(f.Flag)
		if err != nil {
			return nil, err
		}
		if err := validateEnum(f.Enum, v, f.Flag); err != nil {
			return nil, err
		}
		return v, nil
	}
}

// buildMultipartFields assembles multipart form fields from three sources in
// increasing precedence: --input JSON object → typed flags → --set. Each
// source fully overrides a field it names. The returned presence map feeds
// checkRequired. Values of file-typed fields given as @path become file parts.
func buildMultipartFields(cmd *cobra.Command, op *registry.Op, inputArg string, setArgs []string) ([]httpx.FieldValue, map[string]any, error) {
	fieldByName := map[string]*registry.BodyField{}
	for i := range op.Body {
		fieldByName[op.Body[i].Name] = &op.Body[i]
	}
	isFileField := func(name string) bool {
		f, ok := fieldByName[name]
		return ok && f.Type == registry.TypeFile
	}
	// @path is expanded only for a value the operator typed on the file flag
	// itself. --input and --set carry data an agent may have assembled from
	// content it did not author (a candidate's application, a webhook payload),
	// and expanding @ there turns any such field into an arbitrary local file
	// read — including the CLI's own config file.
	fileRefValue := func(name, v string) httpx.FieldValue {
		if path, ok := strings.CutPrefix(v, "@"); ok && isFileField(name) {
			return httpx.FieldValue{Name: name, Value: path, IsFile: true}
		}
		return httpx.FieldValue{Name: name, Value: v}
	}
	// Data-channel values keep the literal string; a bare @ on a file field is
	// almost certainly an attempt to upload a file, so say so rather than
	// silently storing "@/etc/passwd" as the document's text.
	dataValue := func(channel, name, v string) (httpx.FieldValue, error) {
		if strings.HasPrefix(v, "@") && isFileField(name) {
			return httpx.FieldValue{}, usageErr(
				"%s field %q: file references (@path) are only accepted from the --%s flag; pass the file with --%s @path",
				channel, name, fieldByName[name].Flag, fieldByName[name].Flag)
		}
		return httpx.FieldValue{Name: name, Value: v}, nil
	}

	values := map[string][]httpx.FieldValue{}
	var order []string
	setField := func(name string, fvs ...httpx.FieldValue) {
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = fvs
	}

	// 1) --input: JSON object of scalars / arrays of scalars.
	if inputArg != "" {
		raw, err := readInput(inputArg)
		if err != nil {
			return nil, nil, err
		}
		var m map[string]any
		if err := unmarshalPreservingNumbers(raw, &m); err != nil {
			return nil, nil, usageErr("--input is not a JSON object: %v", err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch v := m[k].(type) {
			case []any:
				fvs := make([]httpx.FieldValue, 0, len(v))
				for _, item := range v {
					s, err := scalarString(item)
					if err != nil {
						return nil, nil, usageErr("--input field %q: %v", k, err)
					}
					fv, err := dataValue("--input", k, s)
					if err != nil {
						return nil, nil, err
					}
					fvs = append(fvs, fv)
				}
				setField(k, fvs...)
			case map[string]any:
				return nil, nil, usageErr("--input field %q: nested objects are not supported for multipart operations", k)
			default:
				s, err := scalarString(v)
				if err != nil {
					return nil, nil, usageErr("--input field %q: %v", k, err)
				}
				fv, err := dataValue("--input", k, s)
				if err != nil {
					return nil, nil, err
				}
				setField(k, fv)
			}
		}
	}

	// 2) typed flags.
	for i := range op.Body {
		f := &op.Body[i]
		if !cmd.Flags().Changed(f.Flag) {
			continue
		}
		if f.Repeatable {
			raw, _ := cmd.Flags().GetStringArray(f.Flag)
			fvs := make([]httpx.FieldValue, 0, len(raw))
			for _, v := range raw {
				if err := validateEnum(f.Enum, v, f.Flag); err != nil {
					return nil, nil, err
				}
				fvs = append(fvs, fileRefValue(f.Name, v))
			}
			setField(f.Name, fvs...)
			continue
		}
		switch f.Type {
		case registry.TypeFile:
			v, _ := cmd.Flags().GetString(f.Flag)
			// Non-@ values pass through: the document field also accepts
			// base64/data-URI strings.
			setField(f.Name, fileRefValue(f.Name, v))
		default:
			v, err := flagValueString(cmd, f.Flag, f.Type)
			if err != nil {
				return nil, nil, err
			}
			if err := validateEnum(f.Enum, v, f.Flag); err != nil {
				return nil, nil, err
			}
			setField(f.Name, httpx.FieldValue{Name: f.Name, Value: v})
		}
	}

	// 3) --set. Repeating a key appends rather than replaces: repeating is how
	// a bracket-array multipart field expresses a list, and --set is the only
	// route to fields that have no typed flag. The first --set for a key still
	// overrides whatever --input or a flag put there.
	setSeen := map[string]bool{}
	for _, s := range setArgs {
		key, value, isJSON, err := splitSet(s)
		if err != nil {
			return nil, nil, err
		}
		if isJSON {
			return nil, nil, usageErr("--set %s: JSON values are not supported for multipart operations", key)
		}
		fv, err := dataValue("--set", key, value)
		if err != nil {
			return nil, nil, err
		}
		if setSeen[key] {
			values[key] = append(values[key], fv)
			continue
		}
		setSeen[key] = true
		setField(key, fv)
	}

	var fields []httpx.FieldValue
	presence := map[string]any{}
	for _, name := range order {
		fields = append(fields, values[name]...)
		presence[name] = true
	}
	return fields, presence, nil
}

func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

// unmarshalPreservingNumbers decodes JSON keeping numerals exact
// (json.Number instead of float64), so large IDs survive round-trips.
func unmarshalPreservingNumbers(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(dst)
}

// readInput loads a request body from @file, "-" (stdin), or inline JSON.
func readInput(arg string) ([]byte, error) {
	switch {
	case arg == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, usageErr("reading stdin: %v", err)
		}
		return data, nil
	case strings.HasPrefix(arg, "@"):
		data, err := os.ReadFile(arg[1:])
		if err != nil {
			return nil, usageErr("--input: %v", err)
		}
		return data, nil
	case strings.HasPrefix(arg, "{") || strings.HasPrefix(arg, "["):
		return []byte(arg), nil
	default:
		return nil, usageErr("--input expects @file, - (stdin), or an inline JSON object")
	}
}

// splitSet parses one --set argument:
//
//	key=value    string value
//	key:=json    raw JSON value (numbers, booleans, arrays, objects)
//
// Keys may use dots to address nested objects: address.city=Kyiv.
func splitSet(s string) (key, value string, isJSON bool, err error) {
	eq := strings.Index(s, "=")
	if eq <= 0 {
		return "", "", false, usageErr("--set expects key=value or key:=json, got %q", s)
	}
	key, value = s[:eq], s[eq+1:]
	if strings.HasSuffix(key, ":") {
		key = strings.TrimSuffix(key, ":")
		if key == "" { // ":=1" slipped past the eq<=0 guard and named a field ""
			return "", "", false, usageErr("--set expects key=value or key:=json, got %q", s)
		}
		return key, value, true, nil
	}
	return key, value, false, nil
}

func applySet(body map[string]any, s string) error {
	key, value, isJSON, err := splitSet(s)
	if err != nil {
		return err
	}
	var v any = value
	if isJSON {
		if err := unmarshalPreservingNumbers([]byte(value), &v); err != nil {
			return usageErr("--set %s: invalid JSON value: %v", key, err)
		}
	}

	segments := strings.Split(key, ".")
	m := body
	for i, seg := range segments[:len(segments)-1] {
		next, ok := m[seg].(map[string]any)
		if !ok {
			if _, exists := m[seg]; exists {
				return usageErr("--set %s: %q is already set to a non-object value", key, strings.Join(segments[:i+1], "."))
			}
			next = map[string]any{}
			m[seg] = next
		}
		m = next
	}
	m[segments[len(segments)-1]] = v
	return nil
}

// checkRequired validates required query params and body fields after merge.
// body carries the merged JSON body, or a presence map for multipart ops.
func checkRequired(cmd *cobra.Command, op *registry.Op, body map[string]any) error {
	var missing []string
	for i := range op.Query {
		q := &op.Query[i]
		if q.Required && !cmd.Flags().Changed(q.Flag) {
			missing = append(missing, "--"+q.Flag)
		}
	}
	for i := range op.Body {
		f := &op.Body[i]
		if !f.Required {
			continue
		}
		if body != nil {
			if _, ok := body[f.Name]; ok {
				continue
			}
		}
		if cmd.Flags().Changed(f.Flag) {
			continue
		}
		missing = append(missing, "--"+f.Flag)
	}
	if len(missing) > 0 {
		return usageErr("missing required: %s", strings.Join(missing, ", "))
	}
	return nil
}

// jsonBodyBytes marshals a body map, returning nil for an empty body.
func jsonBodyBytes(body map[string]any) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}
	return b, nil
}
