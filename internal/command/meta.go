package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/3bagels/peopleforce-cli/internal/agentdocs"
	"github.com/3bagels/peopleforce-cli/internal/config"
	"github.com/3bagels/peopleforce-cli/internal/envelope"
	"github.com/3bagels/peopleforce-cli/internal/httpx"
	"github.com/3bagels/peopleforce-cli/internal/output"
	"github.com/3bagels/peopleforce-cli/internal/registry"
)

// renderValue emits a meta-command payload through the same output pipeline
// as API responses, so --jq/--fields/--output and the {"data": ...} contract
// apply uniformly.
func renderValue(app *App, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return output.Render(app.Stdout, envelope.Normalized{Data: data}, app.outputOptions())
}

func newAuthCommand(app *App) *cobra.Command {
	auth := newGroup("auth", "Configure and verify API credentials")

	login := &cobra.Command{
		Use:   "login --api-key <key>",
		Short: "Save an API key to the config file",
		Long: `Saves the key into the selected profile of the config file
(` + "$XDG_CONFIG_HOME/peopleforce/config.toml" + `). Non-interactive by design:
the key is passed via the global --api-key flag.`,
		Example: `  peopleforce auth login --api-key "$KEY"
  peopleforce auth login --api-key "$KEY" --profile staging`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.flagAPIKey == "" {
				return usageErr("--api-key is required")
			}
			cfg, err := config.Load()
			if err != nil {
				return usageErr("%v", err)
			}
			if cfg.Profiles == nil {
				cfg.Profiles = map[string]config.Profile{}
			}
			// Same precedence as config.Resolve: flag > env > default —
			// the key must land in the profile that resolution will read.
			profile := app.flagProfile
			if profile == "" {
				profile = os.Getenv(config.EnvProfile)
			}
			if profile == "" {
				profile = "default"
			}
			p := cfg.Profiles[profile]
			p.APIKey = app.flagAPIKey
			if app.flagAPIURL != "" {
				p.APIURL = app.flagAPIURL
			}
			cfg.Profiles[profile] = p
			if err := config.Save(cfg); err != nil {
				return usageErr("saving config: %v", err)
			}
			path, _ := config.Path()
			fmt.Fprintf(app.Stderr, "saved profile %q to %s\n", profile, path)
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Report where credentials come from and whether they work",
		Long: `Prints the resolved credential source (flag > env > config file) and
probes the API with a cheap request. Exit code 0 when authenticated,
3 when the key is missing or rejected — agents use this to self-diagnose.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := app.resolveConfig()
			if err != nil {
				return err
			}
			result := map[string]any{
				"profile":        r.Profile,
				"api_key_source": r.APIKeySource,
				"api_url":        httpx.DefaultBaseURL,
				"api_url_source": r.APIURLSource,
			}
			if r.APIURL != "" {
				result["api_url"] = r.APIURL
			}

			var probeErr *ExitError
			if r.APIKey == "" {
				result["authenticated"] = false
				probeErr = &ExitError{Code: ExitAuth, Type: "auth",
					Message: "no API key configured: set " + config.EnvAPIKey + " or run `peopleforce auth login`"}
			} else {
				client, err := app.client()
				if err != nil {
					return err
				}
				resp, err := client.Do(context.Background(), httpx.Request{Method: "GET", Path: "/calendars"})
				if err != nil {
					return wrapTransport(err)
				}
				result["probe_status"] = resp.Status
				result["authenticated"] = resp.Status >= 200 && resp.Status < 300
				switch {
				case resp.Status == 401 || resp.Status == 403:
					probeErr = &ExitError{Code: ExitAuth, Type: "auth", Status: resp.Status,
						Message: fmt.Sprintf("API key rejected (HTTP %d), source: %s", resp.Status, r.APIKeySource)}
				case resp.Status < 200 || resp.Status > 299:
					// Probe failed for a non-auth reason (5xx, network path
					// issue) — exit non-zero so agents don't read "ok".
					probeErr = classifyStatus(resp.Status, resp.Body)
				}
			}

			if err := renderValue(app, result); err != nil {
				return err
			}
			if probeErr != nil {
				return probeErr
			}
			return nil
		},
	}

	auth.AddCommand(login, status)
	return auth
}

func newConfigCommand(app *App) *cobra.Command {
	cfgCmd := newGroup("config", "Manage the config file")
	cfgCmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file location",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := config.Path()
			if err != nil {
				return err
			}
			fmt.Fprintln(app.Stdout, p)
			return nil
		},
	})
	return cfgCmd
}

// commandInfo is the JSON shape of the full-tree dump — the one-call
// onboarding surface for agents.
type commandInfo struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Short       string        `json:"short,omitempty"`
	Use         string        `json:"use,omitempty"`
	Examples    string        `json:"examples,omitempty"`
	Flags       []flagInfo    `json:"flags,omitempty"`
	Subcommands []commandInfo `json:"subcommands,omitempty"`
}

type flagInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage,omitempty"`
}

func newCommandsCommand(app *App, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "Dump the entire command tree as JSON (agent onboarding, one call)",
		Long: `Prints every command, flag, and example in one JSON document so an agent
can learn the full CLI surface in a single call instead of many --help round-trips.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderValue(app, dumpCommand(root, "peopleforce"))
		},
	}
}

func dumpCommand(c *cobra.Command, path string) commandInfo {
	info := commandInfo{
		Name:     c.Name(),
		Path:     path,
		Short:    c.Short,
		Use:      c.Use,
		Examples: c.Example,
	}
	collect := func(f *pflag.Flag) {
		info.Flags = append(info.Flags, flagInfo{
			Name: f.Name, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage,
		})
	}
	c.LocalFlags().VisitAll(collect)
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		info.Subcommands = append(info.Subcommands, dumpCommand(sub, path+" "+sub.Name()))
	}
	return info
}

func newSkillCommand(app *App) *cobra.Command {
	var global bool
	skill := newGroup("skill", "Agent onboarding files")
	install := &cobra.Command{
		Use:   "install",
		Short: "Install a Claude Code skill describing this CLI",
		Long: `Writes SKILL.md into .claude/skills/peopleforce/ (project-local by default,
~/.claude/skills/peopleforce/ with --global) so Claude Code discovers how to
drive this CLI: auth, output contract, exit codes, and common recipes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := ".claude/skills/peopleforce"
			if global {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				base = filepath.Join(home, ".claude", "skills", "peopleforce")
			}
			if err := os.MkdirAll(base, 0o755); err != nil {
				return err
			}
			dest := filepath.Join(base, "SKILL.md")
			if err := os.WriteFile(dest, []byte(agentdocs.SkillMD), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(app.Stderr, "installed %s\n", dest)
			return nil
		},
	}
	install.Flags().BoolVar(&global, "global", false, "install to ~/.claude/skills instead of the current project")
	skill.AddCommand(install)
	return skill
}

func newAgentsMDCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "agents-md",
		Short: "Print an AGENTS.md snippet describing this CLI",
		Long:  "Prints a snippet to paste into a repository's AGENTS.md (or CLAUDE.md) so coding agents know this CLI exists and how to call it.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(app.Stdout, agentdocs.AgentsMD)
			return nil
		},
	}
}

func newVersionCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and spec info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderValue(app, map[string]any{
				"version":      Version,
				"commit":       Commit,
				"spec_title":   registry.Info.SpecTitle,
				"spec_version": registry.Info.SpecVersion,
				"operations":   registry.Info.OpCount,
			})
		},
	}
}
