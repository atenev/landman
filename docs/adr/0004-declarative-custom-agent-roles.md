# ADR-0004: Declarative Custom Agent Role Definitions

- **Status**: Proposed
- **Date**: 2026-03-17
- **Beads issue**: dgt-bfp
- **Deciders**: Aleksandar Tenev
- **Extends**: ADR-0001 (town.toml manifest format), ADR-0002 (Surveyor reconcile scope)

---

## Context

Gas Town has seven built-in agent roles: Mayor, Polecats, Witness, Refinery, Deacon, Dogs,
and Crew. These roles are hardcoded in the `gt` binary. Operators who need a new agent
archetype — a code Reviewer between Polecat and Refinery, a SecurityScanner on every new
branch, a PlanningAgent that breaks epics into Beads — have no mechanism to add one without
modifying `gt` source.

Gas Town's architecture provides the necessary substrate for extensibility without binary
modification:
- **CLAUDE.md** defines agent identity and behaviour — independently of `gt`
- **Dolt** is the shared state plane — accessible from any process
- **Beads** is the coordination primitive — writable from any agent or tool
- **The Surveyor** (ADR-0002) already reconciles `desired_topology` to actual state

A custom role is a Claude Code process with a CLAUDE.md identity, a trigger condition, a
supervision relationship, and a resource limit. All of these can be declared in `town.toml`
and stored in Dolt without touching `gt` internals.

ADR-0001 reserved `# [[rig.role]] → dgt-bfp` as a named extension slot in the manifest
skeleton. This ADR fills that slot with a complete design.

---

## Decisions

### Decision 1: Global `[[role]]` definitions with per-rig opt-in

**Chosen**: Custom roles are declared globally in `town.toml` as `[[role]]` array-of-tables
entries. Individual rigs opt in via `[rig.agents].roles = ["role-name", ...]`.

**Alternatives considered**:

**Option A — Inline `[[rig.role]]` per rig**

```toml
[[rig]]
name = "backend"
  [[rig.role]]
  name = "reviewer"
  # ... full spec repeated
```

Rejected because: the full role spec must be repeated for every rig that uses the role.
Rigs sharing the same role archetype will diverge silently over time. There is no single
source of truth for a role definition.

**Option B — Global `[[role]]` + per-rig opt-in (chosen)**

```toml
[[role]]
name = "reviewer"
# ... defined once

[[rig]]
name = "backend"
  [rig.agents]
  roles = ["reviewer"]
```

Chosen because: define once, enable per rig. Consistent with how `[defaults]` works —
global values, per-rig overrides. A role defined but not opted into by any rig is valid
(defined but inactive). Dolt diffs on role changes are localised to the single
`desired_custom_roles` row, not scattered across every rig that uses the role.

---

### Decision 2: CLAUDE.md as a file reference only — no inline content

**Chosen**: `[role.identity]` requires a `claude_md` path field pointing to a CLAUDE.md
file on the filesystem. Inline content (a `content` field in TOML) is not supported and
is a hard validation error.

**Rationale**:

1. **Maintainability**: a CLAUDE.md file can be edited, version-controlled, and diffed
   independently of `town.toml`. Inline content embeds agent identity in the infrastructure
   manifest — two different concerns in one file.
2. **Consistency**: every other Gas Town role identity is a CLAUDE.md file on disk. Custom
   roles following the same pattern are first-class Gas Town participants, not second-class
   inline definitions.
3. **Enforced at schema level**: `claude_md_path NOT NULL` with no `claude_md_content`
   column in `desired_custom_roles` makes inline content impossible at the Dolt layer, not
   just by convention.
4. **Path interpolation**: `claude_md = "${GT_HOME}/roles/reviewer/CLAUDE.md"` allows
   machine-portable manifests using the same interpolation mechanism as all other path
   fields (ADR-0001 Decision 6).

---

### Decision 3: Every custom role must declare a supervision relationship

**Chosen**: `[role.supervision]` with a required `parent` field is mandatory for every
`[[role]]` entry. `parent` must be a valid built-in role name or a defined custom role name.
`reports_to` (escalation target) is optional.

**Rationale**:

