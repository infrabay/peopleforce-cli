// Package config resolves credentials and settings with the precedence
// agents expect: flag > environment > config file > default.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const (
	EnvAPIKey  = "PEOPLEFORCE_API_KEY"
	EnvAPIURL  = "PEOPLEFORCE_API_URL"
	EnvProfile = "PEOPLEFORCE_PROFILE"
)

// Profile is one named credential set in the config file.
type Profile struct {
	APIKey string `toml:"api_key"`
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
}

// Path returns the config file location, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "peopleforce", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "peopleforce", "config.toml"), nil
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
		return f, err
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parsing %s: %w", path, err)
	}
	return f, nil
}

// Save writes the config file with owner-only permissions (it holds keys).
func Save(f File) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
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

	r.Profile = "default"
	if p := os.Getenv(EnvProfile); p != "" {
		r.Profile = p
	}
	if flagProfile != "" {
		r.Profile = flagProfile
	}

	cfg, err := Load()
	if err != nil {
		return r, err
	}
	if prof, ok := cfg.Profiles[r.Profile]; ok {
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
	return r, nil
}
