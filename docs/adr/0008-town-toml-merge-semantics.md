# ADR-0008: `town.toml` Composition Merge Semantics

- **Status**: Proposed
- **Date**: 2026-03-18
- **Beads issue**: dgt-cfi
- **Deciders**: Aleksandar Tenev
- **Depends on**: ADR-0001 (town.toml format)
- **Blocks**: dgt-apu (`town-ctl` implementation), dgt-i36 (actuator design)

---

## Context

ADR-0001 introduced two composition mechanisms for `town.toml`:

1. **`includes`** — file composition via glob patterns (e.g.
   `includes = ["./rigs/*.toml"]`) that merge per-rig fragments into a single
   manifest before the Dolt write.
2. **Environment overlays** — a second TOML file (e.g. `town.prod.toml`) that
   overlays a base `town.toml` to produce an environment-specific manifest.

ADR-0001 explicitly deferred "merge semantics for `includes` and overlays" to
this ADR. The `town-ctl` actuator must implement these semantics before writing
to Dolt (dgt-i36). Ambiguity in the spec becomes a `town-ctl` correctness bug.

This ADR defines the exact merge semantics for both mechanisms.

---

## Definitions

**Base file** — the primary `town.toml` passed to `town-ctl apply`.

**Fragment file** — a TOML file matched by a glob pattern in `includes`. A
fragment file is a partial manifest: it may contain any top-level keys except
`version` and `[town]`.

**Overlay file** — a second TOML file (e.g. `town.prod.toml`) passed to
`town-ctl apply --overlay=town.prod.toml`. An overlay applies targeted
environment-specific overrides over the fully-composed base.

**Resolved manifest** — the final `TownManifest` struct produced after all
includes are merged and any overlay is applied. This is what `town-ctl` writes
to Dolt.

**Merge order** (high to low precedence):

```
overlay file  (highest — overrides everything)
    ↑
base file  (after includes merged in)
    ↑
fragment files  (lowest — in lexicographic order)
```

---

## Decisions

### Decision 1: `includes` — ordering guarantee

**Chosen**: Fragment files matched by `includes` glob patterns are resolved and
merged in **lexicographic order of their resolved absolute paths**. When multiple
glob patterns appear in the `includes` array, each pattern's matches are
collected and sorted, and then the pattern groups are concatenated in array order.

**Example**:

```toml
includes = ["./rigs/*.toml", "./teams/*.toml"]
```

Merge order: `rigs/backend.toml`, `rigs/frontend.toml` (lex order within the
first glob), then `teams/alpha.toml`, `teams/beta.toml` (lex order within the
second glob). Patterns are processed in declaration order.

**Rationale**: Lexicographic ordering is deterministic, machine-independent, and
produces reproducible resolved manifests regardless of filesystem inode order.
Alphabetical file names (`01-backend.toml`, `02-frontend.toml`) give operators
explicit control when order matters.

---

### Decision 2: `includes` — conflict resolution for `[[rig]]` entries

**Chosen**: A duplicate `rig.name` across any combination of the base file and
fragment files is a **hard error**. `town-ctl apply` aborts with a descriptive
message listing the conflicting files and the duplicate name.

```
town-ctl: error: duplicate rig name "backend"
  first defined in: town.toml (line 12)
  also defined in:  rigs/backend.toml (line 1)
```

**Rationale**: Silently using last-wins for rig entries would produce a resolved
manifest that is invisible to the operator. Two fragment files disageing on a rig
is a configuration error, not a composition operation. The same logic applies to
`[[role]]` entries: a duplicate `role.name` across base and fragments is a hard
error.

**`[[rig]]` and `[[role]]` are identity-keyed**: their `name` field is the
deduplication key. A fragment file that redefines an existing rig name is always
an error.

---

### Decision 3: `includes` — conflict resolution for non-array top-level keys

**Chosen**: For scalar and table keys outside `[[rig]]` / `[[role]]` (i.e.
`[defaults]`, `[secrets]`, `[town.agents]`), **last-wins** applies: the last
fragment file (in merge order) to define a key takes precedence, and the base
file overrides all fragments.

Specifically, merge precedence for non-array keys (low to high):

```
fragment files (lexicographic order, later = higher)
    ↑
base file  (always overrides fragments for identical keys)
```

**Rationale**: Operator-edited base `town.toml` should not be surprised by
fragmented defaults. The base file is the authoritative document; fragments
supply additive rig definitions and optional defaults that the base can override.
Last-wins within fragments follows the lexicographic ordering guarantee
(Decision 1), making it deterministic.

**`[town]` and `version` are locked** (see Decision 7).

---

### Decision 4: `includes` — circular include detection

**Chosen**: `town-ctl` detects circular `includes` references and aborts with a
hard error before any merging begins. Detection uses a depth-first traversal
with a visited-path set.

