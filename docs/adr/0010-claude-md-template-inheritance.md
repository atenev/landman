# ADR-0010: CLAUDE.md Template Inheritance for Custom Roles

- **Status**: Accepted
- **Date**: 2026-03-20
- **Beads issue**: dgt-90x
- **Deciders**: Aleksandar Tenev
- **Extends**: ADR-0004 (declarative custom agent roles), ADR-0005 (K8s operator CRD design)

---

## Context

ADR-0004 introduced custom agent roles in Gas Town. Each role declares a `claude_md` path
pointing to a CLAUDE.md file on the filesystem. Roles are fully self-contained — there is no
mechanism to share common behaviour between roles without duplicating content across CLAUDE.md
files.

Use cases that motivate inheritance:

1. **SecurityReviewer** that extends a base code-review CLAUDE.md and adds security scanning
   instructions. Without inheritance, the security-review instructions must either be duplicated
   into the base file or all common content must be duplicated into the SecurityReviewer's file.

2. **SeniorReviewer** that extends **Reviewer** and adds stricter criteria. Teams frequently
   create role hierarchies with escalating capability levels; inheritance enables a layered
   specification without copy-paste.

3. **Specialised Polecats** where multiple custom roles share a common "polecat-style"
   CLAUDE.md base but differ only in domain-specific additions (frontend, backend, data).

ADR-0004 explicitly deferred this to V2 (dgt-90x). This ADR fills that deferral with a
complete design and implementation.

---

## Decisions

### Decision 1: String concatenation as the inheritance model

**Chosen**: Merged CLAUDE.md content is produced by concatenating the full content of the
base role's CLAUDE.md file followed by a human-readable separator and then the full content
of the inheriting role's CLAUDE.md file.

```
<base content>

---
<!-- extends override -->

<override content>
```

**Alternatives considered**:

