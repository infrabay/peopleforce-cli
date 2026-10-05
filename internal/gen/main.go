// Command gen compiles the vendored PeopleForce OpenAPI 3.1 spec plus
// overrides.yaml into the static registry the binary ships with
// (internal/registry/registry.gen.go) and a golden snapshot
// (testdata/golden/commands.json) that CI diffs on spec updates.
//
// Run from the repo root:
//
//	go run ./internal/gen
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"gopkg.in/yaml.v3"

	"github.com/infrabay/peopleforce-cli/internal/registry"
)

type overridesFile struct {
	Operations map[string]opOverride `yaml:"operations"`
}

type opOverride struct {
	Command     string                   `yaml:"command"`
	Destructive *bool                    `yaml:"destructive"`
	Examples    []string                 `yaml:"examples"`
	Query       map[string]fieldOverride `yaml:"query"`
	Body        map[string]fieldOverride `yaml:"body"`
}

type fieldOverride struct {
	Type          string `yaml:"type"`
	Description   string `yaml:"description"`
	FileToDataURI bool   `yaml:"file_to_data_uri"`
	// Skip drops a field the spec declares but the backend ignores. Use it
	// only with evidence from a live call: a flag that looks like it works
	// and silently does nothing is worse than no flag at all.
	Skip bool `yaml:"skip"`
}

// fixDescription repairs systematic upstream description bugs: the "Fitler"
// typo, and range filters whose before/after wording is swapped (gte/gt mean
// "on or after", lte/lt mean "on or before" — the spec says the opposite for
// hired_on, terminated_on and several others).
func fixDescription(wire, desc string) string {
	desc = strings.ReplaceAll(desc, "Fitler", "Filter")
	switch {
	case strings.HasSuffix(wire, "[gte]") || strings.HasSuffix(wire, "[gt]"):
		desc = strings.ReplaceAll(desc, " or before", " or after")
	case strings.HasSuffix(wire, "[lte]") || strings.HasSuffix(wire, "[lt]"):
		desc = strings.ReplaceAll(desc, " or after", " or before")
	}
	return desc
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	specPath := filepath.Join(*root, "internal/spec/peopleforce-openapi.json")
	overridesPath := filepath.Join(*root, "internal/spec/overrides.yaml")
	outPath := filepath.Join(*root, "internal/registry/registry.gen.go")
	goldenPath := filepath.Join(*root, "testdata/golden/commands.json")

	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		log.Fatalf("reading spec: %v", err)
	}
	ovBytes, err := os.ReadFile(overridesPath)
	if err != nil {
		log.Fatalf("reading overrides: %v", err)
	}
	var overrides overridesFile
	// KnownFields makes a misspelled key ("destructve", "file_to_data_url") a
	// build failure instead of a silent no-op that ships a binary missing the
	// --yes guard or the data-URI encoding.
	dec := yaml.NewDecoder(bytes.NewReader(ovBytes))
	dec.KnownFields(true)
	if err := dec.Decode(&overrides); err != nil {
		log.Fatalf("parsing overrides.yaml: %v", err)
	}

	doc, err := libopenapi.NewDocument(specBytes)
	if err != nil {
		log.Fatalf("parsing spec: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		log.Fatalf("building v3 model: %v", err)
	}

	meta, ops, err := compile(&model.Model, overrides)
	if err != nil {
		log.Fatalf("compiling registry: %v", err)
	}

	src, err := emit(meta, ops)
	if err != nil {
		log.Fatalf("emitting registry.gen.go: %v", err)
	}
	if err := os.WriteFile(outPath, src, 0o644); err != nil {
		log.Fatalf("writing %s: %v", outPath, err)
	}

	golden, err := json.MarshalIndent(struct {
		Meta registry.Meta
		Ops  []registry.Op
	}{meta, ops}, "", "  ")
	if err != nil {
		log.Fatalf("marshaling golden snapshot: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		log.Fatalf("creating golden dir: %v", err)
	}
	if err := os.WriteFile(goldenPath, append(golden, '\n'), 0o644); err != nil {
		log.Fatalf("writing %s: %v", goldenPath, err)
	}

	curated := 0
	for _, op := range ops {
		if op.Command != "" {
			curated++
		}
	}
	fmt.Printf("registry: %d operations (%d curated) → %s\n", len(ops), curated, outPath)
}

var methodOrder = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

func compile(model *v3.Document, overrides overridesFile) (registry.Meta, []registry.Op, error) {
	meta := registry.Meta{
		SpecTitle:   model.Info.Title,
		SpecVersion: model.Info.Version,
	}
	if len(model.Servers) > 0 {
		meta.BaseURL = strings.TrimSuffix(model.Servers[0].URL, "/")
	}

	usedOverrides := map[string]bool{}
	var ops []registry.Op

	for pair := model.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path := pair.Key()
		item := pair.Value()
		byMethod := map[string]*v3.Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put,
			"PATCH": item.Patch, "DELETE": item.Delete,
		}
		for _, method := range methodOrder {
			specOp := byMethod[method]
			if specOp == nil {
				continue
			}
			key := method + " " + path
			ov := overrides.Operations[key]
			if _, ok := overrides.Operations[key]; ok {
				usedOverrides[key] = true
			}
			op, err := compileOp(method, path, item, specOp, ov)
			if err != nil {
				return meta, nil, fmt.Errorf("%s: %w", key, err)
			}
			ops = append(ops, op)
		}
	}

	// Every override must have matched a real operation.
	var unknown []string
	for key := range overrides.Operations {
		if !usedOverrides[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return meta, nil, fmt.Errorf("overrides.yaml references unknown operations:\n  %s",
			strings.Join(unknown, "\n  "))
	}

	// Curated command paths must be unique, must not shadow a built-in, and
	// must not be a strict prefix of another (a leaf and a group cannot share
	// a name — the second registration silently wins).
	seen := map[string]string{}
	for _, op := range ops {
		if op.Command == "" {
			continue
		}
		key := op.Method + " " + op.Path
		if prev, dup := seen[op.Command]; dup {
			return meta, nil, fmt.Errorf("command %q mapped to both %s and %s", op.Command, prev, key)
		}
		if err := checkReservedCommand(op.Command); err != nil {
			return meta, nil, fmt.Errorf("%s: %w", key, err)
		}
		seen[op.Command] = key
	}
	for cmd := range seen {
		for other := range seen {
			if cmd != other && strings.HasPrefix(other, cmd+" ") {
				return meta, nil, fmt.Errorf("command %q is both a leaf (%s) and the prefix of %q; "+
					"rename one in overrides.yaml", cmd, seen[cmd], other)
			}
		}
	}

	meta.OpCount = len(ops)
	return meta, ops, nil
}

var pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

func compileOp(method, path string, item *v3.PathItem, specOp *v3.Operation, ov opOverride) (registry.Op, error) {
	op := registry.Op{
		Command:     ov.Command,
		Method:      method,
		Path:        path,
		Summary:     strings.TrimSpace(specOp.Summary),
		Description: strings.TrimSpace(specOp.Description),
		Envelope:    detectEnvelope(specOp),
		Destructive: method == "DELETE",
		Examples:    ov.Examples,
		BodyKind:    registry.BodyNone,
	}
	if ov.Destructive != nil {
		op.Destructive = *ov.Destructive
	}
	if op.Summary == "" {
		op.Summary = strings.ToLower(method) + " " + path
	}

	for _, m := range pathParamRe.FindAllStringSubmatch(path, -1) {
		op.PathParams = append(op.PathParams, m[1])
	}

	// Query params: path-item level first, op level second (op wins on name).
	params := map[string]*v3.Parameter{}
	var order []string
	for _, src := range [][]*v3.Parameter{item.Parameters, specOp.Parameters} {
		for _, p := range src {
			if p == nil || p.In != "query" {
				continue
			}
			if _, exists := params[p.Name]; !exists {
				order = append(order, p.Name)
			}
			params[p.Name] = p
		}
	}
	for _, name := range order {
		p := params[name]
		q := registry.Param{
			WireName:    name,
			Flag:        flagName(name),
			Type:        schemaParamType(schemaOf(p.Schema)),
			Repeatable:  strings.HasSuffix(name, "[]"),
			Required:    p.Required != nil && *p.Required,
			Enum:        enumValues(schemaOf(p.Schema)),
			Description: fixDescription(name, strings.TrimSpace(p.Description)),
		}
		if fo, ok := ov.Query[name]; ok {
			// fieldOverride is shared with the body: map, but these two keys are
			// only read while compiling body fields. KnownFields validation
			// accepts them here, so without this guard `skip: true` under query:
			// keeps shipping the flag it was written to remove.
			if fo.Skip {
				return op, fmt.Errorf("query override %q sets skip, which is only honoured for body fields", name)
			}
			if fo.FileToDataURI {
				return op, fmt.Errorf("query override %q sets file_to_data_uri, which is only honoured for body fields", name)
			}
			if fo.Type != "" {
				q.Type = registry.ParamType(fo.Type)
			}
			if fo.Description != "" {
				q.Description = fo.Description
			}
		}
		if name == "page" {
			op.Paginated = true
		}
		op.Query = append(op.Query, q)
	}

	// A few operations document a Pagination-shaped 200 response yet never
	// declare ?page= — a spec omission, not a backend one: /audits provably
	// serves 93 distinct pages. Synthesize the parameter rather than only
	// setting the flag, because --all's resume hint tells the operator to
	// re-run with --page, which cobra rejects unless the command registers it.
	if responseRefsPagination(specOp) && !op.Paginated {
		op.Paginated = true
		op.Query = append(op.Query, registry.Param{
			WireName:    "page",
			Flag:        "page",
			Type:        registry.TypeInteger,
			Description: "A cursor for pagination across multiple pages of results. Undeclared in the spec; the response carries pagination metadata.",
		})
	}

	skipped, err := compileBody(&op, specOp, ov)
	if err != nil {
		return op, err
	}
	if err := checkFieldOverrides(&op, ov, skipped); err != nil {
		return op, err
	}
	if err := checkFlagCollisions(&op); err != nil {
		return op, err
	}
	return op, nil
}

