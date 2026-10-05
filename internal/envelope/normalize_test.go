package envelope

import (
	"encoding/json"
	"testing"
)

func TestNormalizeAllUpstreamShapes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		status   int
		wantData string
		wantMeta map[string]any
	}{
		{
			name:     "standard list with pagination",
			body:     `{"data":[{"id":1},{"id":2}],"metadata":{"page":1,"pages":3,"count":50,"items":25}}`,
			status:   200,
			wantData: `[{"id":1},{"id":2}]`,
			wantMeta: map[string]any{"status": 200, "page": 1.0, "pages": 3.0, "count": 50.0, "items": 25.0},
		},
		{
			name:     "standard single",
			body:     `{"data":{"id":7,"email":"a@x.co"}}`,
			status:   200,
			wantData: `{"email":"a@x.co","id":7}`, // compactJSON sorts keys
			wantMeta: map[string]any{"status": 200},
		},
		{
			name:     "null data on list becomes empty array",
			body:     `{"data":null,"metadata":{"page":1,"pages":0,"count":0,"items":25}}`,
			status:   200,
			wantData: `[]`,
			wantMeta: map[string]any{"status": 200, "page": 1.0, "pages": 0.0, "count": 0.0, "items": 25.0},
		},
		{
			name:     "null data without metadata stays null",
			body:     `{"data":null}`,
			status:   200,
			wantData: `null`,
			wantMeta: map[string]any{"status": 200},
		},
		{
			name:     "bare resource without envelope",
			body:     `{"id":9,"name":"Engineering"}`,
			status:   201,
			wantData: `{"id":9,"name":"Engineering"}`,
			wantMeta: map[string]any{"status": 201},
		},
		{
			name:     "bare array",
			body:     `[{"id":1}]`,
			status:   200,
			wantData: `[{"id":1}]`,
			wantMeta: map[string]any{"status": 200},
		},
		{
			name:     "empty body 204",
			body:     ``,
			status:   204,
			wantData: `null`,
			wantMeta: map[string]any{"status": 204},
		},
		{
			name:     "stray top-level page preserved in meta (timesheets)",
			body:     `{"data":[{"id":1}],"metadata":{"page":2,"pages":2,"count":1,"items":25},"page":2}`,
			status:   200,
			wantData: `[{"id":1}]`,
			wantMeta: map[string]any{"status": 200, "page": 2.0, "pages": 2.0, "count": 1.0, "items": 25.0},
		},
		{
			// The stray key must not win: --all reads meta.page to decide when
			// to stop, so letting a disagreeing top-level page through cut the
			// list short at page 1 of 3.
			name:     "stray top-level page disagreeing with metadata loses",
			body:     `{"data":[{"id":1}],"metadata":{"page":1,"pages":3,"count":3,"items":1},"page":3}`,
			status:   200,
			wantData: `[{"id":1}]`,
			wantMeta: map[string]any{"status": 200, "page": 1.0, "pages": 3.0},
		},
		{
			name:     "stray top-level status does not shadow the HTTP status",
			body:     `{"data":[{"id":1}],"metadata":{"page":1,"pages":1},"status":"processing"}`,
			status:   200,
			wantData: `[{"id":1}]`,
			wantMeta: map[string]any{"status": 200},
		},
		{
			name:     "unrelated stray keys are still preserved",
			body:     `{"data":[{"id":1}],"metadata":{"page":1,"pages":1},"warning":"deprecated"}`,
			status:   200,
			wantData: `[{"id":1}]`,
			wantMeta: map[string]any{"status": 200, "warning": "deprecated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := Normalize([]byte(tt.body), tt.status)
			if got := compactJSON(t, n.Data); got != tt.wantData {
				t.Errorf("data = %s, want %s", got, tt.wantData)
			}
			for k, want := range tt.wantMeta {
				if got := n.Meta[k]; got != want {
					t.Errorf("meta[%s] = %v (%T), want %v", k, got, got, want)
				}
			}
		})
	}
}

func TestNormalizeBulkRecordsAndErrors(t *testing.T) {
	body := `{"records":[{"id":1}],"errors":[{"index":1,"message":"bad date"}]}`
	n := Normalize([]byte(body), 200)
	if got := compactJSON(t, n.Data); got != `[{"id":1}]` {
		t.Errorf("data = %s", got)
	}
	if !n.HasBulkErrors() {
		t.Error("HasBulkErrors() = false, want true")
	}

	clean := Normalize([]byte(`{"records":[{"id":1}],"errors":[]}`), 200)
	if clean.HasBulkErrors() {
		t.Error("HasBulkErrors() = true for empty errors")
	}
}

func TestNormalizeNonJSONBody(t *testing.T) {
	n := Normalize([]byte("Internal error"), 200)
	if got := compactJSON(t, n.Data); got != "null" {
		t.Errorf("data = %s", got)
	}
	if n.Meta["raw"] != "Internal error" {
		t.Errorf("meta[raw] = %v", n.Meta["raw"])
	}
}

