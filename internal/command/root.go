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

	flagAPIKey  string
	flagAPIURL  string
	flagProfile string

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
	switch a.outFormat {
	case "json", "table", "ndjson":
	default:
		return usageErr("unknown output format %q (want json, table, or ndjson)", a.outFormat)
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

// resolveConfig resolves credentials once per process (flag > env > file).
func (a *App) resolveConfig() (*config.Resolved, error) {
	if a.resolved != nil {
		return a.resolved, nil
	}
	r, err := config.Resolve(a.flagAPIKey, a.flagAPIURL, a.flagProfile)
	if err != nil {
		return nil, usageErr("loading config: %v", err)
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
		return nil, &ExitError{Code: ExitAuth, Type: "auth",
			Message: "no API key configured: set " + config.EnvAPIKey + ", pass --api-key, or run `peopleforce auth login --api-key <key>`"}
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
	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}

	root := &cobra.Command{
		Use:           "peopleforce",
		Short:         "CLI for the PeopleForce HR API",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Fail on bad --output/--jq BEFORE any request is sent — a flag typo
		// must never discard the response of an already-executed mutation.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return app.validateOutputOptions()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&app.flagAPIKey, "api-key", "", "API key (prefer "+config.EnvAPIKey+" to keep it out of shell history)")
	pf.StringVar(&app.flagAPIURL, "api-url", "", "API base URL (default "+httpx.DefaultBaseURL+")")
	pf.StringVar(&app.flagProfile, "profile", "", "config profile name (default \"default\")")
	pf.StringVarP(&app.outFormat, "output", "o", "json", "output format: json, table, or ndjson")
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
	"employees":   "Manage employees (people records, salaries, documents, lifecycle)",
	"leave":       "Leave requests, adjustments, types and policies",
	"tasks":       "Tasks assigned to employees",
	"teams":       "Teams and team membership",
	"departments": "Departments",
	"divisions":   "Divisions",
	"locations":   "Locations",
	"positions":   "Positions",
	"holidays":    "Company holidays",
	"calendars":   "Calendars",
	"termination": "Termination types and reasons",
	"recruitment": "Recruitment (candidates)",
	"candidates":  "Recruitment candidates",
	"employee-fields": "Employee custom field definitions (internal_name lookup)",
	"requests":    "Leave requests",
	"types":       "Reference types",
	"reasons":     "Reference reasons",
	"salaries":    "Employee salaries",
	"documents":   "Employee documents",
	"notes":       "Employee notes",
	"members":     "Team members",
	"adjustments": "Leave adjustments",
	"policies":    "Leave policies",
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
