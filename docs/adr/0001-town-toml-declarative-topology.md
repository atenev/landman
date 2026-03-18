# ADR-0001: Declarative Town Topology — Format and Actuator Design

- **Status**: Proposed (amended by ADR-0003, ADR-0004)
- **Date**: 2026-03-15
- **Beads issue**: dgt-bp7
- **Deciders**: Aleksandar Tenev

---

## Context

Gas Town is a multi-agent AI coding orchestrator (~189k lines of Go). It runs 20–30 Claude
Code instances in parallel on isolated git worktrees, coordinated by seven specialised agent
roles (Mayor, Polecats, Witness, Refinery, Deacon, Dogs, Crew).

Today, setting up a Gas Town instance requires a sequence of imperative CLI commands:

```bash
gt install
gt rig add --repo=/path/to/repo --branch=main
gt config set mayor.model claude-opus-4-6
gt config set polecats.max 20
# … repeated per rig, per environment
```

There is no single artifact that describes a complete Gas Town topology. Recreating an
environment after a crash, onboarding a new contributor, or reviewing an infrastructure change
in a PR requires reading CLI history or tribal knowledge.

Gas Town already has **partial declarative DNA**:
- Formulas (TOML files describing scheduled agent workflows)
- CLAUDE.md files (declarative agent identity and constraints)
- Beads issues (structured work items with dependency graphs)
- The GUPP invariant (crash-resilience protocol)

The goal of this ADR is to extend that DNA to **topology**: define a manifest format that
describes the full desired state of a Gas Town instance — rigs, agents, models, schedules,
capacity — and decide how that manifest drives actual Gas Town state.

---

## Decisions

### Decision 1: TOML as the manifest format

**Chosen**: TOML

**Alternatives considered**:

| Format | Reason rejected |
|--------|-----------------|
| YAML   | Implicit type coercions (Norway problem), indentation-sensitive, footguns in multi-document files. Familiar to K8s practitioners but wrong tradeoffs for a Go-native CLI tool. |
| CUE    | Excellent schema validation and constraints, but steep learning curve, immature Go tooling, and overkill when constraints can be expressed via `go-validator` struct tags. |
| JSON   | No comments, verbose, hostile to human editing. |

**Rationale for TOML**:
1. Gas Town already uses TOML for Formulas — consistent ecosystem, no new tooling.
2. Go has first-class TOML support (`pelletier/go-toml`).
3. `[[array-of-tables]]` syntax makes rig definitions explicitly delimited and produces
   clean, reviewable git diffs.
4. No footguns: no implicit type coercions, no indentation sensitivity.
5. Validation belongs in Go struct tags (`go-validator`) and JSON Schema — not the config
   language itself.

**Future path**: If schema validation becomes a first-class user concern, CUE can be layered
on top as a linting step without changing the underlying TOML format.

---

### Decision 2: Dolt as the actuator coupling point — not the `gt` CLI

**Chosen**: Actuator writes desired state to Dolt tables. Gas Town converges by watching Dolt.

**Alternatives considered**:

**Option A — Tight `gt apply` (imperative CLI inside gt binary)**

```
town.toml → gt apply → Gas Town internals
```

`gt` owns schema parsing, validation, and directly mutates its own internal state.
Rejected because:
- Manifest format is permanently coupled to Gas Town's release cycle.
- Impossible to consume `town.toml` from an external tool (CI, K8s operator, GitOps
  controller) without importing Gas Town internals.
- CLI UX and topology reconcile logic become entangled.

**Option B — Actuator calls `gt` CLI as its API**

```
town.toml → actuator → gt rig add / gt status --json → Gas Town
```

Like `helm` calling `kubectl`. Rejected because:
- Gas Town's CLI output format (`gt status --json`) becomes a versioned public contract,
  creating a hidden coupling as fragile as the internal one.
- Shell-parsing of CLI output is brittle across Gas Town versions.

**Option C — Dolt as the shared state plane (chosen)**

```
town.toml → actuator → Dolt (desired_topology tables) → Gas Town converges
```

The actuator translates `town.toml` into rows in Dolt's `desired_topology` schema. Gas
Town's Mayor and Deacon watch those tables via Dolt's change feed and converge to match.

**Why this is correct**:
1. **Zero CLI coupling**: the actuator speaks SQL to Dolt, not shell to `gt`. The Dolt
   schema is an explicit, versioned contract — far more stable than CLI output.
2. **Natural audit trail**: Dolt is git-for-SQL. Every `town.toml` apply is a Dolt commit.
   The full history of desired-state changes is queryable and diffable.
3. **Reactive convergence**: Gas Town responds to Dolt changes in near-real-time. No
   polling loop required in the actuator.
4. **Multi-actuator topology**: the same `town.toml` can be consumed by:
   - A local `town-ctl` binary (CLI, dev laptop)
   - A K8s operator (production cluster, dgt-3j8) — operator writes to Dolt
   - A Flux/ArgoCD plugin (GitOps trigger)
5. **Dolt transaction = atomic apply**: a `town.toml` apply is a single Dolt transaction.
   Partial failure rolls back cleanly.
6. **Disaster recovery**: any machine with `town.toml` and Dolt access can reconstruct
   the full desired topology from scratch.

---

### Decision 3: `town-ctl` as a standalone actuator binary

