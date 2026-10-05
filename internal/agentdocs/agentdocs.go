// Package agentdocs exposes the onboarding files the CLI can install for
// AI coding agents (`peopleforce skill install`, `peopleforce agents-md`).
package agentdocs

import (
	_ "embed"

	"github.com/infrabay/peopleforce-cli/skills"
)

// SkillMD is the Agent Skill, kept in skills/peopleforce/SKILL.md at the
// repository root so agents can install it from GitHub as well.
var SkillMD = skills.Peopleforce

//go:embed agents.md
var AgentsMD string
