package command

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Exit codes are part of the agent contract — documented in --help, README
// and SKILL.md. Agents branch on $? instead of parsing prose.
const (
	ExitOK         = 0
	ExitUsage      = 2 // bad flags/args, invalid --set/--input, missing required
	ExitAuth       = 3 // 401/403 or no API key configured
	ExitNotFound   = 4 // 404
	ExitValidation = 5 // 422/400, or bulk response with non-empty errors
	ExitRateLimit  = 6 // 429 after retries exhausted
	ExitServer     = 7 // 5xx
	ExitNetwork    = 8 // DNS/TLS/timeout/connection failures
)

// ExitError carries the process exit code plus a structured, machine-readable
// error for stderr.
type ExitError struct {
	Code    int             `json:"-"`
	Type    string          `json:"type"`              // auth|not_found|validation|rate_limit|server|network|usage|api
	Status  int             `json:"status,omitempty"`  // HTTP status when applicable
	Message string          `json:"message"`
	Detail  json.RawMessage `json:"detail,omitempty"` // raw API response body when it was JSON
}

func (e *ExitError) Error() string { return e.Message }

// classifyStatus maps a non-2xx API response to an ExitError.
func classifyStatus(status int, body []byte) *ExitError {
	e := &ExitError{Status: status}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Code, e.Type = ExitAuth, "auth"
		e.Message = fmt.Sprintf("authentication failed (HTTP %d): check your API key (`peopleforce auth status`)", status)
	case status == http.StatusNotFound:
		e.Code, e.Type = ExitNotFound, "not_found"
		e.Message = fmt.Sprintf("resource not found (HTTP %d)", status)
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		e.Code, e.Type = ExitValidation, "validation"
		e.Message = fmt.Sprintf("the API rejected the request (HTTP %d)", status)
	case status == http.StatusTooManyRequests:
		e.Code, e.Type = ExitRateLimit, "rate_limit"
		e.Message = "rate limited (HTTP 429) and retries exhausted; wait and try again"
	case status >= 500:
		e.Code, e.Type = ExitServer, "server"
		e.Message = fmt.Sprintf("PeopleForce server error (HTTP %d)", status)
	default:
		e.Code, e.Type = ExitValidation, "api"
		e.Message = fmt.Sprintf("unexpected API response (HTTP %d)", status)
	}
	if json.Valid(body) && len(body) > 0 {
		e.Detail = json.RawMessage(body)
	}
	return e
}

func usageErr(format string, args ...any) *ExitError {
	return &ExitError{Code: ExitUsage, Type: "usage", Message: fmt.Sprintf(format, args...)}
}

// PrintError writes the error to stderr honoring the output mode: structured
// JSON for agents, a plain sentence for humans.
func PrintError(w io.Writer, err error, jsonMode bool) {
	ee, ok := err.(*ExitError)
	if !ok {
		ee = &ExitError{Code: ExitUsage, Type: "usage", Message: err.Error()}
	}
	if jsonMode {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]*ExitError{"error": ee})
		return
	}
	fmt.Fprintf(w, "error: %s\n", ee.Message)
}

// CodeFor extracts the exit code for main().
func CodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	if ee, ok := err.(*ExitError); ok {
		return ee.Code
	}
	return ExitUsage
}
