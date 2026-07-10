// Package envelope normalizes every response shape the PeopleForce API
// actually produces into one stable contract for agents:
//
//	{"data": <resource(s)>, "meta": {...}}
//
// The OpenAPI spec is wrong about responses in ~50 places (missing schemas,
// wrong content types), so normalization is driven purely by the JSON that
// comes back, never by the spec.
package envelope

import (
	"bytes"
	"encoding/json"
)

// Normalized is the CLI's single output envelope.
type Normalized struct {
	Data json.RawMessage `json:"data"`
	Meta map[string]any  `json:"meta,omitempty"`
}

var nullJSON = json.RawMessage("null")

// Normalize converts a raw 2xx response body into the {data, meta} contract.
// Upstream shapes handled:
//
//  1. {data: [...], metadata: {page,pages,count,items}}  — standard lists
//  2. {data: {...}}                                      — standard singles
//  3. {data: null}                                       — normalized to []
//  4. bare resource object or array (no envelope)        — wrapped as data
//  5. {records: [...], errors: [...]}                    — bulk operations
//  6. empty body / 204                                   — data: null
//
// Extra top-level keys next to data/metadata (e.g. the stray "page" on
// /time/timesheets) are preserved under meta.
func Normalize(body []byte, status int) Normalized {
	meta := map[string]any{"status": status}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return Normalized{Data: nullJSON, Meta: meta}
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &top); err != nil {
		// Not a JSON object: bare array, scalar, or non-JSON payload.
		if json.Valid(trimmed) {
			return Normalized{Data: json.RawMessage(trimmed), Meta: meta}
		}
		meta["raw"] = string(trimmed)
		return Normalized{Data: nullJSON, Meta: meta}
	}

	// Shape 5: bulk {records, errors}.
	if records, ok := top["records"]; ok {
		if errs, ok := top["errors"]; ok {
			meta["errors"] = json.RawMessage(errs)
			addExtraKeys(meta, top, "records", "errors")
			return Normalized{Data: normalizeNull(records, true), Meta: meta}
		}
	}

	// Shapes 1-3: enveloped.
	if data, ok := top["data"]; ok {
		listLike := false
		if md, ok := top["metadata"]; ok {
			var pagination map[string]any
			if err := json.Unmarshal(md, &pagination); err == nil {
				for k, v := range pagination {
					meta[k] = v
				}
			}
			listLike = true
		}
		addExtraKeys(meta, top, "data", "metadata")
		return Normalized{Data: normalizeNull(data, listLike), Meta: meta}
	}

	// Shape 4: bare object.
	return Normalized{Data: json.RawMessage(trimmed), Meta: meta}
}

// HasBulkErrors reports whether a normalized bulk response carried a
// non-empty errors array (the CLI maps this to a validation exit code).
func (n Normalized) HasBulkErrors() bool {
	raw, ok := n.Meta["errors"].(json.RawMessage)
	if !ok {
		return false
	}
	var errs []json.RawMessage
	if err := json.Unmarshal(raw, &errs); err != nil {
		return false
	}
	return len(errs) > 0
}

// normalizeNull turns JSON null into [] when the value is list-like, so
// agents can always iterate .data on list endpoints (review_cycles returns
// data: null when empty).
func normalizeNull(v json.RawMessage, listLike bool) json.RawMessage {
	if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && listLike {
		return json.RawMessage("[]")
	}
	return v
}

func addExtraKeys(meta map[string]any, top map[string]json.RawMessage, consumed ...string) {
	skip := map[string]bool{}
	for _, k := range consumed {
		skip[k] = true
	}
	for k, v := range top {
		if skip[k] {
			continue
		}
		var val any
		if err := json.Unmarshal(v, &val); err == nil {
			meta[k] = val
		}
	}
}

// Page extracts pagination info from a normalized response, if present.
// ok is false when the response carries no usable page/pages metadata.
func (n Normalized) Page() (page, pages int, ok bool) {
	p, okP := asInt(n.Meta["page"])
	ps, okPs := asInt(n.Meta["pages"])
	return p, ps, okP && okPs
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}
