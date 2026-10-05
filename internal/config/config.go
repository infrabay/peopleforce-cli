// Package config resolves credentials and settings with the precedence
// agents expect: flag > environment > config file > default.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const (
	EnvAPIKey  = "PEOPLEFORCE_API_KEY"
	EnvAPIURL  = "PEOPLEFORCE_API_URL"
	EnvProfile = "PEOPLEFORCE_PROFILE"

	envXDGConfigHome = "XDG_CONFIG_HOME"
)

// Profile is one named credential set in the config file.
type Profile struct {
	APIKey string `toml:"api_key,omitempty"`
	APIURL string `toml:"api_url,omitempty"`
}

// File is the on-disk config format ($XDG_CONFIG_HOME/peopleforce/config.toml).
type File struct {
	Profiles map[string]Profile `toml:"profiles"`
}

// Resolved carries the effective settings plus where each came from, so
// `peopleforce auth status` can self-diagnose.
type Resolved struct {
	APIKey       string
	APIKeySource string // "flag" | "env" | "config" | "none"
	APIURL       string
	APIURLSource string // "flag" | "env" | "config" | "default"
	Profile      string

	// Warning is set when resolution succeeded but something looks wrong —
	// currently only a named profile that does not exist while the key came
	// from a higher-precedence source. The caller surfaces it on stderr.
	Warning string
}

// Path returns the config file location, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	// The XDG spec requires absolute paths and mandates ignoring relative
	// ones. Honoring a relative value would resolve against the current
	// directory, so `auth login` inside a repo checkout would write the API
	// key into the working tree, one `git add .` away from being published.
	if xdg := os.Getenv(envXDGConfigHome); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "peopleforce", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "peopleforce", "config.toml"), nil
}

// nonDirAncestor returns the first existing element at or above dir that is
// not a directory, or "" when the path is clear.
func nonDirAncestor(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil {
			if fi.IsDir() {
				return ""
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// explainPathError names the file blocking the config directory. os.IsNotExist
// matches only ENOENT, so a regular file where the directory belongs surfaces
// as a bare "not a directory" and kills every command — including ones that
// have a perfectly good key in the environment and never needed the file.
func explainPathError(path string, err error) error {
	blocker := nonDirAncestor(filepath.Dir(path))
	if blocker == "" {
		return err
	}
	return fmt.Errorf("cannot use config file %s: %s is a file, not a directory — remove it or point %s at a different directory",
		path, blocker, envXDGConfigHome)
}

// Load reads the config file; a missing file yields an empty config.
func Load() (File, error) {
	var f File
	path, err := Path()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, explainPathError(path, err)
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return f, parseError(path, err)
	}
	return f, nil
}

// parseError reports where the config file is broken without quoting it. The
// TOML parser's message echoes the offending token, and in this file that is
// typically an API key someone forgot to quote, so passing it through would
// print the key to stderr and into `auth status`'s config_error.
func parseError(path string, err error) error {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		where := fmt.Sprintf("line %d", pe.Position.Line)
		if pe.LastKey != "" {
			where += fmt.Sprintf(", after key %q", pe.LastKey)
		}
		return fmt.Errorf("parsing %s: invalid TOML at %s (string values such as api_key must be quoted)", path, where)
	}
	return fmt.Errorf("parsing %s: invalid TOML", path)
}

// Save writes the config file with owner-only permissions (it holds keys).
func Save(f File) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return explainPathError(path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(f); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Resolve computes effective settings. flagKey/flagURL/flagProfile are the
// command-line values ("" when unset).
func Resolve(flagKey, flagURL, flagProfile string) (Resolved, error) {
	r := Resolved{
		APIKeySource: "none",
		APIURLSource: "default",
	}

	const defaultProfile = "default"
	r.Profile = defaultProfile
	named := ""
	if p := os.Getenv(EnvProfile); p != "" {
		r.Profile, named = p, p
	}
	if flagProfile != "" {
		r.Profile, named = flagProfile, flagProfile
	}
	// Naming "default" explicitly asks for the profile that is already implied,
	// so it must behave exactly like not naming one: having no config file is
	// the normal case for env-var users, and reporting a missing "default"
	// would replace the accurate "no API key configured" (exit 3) with a
	// misleading profile error (exit 2).
	explicit := named != "" && named != defaultProfile

	cfg, err := Load()
	if err != nil {
		return r, err
	}
	prof, ok := cfg.Profiles[r.Profile]
	if ok {
		if prof.APIKey != "" {
			r.APIKey = prof.APIKey
			r.APIKeySource = "config"
		}
		if prof.APIURL != "" {
			r.APIURL = prof.APIURL
			r.APIURLSource = "config"
		}
	}

	if v := os.Getenv(EnvAPIKey); v != "" {
		r.APIKey = v
		r.APIKeySource = "env"
	}
	if v := os.Getenv(EnvAPIURL); v != "" {
		r.APIURL = v
		r.APIURLSource = "env"
	}

	if flagKey != "" {
		r.APIKey = flagKey
		r.APIKeySource = "flag"
	}
	if flagURL != "" {
		r.APIURL = flagURL
		r.APIURLSource = "flag"
	}

	// A named profile that does not exist is reported only after the higher
	// precedence sources have had their say. Failing earlier would let a
	// missing profile veto a key given by --api-key or the environment, which
	// contradicts the documented flag > env > config order and breaks anyone
	// who exports PEOPLEFORCE_PROFILE while authenticating purely by env var.
	// It still must not pass in silence: a typo'd profile name would otherwise
	// run the command against whatever tenant the environment happens to hold.
	if !ok && explicit {
		path, _ := Path() // Load succeeded, so Path cannot fail here
		detail := fmt.Sprintf("profile %q not found in %s (create it with `peopleforce auth login --profile %s --api-key <key>`)",
			r.Profile, path, r.Profile)
		if r.APIKeySource == "none" {
			return r, errors.New(detail)
		}
		r.Warning = fmt.Sprintf("%s; continuing with the %s API key", detail, r.APIKeySource)
	}
	return r, nil
}
