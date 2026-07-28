// Package command assembles the cobra tree from the compiled registry.
package command

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/itchyny/gojq"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/3bagels/peopleforce-cli/internal/config"
	"github.com/3bagels/peopleforce-cli/internal/httpx"
	"github.com/3bagels/peopleforce-cli/internal/output"
	"github.com/3bagels/peopleforce-cli/internal/registry"
)

// Version info, injected via -ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
)

// App holds global flag state shared by all commands.
type App struct {
	// Stdout carries data only; Stderr carries diagnostics. Tests override
	// them; production wiring uses os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
	// Stdin backs the "-" sentinel of --input and --api-key. It is handed out
	// through claimStdin, never read directly.
	Stdin io.Reader

	flagAPIKey  string
	flagAPIURL  string
	flagProfile string

	stdinOwner   string // flag that already consumed Stdin, "" while unclaimed
	stdinKey     string // --api-key - resolved from stdin
	stdinKeyDone bool

	outFormat  string
	jqExpr     string
	jqRaw      bool
	fields     []string
	yes        bool
	dryRun     bool
	verbose    bool
	maxRetries int
	timeout    time.Duration

	resolved *config.Resolved
}

// JSONErrors reports whether errors should be emitted as structured JSON
// (the default; table mode switches to human sentences).
func (a *App) JSONErrors() bool { return a.outFormat != "table" }

// validateOutputOptions rejects bad global flag values up front, before any
// request is issued.
func (a *App) validateOutputOptions() error {
	if !output.ValidFormat(a.outFormat) {
		return usageErr("unknown output format %q (want %s)", a.outFormat, output.FormatList())
	}
	if a.jqExpr != "" {
		if _, err := gojq.Parse(a.jqExpr); err != nil {
			return usageErr("invalid --jq expression: %v", err)
		}
	}
	// Both used to be coerced in silence: --timeout 0 (the natural way to ask
	// for "no limit") became 30s, and a negative --max-retries became one
	// attempt.
	if a.timeout <= 0 {
		return usageErr("--timeout must be positive, got %s", a.timeout)
	}
	if a.maxRetries < 0 {
		return usageErr("--max-retries cannot be negative, got %d", a.maxRetries)
	}
	return nil
}

func (a *App) outputOptions() output.Options {
	return output.Options{
		Format: a.outFormat,
		JQ:     a.jqExpr,
		Raw:    a.jqRaw,
		Fields: a.fields,
		Pretty: true,
		Warn:   a.Stderr,
	}
}

// profileName mirrors config.Resolve's profile precedence (flag > env >
// "default") for the paths that need the name without — or before — a
// successful resolution.
func (a *App) profileName() string {
	if a.flagProfile != "" {
		return a.flagProfile
	}
	if p := os.Getenv(config.EnvProfile); p != "" {
		return p
	}
	return "default"
}

// claimStdin hands stdin to exactly one consumer per run. Both `--input -`
// and `--api-key -` want the stream, and letting them split it would have the
// key swallow the request body — or the reverse, depending on which ran first.
func (a *App) claimStdin(who string) (io.Reader, error) {
	if a.stdinOwner != "" {
		return nil, usageErr("%s and %s both read stdin, which can only be consumed once", a.stdinOwner, who)
	}
	if a.Stdin == nil {
		return nil, usageErr("%s: no stdin is available", who)
	}
	a.stdinOwner = who
	return a.Stdin, nil
}

