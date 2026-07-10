package command

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/3bagels/peopleforce-cli/internal/registry"
)

// The registry is the compiled contract with the spec: every operation
// mapped, curated commands mounted, golden snapshot in sync.
func TestRegistryCoversWholeSpec(t *testing.T) {
	if registry.Info.OpCount != len(registry.Ops) {
		t.Errorf("Info.OpCount = %d, len(Ops) = %d", registry.Info.OpCount, len(registry.Ops))
	}
	if len(registry.Ops) != 203 {
		t.Errorf("expected 203 operations from the vendored spec, got %d — regenerate with `make generate` and review the golden diff", len(registry.Ops))
	}

	curated := 0
	for i := range registry.Ops {
		op := &registry.Ops[i]
		if op.Command != "" {
			curated++
		}
		if op.Method == "DELETE" && !op.Destructive {
			t.Errorf("%s %s: DELETE must be destructive", op.Method, op.Path)
		}
		for _, q := range op.Query {
			if strings.ContainsAny(q.Flag, "[]_") {
				t.Errorf("%s %s: flag %q not normalized", op.Method, op.Path, q.Flag)
			}
			if strings.HasSuffix(q.WireName, "[]") && !q.Repeatable {
				t.Errorf("%s %s: %q must be repeatable", op.Method, op.Path, q.WireName)
			}
		}
	}
	if curated < 60 {
		t.Errorf("curated commands = %d, expected >= 60", curated)
	}

	// The upstream typo path must exist verbatim — never "fixed".
	if _, ok := registry.Find("PUT", "/termintation_reasons/{termination_reason_id}"); !ok {
		t.Error("typo path /termintation_reasons must be preserved verbatim")
	}
}

func TestGoldenSnapshotInSync(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden/commands.json")
	if err != nil {
		t.Fatalf("golden snapshot missing — run `make generate`: %v", err)
	}
	var golden struct {
		Meta registry.Meta
		Ops  []registry.Op
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	current, err := json.Marshal(struct {
		Meta registry.Meta
		Ops  []registry.Op
	}{registry.Info, registry.Ops})
	if err != nil {
		t.Fatal(err)
	}
	goldenNorm, err := json.Marshal(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, goldenNorm) {
		t.Error("registry.gen.go and testdata/golden/commands.json are out of sync — run `make generate`")
	}
}

// `peopleforce commands` is the one-call onboarding surface; its structure
// is a contract agents rely on.
func TestCommandsDumpContract(t *testing.T) {
	stdout, stderr, code := runCLI(t, "", "commands")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	// Meta commands honor the same {"data": ...} envelope as API responses.
	var wrapper struct {
		Data commandInfo `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &wrapper); err != nil {
		t.Fatalf("commands output is not valid JSON: %v", err)
	}
	tree := wrapper.Data
	if tree.Name != "peopleforce" || len(tree.Subcommands) == 0 {
		t.Fatalf("unexpected tree root: %+v", tree.Name)
	}

	var walk func(c commandInfo) int
	walk = func(c commandInfo) int {
		if c.Path == "" || c.Name == "" {
			t.Errorf("node missing path/name: %+v", c)
		}
		n := 1
		for _, sub := range c.Subcommands {
			if !strings.HasPrefix(sub.Path, c.Path+" ") {
				t.Errorf("subcommand path %q does not extend parent %q", sub.Path, c.Path)
			}
			n += walk(sub)
		}
		return n
	}
	total := walk(tree)
	if total < 80 {
		t.Errorf("command tree has %d nodes, expected at least 80 (65 curated ops + groups + meta)", total)
	}

	// Key agent entry points must exist.
	for _, path := range []string{
		"peopleforce employees list",
		"peopleforce api call",
		"peopleforce api ops",
		"peopleforce auth status",
	} {
		if !treeContains(tree, path) {
			t.Errorf("command %q missing from dump", path)
		}
	}
}

func treeContains(c commandInfo, path string) bool {
	if c.Path == path {
		return true
	}
	for _, sub := range c.Subcommands {
		if treeContains(sub, path) {
			return true
		}
	}
	return false
}
