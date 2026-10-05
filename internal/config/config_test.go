package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the package at a throwaway XDG root and blanks every
// PEOPLEFORCE_* variable, so a developer's real key can neither leak into an
// assertion nor be overwritten by a Save test.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(envXDGConfigHome, dir)
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAPIURL, "")
	t.Setenv(EnvProfile, "")
	return dir
}

func writeConfig(t *testing.T, xdg, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(xdg, "peopleforce"), 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	path := filepath.Join(xdg, "peopleforce", "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// The key and the URL are resolved independently, so a config file that only
// pins a URL must keep supplying it while the key comes from the environment.
func TestResolvePrecedenceIsPerKey(t *testing.T) {
	const bothInConfig = "[profiles.default]\napi_key = \"cfg-key\"\napi_url = \"https://cfg.example\"\n"

	cases := []struct {
		name             string
		config           string
		envKey, envURL   string
		flagKey, flagURL string
		wantKey, keySrc  string
		wantURL, urlSrc  string
	}{
		{
			name:    "nothing set anywhere",
			wantKey: "", keySrc: "none",
			wantURL: "", urlSrc: "default",
		},
		{
			name:    "config file only",
			config:  bothInConfig,
			wantKey: "cfg-key", keySrc: "config",
			wantURL: "https://cfg.example", urlSrc: "config",
		},
		{
			name:   "env beats config",
			config: bothInConfig,
			envKey: "env-key", envURL: "https://env.example",
			wantKey: "env-key", keySrc: "env",
			wantURL: "https://env.example", urlSrc: "env",
		},
		{
			name:   "flag beats env and config",
			config: bothInConfig,
			envKey: "env-key", envURL: "https://env.example",
			flagKey: "flag-key", flagURL: "https://flag.example",
			wantKey: "flag-key", keySrc: "flag",
			wantURL: "https://flag.example", urlSrc: "flag",
		},
		{
			name:    "key from env, url from config",
			config:  bothInConfig,
			envKey:  "env-key",
			wantKey: "env-key", keySrc: "env",
			wantURL: "https://cfg.example", urlSrc: "config",
		},
		{
			name:    "key from flag, url from config",
			config:  bothInConfig,
			flagKey: "flag-key",
			wantKey: "flag-key", keySrc: "flag",
			wantURL: "https://cfg.example", urlSrc: "config",
		},
		{
			name:    "url from flag, key from config",
			config:  bothInConfig,
			envURL:  "https://env.example",
			flagURL: "https://flag.example",
			wantKey: "cfg-key", keySrc: "config",
			wantURL: "https://flag.example", urlSrc: "flag",
		},
		{
			name:    "url from env, key from config",
			config:  bothInConfig,
			envURL:  "https://env.example",
			wantKey: "cfg-key", keySrc: "config",
			wantURL: "https://env.example", urlSrc: "env",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := isolate(t)
			if tt.config != "" {
				writeConfig(t, dir, tt.config)
			}
			t.Setenv(EnvAPIKey, tt.envKey)
			t.Setenv(EnvAPIURL, tt.envURL)

			r, err := Resolve(tt.flagKey, tt.flagURL, "")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if r.APIKey != tt.wantKey || r.APIKeySource != tt.keySrc {
				t.Errorf("key = %q from %q, want %q from %q", r.APIKey, r.APIKeySource, tt.wantKey, tt.keySrc)
			}
			if r.APIURL != tt.wantURL || r.APIURLSource != tt.urlSrc {
				t.Errorf("url = %q from %q, want %q from %q", r.APIURL, r.APIURLSource, tt.wantURL, tt.urlSrc)
			}
		})
	}
}

func TestResolveProfileSelection(t *testing.T) {
	const cfg = "[profiles.default]\napi_key = \"key-default\"\n" +
		"[profiles.staging]\napi_key = \"key-staging\"\n" +
		"[profiles.prod]\napi_key = \"key-prod\"\n"

	cases := []struct {
		name        string
		envProfile  string
		flagProfile string
		wantProfile string
		wantKey     string
	}{
		{"implicit default", "", "", "default", "key-default"},
		{"empty flag falls through to env", "staging", "", "staging", "key-staging"},
		{"flag beats env", "staging", "prod", "prod", "key-prod"},
		{"flag beats the implicit default", "", "prod", "prod", "key-prod"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := isolate(t)
			writeConfig(t, dir, cfg)
			t.Setenv(EnvProfile, tt.envProfile)

			r, err := Resolve("", "", tt.flagProfile)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if r.Profile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", r.Profile, tt.wantProfile)
			}
			if r.APIKey != tt.wantKey {
				t.Errorf("key = %q, want %q (profile %q)", r.APIKey, tt.wantKey, tt.wantProfile)
			}
		})
	}
}