Gas Town's agent hierarchy is the safety mechanism that prevents runaway agents. Every
built-in role has a supervision relationship: Polecats report to Witness, Dogs report to
Deacon, Deacon reports to Mayor. A custom role without a declared parent exists outside the
hierarchy — it has no supervisor to escalate to when it hangs, no path for Mayor to drain
it, and no Deacon patrol covering its health.

Making `parent` required at the schema level (`parent_role NOT NULL` in
`desired_custom_roles`) ensures that no custom role can be declared without being placed in
the supervision tree. This is not a soft convention — it is enforced at apply time by
`town-ctl` and at the Dolt layer.

`parent_role` is validated as a string (built-in role list + parsed custom role names) rather
than a database FK to a `known_roles` enumeration table. A FK would require maintaining that
enumeration table in sync with `gt`'s internal built-in role set — coupling Dolt schema to
`gt` internals, the exact coupling ADR-0001 was designed to avoid.

---

### Decision 4: Custom roles supplement built-ins — they cannot replace them

**Chosen**: Custom roles extend the seven built-in roles. They cannot replace a built-in
role by name. `town-ctl` rejects a `[[role]]` whose `name` matches a built-in role name.

**Rationale**:

The built-in roles are managed by the `gt` binary: Mayor is spawned by `gt`, Polecats are
spawned by Witness under `gt` supervision, Refinery is a `gt` merge-queue process. Replacing
a built-in role would require `gt` to look up a custom role definition where it previously
looked for its own internal implementation — a binary modification.

**Behavioural interception without replacement**: custom roles can be *inserted* between
built-ins via Bead dependency chains. A Reviewer role processes a Polecat's output Bead
before Refinery's Bead unblocks (`bd dep add refinery-bead reviewer-bead`). This achieves
workflow insertion without binary modification — the built-in roles remain unchanged and
the custom role participates through the existing Beads coordination primitive.

Shadowing built-in names is a hard error at apply time to prevent confusion:
`town-ctl` rejects `name = "polecat"` with an explicit message.

---

### Decision 5: Scope model — rig-scoped roles use a junction table, town-scoped do not

**Chosen**: `scope = "rig"` roles require per-rig opt-in rows in `desired_rig_custom_roles`.
`scope = "town"` roles are active globally as long as they exist in `desired_custom_roles` —
no junction table.

**Alternatives considered**:

**Option A — `desired_town_custom_roles (role_name, enabled)` for symmetry**

Rejected because: it adds a table for a trivial case. A town-scoped role's "opt-in" is its
existence in `desired_custom_roles`. Enabling and disabling is done by adding or removing the
`[[role]]` entry from `town.toml` — the manifest is the single source of truth.

**Option B — Scope-aware query pattern (chosen)**

```sql
-- Rig-scoped: join junction table
SELECT r.* FROM desired_custom_roles r
JOIN desired_rig_custom_roles rr ON rr.role_name = r.name
WHERE r.scope = 'rig' AND rr.rig_name = ? AND rr.enabled = TRUE;

-- Town-scoped: existence implies activation
SELECT * FROM desired_custom_roles WHERE scope = 'town';
```

The Surveyor uses these two query patterns. The asymmetry is intentional and simple: town is
a single scope, rig has N scopes. The junction table exists only where it is necessary.

---

### Decision 6: Four trigger types — event triggers via Bead-polling for MVP

**Chosen**: Four trigger types: `bead_assigned`, `schedule`, `event`, `manual`. For
`type = "event"` triggers, the role's CLAUDE.md implements a Bead-polling loop to detect
events (`bd search --tag=event:<event-name> --status=open`). No `gt` hook mechanism is
required for MVP.

**Trigger type rationale**:

| Type | Mechanics | `gt` change? |
|------|-----------|--------------|
| `bead_assigned` | Wakes when a Bead with `assignee = <role-name>` appears | No |
| `schedule` | Cron-driven, same as `[[rig.formula]]` | No |
| `event` | CLAUDE.md polls for event Beads filed by other agents | No |
| `manual` | Human triggers via Mayor Bead | No |