// checkFieldOverrides rejects query:/body: keys that match no compiled
// parameter, and file-typed fields in a JSON body that lack the data-URI
// encoding. Operation keys are already validated in compile(); field keys
// were not, so a field renamed upstream silently dropped its override — and
// losing file_to_data_uri makes the flag advertise "@path" while posting the
// literal string.
func checkFieldOverrides(op *registry.Op, ov opOverride, skippedBody []string) error {
	unmatched := func(names map[string]bool, keys map[string]fieldOverride, kind string) error {
		var unknown []string
		for k := range keys {
			if !names[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) == 0 {
			return nil
		}
		sort.Strings(unknown)
		return fmt.Errorf("%s override(s) match no %s field of this operation: %s",
			kind, kind, strings.Join(unknown, ", "))
	}
	queryNames := map[string]bool{}
	for _, p := range op.Query {
		queryNames[p.WireName] = true
	}
	if err := unmatched(queryNames, ov.Query, "query"); err != nil {
		return err
	}
	bodyNames := map[string]bool{}
	for _, f := range op.Body {
		bodyNames[f.Name] = true
	}
	// A skipped field matched a real spec property, it just did not survive
	// into the registry — it must not be reported as an unknown override key.
	for _, n := range skippedBody {
		bodyNames[n] = true
	}
	if err := unmatched(bodyNames, ov.Body, "body"); err != nil {
		return err
	}

	// Only curated operations register body flags; uncurated ones are reached
	// through `api call`, which takes raw JSON and never expands @path.
	if op.Command == "" || op.BodyKind != registry.BodyJSON {
		return nil
	}
	for _, f := range op.Body {
		if f.Type == registry.TypeFile && !f.FileToDataURI {
			return fmt.Errorf("body field %q is file-typed inside a JSON body: set file_to_data_uri: true "+
				"under body.%s, or the --%s flag will send the literal @path string", f.Name, f.Name, f.Flag)
		}
	}
	return nil
}

func compileBody(op *registry.Op, specOp *v3.Operation, ov opOverride) (skipped []string, _ error) {
	// 14 GET operations carry accidental copy-pasted request bodies (some
	// with bogus $ref-with-sibling schemas). GET never takes a body here.
	if op.Method == "GET" {
		return nil, nil
	}
	rb := specOp.RequestBody
	if rb == nil || rb.Content == nil {
		return nil, nil
	}
	var mediaType string
	var media *v3.MediaType
	for pair := rb.Content.First(); pair != nil; pair = pair.Next() {
		mediaType, media = pair.Key(), pair.Value()
		break // the spec never declares more than one request content type
	}
	if media == nil {
		return nil, nil // 14 GETs carry an accidental empty requestBody {"content": {}}
	}

	switch {
	case strings.HasPrefix(mediaType, "application/json"):
		op.BodyKind = registry.BodyJSON
	case strings.HasPrefix(mediaType, "multipart/form-data"):
		op.BodyKind = registry.BodyMultipart
	default:
		return nil, fmt.Errorf("unsupported request content type %q", mediaType)
	}

	schema := schemaOf(media.Schema)
	if schema == nil || schema.Properties == nil {
		return nil, nil
	}
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	for pair := schema.Properties.First(); pair != nil; pair = pair.Next() {
		name := pair.Key()
		if name == "" {
			continue // update_avatar has a bogus empty-named property
		}
		fieldSchema := schemaOf(pair.Value())
		f := registry.BodyField{
			Name:        name,
			Flag:        flagName(name),
			Type:        schemaParamType(fieldSchema),
			Required:    required[name],
			Enum:        enumValues(fieldSchema),
			Description: strings.TrimSpace(schemaDescription(fieldSchema)),
			Repeatable:  strings.HasSuffix(name, "[]"),
		}
		if fo, ok := ov.Body[name]; ok {
			if fo.Skip {
				skipped = append(skipped, name)
				continue
			}
			if fo.Type != "" {
				f.Type = registry.ParamType(fo.Type)
			}
			if fo.Description != "" {
				f.Description = fo.Description
			}
			f.FileToDataURI = fo.FileToDataURI
		}
		op.Body = append(op.Body, f)
	}
	return skipped, nil
}

func schemaOf(sp *base.SchemaProxy) *base.Schema {
	if sp == nil {
		return nil
	}
	return sp.Schema() // nil on build error; callers must tolerate
}

func schemaDescription(s *base.Schema) string {
	if s == nil {
		return ""
	}
	return s.Description
}

// schemaParamType maps a 3.1 schema to the CLI value type. Binary formats
// (including oneOf branches, as in the employee-document upload field)
// become file fields.
func schemaParamType(s *base.Schema) registry.ParamType {
	if s == nil {
		return registry.TypeString
	}
	if isBinary(s) {
		return registry.TypeFile
	}
	for _, t := range s.Type {
		switch t {
		case "null":
			continue
		case "integer":
			return registry.TypeInteger
		case "number":
			return registry.TypeNumber
		case "boolean":
			return registry.TypeBoolean
		case "array":
			// Arrays of binary/string in multipart become repeatable fields;
			// in JSON bodies they are --set territory.
			if s.Items != nil && s.Items.IsA() {
				if isBinary(schemaOf(s.Items.A)) {
					return registry.TypeFile
				}
			}
			return registry.TypeArray
		case "object":
			return registry.TypeObject
		case "string":
			return registry.TypeString
		}
	}
	return registry.TypeString
}

func isBinary(s *base.Schema) bool {
	if s == nil {
		return false
	}
	if s.Format == "binary" {
		return true
	}
	for _, of := range s.OneOf {
		if sub := schemaOf(of); sub != nil && sub.Format == "binary" {
			return true
		}
	}
	return false
}

func enumValues(s *base.Schema) []string {
	if s == nil || len(s.Enum) == 0 {
		return nil
	}
	vals := make([]string, 0, len(s.Enum))
	for _, n := range s.Enum {
		if n != nil && n.Value != "" {
			vals = append(vals, n.Value)
		}
	}
	return vals
}

// detectEnvelope classifies the documented 2xx response shape. Informational
// only — the runtime normalizer inspects actual JSON.
func detectEnvelope(specOp *v3.Operation) registry.EnvelopeKind {
	if specOp.Responses == nil || specOp.Responses.Codes == nil {
		return registry.EnvelopeUnknown
	}
	saw2xx := false
	for pair := specOp.Responses.Codes.First(); pair != nil; pair = pair.Next() {
		code := pair.Key()
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		saw2xx = true
		resp := pair.Value()
		if resp == nil || resp.Content == nil {
			continue
		}
		for mt := resp.Content.First(); mt != nil; mt = mt.Next() {
			schema := schemaOf(mt.Value().Schema)
			if schema == nil || schema.Properties == nil {
				continue
			}
			has := func(key string) bool {
				_, ok := schema.Properties.Get(key)
				return ok
			}
			switch {
			case has("records"):
				return registry.EnvelopeBulk
			case has("data") && has("metadata"):
				return registry.EnvelopeList
			case has("data"):
				return registry.EnvelopeSingle
			default:
				return registry.EnvelopeBare
			}
		}
	}
	if saw2xx {
		return registry.EnvelopeNone
	}
	return registry.EnvelopeUnknown
}

// responseRefsPagination reports whether the 200 response body declares the
// shared Pagination component ({page, pages, count, items}), which every
// page-able collection endpoint returns under "metadata".
func responseRefsPagination(specOp *v3.Operation) bool {
	if specOp.Responses == nil || specOp.Responses.Codes == nil {
		return false
	}
	resp, ok := specOp.Responses.Codes.Get("200")
	if !ok || resp == nil || resp.Content == nil {
		return false
	}
	for mt := resp.Content.First(); mt != nil; mt = mt.Next() {
		schema := schemaOf(mt.Value().Schema)
		if schema == nil || schema.Properties == nil {
			continue
		}
		for prop := schema.Properties.First(); prop != nil; prop = prop.Next() {
			if isPaginationSchema(prop.Value()) {
				return true
			}
		}
	}
	return false
}

func isPaginationSchema(sp *base.SchemaProxy) bool {
	if sp == nil {
		return false
	}
	if sp.IsReference() {
		return strings.HasSuffix(sp.GetReference(), "/Pagination")
	}
	// The spec writes the metadata property as a $ref with a sibling
	// "type": "object", which some readers collapse into an inline copy of the
	// component rather than keeping the reference.
	s := sp.Schema()
	return s != nil && s.Title == "Pagination"
}

// flagName converts a literal wire name to a CLI flag:
// "employee_ids[]" → "employee-ids", "hired_on[gte]" → "hired-on-gte".
func flagName(wire string) string {
	s := strings.TrimSuffix(wire, "[]")
	s = strings.NewReplacer("[", "-", "]", "", "_", "-", ".", "-").Replace(s)
	s = strings.Trim(strings.ToLower(s), "-")
	return s
}

// reservedCommands are command paths the runtime mounts itself (see
// NewRoot in internal/command). A curated command that lands on one of these
// silently shadows it — `api ops` would issue an HTTP request instead of
// dumping the operation table — so the generator refuses. Top-level names are
// reserved wholesale: everything under them belongs to the built-in tree.
var reservedCommandRoots = map[string]bool{
	"api": true, "auth": true, "config": true, "skill": true,
	"commands": true, "version": true, "agents-md": true,
	"help": true, "completion": true,
}

// reservedCommandPaths are individual built-ins mounted inside a group that
// curated commands otherwise share.
var reservedCommandPaths = map[string]bool{
	"employees bulk-update": true,
}

func checkReservedCommand(command string) error {
	if reservedCommandPaths[command] {
		return fmt.Errorf("command %q is mounted by the CLI itself; rename it in overrides.yaml", command)
	}
	root, _, _ := strings.Cut(command, " ")
	if reservedCommandRoots[root] {
		return fmt.Errorf("command %q lives under the built-in %q tree; rename it in overrides.yaml", command, root)
	}
	return nil
}

// reservedFlags are global/persistent flag names the builder claims.
var reservedFlags = map[string]bool{
	"help": true, "output": true, "jq": true, "raw": true, "fields": true,
	"api-key": true, "api-url": true, "profile": true,
	"all": true, "max-pages": true, "yes": true, "dry-run": true,
	"input": true, "set": true, "max-retries": true, "timeout": true,
	"verbose": true, "version": true,
}

func checkFlagCollisions(op *registry.Op) error {
	seen := map[string]string{}
	claim := func(flag, what string) error {
		if reservedFlags[flag] {
			return fmt.Errorf("%s flag --%s collides with a reserved global flag; rename it in overrides.yaml", what, flag)
		}
		if prev, dup := seen[flag]; dup {
			return fmt.Errorf("flag --%s derived for both %s and %s; disambiguate in overrides.yaml", flag, prev, what)
		}
		seen[flag] = what
		return nil
	}
	for i := range op.Query {
		if err := claim(op.Query[i].Flag, "query param "+op.Query[i].WireName); err != nil {
			return err
		}
	}
	for i := range op.Body {
		f := &op.Body[i]
		if _, dup := seen[f.Flag]; dup || reservedFlags[f.Flag] {
			f.Flag = "body-" + f.Flag // rare: body field shadowed by a query param or global
		}
		if err := claim(f.Flag, "body field "+f.Name); err != nil {
			return err
		}
	}
	return nil
}

// emit renders the registry as formatted Go source.
func emit(meta registry.Meta, ops []registry.Op) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `// Code generated by internal/gen from internal/spec/peopleforce-openapi.json. DO NOT EDIT.
package registry

var Info = Meta{SpecTitle: %q, SpecVersion: %q, BaseURL: %q, OpCount: %d}

var Ops = []Op{
`, meta.SpecTitle, meta.SpecVersion, meta.BaseURL, meta.OpCount)

	for _, op := range ops {
		b.WriteString("\t{\n")
		if op.Command != "" {
			fmt.Fprintf(&b, "\t\tCommand: %q,\n", op.Command)
		}
		fmt.Fprintf(&b, "\t\tMethod: %q,\n", op.Method)
		fmt.Fprintf(&b, "\t\tPath: %q,\n", op.Path)
		fmt.Fprintf(&b, "\t\tSummary: %q,\n", op.Summary)
		if op.Description != "" {
			fmt.Fprintf(&b, "\t\tDescription: %q,\n", op.Description)
		}
		if len(op.PathParams) > 0 {
			fmt.Fprintf(&b, "\t\tPathParams: %s,\n", strSlice(op.PathParams))
		}
		if len(op.Query) > 0 {
			b.WriteString("\t\tQuery: []Param{\n")
			for _, q := range op.Query {
				fmt.Fprintf(&b, "\t\t\t{WireName: %q, Flag: %q, Type: %q", q.WireName, q.Flag, q.Type)
				if q.Repeatable {
					b.WriteString(", Repeatable: true")
				}
				if q.Required {
					b.WriteString(", Required: true")
				}
				if len(q.Enum) > 0 {
					fmt.Fprintf(&b, ", Enum: %s", strSlice(q.Enum))
				}
				if q.Description != "" {
					fmt.Fprintf(&b, ", Description: %q", q.Description)
				}
				b.WriteString("},\n")
			}
			b.WriteString("\t\t},\n")
		}
		if len(op.Body) > 0 {
			b.WriteString("\t\tBody: []BodyField{\n")
			for _, f := range op.Body {
				fmt.Fprintf(&b, "\t\t\t{Name: %q, Flag: %q, Type: %q", f.Name, f.Flag, f.Type)
				if f.Repeatable {
					b.WriteString(", Repeatable: true")
				}
				if f.Required {
					b.WriteString(", Required: true")
				}
				if len(f.Enum) > 0 {
					fmt.Fprintf(&b, ", Enum: %s", strSlice(f.Enum))
				}
				if f.Description != "" {
					fmt.Fprintf(&b, ", Description: %q", f.Description)
				}
				if f.FileToDataURI {
					b.WriteString(", FileToDataURI: true")
				}
				b.WriteString("},\n")
			}
			b.WriteString("\t\t},\n")
		}
		if op.BodyKind != registry.BodyNone {
			fmt.Fprintf(&b, "\t\tBodyKind: %q,\n", op.BodyKind)
		} else {
			fmt.Fprintf(&b, "\t\tBodyKind: %q,\n", registry.BodyNone)
		}
		fmt.Fprintf(&b, "\t\tEnvelope: %q,\n", op.Envelope)
		if op.Paginated {
			b.WriteString("\t\tPaginated: true,\n")
		}
		if op.Destructive {
			b.WriteString("\t\tDestructive: true,\n")
		}
		if len(op.Examples) > 0 {
			fmt.Fprintf(&b, "\t\tExamples: %s,\n", strSlice(op.Examples))
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")

	return format.Source(b.Bytes())
}

func strSlice(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%q", s)
	}
	return "[]string{" + strings.Join(parts, ", ") + "}"
}