// apiKeyFlag returns the --api-key value, resolving the "-" sentinel from
// stdin. Resolution happens before config precedence is applied, so
// config.Resolve keeps seeing a literal key and still reports source "flag".
func (a *App) apiKeyFlag() (string, error) {
	if a.stdinKeyDone {
		return a.stdinKey, nil
	}
	if a.flagAPIKey != "-" {
		return a.flagAPIKey, nil
	}
	// Reading a terminal would hang with no prompt and no timeout, and adding a
	// prompt would break the non-interactive contract agents depend on. The key
	// has to be piped.
	if f, ok := a.Stdin.(*os.File); ok && isatty.IsTerminal(f.Fd()) {
		return "", usageErr("--api-key - expects the key on stdin, but stdin is a terminal; pipe it instead (e.g. `pass show pf | peopleforce ... --api-key -`)")
	}
	r, err := a.claimStdin("--api-key -")
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", usageErr("--api-key -: reading stdin: %v", err)
	}
	// Trim, so a key piped without a trailing newline, with one, or with CRLF
	// all yield the same token instead of an X-API-KEY header the API rejects.
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", usageErr("--api-key -: stdin is empty")
	}
	// Memoised separately from flagAPIKey: a piped key that trims to "-" would
	// otherwise look like the sentinel again and re-enter this branch.
	a.stdinKey, a.stdinKeyDone = key, true
	return key, nil
}

// resolveConfig resolves credentials once per process (flag > env > file).
func (a *App) resolveConfig() (*config.Resolved, error) {
	if a.resolved != nil {
		return a.resolved, nil
	}
	key, err := a.apiKeyFlag()
	if err != nil {
		return nil, err
	}
	r, err := config.Resolve(key, a.flagAPIURL, a.flagProfile)
	if err != nil {
		return nil, usageErr("loading config: %v", err)
	}
	if r.Warning != "" {
		fmt.Fprintf(a.Stderr, "warning: %s\n", r.Warning)
	}
	a.resolved = &r
	return a.resolved, nil
}

// client returns an authenticated API client, failing with the auth exit
// code when no key is configured anywhere.
func (a *App) client() (*httpx.Client, error) {
	r, err := a.resolveConfig()
	if err != nil {
		return nil, err
	}
	if r.APIKey == "" {
		// Name the profile when one was asked for: the key is often present in
		// the config file, just under a different profile than the requested one.
		scope, profileFlag := "", ""
		if r.Profile != "default" {
			scope = fmt.Sprintf(" for profile %q", r.Profile)
			profileFlag = " --profile " + r.Profile
		}
		return nil, &ExitError{Code: ExitAuth, Type: "auth",
			Message: fmt.Sprintf("no API key configured%s: set %s, pass --api-key (or --api-key - to read it from stdin), "+
				"or run `peopleforce auth login --api-key <key>%s`", scope, config.EnvAPIKey, profileFlag)}
	}
	a.warnInsecureURL(r.APIURL)
	c := &httpx.Client{
		BaseURL:    r.APIURL,
		APIKey:     r.APIKey,
		UserAgent:  "peopleforce-cli/" + Version,
		MaxRetries: a.maxRetries,
		Timeout:    a.timeout,
	}
	if a.verbose {
		c.Logf = func(format string, args ...any) {
			fmt.Fprintf(a.Stderr, format+"\n", args...)
		}
	}
	return c, nil
}

// previewClient builds the credential-free client that --dry-run uses to
// render the URL it would call. --dry-run must work with no credentials at
// all, so a config that cannot be resolved is not fatal here — but when it
// does resolve, --api-url / PEOPLEFORCE_API_URL / the config file still decide
// the host, or the preview would name an endpoint the real run never touches.
func (a *App) previewClient() *httpx.Client {
	c := &httpx.Client{}
	if r, err := a.resolveConfig(); err == nil {
		c.BaseURL = r.APIURL
		return c
	}
	// Config resolution failed (unparseable file, missing profile). The flag
	// and the environment do not depend on the config file, so they must still
	// decide the host — otherwise the preview claims the production API while
	// the real run would target somewhere else entirely.
	switch {
	case a.flagAPIURL != "":
		c.BaseURL = a.flagAPIURL
	default:
		c.BaseURL = os.Getenv(config.EnvAPIURL)
	}
	return c
}

// warnInsecureURL flags a plaintext API URL. A copy-pasted http:// base URL
// ships the API key in the clear on every request; that is a legitimate setup
// against a local proxy, so warn rather than refuse — but never in silence.
func (a *App) warnInsecureURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Scheme == "https" {
		return
	}
	if host := u.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return
	}
	fmt.Fprintf(a.Stderr, "warning: %s is not https — the API key is sent in cleartext on every request\n", raw)
}