// A named profile that does not exist must never pass in silence, but it also
// must not veto a key supplied by a higher-precedence source: the documented
// order is flag > env > config, and exporting PEOPLEFORCE_PROFILE while
// authenticating purely by env var is a legitimate setup.
func TestResolveUnknownExplicitProfileWarnsWhenKeyComesFromElsewhere(t *testing.T) {
	cases := []struct {
		name        string
		envProfile  string
		flagProfile string
		flagKey     string
		wantKey     string
		wantSource  string
	}{
		{"env key, profile by flag", "", "typo", "", "env-key", "env"},
		{"env key, profile by env", "typo", "", "", "env-key", "env"},
		{"flag key outranks everything", "", "typo", "flag-key", "flag-key", "flag"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := isolate(t)
			path := writeConfig(t, dir, "[profiles.default]\napi_key = \"key-default\"\n")
			t.Setenv(EnvAPIKey, "env-key")
			t.Setenv(EnvProfile, tt.envProfile)

			r, err := Resolve(tt.flagKey, "", tt.flagProfile)
			if err != nil {
				t.Fatalf("Resolve failed: %v — a missing profile must not override a key from a higher-precedence source", err)
			}
			if r.APIKey != tt.wantKey || r.APIKeySource != tt.wantSource {
				t.Errorf("key = %q (source %q), want %q (source %q)", r.APIKey, r.APIKeySource, tt.wantKey, tt.wantSource)
			}
			if r.Warning == "" {
				t.Fatal("a typo'd profile must still be reported; got no warning")
			}
			if !strings.Contains(r.Warning, `"typo"`) || !strings.Contains(r.Warning, path) {
				t.Errorf("warning %q should name both the profile and the config path", r.Warning)
			}
		})
	}
}

// With nothing else supplying a key, the missing profile IS the failure, and
// naming it beats the generic "no API key configured".
func TestResolveUnknownExplicitProfileErrorsWithoutOtherKey(t *testing.T) {
	dir := isolate(t)
	path := writeConfig(t, dir, "[profiles.default]\napi_key = \"key-default\"\n")
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvProfile, "")

	r, err := Resolve("", "", "typo")
	if err == nil {
		t.Fatalf("Resolve succeeded with key %q, want an error naming the missing profile", r.APIKey)
	}
	if !strings.Contains(err.Error(), `"typo"`) || !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name both the profile and the config path", err)
	}
	if r.APIKey != "" {
		t.Errorf("key = %q, want none", r.APIKey)
	}
}

func TestResolveImplicitDefaultProfileWithoutConfigFile(t *testing.T) {
	isolate(t)
	t.Setenv(EnvAPIKey, "env-key")

	r, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Profile != "default" {
		t.Errorf("profile = %q, want %q", r.Profile, "default")
	}
	if r.APIKey != "env-key" || r.APIKeySource != "env" {
		t.Errorf("key = %q from %q, want %q from %q", r.APIKey, r.APIKeySource, "env-key", "env")
	}
}

func TestResolveProfileWithoutAPIKey(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, dir, "[profiles.default]\napi_url = \"https://cfg.example\"\n")

	r, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "" || r.APIKeySource != "none" {
		t.Errorf("key = %q from %q, want empty from %q", r.APIKey, r.APIKeySource, "none")
	}
	if r.APIURL != "https://cfg.example" || r.APIURLSource != "config" {
		t.Errorf("url = %q from %q, want %q from %q", r.APIURL, r.APIURLSource, "https://cfg.example", "config")
	}
}

// A profile that exists but is empty is still a hit, so it must not trip the
// unknown-profile error.
func TestResolveExplicitProfileWithoutAPIKey(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, dir, "[profiles.default]\napi_key = \"key-default\"\n[profiles.blank]\n")

	r, err := Resolve("", "", "blank")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Profile != "blank" {
		t.Errorf("profile = %q, want %q", r.Profile, "blank")
	}
	if r.APIKey != "" || r.APIKeySource != "none" {
		t.Errorf("key = %q from %q, want empty from %q", r.APIKey, r.APIKeySource, "none")
	}
}

func TestLoadMissingFile(t *testing.T) {
	isolate(t)

	f, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Profiles) != 0 {
		t.Errorf("profiles = %v, want none", f.Profiles)
	}
}

func TestLoadMalformedTOMLNamesPath(t *testing.T) {
	dir := isolate(t)
	path := writeConfig(t, dir, "[profiles.default\napi_key = \n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded on malformed TOML")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the config path %s", err, path)
	}
}

