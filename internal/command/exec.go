package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		Args:    exactArgs(op),
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

func exactArgs(op *registry.Op) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		n := len(op.PathParams)
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
	startPage := 1
	if op.Paginated {
		all, _ = cmd.Flags().GetBool("all")
		// --page alongside --all is the resume point, not a conflict: when a
		// long --all run dies partway the operator needs a way to continue
		// without refetching everything.
		if all && cmd.Flags().Changed("page") {
			// Registered as Int64 by registerQueryFlags, so read it as one.
			p, err := cmd.Flags().GetInt64("page")
			if err != nil {
				return usageErr("--page: %v", err)
			}
			if p < 1 {
				return usageErr("--page must be 1 or greater, got %d", p)
			}
			startPage = int(p)
		}
	}
	if all {
		// runAllPages supplies its own page param each iteration. Leaving the
		// operator's --page in the base set would put two page keys in the URL,
		// and the backend reads the first — pinning every request to the start
		// page and looping on it forever.
		query = dropQueryKey(query, "page")
	}

	var bodyBytes []byte
	contentType := ""
	var jsonBody map[string]any

	switch op.BodyKind {
	case registry.BodyJSON:
		inputArg, _ := cmd.Flags().GetString("input")
		setArgs, _ := cmd.Flags().GetStringArray("set")
		jsonBody, err = buildJSONBody(app, cmd, op, inputArg, setArgs)
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
		fields, presence, err := buildMultipartFields(app, cmd, op, inputArg, setArgs)
		if err != nil {
			return err
		}
		if err := checkRequired(cmd, op, presence); err != nil {
			return err
		}
		if app.dryRun {
			// Don't read the files, but do confirm they exist and are
			// readable: --dry-run is the documented pre-flight check for a
			// mutation, and reporting a request that cannot actually be sent
			// defeats it.
			if err := checkUploadsReadable(fields); err != nil {
				return err
			}
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
		// Mirror jsonBodyBytes: an empty map is sent as no body at all, so
		// previewing "body": {} would not be the request that goes out.
		if len(jsonBody) > 0 {
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
		return runAllPages(app, client, op, path, query, bodyBytes, contentType, maxPages, startPage)
	}

	req := httpx.Request{Method: op.Method, Path: path, Query: query, Body: bodyBytes, ContentType: contentType}
	resp, err := client.Do(context.Background(), req)
	if err != nil {
		return wrapTransport(err)
	}
	return renderResponse(app, resp)
}

// heldDupDropped is reported whenever a held replay page is discarded because
// no further page ever arrived to vindicate it.
const heldDupDropped = "warning: the last page fetched was byte-identical to the first and no further page was available to tell a replaying backend from real duplicate data; it was dropped"

// runAllPages loops the page parameter, concatenating data arrays into a
// single envelope. With pagination metadata it stops at metadata.pages;
// without it (some endpoints omit it) it keeps paging until an empty page —
// never silently returning just page 1 of a longer list.
func runAllPages(app *App, client *httpx.Client, op *registry.Op, path string, baseQuery []httpx.QueryPair, body []byte, contentType string, maxPages, startPage int) error {
	var allItems []json.RawMessage
	var lastMeta map[string]any
	var firstPage []byte
	// A page identical to the run's first page is held here until the NEXT
	// page proves it was a coincidence (legit data) rather than a backend that
	// ignores the page param and replays one page forever. Two consecutive
	// replays confirm the latter; anything else flushes the held page.
	var pendingDup []json.RawMessage
	page := startPage
	pagesFetched := 0
	warnedNoMeta := false
	// resumeFrom is the first page NOT represented in the emitted data; 0 once
	// the run has covered everything the endpoint has.
	resumeFrom := 0

	// emit renders the pages collected so far. nextPage is 0 for a run that
	// completed and the page that could not be fetched for one that died.
	emit := func(nextPage int) error {
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
		delete(meta, "page")
		meta["fetched"] = len(allItems)
		if nextPage > 0 {
			meta["truncated"] = true
			meta["next_page"] = nextPage
		}
		return renderNormalized(app, envelope.Normalized{Data: data, Meta: meta})
	}

	// A run that dies on page N still hands over the N-1 pages already
	// transferred, marked truncated, alongside the non-zero exit code: the
	// resume advice is only honest if what it resumes from is on stdout.
	truncate := func(err error) error {
		var ee *ExitError
		if !errors.As(err, &ee) || pagesFetched == 0 {
			return err // nothing was collected, so there is nothing to hand over
		}
		next := page
		if pendingDup != nil {
			// The held page was never added to allItems, so resuming past it
			// would lose it: point the caller back at that page, not this one.
			fmt.Fprintln(app.Stderr, heldDupDropped)
			pendingDup = nil
			next = page - 1
		}
		// ExitValidation is emit reporting errors carried inside the payload,
		// not a write failure; anything else means the partial data never
		// reached stdout, and resuming from page N would then lose 1..N-1.
		if emitErr := emit(next); emitErr != nil && CodeFor(emitErr) != ExitValidation {
			fmt.Fprintf(app.Stderr, "warning: the %d page(s) already fetched could not be written to stdout: %v\n", pagesFetched, emitErr)
			return ee
		}
		resume := fmt.Sprintf("re-run with --all --page %d", next)
		if op.Command != "" {
			resume = fmt.Sprintf("re-run with `peopleforce %s --all --page %d`", op.Command, next)
		}
		ee.Message = fmt.Sprintf("%s (%s; %s to fetch the rest)",
			ee.Message, app.truncationNote(len(allItems), next), resume)
		return ee
	}

	for {
		query := append([]httpx.QueryPair{}, baseQuery...)
		query = append(query, httpx.QueryPair{Key: "page", Value: strconv.Itoa(page)})
		req := httpx.Request{Method: op.Method, Path: path, Query: query, Body: body, ContentType: contentType}
		resp, err := client.Do(context.Background(), req)
		if err != nil {
			return truncate(wrapTransport(err))
		}
		if resp.Status < 200 || resp.Status > 299 {
			return truncate(classifyStatus(resp.Status, resp.Body))
		}
		n := envelope.Normalize(resp.Body, resp.Status)

		var items []json.RawMessage
		if err := json.Unmarshal(n.Data, &items); err != nil {
			if pagesFetched == 0 {
				// Not a list at all — --all degrades to a single fetch.
				return renderNormalized(app, n)
			}
			// Mid-run: the endpoint stopped returning arrays. Handing back only
			// this page would silently drop every page already collected, so
			// keep them and mark the result incomplete.
			fmt.Fprintf(app.Stderr, "warning: page %d is not a list; stopping and keeping the %d page(s) already fetched\n", page, pagesFetched)
			resumeFrom = page
			break
		}
		pagesFetched++
		fmt.Fprintf(app.Stderr, "page %d: %d items\n", page, len(items))

		current, pages, ok := n.Page()
		if !ok && !warnedNoMeta {
			fmt.Fprintln(app.Stderr, "note: response carries no pagination metadata; paging until an empty page")
			warnedNoMeta = true
		}

		// Replay detection runs whether or not pagination metadata is present:
		// a backend can report page/pages correctly and still ignore ?page=,
		// in which case trusting metadata alone concatenates page 1 N times.
		if page == startPage {
			firstPage = append(firstPage[:0], n.Data...)
		} else if len(items) > 0 && bytes.Equal(firstPage, n.Data) {
			if pendingDup != nil {
				fmt.Fprintln(app.Stderr, "warning: consecutive pages replay the first page — the endpoint seems to ignore the page parameter; keeping that page only")
				pendingDup = nil
				lastMeta = n.Meta
				break
			}
			pendingDup = items
			lastMeta = n.Meta
			// Nothing further can settle whether this page is a replay or real
			// duplicate data, and byte-identical pages are far more often a
			// backend ignoring ?page= than genuinely repeated records — so drop
			// it rather than silently double-count.
			if pagesFetched >= maxPages {
				fmt.Fprintf(app.Stderr, "stopped at --max-pages %d\n", maxPages)
				fmt.Fprintln(app.Stderr, heldDupDropped+" — raise --max-pages to resolve")
				pendingDup = nil
				resumeFrom = page // the dropped page is not in the data
				break
			}
			if ok && current >= pages {
				fmt.Fprintln(app.Stderr, heldDupDropped)
				pendingDup = nil
				resumeFrom = page // the dropped page is not in the data
				break
			}
			page++
			continue
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
		if pagesFetched >= maxPages {
			if ok {
				fmt.Fprintf(app.Stderr, "stopped at --max-pages %d (of %d total pages)\n", maxPages, pages)
			} else {
				fmt.Fprintf(app.Stderr, "stopped at --max-pages %d\n", maxPages)
			}
			resumeFrom = page + 1
			break
		}
		page++
	}

	return emit(resumeFrom)
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
		// Not ExitUsage: the request was issued and may have mutated state,
		// so reporting "bad flags/args" would tell an agent it is safe to
		// re-run — and re-running duplicates the created resource.
		return &ExitError{Code: ExitOutput, Type: "output",
			Message: fmt.Sprintf("the request succeeded but the response could not be rendered: %v", err)}
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
	client := app.previewClient()
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
	// errors.As so a wrapped error keeps its documented exit code.
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee
	}
	return &ExitError{Code: ExitNetwork, Type: "network", Message: err.Error()}
}

// dropQueryKey removes every pair with the given key, preserving order.
func dropQueryKey(pairs []httpx.QueryPair, key string) []httpx.QueryPair {
	out := pairs[:0:0]
	for _, p := range pairs {
		if p.Key != key {
			out = append(out, p)
		}
	}
	return out
}

// checkUploadsReadable verifies every file part can actually be opened,
// without reading its contents. Used by --dry-run so a missing or unreadable
// upload path fails the preview instead of the real request.
func checkUploadsReadable(fields []httpx.FieldValue) error {
	for _, f := range fields {
		if !f.IsFile {
			continue
		}
		st, err := os.Stat(f.Value)
		if err != nil {
			return usageErr("%s: %v", f.Name, err)
		}
		if st.IsDir() {
			return usageErr("%s: %s is a directory", f.Name, f.Value)
		}
		fh, err := os.Open(f.Value)
		if err != nil {
			return usageErr("%s: %v", f.Name, err)
		}
		fh.Close()
	}
	return nil
}