func TestPage(t *testing.T) {
	n := Normalize([]byte(`{"data":[],"metadata":{"page":2,"pages":5,"count":100,"items":25}}`), 200)
	page, pages, ok := n.Page()
	if !ok || page != 2 || pages != 5 {
		t.Errorf("Page() = %d, %d, %v", page, pages, ok)
	}

	n = Normalize([]byte(`{"data":{"id":1}}`), 200)
	if _, _, ok := n.Page(); ok {
		t.Error("Page() ok = true for single resource")
	}
}

// The envelope itself must marshal into the documented contract shape.
func TestNormalizedMarshalContract(t *testing.T) {
	n := Normalize([]byte(`{"data":[{"id":1}],"metadata":{"page":1,"pages":1,"count":1,"items":25}}`), 200)
	out, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var round struct {
		Data []map[string]any `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("contract round-trip: %v (%s)", err, out)
	}
	if len(round.Data) != 1 || round.Meta["pages"] != 1.0 {
		t.Errorf("unexpected contract shape: %s", out)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// The errors key is absent when every record succeeded, so requiring it made
// .data flip between an array and the whole object depending on failures.
func TestNormalizeBulkRecordsWithoutErrors(t *testing.T) {
	n := Normalize([]byte(`{"records":[{"id":1},{"id":2}]}`), 200)
	if got := compactJSON(t, n.Data); got != `[{"id":1},{"id":2}]` {
		t.Errorf("data = %s, want the records array", got)
	}
	if n.HasBulkErrors() {
		t.Error("no errors key means no bulk errors")
	}
}

// A resource that merely carries a records field must keep its shape.
func TestNormalizeResourceWithRecordsFieldIsNotUnwrapped(t *testing.T) {
	body := `{"id":7,"name":"Payroll run","records":[{"id":1}]}`
	n := Normalize([]byte(body), 200)
	if got := compactJSON(t, n.Data); got != body {
		t.Errorf("data = %s, want the whole object %s", got, body)
	}
}

// The shape the live API actually sends. Every other fixture in this package
// used a flat metadata:{page,pages}, which the server never produces — so
// Page() reported ok=false in production and --all silently fell back to
// paging until an empty page while agents reading meta.page found nothing.
func TestNormalizeHoistsNestedPaginationBlock(t *testing.T) {
	body := `{"data":[{"id":1}],"metadata":{"pagination":{"page":2,"pages":5,"count":81,"items":50}}}`
	n := Normalize([]byte(body), 200)

	for k, want := range map[string]any{"page": 2.0, "pages": 5.0, "count": 81.0, "items": 50.0, "status": 200} {
		if got := n.Meta[k]; got != want {
			t.Errorf("meta[%s] = %v (%T), want %v", k, got, got, want)
		}
	}
	if _, still := n.Meta["pagination"]; still {
		t.Error("the nested block should be hoisted, not duplicated")
	}
	page, pages, ok := n.Page()
	if !ok || page != 2 || pages != 5 {
		t.Errorf("Page() = %d, %d, %v; want 2, 5, true", page, pages, ok)
	}
}

// Other metadata keys must survive alongside the hoisted counters.
func TestNormalizeKeepsNonPaginationMetadata(t *testing.T) {
	body := `{"data":[{"id":1}],"metadata":{"pagination":{"page":1,"pages":1},"generated_at":"2026-07-28"}}`
	n := Normalize([]byte(body), 200)
	if n.Meta["generated_at"] != "2026-07-28" {
		t.Errorf("meta.generated_at = %v, want the passthrough value", n.Meta["generated_at"])
	}
	if n.Meta["page"] != 1.0 {
		t.Errorf("meta.page = %v, want 1", n.Meta["page"])
	}
}

// A payload carrying its own "status" must not shadow the HTTP status, whether
// it arrives nested, in metadata, or as a stray top-level key.
func TestNormalizeStatusIsAlwaysTheHTTPStatus(t *testing.T) {
	body := `{"data":[{"id":1}],"metadata":{"pagination":{"status":"paged"},"status":"meta"},"status":"top"}`
	n := Normalize([]byte(body), 201)
	if n.Meta["status"] != 201 {
		t.Errorf("meta.status = %v, want the HTTP status 201", n.Meta["status"])
	}
}

func TestNormalizeNotJSONFlag(t *testing.T) {
	if !Normalize([]byte("<html>"), 200).NotJSON {
		t.Error("non-empty non-JSON body must set NotJSON")
	}
	for _, body := range []string{"", "  \n", "null", "[1]", `{"id":1}`} {
		if Normalize([]byte(body), 200).NotJSON {
			t.Errorf("body %q must not set NotJSON", body)
		}
	}
}
