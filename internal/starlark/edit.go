package starlark

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RewriteUnitField rewrites the `field = "value"` line for the unit
// named `unitName` inside `starPath`. If the field is already present in
// the unit's call block, its value is replaced in place (preserving
// indent and any trailing comment). If the field is absent, a new line
// is inserted right after the `name = ...` line at the same indent.
//
// The rewriter is intentionally regex-based rather than AST-based:
// round-tripping through a Starlark printer would lose comments and
// cosmetic formatting that humans care about. The format yoe units use
// is consistent enough across `unit(...)`, `autotools(...)`,
// `cmake(...)`, etc., that a small regex over the call body covers
// every shape currently in module-core.
//
// On any error the file is left untouched (the new content is written
// to a tmp file and renamed only after a clean build).
func RewriteUnitField(starPath, unitName, field, value string) error {
	data, err := os.ReadFile(starPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", starPath, err)
	}
	content := string(data)

	start, end, ok := findUnitBlock(content, unitName)
	if !ok {
		return fmt.Errorf("%s: unit %q not found", starPath, unitName)
	}

	block := content[start:end]
	updated, err := rewriteFieldInBlock(block, field, value)
	if err != nil {
		return fmt.Errorf("%s: rewriting %s field: %w", starPath, field, err)
	}

	return atomicWrite(starPath, content[:start]+updated+content[end:])
}

// RemoveUnitField drops the `field = ...` line from the unit's call
// block if present. No-op if the field doesn't exist (returns nil).
// Used together with RewriteUnitField when promoting from one
// pin-form to another (e.g. branch → tag): rewrite the new field, then
// remove the now-stale field.
func RemoveUnitField(starPath, unitName, field string) error {
	data, err := os.ReadFile(starPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", starPath, err)
	}
	content := string(data)

	start, end, ok := findUnitBlock(content, unitName)
	if !ok {
		return fmt.Errorf("%s: unit %q not found", starPath, unitName)
	}

	block := content[start:end]
	updated := removeFieldFromBlock(block, field)
	if updated == block {
		return nil
	}
	return atomicWrite(starPath, content[:start]+updated+content[end:])
}

// findUnitBlock returns the byte range of the call expression whose
// `name = "<unitName>"` field matches. The returned span starts at the
// function-name token (`unit`, `autotools`, etc.) and ends at the
// matching close paren plus its trailing newline (or EOF).
//
// Multi-unit .star files are handled: each call is examined in turn.
// Calls whose first arg isn't `name = ...` are still detected — the
// regex looks anywhere inside the call body.
func findUnitBlock(content, unitName string) (int, int, bool) {
	// Match call openers at the start of a line (top-level Starlark
	// expressions; helper-function bodies have indentation). The list
	// of accepted opener names tracks the classes in module-core/classes/.
	openerRE := regexp.MustCompile(`(?m)^(unit|autotools|cmake|go_binary|alpine_pkg|binary|image|container)\s*\(`)
	nameRE := regexp.MustCompile(`(?m)^\s*name\s*=\s*"` + regexp.QuoteMeta(unitName) + `"\s*,?`)

	for _, opener := range openerRE.FindAllStringIndex(content, -1) {
		start := opener[0]
		// Walk forward to the matching close paren, respecting nested
		// parens and string literals. The `(` we just matched is the
		// call's open paren — track depth from there.
		depth := 0
		parenIdx := opener[1] - 1 // index of the `(`
		end := -1
		inStr := false
		var strQuote byte
		i := parenIdx
		for i < len(content) {
			c := content[i]
			switch {
			case inStr:
				if c == '\\' && i+1 < len(content) {
					i += 2
					continue
				}
				if c == strQuote {
					inStr = false
				}
			case c == '"' || c == '\'':
				inStr = true
				strQuote = c
			case c == '#':
				// Skip to end of line.
				for i < len(content) && content[i] != '\n' {
					i++
				}
				continue
			case c == '(':
				depth++
			case c == ')':
				depth--
				if depth == 0 {
					end = i + 1
				}
			}
			if end >= 0 {
				break
			}
			i++
		}
		if end < 0 {
			continue // malformed; skip
		}
		// Include the trailing newline if present.
		if end < len(content) && content[end] == '\n' {
			end++
		}
		block := content[start:end]
		if nameRE.MatchString(block) {
			return start, end, true
		}
	}
	return 0, 0, false
}

// rewriteFieldInBlock replaces the `field = "..."` line inside block
// with the new value. If the field is absent, a new line is inserted
// after the `name = ...` line at the same indent.
//
// Preserves: indent on the modified/inserted line, trailing comma,
// trailing comment if any.
func rewriteFieldInBlock(block, field, value string) (string, error) {
	fieldLineRE := regexp.MustCompile(`(?m)^([ \t]+)` + regexp.QuoteMeta(field) +
		`(\s*=\s*)"([^"]*)"(\s*,)?(\s*#[^\n]*)?$`)
	if loc := fieldLineRE.FindStringSubmatchIndex(block); loc != nil {
		indent := block[loc[2]:loc[3]]
		// Reconstruct: preserve `=` spacing, comma presence, comment.
		eqSpacing := block[loc[4]:loc[5]]
		var comma, comment string
		if loc[8] != -1 {
			comma = block[loc[8]:loc[9]]
		} else {
			comma = ","
		}
		if loc[10] != -1 {
			comment = block[loc[10]:loc[11]]
		}
		newLine := indent + field + eqSpacing + `"` + value + `"` + comma + comment
		return block[:loc[0]] + newLine + block[loc[1]:], nil
	}
	// Insert: place the new line after the name = ... line.
	nameLineRE := regexp.MustCompile(`(?m)^([ \t]+)name\s*=\s*"[^"]*"\s*,?[^\n]*\n`)
	loc := nameLineRE.FindStringSubmatchIndex(block)
	if loc == nil {
		return "", fmt.Errorf("could not locate name line for insertion")
	}
	indent := block[loc[2]:loc[3]]
	insertion := indent + field + ` = "` + value + `",` + "\n"
	return block[:loc[1]] + insertion + block[loc[1]:], nil
}

// removeFieldFromBlock drops the `field = ...` line from block,
// including its trailing newline. Returns block unchanged if the field
// isn't present.
func removeFieldFromBlock(block, field string) string {
	// Match the whole line including the trailing \n so we don't leave
	// a blank line behind.
	lineRE := regexp.MustCompile(`(?m)^[ \t]+` + regexp.QuoteMeta(field) +
		`\s*=\s*"[^"]*"\s*,?[^\n]*\n`)
	return lineRE.ReplaceAllString(block, "")
}

// atomicWrite writes data to path via a tmp file in the same dir and
// rename. Avoids partially-written .star files on power loss or
// interrupted writes.
func atomicWrite(path, data string) error {
	dir := pathDir(path)
	tmp, err := os.CreateTemp(dir, ".yoe-edit-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleaned := false
	defer func() {
		if !cleaned {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleaned = true
	return nil
}

// pathDir is filepath.Dir without the import — keeps this file's
// surface area small and avoids pulling path/filepath into a tiny
// helper file.
func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}
