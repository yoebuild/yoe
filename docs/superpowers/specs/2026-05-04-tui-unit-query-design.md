# TUI unit query language

## Problem

The TUI's unit list is a single flat alphabetical column with substring-only
search (`/<text>`). This worked when a project had ~50 units. With external
modules in play (`units-alpine` ships ~3000 units alone, including ~100
`linux-firmware-*` packages), the list is now too long to scroll and too
undifferentiated to skim. There is no way to:

- See "just the units that get built when I build my image" — the user's primary
  workflow today.
- Filter by type, by module, or by status without scrolling.
- Persist a working-set view across sessions.

The goal is to make the TUI useful again at this scale, with a single mechanism
that scales to future filter dimensions instead of layering modes/toggles on top
of each other.

## Approach

Replace the substring-only search with a small query language inspired by
sndtool (`cmd/query.go` in `/scratch/sndtool`). One search bar, parsed into
field filters and free-text terms. The query string is the unit-of-state for the
list view — anything the user can see is reproducible by typing the query, and
`local.star` saves the last query as the next session's starting point.

The image-as-default-scope is a special case of this: the bootstrap query is
`in:<defaults.image>`, which uses an `in:` closure operator that walks the DAG
from any unit. Same operator answers "what does openssl need", not just "what's
in this image", so the design is workflow-shaped, not image-shaped.

## Query grammar

```
query     := term*
term      := field-filter | view-shortcut | bare-term
field-filter := IDENT ":" VALUE
view-shortcut := "images" | "containers" | "failed" | "building"
bare-term := WORD          // substring match against unit name
```

- Whitespace separates terms. Multiple terms are AND-ed.
- Field names and values are case-insensitive. Unit names match
  case-insensitively.
- Repeated field filters with the same name are OR-ed within the field, then
  AND-ed with everything else: `module:units-core module:units-rpi` matches
  units from either module.
- Bare terms are substring matches on the unit name (the existing behavior of
  `/`).
- View shortcuts are syntactic sugar that desugars to a single field filter
  (`images` → `type:image`, `failed` → `status:failed`, etc.). The parser
  recognises them before falling back to bare-term substring matching, so typing
  `images` filters by type, not by units whose names contain "images".
- Unknown field names render the entire query as invalid; the bar shows an error
  and the list stays at whatever was last valid.
- Unknown values for a known field (e.g. `type:gizmo`) match nothing; the list
  shows "no units match" without erroring.

### v1 fields

| Field     | Values                                             | Notes                                                                                      |
| --------- | -------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `type:`   | `image`, `container`, `unit`                       | Today maps directly to `Unit.Class`.                                                       |
| `module:` | Any module name in the project                     | Project root counts as the empty string; the spelling `module:project` matches it.         |
| `status:` | `cached`, `building`, `failed`, `stale`, `pending` | Sourced from the TUI's own `m.statuses` map; matches the colors already shown in the list. |
| `in:`     | Any unit name                                      | The closure operator — see below.                                                          |
| _(none)_  | substring on unit name                             | Bare terms; identical to the current `/` search.                                           |

### `in:` closure operator

`in:X` matches the set `{X} ∪ build-deps*(X) ∪ runtime-deps*(X)`, i.e. X plus
every unit reachable from X by walking `deps` and `runtime_deps` transitively,
with `runtime_deps` routed through `proj.Provides` (same routing the image
resolver uses). `X` may be any unit, not just an image.

Examples:

