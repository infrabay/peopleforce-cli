// Package registry holds the static operation table compiled from the
// PeopleForce OpenAPI spec at build time (see internal/gen). The shipped
// binary never parses the spec; it only reads these tables.
//
// JSON tags define the machine-readable shape emitted by `api describe`,
// `api ops`, and the golden snapshot — snake_case, stable for agents.
package registry

// EnvelopeKind describes the documented response shape of an operation.
// The runtime normalizer is shape-driven and does not depend on this value;
// it exists for help text and docs (the spec is wrong about responses in
// ~50 places, so decoding always inspects the actual JSON).
type EnvelopeKind string

const (
	EnvelopeList    EnvelopeKind = "list"   // {data: [...], metadata: {...}}
	EnvelopeSingle  EnvelopeKind = "single" // {data: {...}}
	EnvelopeBare    EnvelopeKind = "bare"   // resource object without envelope
	EnvelopeBulk    EnvelopeKind = "bulk"   // {records: [...], errors: [...]}
	EnvelopeNone    EnvelopeKind = "none"   // empty body / 204
	EnvelopeUnknown EnvelopeKind = "unknown"
)

// BodyKind describes how an operation's input is transmitted.
type BodyKind string

const (
	BodyNone      BodyKind = "none"
	BodyJSON      BodyKind = "json"
	BodyMultipart BodyKind = "multipart"
)

// ParamType is the wire type used to parse and validate flag values.
type ParamType string

const (
	TypeString  ParamType = "string"
	TypeInteger ParamType = "integer"
	TypeNumber  ParamType = "number"
	TypeBoolean ParamType = "boolean"
	TypeObject  ParamType = "object"
	TypeArray   ParamType = "array"
	TypeFile    ParamType = "file" // binary upload field; flag accepts @path
)

// Param is a query parameter. WireName is sent verbatim — bracket names like
// "employee_ids[]" or "hired_on[gte]" must never be sanitized or
// percent-encoded on the wire.
type Param struct {
	WireName    string    `json:"wire_name"`            // literal name on the wire, e.g. "hired_on[gte]"
	Flag        string    `json:"flag"`                 // CLI flag name, e.g. "hired-on-gte"
	Type        ParamType `json:"type"`                 // scalar type of a single value
	Repeatable  bool      `json:"repeatable,omitempty"` // bracket [] params: repeated key per value
	Required    bool      `json:"required,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Description string    `json:"description,omitempty"`
}

// BodyField is a top-level request-body property exposed as a typed flag.
// Object/array fields are settable via --set / --input instead of flags.
type BodyField struct {
	Name          string    `json:"name"` // JSON key (or multipart field name)
	Flag          string    `json:"flag"`
	Type          ParamType `json:"type"`
	Repeatable    bool      `json:"repeatable,omitempty"` // array fields with bracket names (skills[], urls[])
	Required      bool      `json:"required,omitempty"`
	Enum          []string  `json:"enum,omitempty"`
	Description   string    `json:"description,omitempty"`
	FileToDataURI bool      `json:"file_to_data_uri,omitempty"` // @file is encoded as base64 data-URI string (update_avatar)
}

// Op is one API operation.
type Op struct {
	// Command is the curated CLI command path, space-separated
	// (e.g. "employees list", "leave requests create"). Empty for
	// operations reachable only through `peopleforce api call`.
	Command string `json:"command,omitempty"`

	Method      string `json:"method"` // GET/POST/PUT/DELETE
	Path        string `json:"path"`   // literal path template — upstream typos preserved
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`

	PathParams []string    `json:"path_params,omitempty"` // names in path order; all treated as strings
	Query      []Param     `json:"query,omitempty"`
	Body       []BodyField `json:"body,omitempty"`
	BodyKind   BodyKind    `json:"body_kind"`

	Envelope    EnvelopeKind `json:"envelope"`
	Paginated   bool         `json:"paginated,omitempty"`   // has the page query param
	Destructive bool         `json:"destructive,omitempty"` // requires --yes (DELETE, terminate, ...)

	Examples []string `json:"examples,omitempty"` // curated example invocations for --help
}

// Meta describes the compiled registry as a whole.
type Meta struct {
	SpecTitle   string `json:"spec_title"`
	SpecVersion string `json:"spec_version"`
	BaseURL     string `json:"base_url"` // default server URL from the spec
	OpCount     int    `json:"op_count"`
}

// ByCommand returns the op mounted at the given curated command path, if any.
func ByCommand(command string) (*Op, bool) {
	for i := range Ops {
		if Ops[i].Command == command {
			return &Ops[i], true
		}
	}
	return nil, false
}

// Find returns the op with the given method and path template, if any.
func Find(method, path string) (*Op, bool) {
	for i := range Ops {
		if Ops[i].Method == method && Ops[i].Path == path {
			return &Ops[i], true
		}
	}
	return nil, false
}
