package skills

import (
	"regexp"
	"strings"
	"testing"
)

// Claude Code, `npx skills` and the Agent Skills spec (agentskills.io) all
// reject or silently skip a SKILL.md whose frontmatter breaks these rules,
// and nothing else in CI would notice.
func TestPeopleforceSkillFrontmatter(t *testing.T) {
	if !strings.HasPrefix(Peopleforce, "---\n") {
		t.Fatal("SKILL.md must start with a YAML frontmatter block")
	}
	end := strings.Index(Peopleforce[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter is not closed")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(Peopleforce[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	// The name must match the directory, skills/peopleforce/.
	if name := fields["name"]; name != "peopleforce" {
		t.Errorf("name = %q, want %q", name, "peopleforce")
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(fields["name"]) {
		t.Errorf("name %q is not lowercase letters, digits and single hyphens", fields["name"])
	}
	desc := fields["description"]
	if desc == "" || len(desc) > 1024 {
		t.Errorf("description must be 1-1024 characters, got %d", len(desc))
	}
}
