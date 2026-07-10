// Package agentdocs embeds the onboarding files the CLI can install for
// AI coding agents (`peopleforce skill install`, `peopleforce agents-md`).
package agentdocs

import _ "embed"

//go:embed skill.md
var SkillMD string

//go:embed agents.md
var AgentsMD string