**Chosen**: a separate binary `town-ctl`, independent of the `gt` binary.

`town-ctl` is the **manifest-to-Dolt translator**. Its only responsibilities:

1. Parse and validate `town.toml` against the JSON Schema.
2. Resolve `includes` and environment overlays.
3. Resolve secrets (env-var interpolation, secrets file) — secrets never leave the
   actuator process.
4. Diff resolved desired state against current `desired_topology` tables in Dolt.
5. Write atomic Dolt transaction.
6. Support `--dry-run` (print structured plan, no writes).

`town-ctl` has **no knowledge of Gas Town internals**. It speaks only to Dolt. This makes
it independently testable, versionable, and replaceable.

Gas Town's `gt` binary remains the **agent runtime**. It watches Dolt and acts. The
separation of intent (town-ctl) from execution (gt) is the core architectural principle.

---

### Decision 4: Secrets never written to Dolt

Secrets are resolved by `town-ctl` at apply time and injected as environment variables into
Gas Town agent processes. They are never written to:
- The `town.toml` file (safe to commit to git)
- Dolt tables (audit log must not contain credentials)
- Log files

The manifest references secrets by env-var name:

```toml
[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"
```

`town-ctl` fails fast if any referenced env var is unresolvable at apply time.

An optional `file = "~/.gt/secrets.toml"` path (gitignored) overrides inline env-var refs
for environments where env vars are inconvenient. Path interpolation applies (see Decision 6).

---

### Decision 5: `version` field as the compatibility contract

Every `town.toml` carries a top-level `version = "1"` field. `town-ctl` refuses to apply a
manifest whose version it does not understand, printing a clear error:

```
town-ctl: manifest version "2" requires town-ctl >= 0.2.0 (current: 0.1.3)
```

This creates an explicit, enforceable compatibility contract between the manifest schema and
the actuator — decoupled from Gas Town's own versioning. Schema evolution happens through
version bumps, not silent behavioural changes.

---

### Decision 6: Path interpolation in all path fields

All fields that accept file system paths support `${VAR}` interpolation resolved at apply
time by `town-ctl`:

```toml
[[rig]]
repo           = "${PROJECTS_DIR}/backend"
mayor_claude_md = "${GT_HOME}/rigs/backend/CLAUDE.md"

[secrets]
file = "${HOME}/.gt/secrets.toml"
```

Supported variables (at minimum): `${HOME}`, `${GT_HOME}`, any env var present at apply
time. This prevents brittle absolute paths that break across machines and users.

---

## Consequences

### What becomes easier

- **GitOps**: `town.toml` can live in a git repo. A CI commit triggers `town-ctl apply`,
  and Gas Town converges automatically. No imperative runbooks.
- **Environment parity**: `town.toml` + `town.prod.toml` overlay describes dev and prod
  topology in one place. Differences are reviewable in PRs.
- **Disaster recovery**: full topology rebuild from `town.toml` + Dolt backup.
- **Auditability**: every topology change is a Dolt commit — who changed what, when.
- **Multi-actuator**: K8s operator (dgt-3j8), local CLI, and GitOps controller all consume
  the same manifest format.
- **Cost optimisation**: per-rig model overrides (`polecat_model`, `max_polecats`) fall out
  of the defaults-inheritance chain without a special mechanism.

### New constraints introduced

- The Dolt `desired_topology` schema becomes a **versioned public contract**. Breaking
  changes require a migration path.
- `town-ctl` and `gt` must agree on the Dolt schema version. ~~A `schema_version` column in
  each table enforces this at runtime.~~ **Superseded by ADR-0003**: schema versioning is
  handled by the `desired_topology_versions` table — no per-row `schema_version` column on
  any `desired_topology` table.
- The `gt` binary must implement a **Dolt change-feed watcher** if it does not already.
  This is the only new internal requirement Gas Town must satisfy.

### Out of scope for this ADR

- The exact Dolt table schema (→ dgt-9ft)
- Merge semantics for `includes` and overlays (→ dgt-cfi)
- Go struct definitions and JSON Schema (→ dgt-4gp)
- Annotated example manifests (→ dgt-wpk)
- `town-ctl` binary implementation (→ dgt-i36)
- K8s CRD design (→ dgt-3j8)

---

## Reference: Canonical Manifest Skeleton

```toml
# town.toml
version = "1"

[town]
name      = "my-town"
home      = "${GT_HOME}"
dolt_port = 3306

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"
# file = "${HOME}/.gt/secrets.toml"   # alternative: external secrets file

# Compose from per-rig files (resolved before Dolt write)
includes = ["./rigs/*.toml"]

[[rig]]
name    = "backend"
repo    = "${PROJECTS_DIR}/backend"
branch  = "main"
enabled = true

  [rig.agents]
  mayor        = true
  witness      = true
  refinery     = true
  deacon       = true
  max_polecats = 30
  polecat_model = "claude-haiku-4-5-20251001"   # override default
  mayor_claude_md = "${GT_HOME}/rigs/backend/CLAUDE.md"

  [[rig.formula]]
  name     = "nightly-tests"
  schedule = "0 2 * * *"

  # Reserved extension slots (not yet implemented):
  # [rig.cost]          → dgt-afk (cost controls)
  # [[rig.role]]        → dgt-bfp (custom roles)
```