// confirmDestructive enforces the guard on destructive operations: --yes,
// or an interactive confirmation when a human is at the terminal.
func (a *App) confirmDestructive(what string) error {
	if a.yes || a.dryRun {
		return nil
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return usageErr("%s is destructive and requires --yes in non-interactive mode", what)
	}
	fmt.Fprintf(a.Stderr, "About to run %s. Type 'yes' to confirm: ", what)
	var answer string
	fmt.Fscanln(os.Stdin, &answer)
	if strings.TrimSpace(answer) != "yes" {
		return usageErr("aborted by user")
	}
	return nil
}

const rootLong = `peopleforce is a command-line client for the PeopleForce HR API,
designed to be driven by both humans and AI agents.

Output contract:
  stdout carries data only — JSON by default, shaped {"data": ..., "meta": {...}}.
  stderr carries diagnostics; in JSON mode errors are {"error": {...}} objects.

Exit codes:
  0 success · 2 usage error · 3 auth failed · 4 not found ·
  5 validation rejected · 6 rate-limited · 7 server error · 8 network error

Environment:
  PEOPLEFORCE_API_KEY   API token (X-API-KEY); get one in PeopleForce settings
  PEOPLEFORCE_API_URL   override the API base URL
  PEOPLEFORCE_PROFILE   named profile from the config file

Discovery for agents:
  peopleforce commands --output json     entire command tree in one call
  peopleforce api ops                    all 203 API operations
  peopleforce api describe GET /employees
  peopleforce api call GET '/employees?page=2'   raw escape hatch`

// NewRoot builds the full command tree.
func NewRoot() (*cobra.Command, *App) {
	app := &App{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}

	root := &cobra.Command{
		Use:           "peopleforce",
		Short:         "CLI for the PeopleForce HR API",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Fail on bad --output/--jq BEFORE any request is sent — a flag typo
		// must never discard the response of an already-executed mutation.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := app.validateOutputOptions(); err != nil {
				return err
			}
			// Claim stdin for the key here, before any body reader can touch
			// it, so `--api-key - --input -` is always the same usage error
			// rather than whichever consumer happened to run first.
			_, err := app.apiKeyFlag()
			return err
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&app.flagAPIKey, "api-key", "", "API key, or - to read it from stdin (a key in argv is visible to ps and to CI logs; prefer "+config.EnvAPIKey+" or -)")
	pf.StringVar(&app.flagAPIURL, "api-url", "", "API base URL (default "+httpx.DefaultBaseURL+")")
	pf.StringVar(&app.flagProfile, "profile", "", "config profile name (default \"default\")")
	pf.StringVarP(&app.outFormat, "output", "o", "json", "output format: "+output.FormatList())
	pf.StringVar(&app.jqExpr, "jq", "", "filter the response with a jq expression (in-process, no jq needed)")
	pf.BoolVarP(&app.jqRaw, "raw", "r", false, "with --jq: print string results without JSON quotes (like jq -r)")
	pf.StringSliceVar(&app.fields, "fields", nil, "project only these fields from results")
	pf.BoolVar(&app.yes, "yes", false, "skip confirmation for destructive operations")
	pf.BoolVar(&app.dryRun, "dry-run", false, "print the request that would be sent, without sending it")
	pf.BoolVar(&app.verbose, "verbose", false, "log requests and retries to stderr")
	pf.IntVar(&app.maxRetries, "max-retries", 3, "max retries on 429/5xx (0 disables)")
	pf.DurationVar(&app.timeout, "timeout", 30*time.Second, "HTTP timeout")

	mountCurated(root, app)
	root.AddCommand(newAPICommand(app))
	root.AddCommand(newAuthCommand(app))
	root.AddCommand(newConfigCommand(app))
	root.AddCommand(newCommandsCommand(app, root))
	root.AddCommand(newSkillCommand(app))
	root.AddCommand(newAgentsMDCommand(app))
	root.AddCommand(newVersionCommand(app))

	return root, app
}