Fragment files may themselves contain `includes` fields (recursive composition).
Circular references are detected across all recursion levels:

```
town.toml → includes rigs/backend.toml
rigs/backend.toml → includes ../town.toml   ← ERROR: circular include
```

```
town-ctl: error: circular include detected
  include chain: town.toml → rigs/backend.toml → town.toml
```

Resolved absolute paths are used for cycle detection to handle symlinks and
relative `../` references correctly.

---

### Decision 5: Overlay files — deep merge for table keys

**Chosen**: Overlay files use **deep merge** semantics for TOML table keys. A
table key in the overlay is merged key-by-key into the base table — the overlay
does not replace the entire table. This continues recursively for nested tables.

**Example**:

```toml
# base town.toml
[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20
```

```toml
# town.prod.toml overlay
[defaults]
max_polecats = 30   # override only max_polecats
```

**Resolved**:

```toml
[defaults]
mayor_model   = "claude-opus-4-6"    # preserved from base
polecat_model = "claude-sonnet-4-6"  # preserved from base
max_polecats  = 30                   # overridden by overlay
```

**Rationale**: Shallow table merge (replacing the entire `[defaults]` table)
would silently drop all non-overridden keys from the base. This is the primary
footgun of YAML-style environment overlays. Deep merge means an overlay file
only needs to contain the keys it wants to change — it is minimal and reviewable.

---

### Decision 6: Overlay files — array semantics (replace, not append)

**Chosen**: Arrays in overlay files **replace** the corresponding array in the
base. Overlay arrays do not append to base arrays.

**Example**:

```toml
# base town.toml
includes = ["./rigs/*.toml", "./teams/*.toml"]
```

```toml
# town.prod.toml overlay
includes = ["./rigs/prod/*.toml"]   # replaces entire array
```

**Resolved**: `includes = ["./rigs/prod/*.toml"]`

**This is intentional** and is **explicitly documented as a footgun**: operators
who want to extend an array overlay must repeat all desired values. The
alternative — append-only overlays — leads to an impossible-to-disable base
array value without a separate "remove" notation. Replace semantics are simpler,
explicit, and avoid hidden accumulation.

**`[[rig]]` and `[[role]]` arrays in overlays**: overlay files may add new
`[[rig]]` or `[[role]]` entries (append to the list), or **replace** an existing
entry by providing a full `[[rig]]` block with the same `name`. A `[[rig]]` block
in an overlay with a name that exists in the base is a **full replacement** of
that rig's configuration (not a field-level merge within the rig).

---

### Decision 7: Locked keys — `version` and `town.name` cannot be overlaid

**Chosen**: Two keys are **locked** — they cannot be overridden by either
fragment files or overlay files:

| Locked key | Reason |
|------------|--------|
| `version` | The manifest schema version is a compatibility contract. Allowing an overlay to change `version` without changing the actual schema is deceptive. |
| `town.name` | The town name is a stable identity used as a namespace in Dolt tables, Bead IDs, and agent CLAUDE.md files. An overlay changing the town name would produce Dolt writes to a different namespace, silently diverging production from development. |

If a fragment file or overlay specifies `version` or `town.name` and the value
differs from the base file, `town-ctl` aborts with a hard error:

```
town-ctl: error: overlay file may not override locked key "town.name"
  base value:    "prod-town"
  overlay value: "dev-town"
  overlay file:  town.dev.toml
```

If the overlay specifies a locked key with the **same value** as the base, no
error is raised (idempotent).

---

## Truth Table

All examples assume base `town.toml` on the left and an overlay `town.prod.toml`
on the right. Merge order for the table: fragment files are merged first (not
shown); base overrides fragments; overlay overrides base.

| Case | Base | Overlay | Resolved | Notes |
|------|------|---------|----------|-------|
| Scalar override | `max_polecats = 20` | `max_polecats = 30` | `30` | Overlay wins |
| Scalar not in overlay | `mayor_model = "opus"` | _(absent)_ | `"opus"` | Base preserved |
| Scalar not in base | _(absent)_ | `max_polecats = 30` | `30` | Overlay adds |
| Array override | `includes = ["a.toml"]` | `includes = ["b.toml"]` | `["b.toml"]` | Replace semantics (Decision 6) |
| Array not in overlay | `includes = ["a.toml"]` | _(absent)_ | `["a.toml"]` | Base preserved |
| Nested table override | `[defaults] max_polecats = 20` | `[defaults] max_polecats = 10` | `max_polecats = 10` | Deep merge (Decision 5) |
| Nested table partial override | `[defaults] model = "opus", max = 20` | `[defaults] max = 10` | `model = "opus", max = 10` | Non-overridden keys preserved (Decision 5) |
| Locked scalar override (same value) | `town.name = "prod"` | `town.name = "prod"` | `"prod"` | No error — idempotent |
| Locked scalar override (different value) | `town.name = "prod"` | `town.name = "dev"` | ERROR | Hard error (Decision 7) |
| Duplicate rig in fragment | `[[rig]] name = "backend"` (base) | `[[rig]] name = "backend"` (fragment) | ERROR | Hard error (Decision 2) |
| New rig in overlay | _(no "frontend" rig)_ | `[[rig]] name = "frontend"` | frontend rig added | Appended to rig list |
| Existing rig in overlay | `[[rig]] name = "backend" max_polecats = 20` | `[[rig]] name = "backend" max_polecats = 30` | Full replacement | Entire rig config replaced (Decision 6) |

