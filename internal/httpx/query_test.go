package httpx

import "testing"

// The PeopleForce backend parses literal bracket param names (Rails style).
// These strings are pinned byte-for-byte: keys must never be sanitized or
// percent-encoded, values must be escaped, repeated keys must repeat.
func TestEncodeQueryByteForByte(t *testing.T) {
	tests := []struct {
		name  string
		pairs []QueryPair
		want  string
	}{
		{
			name: "repeatable bracket array params",
			pairs: []QueryPair{
				{"employee_ids[]", "1"},
				{"employee_ids[]", "2"},
			},
			want: "employee_ids[]=1&employee_ids[]=2",
		},
		{
			name:  "range filter keeps literal brackets",
			pairs: []QueryPair{{"hired_on[gte]", "2025-01-01"}},
			want:  "hired_on[gte]=2025-01-01",
		},
		{
			name: "mixed filters preserve order",
			pairs: []QueryPair{
				{"status", "active"},
				{"department_ids[]", "12"},
				{"department_ids[]", "14"},
				{"created_at[gte]", "1683557122"},
			},
			want: "status=active&department_ids[]=12&department_ids[]=14&created_at[gte]=1683557122",
		},
		{
			name:  "values are escaped, keys are not",
			pairs: []QueryPair{{"emails[]", "a+b@x.co"}},
			want:  "emails[]=a%2Bb%40x.co",
		},
		{
			name:  "spaces in values",
			pairs: []QueryPair{{"name", "Jane Doe"}},
			want:  "name=Jane+Doe",
		},
		{
			name:  "empty",
			pairs: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EncodeQuery(tt.pairs); got != tt.want {
				t.Errorf("EncodeQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every distinct bracket-param style in the spec, pinned.
func TestEncodeQueryAllSpecBracketStyles(t *testing.T) {
	styles := []string{
		"ids[]", "employee_ids[]", "emails[]", "employee_numbers[]",
		"department_ids[]", "division_ids[]", "location_ids[]", "position_ids[]",
		"team_ids[]", "project_ids[]", "holiday_policy_ids[]", "vacancy_ids[]",
		"applicant_ids[]", "tag_ids[]", "skills[]", "states[]", "status[]",
		"hired_on[gte]", "hired_on[lte]", "terminated_on[gte]", "terminated_on[lte]",
		"created_at[gte]", "created_at[lte]", "created_at[gt]", "created_at[lt]",
		"updated_at[gte]", "updated_at[lte]", "updated_at[gt]", "updated_at[lt]",
		"date[gte]", "date[lte]", "starts_on[gte]", "starts_on[lte]",
		"ends_on[gte]", "ends_on[lte]",
	}
	for _, key := range styles {
		got := EncodeQuery([]QueryPair{{key, "v"}})
		want := key + "=v"
		if got != want {
			t.Errorf("EncodeQuery(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBuildPath(t *testing.T) {
	got, err := BuildPath("/employees/{employee_id}/salaries/{id}", map[string]string{
		"employee_id": "42", "id": "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/employees/42/salaries/7" {
		t.Errorf("BuildPath() = %q", got)
	}

	// The upstream typo path must survive verbatim.
	got, err = BuildPath("/termintation_reasons/{termination_reason_id}", map[string]string{
		"termination_reason_id": "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/termintation_reasons/5" {
		t.Errorf("BuildPath() = %q", got)
	}

	// Path values get escaped.
	got, err = BuildPath("/employees/{employee_id}/tables/{internal_name}", map[string]string{
		"employee_id": "1", "internal_name": "a b/c",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/employees/1/tables/a%20b%2Fc" {
		t.Errorf("BuildPath() = %q", got)
	}

	if _, err := BuildPath("/x/{a}", map[string]string{"b": "1"}); err == nil {
		t.Error("expected error for unknown path param")
	}
	if _, err := BuildPath("/x/{a}", map[string]string{}); err == nil {
		t.Error("expected error for unresolved path param")
	}
}

func TestSanitizeRequestTarget(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/employees?search=John Doe", "/employees?search=John%20Doe"},
		{"/employees?ids[]=1&ids[]=2", "/employees?ids[]=1&ids[]=2"}, // brackets stay verbatim
		{"/employees?x=100%", "/employees?x=100%25"},                 // lone % encoded
		{"/employees?v=a%20b", "/employees?v=a%20b"},                 // valid escape kept
		{"/employees?tag=a#b", "/employees?tag=a%23b"},               // # is data, not fragment
		{"/employees?name=Андрій", "/employees?name=%D0%90%D0%BD%D0%B4%D1%80%D1%96%D0%B9"},
		{"/job_levels", "/job_levels"},
	}
	for _, tt := range tests {
		if got := SanitizeRequestTarget(tt.in); got != tt.want {
			t.Errorf("SanitizeRequestTarget(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