// groupShort provides one-line descriptions for curated command groups.
var groupShort = map[string]string{
	"employees":       "Manage employees (people records, salaries, documents, lifecycle)",
	"leave":           "Leave requests, adjustments, types and policies",
	"tasks":           "Tasks assigned to employees",
	"teams":           "Teams and team membership",
	"departments":     "Departments",
	"divisions":       "Divisions",
	"locations":       "Locations",
	"positions":       "Positions",
	"holidays":        "Company holidays",
	"calendars":       "Calendars",
	"termination":     "Termination types and reasons",
	"recruitment":     "Recruitment (candidates)",
	"candidates":      "Recruitment candidates",
	"employee-fields": "Employee custom field definitions (internal_name lookup)",
	"requests":        "Leave requests",
	"types":           "Reference types",
	"reasons":         "Reference reasons",
	"salaries":        "Employee salaries",
	"documents":       "Employee documents",
	"notes":           "Employee notes",
	"members":         "Team members",
	"adjustments":     "Leave adjustments",
	"policies":        "Leave policies",
}

// newGroup builds an intermediate command node. A group names no operation,
// so invoking one bare is a usage error (exit 2) with help on stderr — never
// a help-dump on stdout with exit 0, which would hand `peopleforce employees
// | jq .data` unparseable text and a success code.
func newGroup(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("unknown command %q for %q — run `%s --help`",
					args[0], cmd.CommandPath(), cmd.CommandPath())
			}
			cmd.SetOut(cmd.ErrOrStderr())
			_ = cmd.Help()
			return usageErr("%q needs a subcommand — see the list above", cmd.CommandPath())
		},
	}
}

// mountCurated registers every curated registry op under its command path.
func mountCurated(root *cobra.Command, app *App) {
	nodes := map[string]*cobra.Command{}

	ensureNode := func(pathParts []string) *cobra.Command {
		parent := root
		for i := range pathParts {
			key := strings.Join(pathParts[:i+1], " ")
			node, ok := nodes[key]
			if !ok {
				name := pathParts[i]
				short := groupShort[name]
				if short == "" {
					short = "Operations on " + name
				}
				node = newGroup(name, short)
				nodes[key] = node
				parent.AddCommand(node)
			}
			parent = node
		}
		return parent
	}

	for i := range registry.Ops {
		op := &registry.Ops[i]
		if op.Command == "" {
			continue
		}
		parts := strings.Split(op.Command, " ")
		parent := ensureNode(parts[:len(parts)-1])
		parent.AddCommand(newOpCommand(app, op, parts[len(parts)-1]))
	}

	// Synthetic commands that compose registry ops.
	if employees, ok := nodes["employees"]; ok {
		employees.AddCommand(newEmployeesBulkUpdateCommand(app))
	}
}

// truncationNote describes an incomplete --all result for the stderr error,
// naming the meta fields only when the chosen output format actually carries
// meta: ndjson emits bare records and table prints columns, so pointing an
// operator at meta.truncated there would send them looking for something that
// was never written.
func (a *App) truncationNote(items, nextPage int) string {
	// --jq replaces the envelope with whatever the expression selects, so meta
	// survives only by coincidence; ndjson and table never carry it.
	switch {
	case a.jqExpr != "":
		return fmt.Sprintf("the %d item(s) already fetched are on stdout, but --jq projected the envelope away, "+
			"so meta.truncated is not there — the exit code is the only completeness signal", items)
	case a.outFormat == "" || a.outFormat == "json":
		return fmt.Sprintf("the %d item(s) already fetched are on stdout as a truncated envelope "+
			"(meta.truncated=true, meta.next_page=%d)", items, nextPage)
	default:
		return fmt.Sprintf("the %d item(s) already fetched are on stdout, but --output %s carries no meta, "+
			"so the exit code is the only completeness signal", items, a.outFormat)
	}
}
