package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/infrabay/peopleforce-cli/internal/envelope"
	"github.com/infrabay/peopleforce-cli/internal/httpx"
	"github.com/infrabay/peopleforce-cli/internal/output"
	"github.com/infrabay/peopleforce-cli/internal/registry"
)

// newAPICommand is the escape hatch: every spec operation is reachable here
// even without a curated command.
func newAPICommand(app *App) *cobra.Command {
	api := newGroup("api", "Low-level API access: list, describe, and call any operation")
	api.Long = `Direct access to all ` + fmt.Sprint(registry.Info.OpCount) + ` PeopleForce API operations,
including those without a curated command.`
	api.AddCommand(newAPIOpsCommand(app))
	api.AddCommand(newAPIDescribeCommand(app))
	api.AddCommand(newAPICallCommand(app))
	return api
}

type opSummary struct {
	Command     string `json:"command,omitempty"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Summary     string `json:"summary"`
	Paginated   bool   `json:"paginated,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

func newAPIOpsCommand(app *App) *cobra.Command {
	var method string
	var pathFilter string
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "List every API operation (method, path, curated command)",
		Example: `  peopleforce api ops
  peopleforce api ops --method POST --path-contains recruitment`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var out []opSummary
			for i := range registry.Ops {
				op := &registry.Ops[i]
				if method != "" && !strings.EqualFold(method, op.Method) {
					continue
				}
				if pathFilter != "" && !strings.Contains(op.Path, pathFilter) {
					continue
				}
				out = append(out, opSummary{
					Command: op.Command, Method: op.Method, Path: op.Path,
					Summary: op.Summary, Paginated: op.Paginated, Destructive: op.Destructive,
				})
			}
			data, err := json.Marshal(out)
			if err != nil {
				return err
			}
			n := envelope.Normalized{Data: data, Meta: map[string]any{"count": len(out)}}
			return output.Render(app.Stdout, n, app.outputOptions())
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "filter by HTTP method")
	cmd.Flags().StringVar(&pathFilter, "path-contains", "", "filter by path substring")
	return cmd
}

func newAPIDescribeCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "describe <METHOD> <PATH>",
		Short: "Show the full definition of one operation (params, body fields, envelope)",
		Example: `  peopleforce api describe GET /employees
  peopleforce api describe POST /leave_requests`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			op, ok := registry.Find(method, args[1])
			if !ok {
				return usageErr("no operation %s %s — run `peopleforce api ops` to list all; note some upstream paths contain typos (e.g. /termintation_reasons)", method, args[1])
			}
			data, err := json.Marshal(op)
			if err != nil {
				return err
			}
			n := envelope.Normalized{Data: data, Meta: map[string]any{}}
			return output.Render(app.Stdout, n, app.outputOptions())
		},
	}
}

func newAPICallCommand(app *App) *cobra.Command {
	var inputArg string
	var setArgs []string
	cmd := &cobra.Command{
		Use:   "call <METHOD> <PATH>",
		Short: "Call any endpoint directly (authenticated, response normalized)",
		Long: `Raw escape hatch: call any API path with any method. The path may include
a query string. Bracket keys (ids[]=1&ids[]=2) pass through verbatim; bytes
that are illegal in a URL (spaces, non-ASCII, stray %) are percent-encoded
automatically. The response is normalized into the standard
{"data": ..., "meta": ...} envelope.

Bodies are JSON only — the few multipart endpoints (candidate/document
uploads) have curated commands instead, see ` + "`peopleforce api ops`" + `.

DELETE calls, and the operations the registry marks destructive (such as
employees terminate), require --yes in non-interactive mode. A path with a
"." or ".." segment, also percent-encoded, is rejected (exit 2): proxies and
routers resolve dot segments, so the request would not go where it says.`,
		Example: `  peopleforce api call GET '/employees?page=2'
  peopleforce api call GET /job_levels
  peopleforce api call POST /working_patterns --set name="4-day week"
  peopleforce api call DELETE /departments/9 --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			path = httpx.SanitizeRequestTarget(path)
			if err := rejectDotSegments(path); err != nil {
				return err
			}

			var bodyBytes []byte
			contentType := ""
			var body map[string]any
			if inputArg != "" || len(setArgs) > 0 {
				body = map[string]any{}
				if inputArg != "" {
					raw, err := readInput(app, inputArg)
					if err != nil {
						return err
					}
					// Same decoder as the curated commands: plain Unmarshal
					// routes every number through float64, silently rewriting
					// large IDs and exact decimals on a write path.
					if body, err = unmarshalObject(raw); err != nil {
						return usageErr("--input is not a JSON object: %v", err)
					}
				}
				for _, s := range setArgs {
					if err := applySet(body, s); err != nil {
						return err
					}
				}
				var err error
				bodyBytes, err = jsonBodyBytes(body)
				if err != nil {
					return err
				}
				// An empty object is sent as no body at all (see
				// jsonBodyBytes), so it carries no Content-Type either, and
				// the dry-run preview must not show "body": {} for it.
				if len(bodyBytes) > 0 {
					contentType = "application/json"
				} else {
					body = nil
				}
			}

			// The registry already knows which endpoints are destructive
			// (every DELETE, plus overrides like employees terminate), and
			// the docs promise the guard applies to those too — so consult it
			// rather than re-deriving the policy from the method alone.
			destructive := method == "DELETE"
			if op, ok := registry.Match(method, path); ok && op.Destructive {
				destructive = true
			}
			if destructive {
				if err := app.confirmDestructive(method + " " + path); err != nil {
					return err
				}
			}
			if app.dryRun {
				var pretty any
				if body != nil { // nil unless a body is actually sent, see above
					pretty = body
				}
				return printDryRun(app, method, path, nil, pretty)
			}

			client, err := app.client()
			if err != nil {
				return err
			}
			resp, err := client.Do(context.Background(), httpx.Request{
				Method: method, Path: path, Body: bodyBytes, ContentType: contentType,
			})
			if err != nil {
				return wrapTransport(err)
			}
			return renderResponse(app, method, resp)
		},
	}
	cmd.Flags().StringVar(&inputArg, "input", "", "request body from @file, - (stdin), or inline JSON")
	cmd.Flags().StringArrayVar(&setArgs, "set", nil, "set a body field: key=value or key:=json (repeatable)")
	return cmd
}

// rejectDotSegments refuses a request target whose path has a "." or ".."
// segment, either literal or after one percent-decode (%2e%2e).
//
// The path is matched against the registry (to decide whether --yes is
// required) in cleaned form, but it is sent verbatim after the base path
// (/api/public/v3). `/../v3/employees/1/terminate` therefore matched no
// operation and skipped the destructive guard, while a proxy or router
// resolving dot segments turned the sent target into the terminate endpoint.
// The matched path and the sent path must be the same string, so instead of
// cleaning the path here we refuse it, exactly as httpx.BuildPath does for
// the path values of curated commands. registry.Match still canonicalises as
// defence in depth.
func rejectDotSegments(target string) error {
	p, _, _ := strings.Cut(target, "?")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return usageErr("path %q contains a %q segment; dot segments are resolved by proxies and routers, so the request would not go where the path says", target, seg)
		}
	}
	return nil
}
