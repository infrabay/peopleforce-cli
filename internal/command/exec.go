package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/3bagels/peopleforce-cli/internal/envelope"
	"github.com/3bagels/peopleforce-cli/internal/httpx"
	"github.com/3bagels/peopleforce-cli/internal/output"
	"github.com/3bagels/peopleforce-cli/internal/registry"
)

// newOpCommand builds the cobra command for one curated registry operation.
func newOpCommand(app *App, op *registry.Op, verb string) *cobra.Command {
	use := verb
	for _, p := range op.PathParams {
		use += " <" + strings.ReplaceAll(p, "_", "-") + ">"
	}

	long := op.Summary
	if op.Description != "" && op.Description != op.Summary {
		long += "\n\n" + op.Description
	}
	long += fmt.Sprintf("\n\nAPI: %s %s", op.Method, op.Path)
	if op.Destructive {
		long += "\nDestructive: requires --yes in non-interactive mode."
	}

	cmd := &cobra.Command{
		Use:     use,
		Short:   op.Summary,
		Long:    long,
		Example: exampleBlock(op.Examples),
		Args:    exactArgs(op, len(op.PathParams)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOp(app, op, cmd, args)
		},
	}

	registerQueryFlags(cmd, op)
	if op.BodyKind != registry.BodyNone {
		registerBodyFlags(cmd, op)
		cmd.Flags().String("input", "", "request body from @file, - (stdin), or inline JSON")
		cmd.Flags().StringArray("set", nil, "set a body field: key=value or key:=json (repeatable, dots nest)")
	}
	if op.Paginated {
		cmd.Flags().Bool("all", false, "fetch all pages (loops the page parameter)")
		cmd.Flags().Int("max-pages", 20, "safety cap for --all")
	}
	return cmd
}

func exampleBlock(examples []string) string {
	if len(examples) == 0 {
		return ""
	}
	return "  " + strings.Join(examples, "\n  ")
}

func exactArgs(op *registry.Op, n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			names := make([]string, n)
			for i, p := range op.PathParams {
				names[i] = "<" + strings.ReplaceAll(p, "_", "-") + ">"
			}
			return usageErr("expected %d positional argument(s): %s", n, strings.Join(names, " "))
		}
		return nil
	}
}

// runOp is the generic execution pipeline shared by all curated commands.
func runOp(app *App, op *registry.Op, cmd *cobra.Command, args []string) error {
	pathValues := map[string]string{}
	for i, name := range op.PathParams {
		// An empty value (e.g. an unset shell variable) would silently
		// target a different endpoint: /employees/{id} → /employees/.
		if strings.TrimSpace(args[i]) == "" {
			return usageErr("positional argument <%s> must not be empty", strings.ReplaceAll(name, "_", "-"))
		}
		pathValues[name] = args[i]
	}
	path, err := httpx.BuildPath(op.Path, pathValues)
	if err != nil {
		return usageErr("%v", err)
	}

	query, err := collectQuery(cmd, op)
	if err != nil {
		return err
	}

	all := false
	if op.Paginated {
		all, _ = cmd.Flags().GetBool("all")
		if all && cmd.Flags().Changed("page") {
			return usageErr("--all and --page are mutually exclusive")
		}
	}

	var bodyBytes []byte
	contentType := ""
	var jsonBody map[string]any

	switch op.BodyKind {
	case registry.BodyJSON:
		inputArg, _ := cmd.Flags().GetString("input")
		setArgs, _ := cmd.Flags().GetStringArray("set")
		jsonBody, err = buildJSONBody(cmd, op, inputArg, setArgs)
		if err != nil {
			return err
		}
		if err := checkRequired(cmd, op, jsonBody); err != nil {
			return err
		}
		bodyBytes, err = jsonBodyBytes(jsonBody)
		if err != nil {
			return err
		}
		contentType = "application/json"
	case registry.BodyMultipart:
		inputArg, _ := cmd.Flags().GetString("input")
		setArgs, _ := cmd.Flags().GetStringArray("set")
		fields, presence, err := buildMultipartFields(cmd, op, inputArg, setArgs)
		if err != nil {
			return err
		}
		if err := checkRequired(cmd, op, presence); err != nil {
			return err
		}
		if app.dryRun {
			// Show field names without reading files.
			return printDryRun(app, op.Method, path, query, dryRunMultipartBody(fields))
		}
		bodyBytes, contentType, err = httpx.EncodeMultipart(fields)
		if err != nil {
			return usageErr("%v", err)
		}
	default:
		if err := checkRequired(cmd, op, nil); err != nil {
			return err
		}
	}

	if op.Destructive {
		if err := app.confirmDestructive(fmt.Sprintf("%s %s", op.Method, path)); err != nil {
			return err
		}
	}
	if app.dryRun {
		var pretty any
		if jsonBody != nil {
			pretty = jsonBody
		}
		return printDryRun(app, op.Method, path, query, pretty)
	}

	client, err := app.client()
	if err != nil {
		return err
	}

	if all {
		maxPages, _ := cmd.Flags().GetInt("max-pages")
		return runAllPages(app, client, op, path, query, bodyBytes, contentType, maxPages)
	}

	req := httpx.Request{Method: op.Method, Path: path, Query: query, Body: bodyBytes, ContentType: contentType}
	resp, err := client.Do(context.Background(), req)
	if err != nil {
		return wrapTransport(err)
	}
	return renderResponse(app, resp)
}

