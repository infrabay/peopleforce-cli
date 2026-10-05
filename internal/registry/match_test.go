package registry

import "testing"

// Match resolves the concrete paths `api call` takes onto the templates the
// registry stores; Find cannot, because it compares template strings.
func TestMatchResolvesConcretePaths(t *testing.T) {
	cases := []struct {
		method, path string
		wantPath     string
		wantOK       bool
	}{
		{"GET", "/employees", "/employees", true},
		{"GET", "/employees/42", "/employees/{employee_id}", true},
		{"POST", "/employees/1/terminate", "/employees/{employee_id}/terminate", true},
		{"POST", "/employees/1/terminate?foo=bar", "/employees/{employee_id}/terminate", true},
		{"DELETE", "/leave_requests/9", "/leave_requests/{id}", true},
		// Wrong method, unknown path, and a segment-count mismatch must miss.
		{"PATCH", "/employees", "", false},
		{"GET", "/nope", "", false},
		{"POST", "/employees/1/terminate/extra", "", false},
		{"POST", "/employees//terminate", "", false},
	}
	for _, tt := range cases {
		op, ok := Match(tt.method, tt.path)
		if ok != tt.wantOK {
			t.Errorf("Match(%s, %s) ok = %v, want %v", tt.method, tt.path, ok, tt.wantOK)
			continue
		}
		if ok && op.Path != tt.wantPath {
			t.Errorf("Match(%s, %s) path = %s, want %s", tt.method, tt.path, op.Path, tt.wantPath)
		}
	}
}

// A literal segment must beat a placeholder regardless of table order,
// otherwise the result depends on where the generator emitted each op.
func TestMatchPrefersLiteralOverPlaceholder(t *testing.T) {
	op, ok := Match("GET", "/employees/terminated")
	if !ok {
		t.Fatal("GET /employees/terminated not matched")
	}
	if op.Path != "/employees/terminated" {
		t.Errorf("path = %s, want the literal /employees/terminated", op.Path)
	}
}

// The destructive guard in `api call` depends on this lookup.
func TestMatchFindsDestructiveOps(t *testing.T) {
	for _, tt := range []struct{ method, path string }{
		{"DELETE", "/leave_requests/9"},
		{"POST", "/employees/7/terminate"},
	} {
		op, ok := Match(tt.method, tt.path)
		if !ok {
			t.Errorf("%s %s not matched", tt.method, tt.path)
			continue
		}
		if !op.Destructive {
			t.Errorf("%s %s should be marked destructive", tt.method, tt.path)
		}
	}
}

// The router decodes and normalises the path before routing, so each of these
// reaches POST /employees/{id}/terminate on the server. The guard must see the
// same route, or `api call` terminates an employee without --yes.
func TestMatchSeesThroughPathSpellings(t *testing.T) {
	for _, path := range []string{
		"/employees/1/%74erminate",
		"/employees//1/terminate",
		"/employees/1/./terminate",
		"/employees/1/x/../terminate",
		"/employees/1/terminate.json",
		"/employees/1/terminate.json?reason=x",
	} {
		op, ok := Match("POST", path)
		if !ok || !op.Destructive {
			t.Errorf("POST %s: matched=%v, want the destructive /employees/{employee_id}/terminate", path, ok)
		}
	}
}