- `in:base-image` → image + busybox + kernel + openssl + toolchain-musl + … (the
  user's working set today).
- `in:openssl` → openssl + zlib + toolchain-musl. Useful when iterating on a
  single library.
- `in:apk-tools` → apk-tools + (libcrypto3/libssl3 → openssl via PROVIDES) +
  zlib + musl. Useful for verifying what a deploy will actually pull.
- `in:nonexistent` → empty result, with a one-line "unit not found" hint in the
  query bar; query is otherwise valid (combinable).

Implementation: walk via `resolve.BuildDAG` (already exists) plus
`resolve.RuntimeClosure` (already exists), union the visited sets, include the
root.

### Worked examples

| Query                                | Meaning                                                                   |
| ------------------------------------ | ------------------------------------------------------------------------- |
| _(empty)_                            | All units — the current default behavior, kept as the empty case.         |
| `in:base-image`                      | Working-set view: just what gets built when the image builds.             |
| `in:base-image status:failed`        | What's broken in my image right now.                                      |
| `in:base-image type:image`           | Just the image itself (degenerate but valid).                             |
| `module:units-alpine`                | Browse what units-alpine ships.                                           |
| `linux-firmware`                     | Old-style substring search across all units.                              |
| `module:units-alpine linux-firmware` | linux-firmware-\* in units-alpine specifically.                           |
| `failed`                             | Same as `status:failed`.                                                  |
| `images`                             | Same as `type:image`. All images in the project, useful for choosing one. |

## `local.star` schema change

Add a single field:

```python
local(
    machine     = "qemu-x86_64",
    deploy_host = "localhost:2222",
    query       = "in:base-image",       # new
)
```

`yoestar.LocalOverrides` gains `Query string`. `LoadLocalOverrides` reads it;
`WriteLocalOverrides` writes it whenever the TUI persists the user's selection
(same trigger points as the existing `machine` save). Empty string means "no
override".

### Bootstrap query

When the TUI starts, the active query is resolved in this order:

1. `local.star.query` if non-empty.
2. Otherwise, `in:<PROJECT.star defaults.image>` if `defaults.image` is set.
3. Otherwise, the empty query (= show everything).

The bootstrap (case 2) is computed once at TUI startup, not persisted to
`local.star`. The user only writes to `local.star` when they explicitly save a
query (see "Header & gestures" below).

## TUI changes

### Header

Add one line under the existing Machine/Image header:

```
  [yoe]  Machine: qemu-x86_64  Image: base-image
  Query: in:base-image                                Units: 87/3104
```

- `Query:` shows the active query string, dim if it equals the saved default,
  bright if the user has typed something new.
- `Units: N/M` shows visible-after-filter / total. Replaces the `↑ N more` /
  `↓ N more` indicators only as a global counter — the per-page indicators stay,
  since they answer a different question (how much off-screen).

### Search bar (`/`)

`/` enters query mode (already wired). The bar accepts the full query language.

**Live filtering (required for v1).** Every keystroke re-parses the query and
re-filters the visible list. The user sees results update as they type; there is
no "submit" step before filtering kicks in. The `Units: N/M` counter updates
every keystroke too. This is how sndtool's `liveLibraryQuery` behaves and it is
the bar that matters at 3000 units — batched-on-Enter feels frozen at this
scale.

`Enter` exits query mode keeping the typed query active. `Esc` exits and reverts
to whatever was active before query mode opened.

### Tab completion (required for v1)

Inside the query bar, `Tab` completes the token under the cursor:

- **Field names** when the token has no `:` yet (`mo<Tab>` → `module:`).
  Candidates: `type`, `module`, `status`, `in`, plus the view shortcuts
  (`images`, `containers`, `failed`, `building`).
- **Values** after `:` based on the field:
  - `type:` → `image`, `container`, `unit` (static).
  - `status:` → `cached`, `building`, `failed`, `stale`, `pending` (static).
  - `module:` → every loaded module name in the project (live).
  - `in:` → every unit name in the project (live, ~3000 entries — needs
    prefix-matched filtering against the partial token, not a dump).
- **Bare terms** (no `:` and not a known field name): completion candidates are
  unit names whose name contains the partial token, same matching the bare-term
  filter would do. This makes completion feel like an inline autocomplete on the
  same data the filter is searching.

If there is exactly one candidate, `Tab` inserts it. If there are several, the
first `Tab` completes the longest common prefix; a second `Tab` shows a ghost
line of candidates under the bar. `Esc` dismisses the ghost line without
changing the query.

Completion is high-leverage at 3000 units and is non-negotiable in v1; the list
of candidates is finite and the matching is cheap, so there's no performance
reason to defer it.

### Snap-back

`\` (single keystroke, no query bar open) resets the active query to the saved
default. If there is no saved default, it clears the query. Predictable
round-trip: type whatever you want, hit `\` to come home.

### Save query

`S` (capital, mnemonic "save") writes the current query to `local.star` as the
new default. Status bar confirms with a one-line "saved query: <query>" flash.

### Persistence triggers

The TUI writes `local.star` only on explicit save (`S`) and on machine /
deploy-host change (existing behavior). It does not autosave query changes — the
user might be temporarily filtering and would not want every keystroke to alter
the project's saved state.

### Failure modes

| Case                                                | Behavior                                                                                                                       |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Query parse error (e.g. trailing `:`)               | Bar turns red, list freezes at last-valid query, error string shown after the bar.                                             |
| Unknown field (`fizz:foo`)                          | Same as parse error.                                                                                                           |
| Unknown value for known field (`type:gizmo`)        | Query is valid, list is empty, header reads `Units: 0/3104`.                                                                   |
| `in:nonexistent`                                    | Same as unknown value: empty list, no error.                                                                                   |
| Empty result                                        | List body shows a single dim line: `no units match`.                                                                           |
| Saved query references a unit that no longer exists | Treated as empty result. The query is left as-is in `local.star`; the user can fix it manually or pick a new one and `S`-save. |

### Highlighting

When the query has bare terms, those substrings are highlighted in the unit name
column (same approach as sndtool's `highlightText`). Field filters do not
highlight — they don't correspond to a substring in the visible row.

## Code shape

```
internal/tui/query/parser.go     # ParseQuery, Query type
internal/tui/query/match.go      # Query.Matches(unit, status, closures) bool
internal/tui/query/closure.go    # cached in:X expansion (DAG + runtime walk)
internal/tui/query/complete.go   # Tab-completion candidates
internal/tui/query/parser_test.go
internal/tui/query/match_test.go
internal/tui/query/closure_test.go
```

`internal/tui/app.go` changes:

- Replace `searchText string` and `filtered []int` with a single
  `query query.Query` plus its compiled match function.
- `m.units` stays as the full sorted list; the visible set is computed by
  filtering through `query.Matches`.
- `viewUnits` reads the active query from `m`, not from a local search-mode
  bool. Search mode is just "is the query bar focused?".

`internal/starlark/local.go`:

- `LocalOverrides.Query string` added.
- Parser recognises a `query =` kwarg on the `local()` call.
- `WriteLocalOverrides` emits `query = "..."` only when non-empty.

## Tests

- `parser_test.go`: every grammar form, including malformed inputs and
  case-insensitivity. Round-trip a parsed query back to its canonical string.
- `match_test.go`: synthetic project with ~10 units covering every type, module,
  status, and a small dep DAG. Exercise each field and every combination listed
  in the worked-examples table.
- `closure_test.go`: a unit with deps, with runtime_deps via PROVIDES, with a
  cycle, and the missing-unit case.
- `complete_test.go`: completion candidates for each cursor position.
- `local_test.go`: round-trip `query` field through write+read.

No new browser-driven UI test surface; this is all terminal text.

## What ships in v1 vs deferred

**v1:** the parser, the four fields above (`type`, `module`, `status`, `in`),
bare-term substring, the four view shortcuts, `local.star.query`, header line,
query bar editing, tab completion, snap-back, save-query, all failure modes,
highlighting on bare terms.

**Deferred (future work):**

- `dep-of:X` / `used-by:X` — reverse closure for impact analysis. Adds a reverse
  DAG and answers "what would break if I touch X". Worth shipping once someone
  needs it; not urgent.
- `class:` — only meaningful once `Unit.BuiltVia` (or equivalent) is tracked on
  the resolved unit. See the corresponding entry in `docs/roadmap.md` under
  Format / Modules. The query field name is reserved.
- `arch:`, `dirty:`, `since:` — straightforward additions; keep as one-bullet
  examples in the doc so the extension story is concrete.
- Saved named queries (`q:my-broken-things`) — not yet, but the parser shouldn't
  preclude them.

## Migration / compatibility

No on-disk format changes. `local.star` files written by older yoe still parse
cleanly (the new `query` field is optional). The TUI's existing substring-only
behavior is exactly the v1 behavior of "a query containing a single bare term",
so muscle memory carries over.
