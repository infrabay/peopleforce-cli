// Package skills holds the Agent Skills shipped with peopleforce-cli.
//
// Agents read skills/<name>/SKILL.md straight from the repository: the Claude
// Code plugin in .claude-plugin/ points here, and `npx skills add
// infrabay/peopleforce-cli` installs from here for Codex, Cursor, Gemini CLI
// and other Agent Skills-compatible tools. The binary embeds the same file for
// `peopleforce skill install`, so there is exactly one copy to keep current.
package skills

import _ "embed"

// Peopleforce is skills/peopleforce/SKILL.md.
//
//go:embed peopleforce/SKILL.md
var Peopleforce string
