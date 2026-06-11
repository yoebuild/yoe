// Package embedded holds assets baked into the yoe binary at build time.
//
// It lives at the module root because go:embed paths cannot traverse out of
// the embedding file's directory (no ".."), and the canonical skill sources
// live under .claude/skills — the same directory Claude Code reads when
// developing yoe itself. Keeping one copy here, rather than a mirror under
// internal/, means the skills you edit while working on yoe are exactly the
// skills `yoe skills install` ships to a user's project.
package embedded

import "embed"

// SkillsFS contains the Claude Code skill directories under .claude/skills.
// Read and materialize them via internal/skills; `yoe skills install` writes
// them into a project's own .claude/skills directory.
//
//go:embed all:.claude/skills
var SkillsFS embed.FS