---

## Processing Order (Normative)

`town-ctl apply` resolves the manifest in this exact sequence. Steps are not
interchangeable:

```
1. Parse base file → base TownManifest
2. Detect and reject circular includes (Decision 4)
3. Collect fragment files:
     For each glob in base.Includes (in declaration order):
       Sort matched paths lexicographically (Decision 1)
       For each fragment path:
         Parse fragment → partial TownManifest
         Merge into working manifest (Decisions 2, 3)
4. Recursively resolve includes within fragments (repeat steps 2–3)
5. Apply overlay file (if --overlay flag provided):
     Parse overlay → partial TownManifest
     Deep-merge overlay into working manifest (Decisions 5, 6, 7)
6. Validate resolved manifest (go-validator + cross-field checks)
7. Resolve env-var interpolation in all path and value fields
8. Write resolved manifest to Dolt (single transaction)
```

---

## Error Catalogue

| Error | Trigger | `town-ctl` exit code |
|-------|---------|---------------------|
| `ERR_CIRCULAR_INCLUDE` | Include chain forms a cycle | 2 |
| `ERR_DUPLICATE_RIG` | Same `rig.name` in two source files | 2 |
| `ERR_DUPLICATE_ROLE` | Same `role.name` in two source files | 2 |
| `ERR_LOCKED_KEY_OVERRIDE` | Overlay changes `version` or `town.name` to a different value | 2 |
| `ERR_FRAGMENT_DEFINES_LOCKED_KEY` | Fragment file defines `version` or `[town]` | 2 |
| `ERR_GLOB_NO_MATCH` | A glob pattern in `includes` matches zero files | 1 (warning, not abort — empty include is legal) |
| `ERR_OVERLAY_PARSE` | Overlay file is not valid TOML | 2 |
| `ERR_FRAGMENT_PARSE` | Fragment file is not valid TOML | 2 |

**`ERR_GLOB_NO_MATCH` is a warning, not an error.** An `includes` pattern that
matches no files is treated as an empty contribution. Operators composing
environment-specific configs often have a base `includes = ["./rigs/*.toml"]`
that matches files in dev but not in a clean CI directory; this should not abort.

---

## Consequences

### What becomes easier

- **Environment parity**: `town.toml` + `town.prod.toml` overlay is the canonical
  pattern for managing environment differences. The diff between the two files is
  an exact record of what differs between environments — reviewable in a PR.
- **Per-rig fragments**: large deployments can split rig definitions into
  `rigs/backend.toml`, `rigs/frontend.toml` etc. and include them from a minimal
  root `town.toml`. Each rig file is a focused, reviewable artefact.
- **`town-ctl --dry-run`**: the resolved manifest is computable without a Dolt
  connection. `--dry-run` can print the full resolved manifest (after all merges
  and overlay) before any writes.

### New constraints introduced

- **Fragment files must not define `version` or `[town]`**. This restriction
  keeps fragments focused on additive rig/role definitions and optional defaults.
  `town-ctl` enforces this with `ERR_FRAGMENT_DEFINES_LOCKED_KEY`.
- **Array replace semantics are a footgun** (Decision 6). Operators who want to
  add one rig to a prod overlay must repeat all prod rigs in the overlay's
  `[[rig]]` entries. This is documented in `town-ctl --help` and the annotated
  example manifests (dgt-cub).
- **Lexicographic ordering couples include semantics to file naming**. Operators
  relying on precedence order must name files to enforce it. This is documented
  in the `includes` field comment in the JSON Schema (dgt-4gp).
- **Recursive includes add depth**. Fragment files that themselves contain
  `includes` create multi-level composition graphs. Circular detection handles
  the pathological case, but deeply nested includes are hard to reason about.
  `town-ctl` logs the full resolved include graph at `--verbose` level to aid
  debugging.

### Out of scope for this ADR

- `town-ctl` binary implementation (→ dgt-apu)
- JSON Schema changes for `includes` field (→ dgt-4gp)
- Annotated example manifests showing includes and overlay patterns (→ dgt-cub)
- Actuator design (standalone `town-ctl` architecture) (→ dgt-i36)