**Option A — Section-level overrides (e.g., replace `## Goals` with override's `## Goals`)**

Rejected. Section-level merging requires parsing CLAUDE.md files as structured documents
with a defined section schema. CLAUDE.md files are free-form Markdown; Gas Town has no
schema for their sections. Parsing and merging free-form Markdown is fragile and couples
the inheritance mechanism to CLAUDE.md authoring conventions that operators do not follow
uniformly.

**Option B — Prepend/append snippets (partial concatenation)**

Rejected. Partial concatenation (e.g., a `prefix` field and a `suffix` field) adds
configuration surface area without providing meaningful structure. Operators who need
conditional override behaviour are better served by crafting their CLAUDE.md files so that
Claude prioritises later instructions over earlier ones — a well-understood behaviour of
LLM prompting.

**Option C — String concatenation (chosen)**

Chosen because it is simple, predictable, and requires no CLAUDE.md schema. The base
content appears first (foundational context), the override content follows and takes
priority for any conflicting instructions. This matches how operators naturally layer
Claude prompts: general instructions first, specific overrides after.

---

### Decision 2: Extends references custom roles only — built-in roles excluded

**Chosen**: The `extends` field in `[role.identity]` may only reference other custom
`[[role]]` entries defined in the same `town.toml` manifest. Referencing a built-in role
name (`mayor`, `polecat`, `witness`, `refinery`, `deacon`, `dog`, `crew`) is a hard
validation error.

**Rationale**:

Built-in roles do not declare `claude_md` paths in the manifest — their identities are
implementation details of the `gt` binary. Gas Town does not maintain a stable, versioned,
filesystem path for built-in CLAUDE.md files that external manifests can depend on.

Allowing `extends = "polecat"` would require:
1. A stable, published CLAUDE.md path for every built-in role (`${GT_HOME}/roles/polecat/CLAUDE.md`).
2. A guarantee that this file exists at apply time on every host running `town-ctl`.
3. A versioning promise: if `gt` updates the polecat CLAUDE.md, what happens to derived roles?

None of these constraints are reasonable for a first implementation. Operators who want
polecat-like behaviour in a custom role should copy the relevant polecat instructions into
a base custom role's CLAUDE.md, then extend that base role. This is explicit and auditable.

The reserved built-in role namespace already prevents operators from naming a custom role
`polecat` (ADR-0004, Decision 4). Excluding built-ins from `extends` is the natural
complement to that restriction.

---

### Decision 3: Merged file generated at apply time; merged path stored in Dolt

**Chosen**: When a role declares `extends`, `town-ctl apply` generates a merged CLAUDE.md
file on the filesystem before writing to Dolt. The `claude_md_path` column in
`desired_custom_roles` stores the path of the merged file, not the role's own `claude_md`
path. The `extends_role` column stores the source relationship for auditability.

**Merge output path convention**: `${GT_HOME}/roles/merged/<role-name>.md`

**Rationale**:

The Surveyor reads `claude_md_path` from Dolt to spawn Claude Code processes. If inheritance
were resolved at spawn time, the Surveyor would need to:
1. Know that a role has an `extends` relationship (stored in a separate column).
2. Locate the base role's `claude_md_path`.
3. Merge the two files before spawning.

This adds inheritance-resolution logic to the Surveyor's spawn path — a real-time, hot
code path that runs for every agent spawn. Failures in merge (missing base file) would
manifest as spawn failures, far from the apply step where the problem was introduced.

By resolving at apply time:
- The Surveyor spawn path is unchanged: it reads `claude_md_path` and uses it directly.
- File-not-found errors for base or override CLAUDE.md files are caught at apply time,
  not at spawn time.
- `ValidateApplyTimeFS` checks that both the base role's `claude_md` and the inheriting
  role's `claude_md` exist before any Dolt write proceeds.
- The merged file is a stable, inspectable artifact at a known path — operators can read
  it to understand what the agent will see.

**`extends_role` column**: Stored in Dolt for auditability. The Surveyor does not use it.
Future tooling (e.g., `town-ctl export`) can reconstruct the `extends` field from it.

---

### Decision 4: Multi-hop extends chains are supported; cycles are rejected at parse time

**Chosen**: A role may extend a role that itself extends another role, forming an
inheritance chain of arbitrary depth. Chain depth is validated at parse time:
`crossValidate` walks the full extends chain for each role and returns an error if a
cycle is detected. Cycle detection uses a depth-bounded walk (max hops = `len(m.Roles)`)
with a visited-set guard.

**Merge semantics for chains**: Files are concatenated in root-first order. For a chain
`root ← mid ← leaf`:

```
<root content>

---
<!-- extends override -->

<mid content>

---
<!-- extends override -->

<leaf content>
```

Root instructions provide the widest context; leaf instructions are most specific and
take precedence when Claude encounters conflicting guidance.

**Rationale**:

Multi-hop chains occur naturally in role taxonomies: a general Reviewer, a StrictReviewer,
a SecurityStrictReviewer. Restricting to single-hop would force operators to manually
flatten chains, defeating the purpose of inheritance.

Cycle detection is mandatory at parse time — a cyclic `extends` chain would cause
unbounded recursion in the chain resolver and must be caught before any filesystem
operation.

---

## Implementation

### New `extends` field in `RoleIdentity` (`pkg/manifest/manifest.go`)

```toml
[[role]]
name  = "senior-reviewer"
scope = "rig"

  [role.identity]
  claude_md = "${GT_HOME}/roles/senior-reviewer/CLAUDE.md"
  extends   = "reviewer"        # <-- new optional field

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"
```

### Validation (`pkg/manifest/validate.go`)

- `extends` must reference a custom role defined in the same manifest (not a built-in).
- Self-reference (`extends = <own-name>`) is rejected.
- Cycle detection via `detectExtendsCycle`: depth-bounded walk with visited-set guard.
- `ValidateApplyTimeFS`: when `extends` is set, checks that the base role's `claude_md`
  path exists in addition to the inheriting role's own path.

### Merge utilities (`pkg/manifest/claudemd_merge.go`)

- `MergeClaudeMDFiles(basePath, overridePath string) (string, error)` — two-file merge.
- `ResolveExtendsChain(name string, roles []RoleSpec) ([]string, error)` — returns the
  ordered chain of claude_md paths from root to the named role.
- `MergeExtendsChain(chain []string) (string, error)` — merges N files in chain order.

### Dolt storage (`pkg/townctl/customroles.go`)

New `extends_role VARCHAR NULL` column in `desired_custom_roles`:
- Populated from `role.Identity.Extends`.
- `claude_md_path` stores the merged output path when `extends` is set.
- SQL UPSERT and UPDATE include `extends_role`.

---

## Consequences

### What becomes easier

- **Role composition without duplication**: a SecurityReviewer can extend a base Reviewer
  CLAUDE.md and add only the delta. The base file is a single source of truth.
- **Layered role hierarchies**: teams can define Junior, Senior, and Lead reviewer roles
  as a three-level extends chain, each adding instructions to the previous level.
- **Surveyors unchanged**: the Surveyor spawn path reads `claude_md_path` and uses it
  directly. No Surveyor changes are required.

### New constraints introduced

- **`${GT_HOME}/roles/merged/` must be writable at apply time**: `town-ctl apply` writes
  merged CLAUDE.md files to this directory. The directory must exist and be writable by
  the user running `town-ctl`. Error is surfaced at apply time, not at spawn time.
- **Base role `claude_md` must exist at apply time**: `ValidateApplyTimeFS` now checks
  base role paths in addition to the inheriting role's own path. Stale base file paths
  (e.g., after moving the base role's CLAUDE.md) are caught at apply time.
- **`desired_custom_roles` DDL change**: the `extends_role VARCHAR NULL` column must be
  added via a migration. The schema version in `desired_topology_versions` is bumped to 2
  for `desired_custom_roles`.
- **`town-ctl export` must reconstruct `extends`**: when exporting Dolt state back to
  `town.toml`, the exporter must read `extends_role` and emit the `extends` field.
  This is tracked as follow-up work.

### Out of scope

- Section-level override (replace specific Markdown sections with override's version).
- Built-in role inheritance (requires stable published CLAUDE.md paths for built-ins).
- `town-ctl export` reconstruction of `extends` field (→ follow-up issue).
- `desired_custom_roles` DDL migration for `extends_role` column (→ migration 003).
