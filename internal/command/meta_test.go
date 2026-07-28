package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3bagels/peopleforce-cli/internal/agentdocs"
	"github.com/3bagels/peopleforce-cli/internal/registry"
)

func TestSkillInstallWritesEmbeddedSkill(t *testing.T) {
	t.Chdir(t.TempDir())

	_, stderr, code := runCLI(t, "", "skill", "install")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	dest := filepath.Join(".claude", "skills", "peopleforce", "SKILL.md")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading installed skill: %v", err)
	}
	if string(got) != agentdocs.SkillMD {
		t.Errorf("installed SKILL.md differs from the embedded copy (%d bytes on disk, %d embedded)",
			len(got), len(agentdocs.SkillMD))
	}
	if !strings.Contains(stderr, dest) {
		t.Errorf("stderr should report where the skill landed, got: %q", stderr)
	}
}

// The destination is a fixed relative path, so a cloned repo can ship a
// symlink there and pick the file this command overwrites — a shell rc, a CI
// config. Installing must refuse rather than write through it.
func TestSkillInstallRefusesToFollowSymlink(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	const original = "export API_TOKEN=secret\n"
	victim := filepath.Join(root, "shell-rc")
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".claude", "skills", "peopleforce")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCLI(t, "", "skill", "install")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr should explain the refusal, got: %q", stderr)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("the symlink target was overwritten: %d bytes, want %q", len(got), original)
	}
}

func TestConfigPathPrintsResolvedLocation(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "config", "path")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	got := strings.TrimSpace(stdout)
	if !filepath.IsAbs(got) || filepath.Base(got) != "config.toml" ||
		filepath.Base(filepath.Dir(got)) != "peopleforce" {
		t.Errorf("config path = %q, want an absolute .../peopleforce/config.toml", got)
	}
}

func TestAgentsMDPrintsEmbeddedSnippet(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "agents-md")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if stdout != agentdocs.AgentsMD {
		t.Errorf("agents-md output differs from the embedded snippet (%d bytes printed, %d embedded)",
			len(stdout), len(agentdocs.AgentsMD))
	}
}

func TestVersionReportsSpecInfo(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "version")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	var got struct {
		Data struct {
			Version     string `json:"version"`
			Commit      string `json:"commit"`
			SpecTitle   string `json:"spec_title"`
			SpecVersion string `json:"spec_version"`
			Operations  int    `json:"operations"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout)
	}
	if got.Data.Version != Version || got.Data.Commit != Commit {
		t.Errorf("version/commit = %q/%q, want %q/%q",
			got.Data.Version, got.Data.Commit, Version, Commit)
	}
	if got.Data.SpecTitle != registry.Info.SpecTitle || got.Data.SpecVersion != registry.Info.SpecVersion {
		t.Errorf("spec = %q %q, want %q %q",
			got.Data.SpecTitle, got.Data.SpecVersion, registry.Info.SpecTitle, registry.Info.SpecVersion)
	}
	if got.Data.Operations != registry.Info.OpCount {
		t.Errorf("operations = %d, want %d", got.Data.Operations, registry.Info.OpCount)
	}
}
