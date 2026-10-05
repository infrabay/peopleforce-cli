// Package httpx is the wire layer: it builds requests exactly as the
// PeopleForce backend expects them (verbatim bracket query keys, typo'd
// paths preserved, DELETE with body, multipart) and retries rate limits.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the public API server from the OpenAPI spec.
const DefaultBaseURL = "https://app.peopleforce.io/api/public/v3"

// authHeader carries the API key. It is not one of the headers net/http
// strips on a cross-origin redirect, so dropCredentialsCrossHost does it.
const authHeader = "X-API-KEY"

// maxResponseBytes caps a single response body held in memory.
const maxResponseBytes int64 = 256 << 20

// Client executes API requests.
type Client struct {
	BaseURL    string
	APIKey     string
	UserAgent  string
	MaxRetries int                              // bounded retries on 429/502/503/504
	HTTP       *http.Client                     // if nil, a client with Timeout is used
	Timeout    time.Duration                    // used when HTTP is nil (default 30s)
	Logf       func(format string, args ...any) // optional stderr logging (--verbose, retry notices)

	httpOnce   sync.Once
	httpCached *http.Client
}

// Request is a fully specified API call. Path must already have path params
// substituted (values path-escaped by the caller via BuildPath).
type Request struct {
	Method      string
	Path        string // e.g. "/employees/123" or "/termintation_reasons/5" — sent verbatim
	Query       []QueryPair
	Body        []byte
	ContentType string // required when Body != nil
}

// Response is the raw API answer; envelope normalization happens upstream.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// BuildPath substitutes {name} placeholders in a path template with
// path-escaped values, in template order.
func BuildPath(template string, values map[string]string) (string, error) {
	out := template
	for name, v := range values {
		placeholder := "{" + name + "}"
		if !strings.Contains(out, placeholder) {
			return "", fmt.Errorf("path template %q has no parameter %q", template, name)
		}
		// PathEscape leaves dots alone, and "." or ".." is resolved as a dot
		// segment by any proxy or router on the way: `teams members remove
		// 3 ..` would DELETE /teams/3, the whole team, not one membership.
		if v == "." || v == ".." {
			return "", fmt.Errorf("%q is not a valid value for {%s}", v, name)
		}
		out = strings.ReplaceAll(out, placeholder, url.PathEscape(v))
	}
	if i := strings.IndexByte(out, '{'); i >= 0 {
		return "", fmt.Errorf("path %q still has unresolved parameters", out)
	}
	return out, nil
}

// URL renders the full request URL for req without sending it (used by --dry-run).
func (c *Client) URL(req Request) string {
	base := strings.TrimSuffix(c.baseURL(), "/")
	u := base + req.Path
	if q := EncodeQuery(req.Query); q != "" {
		u += "?" + q
	}
	return u
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

// httpClient is cached so sequential calls (pagination loops, bulk updates)
// reuse one transport and its keep-alive connections.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	c.httpOnce.Do(func() {
		timeout := c.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		c.httpCached = &http.Client{Timeout: timeout, CheckRedirect: dropCredentialsCrossHost}
	})
	return c.httpCached
}

// dropCredentialsCrossHost strips the API key when a redirect leaves the
// origin the request was aimed at. net/http does this automatically for
// Authorization and Cookie, but the PeopleForce credential travels in a
// custom header, so an open redirect on the API host — or any on-path
// attacker when the base URL is http:// — would otherwise hand a full-scope
// HR token to whatever host the redirect names.
//
// The key is not the only thing worth stealing: a 307/308 keeps the method
// and resends the body, which for an employee create or a document upload is
// the HR record itself. Only GET and HEAD carry no body, so any other method
// stops at a cross-origin redirect instead of following it.
func dropCredentialsCrossHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return fmt.Errorf("refusing to follow a redirect to another origin (%s://%s) for %s: it would resend the request body there",
				req.URL.Scheme, req.URL.Host, req.Method)
		}
		req.Header.Del(authHeader)
	}
	return nil
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Do executes the request with bounded retries on 429 and transient 5xx.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	rawURL := c.URL(req)
	attempts := c.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastResp *Response
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if asked, ok := serverDelay(lastResp); ok && asked > maxRetryAfter {
				// Waiting less would just earn another 429; waiting this long
				// would hang the CLI. Hand the response back (exit 6 for a 429)
				// and say what the server asked for.
				c.logf("not retrying: the server asked to wait %s (Retry-After: %s), longer than the %s limit",
					formatAsked(asked), lastResp.Header.Get("Retry-After"), maxRetryAfter)
				return lastResp, nil
			}
			delay := retryDelay(lastResp, attempt)
			c.logf("retrying in %s (attempt %d/%d, HTTP %d)", delay, attempt+1, attempts, lastResp.Status)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		var bodyReader io.Reader
		if req.Body != nil {
			bodyReader = bytes.NewReader(req.Body)
		}
		httpReq, err := http.NewRequestWithContext(ctx, req.Method, rawURL, bodyReader)
		if err != nil {
			return nil, err
		}
		// net/http keeps RawQuery byte-for-byte, so bracket keys survive.
		httpReq.Header.Set(authHeader, c.APIKey)
		httpReq.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			httpReq.Header.Set("User-Agent", c.UserAgent)
		}
		if req.Body != nil {
			httpReq.Header.Set("Content-Type", req.ContentType)
		}

		c.logf("%s %s", req.Method, rawURL)
		httpResp, err := c.httpClient().Do(httpReq)
		if err != nil {
			return nil, &TransportError{Err: err}
		}
		// Bounded: an unbounded ReadAll lets a hostile or misconfigured
		// endpoint stream until the CLI is OOM-killed. No PeopleForce list
		// page comes close to this.
		respBody, readErr := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
		httpResp.Body.Close()
		// The status line is already in hand from here on: the server did
		// answer, so a failed or oversized body is not a plain network error.
		if readErr != nil {
			return nil, &ResponseReadError{Status: httpResp.StatusCode, Method: req.Method, Err: readErr}
		}
		if int64(len(respBody)) > maxResponseBytes {
			return nil, &ResponseReadError{Status: httpResp.StatusCode, Method: req.Method,
				Err: fmt.Errorf("response exceeds the %d MiB limit", maxResponseBytes>>20)}
		}

		lastResp = &Response{Status: httpResp.StatusCode, Header: httpResp.Header, Body: respBody}
		if !isRetryable(req.Method, httpResp.StatusCode) {
			if httpResp.StatusCode >= 500 && req.Method == http.MethodPost {
				c.logf("not retrying POST on HTTP %d: the backend may have committed the write", httpResp.StatusCode)
			}
			return lastResp, nil
		}
	}
	// Retries exhausted; hand the last response back so the caller can
	// classify it (429 → rate-limit exit code).
	return lastResp, nil
}

// TransportError wraps network-level failures (DNS, TLS, timeouts) so the
// CLI can map them to a dedicated exit code.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// ResponseReadError reports that the server sent a status line but the body
// could not be read in full (connection reset, truncated body, over the size
// limit). It is distinct from TransportError because the request reached the
// server and may already have taken effect: after a 2xx to a mutation the
// caller must not tell the user to simply retry.
type ResponseReadError struct {
	Status int
	Method string
	Err    error
}

func (e *ResponseReadError) Error() string { return e.Err.Error() }
func (e *ResponseReadError) Unwrap() error { return e.Err }

// formatAsked renders a Retry-After that may be absurdly large without
// printing a meaningless multi-century Duration.
func formatAsked(d time.Duration) string {
	if d >= hugeRetryAfter {
		return "an unreasonably long time"
	}
	return d.String()
}