// The TOML parser quotes the token it choked on, and the likeliest one in this
// file is an unquoted API key: the error goes to stderr and into `auth
// status` output, so it must locate the problem without repeating it.
func TestLoadMalformedTOMLDoesNotEchoTheKey(t *testing.T) {
	dir := isolate(t)
	const secret = "pfSuperSecretHrToken"
	writeConfig(t, dir, "[profiles.default]\napi_key = "+secret+"\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded on malformed TOML")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the unquoted key: %q", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q does not say which line is broken", err)
	}
}

// os.IsNotExist only matches ENOENT, so without the dedicated explanation a
// file squatting on the config directory kills every command with a bare
// "not a directory".
func TestConfigDirBlockedByRegularFile(t *testing.T) {
	check := func(t *testing.T, blocker string, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error, got none")
		}
		if !strings.Contains(err.Error(), blocker+" is a file, not a directory") {
			t.Errorf("error %q does not point at the blocking file %s", err, blocker)
		}
		if !strings.Contains(err.Error(), envXDGConfigHome) {
			t.Errorf("error %q does not suggest repointing %s", err, envXDGConfigHome)
		}
	}

	t.Run("Load", func(t *testing.T) {
		dir := isolate(t)
		blocker := filepath.Join(dir, "peopleforce")
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("writing blocker: %v", err)
		}
		_, err := Load()
		check(t, blocker, err)
	})

	t.Run("Save", func(t *testing.T) {
		dir := isolate(t)
		blocker := filepath.Join(dir, "peopleforce")
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("writing blocker: %v", err)
		}
		check(t, blocker, Save(File{Profiles: map[string]Profile{"default": {APIKey: "k"}}}))
	})
}

func TestSaveRoundTripsAndKeepsUnrelatedProfiles(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, dir, "[profiles.other]\napi_key = \"other-key\"\napi_url = \"https://other.example\"\n")

	f, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.Profiles["default"] = Profile{APIKey: "new-key"}
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	want := map[string]Profile{
		"other":   {APIKey: "other-key", APIURL: "https://other.example"},
		"default": {APIKey: "new-key"},
	}
	for name, w := range want {
		if got.Profiles[name] != w {
			t.Errorf("profile %q = %+v, want %+v", name, got.Profiles[name], w)
		}
	}
	if len(got.Profiles) != len(want) {
		t.Errorf("profiles = %v, want exactly %d", got.Profiles, len(want))
	}
}

// The file holds API keys, so it must never be group- or world-readable.
func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	isolate(t)
	if err := Save(File{Profiles: map[string]Profile{"default": {APIKey: "k"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}
}

// An `api_key = ""` line reads like a broken credential and, once round-tripped
// through `auth login --profile`, would look like the key had been wiped.
func TestSaveOmitsEmptyAPIKey(t *testing.T) {
	isolate(t)
	if err := Save(File{Profiles: map[string]Profile{"readonly": {APIURL: "https://cfg.example"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if strings.Contains(string(data), "api_key") {
		t.Errorf("config contains an api_key line for a url-only profile:\n%s", data)
	}
}

func TestPath(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	homePath := filepath.Join(home, ".config", "peopleforce", "config.toml")

	cases := []struct {
		name string
		xdg  string
		want string
	}{
		{"absolute XDG_CONFIG_HOME", xdg, filepath.Join(xdg, "peopleforce", "config.toml")},
		{"empty XDG_CONFIG_HOME falls back to home", "", homePath},
		// A relative value would resolve against the working directory and
		// drop the API key wherever the command happened to run.
		{"relative XDG_CONFIG_HOME is ignored", filepath.Join("relative", "config"), homePath},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envXDGConfigHome, tt.xdg)
			got, err := Path()
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if got != tt.want {
				t.Errorf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Naming "default" is asking for the profile that is already implied, so it
// must not turn a plain missing-key situation into a profile error: the
// documented codes are 3 for "no key" and 2 for "the config could not be read".
func TestResolveExplicitDefaultProfileIsNotAnError(t *testing.T) {
	for _, via := range []string{"env", "flag"} {
		t.Run(via, func(t *testing.T) {
			isolate(t) // no config file at all
			flagProfile := ""
			if via == "env" {
				t.Setenv(EnvProfile, "default")
			} else {
				flagProfile = "default"
			}

			r, err := Resolve("", "", flagProfile)
			if err != nil {
				t.Fatalf("Resolve: %v — naming the implicit default must behave like naming nothing", err)
			}
			if r.Warning != "" {
				t.Errorf("warning = %q, want none", r.Warning)
			}
			if r.APIKeySource != "none" {
				t.Errorf("source = %q, want none so the caller reports the missing key", r.APIKeySource)
			}
		})
	}
}