`event` trigger implementation for MVP: existing roles that observe system events (branch
creation, PR open, merge) file a Bead tagged `event:<event-name>`. The custom role's
CLAUDE.md polls for these Beads and processes them. No new `gt` event emission hook is
needed. This defers the hook question to a future `gt` change while providing full
`event`-trigger semantics through the existing Beads coordination primitive.

**Explicit columns over JSON**: `trigger_schedule VARCHAR(64)` and `trigger_event VARCHAR(128)`
are separate columns rather than a `trigger_spec JSON` blob. This enables a CHECK constraint
at the Dolt layer enforcing that the correct field is non-null for the given trigger type —
invariant enforcement at the DB layer, not just in `town-ctl` validation.

---

### Decision 7: `max_instances` only — `max_concurrent` deferred

**Chosen**: `desired_custom_roles` carries `max_instances INT NOT NULL DEFAULT 1` only.
`max_concurrent` (throttling simultaneous active instances below the capacity ceiling) is
deferred.

**Rationale**:

For ephemeral roles, `max_instances` sets the capacity ceiling and is the primary operational
knob operators need. `max_concurrent` is a throttling mechanism for roles where spawn cost
is high (Claude Code startup is non-trivial) or where downstream services need protection
from burst load. Neither concern is validated in practice at this stage.

Adding `max_concurrent` later is a non-breaking schema addition — a new nullable column with
a sensible default. Deferred to when throttling becomes a real operational concern.

---

## Consequences

### What becomes easier

- **Role extensibility without `gt` fork**: new agent archetypes are a `town.toml` change
  and a CLAUDE.md file. No binary modification. Deploy a new custom role by adding a
  `[[role]]` block and running `town-ctl apply`.
- **Workflow insertion**: a Reviewer, SecurityScanner, or Documenter can be inserted between
  existing built-in roles via Bead dependency chains — without touching `gt` internals.
- **Surveyor handles custom roles automatically**: the Surveyor's existing reconcile loop
  (desired vs actual diff → Dog Beads → verify) extends to custom roles by widening its diff
  scope. No new Surveyor mechanism required.
- **Custom roles are first-class Gas Town participants**: they use Dolt for state, Beads for
  coordination, CLAUDE.md for identity — the same primitives as every built-in role.
- **GitOps-friendly**: custom role definitions live in `town.toml` alongside rig and capacity
  config. A PR adding a `[[role]]` block is a reviewable, auditable topology change.

### New constraints introduced

- **`actual_custom_roles` Dolt table** must exist for the Surveyor to diff against.
  The Surveyor needs to know which custom role instances are currently running per rig.
  Schema design deferred to dgt-fkm (actual_topology schema).
- **Surveyor reconcile scope extends to custom roles**: the Surveyor must diff
  `desired_custom_roles` + `desired_rig_custom_roles` against `actual_custom_roles` and
  file Dog Beads for start/stop operations. Surveyor CLAUDE.md (dgt-9tj) must specify
  the extended diff logic.
- **Built-in role name reservation**: `town-ctl` maintains a hardcoded list of reserved
  built-in role names (`mayor`, `polecat`, `witness`, `refinery`, `deacon`, `dog`, `crew`).
  If Gas Town adds a new built-in role in a future `gt` version, the reserved list must be
  updated in `town-ctl` to prevent shadowing.
- **`desired_topology_versions` applies here**: `desired_custom_roles` and
  `desired_rig_custom_roles` carry no per-row `schema_version` column — versioning is via
  `desired_topology_versions` per ADR-0003.

### Out of scope for this ADR

- `Role` Go structs and JSON Schema (→ dgt-lai)
- `desired_custom_roles` and `desired_rig_custom_roles` DDL detail (→ dgt-uxa)
- `actual_custom_roles` Dolt table design (→ dgt-fkm)
- Surveyor CLAUDE.md extensions for custom role reconcile (→ dgt-9tj)
- Annotated `town.toml` examples with custom roles (→ dgt-rlf)
- CLAUDE.md template inheritance / role `extends` (→ dgt-90x, V2)
- Custom roles replacing built-in roles (requires `gt` modification, future)
- `max_concurrent` throttling (→ deferred until operational need validated)
- K8s operator custom role support (→ dgt-3j8)