// runAllPages loops the page parameter, concatenating data arrays into a
// single envelope. With pagination metadata it stops at metadata.pages;
// without it (some endpoints omit it) it keeps paging until an empty page —
// never silently returning just page 1 of a longer list.
func runAllPages(app *App, client *httpx.Client, op *registry.Op, path string, baseQuery []httpx.QueryPair, body []byte, contentType string, maxPages int) error {
	var allItems []json.RawMessage
	var lastMeta map[string]any
	var firstPage []byte
	// A page identical to page 1 is held here until the NEXT page proves it
	// was a coincidence (legit data) rather than a backend that ignores the
	// page param and replays page 1 forever. Two consecutive replays of
	// page 1 confirm the latter; anything else flushes the held page.
	var pendingDup []json.RawMessage
	page := 1
	warnedNoMeta := false

	for {
		query := append([]httpx.QueryPair{}, baseQuery...)
		query = append(query, httpx.QueryPair{Key: "page", Value: strconv.Itoa(page)})
		req := httpx.Request{Method: op.Method, Path: path, Query: query, Body: body, ContentType: contentType}
		resp, err := client.Do(context.Background(), req)
		if err != nil {
			return wrapTransport(err)
		}
		if resp.Status < 200 || resp.Status > 299 {
			return classifyStatus(resp.Status, resp.Body)
		}
		n := envelope.Normalize(resp.Body, resp.Status)

		var items []json.RawMessage
		if err := json.Unmarshal(n.Data, &items); err != nil {
			// Not a list — --all degrades to a single fetch.
			return renderNormalized(app, n)
		}
		fmt.Fprintf(app.Stderr, "page %d: %d items\n", page, len(items))

		current, pages, ok := n.Page()
		if !ok {
			if !warnedNoMeta {
				fmt.Fprintln(app.Stderr, "note: response carries no pagination metadata; paging until an empty page")
				warnedNoMeta = true
			}
			if page == 1 {
				firstPage = append(firstPage[:0], n.Data...)
			} else if len(items) > 0 && bytes.Equal(firstPage, n.Data) {
				if pendingDup != nil {
					fmt.Fprintln(app.Stderr, "warning: consecutive pages replay page 1 — the endpoint seems to ignore the page parameter; keeping page 1 only")
					pendingDup = nil
					lastMeta = n.Meta
					break
				}
				pendingDup = items
				lastMeta = n.Meta
				if page >= maxPages {
					fmt.Fprintf(app.Stderr, "stopped at --max-pages %d\n", maxPages)
					break
				}
				page++
				continue
			}
		}

		if pendingDup != nil {
			allItems = append(allItems, pendingDup...) // coincidence, keep it
			pendingDup = nil
		}
		allItems = append(allItems, items...)
		lastMeta = n.Meta
		if len(items) == 0 || (ok && current >= pages) {
			break
		}
		if page >= maxPages {
			if ok {
				fmt.Fprintf(app.Stderr, "stopped at --max-pages %d (of %d total pages)\n", maxPages, pages)
			} else {
				fmt.Fprintf(app.Stderr, "stopped at --max-pages %d\n", maxPages)
			}
			break
		}
		page++
	}
	if pendingDup != nil {
		// Loop ended (empty page / --max-pages) before the hold could be
		// confirmed either way — it was real data, keep it.
		allItems = append(allItems, pendingDup...)
	}

	if allItems == nil {
		allItems = []json.RawMessage{} // keep the contract: lists are never null
	}
	data, err := json.Marshal(allItems)
	if err != nil {
		return err
	}
	meta := map[string]any{}
	for k, v := range lastMeta {
		meta[k] = v
	}
	meta["fetched"] = len(allItems)
	delete(meta, "page")
	return renderNormalized(app, envelope.Normalized{Data: data, Meta: meta})
}

func renderResponse(app *App, resp *httpx.Response) error {
	if resp.Status < 200 || resp.Status > 299 {
		return classifyStatus(resp.Status, resp.Body)
	}
	n := envelope.Normalize(resp.Body, resp.Status)
	return renderNormalized(app, n)
}

func renderNormalized(app *App, n envelope.Normalized) error {
	if err := output.Render(app.Stdout, n, app.outputOptions()); err != nil {
		return usageErr("%v", err)
	}
	if n.HasBulkErrors() {
		return &ExitError{Code: ExitValidation, Type: "validation",
			Message: "bulk operation completed with errors (see meta.errors)"}
	}
	return nil
}

// printDryRun emits the exact request that would be sent. The API key is
// never included.
func printDryRun(app *App, method, path string, query []httpx.QueryPair, body any) error {
	// --dry-run must work without credentials but still honor --api-url /
	// PEOPLEFORCE_API_URL / config, so build the preview from resolved config.
	client := &httpx.Client{}
	if r, err := app.resolveConfig(); err == nil {
		client.BaseURL = r.APIURL
	}
	preview := map[string]any{
		"dry_run": true,
		"method":  method,
		"url":     client.URL(httpx.Request{Method: method, Path: path, Query: query}),
	}
	if body != nil {
		preview["body"] = body
	}
	enc := json.NewEncoder(app.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(preview)
}

func dryRunMultipartBody(fields []httpx.FieldValue) map[string]any {
	m := map[string]any{}
	for _, f := range fields {
		if f.IsFile {
			m[f.Name] = "@" + f.Value
			continue
		}
		m[f.Name] = f.Value
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func wrapTransport(err error) error {
	if _, ok := err.(*httpx.TransportError); ok {
		return &ExitError{Code: ExitNetwork, Type: "network", Message: err.Error()}
	}
	if ee, ok := err.(*ExitError); ok {
		return ee
	}
	return &ExitError{Code: ExitNetwork, Type: "network", Message: err.Error()}
}
