## Why

Gas Town setup is entirely imperative: `gt install`, `gt rig add`, `gt config set` — repeated
per rig, per environment. There is no single artifact describing a complete Gas Town topology,
making crash recovery, environment parity, and GitOps workflows impossible without tribal
knowledge or CLI history.

## What Changes

- Introduce `town.toml`, a TOML manifest declaring full Gas Town desired state: town metadata,
  rigs, per-rig agent configuration, Formula schedules, and global defaults with inheritance.
- Introduce `town-ctl`, a standalone binary (independent of `gt`) that translates `town.toml`
  into rows in Dolt `desired_topology` tables via atomic SQL transaction.
- Define the `desired_topology` Dolt schema as the versioned contract between `town-ctl` and
  the rest of the system. Every apply is an auditable Dolt commit.
- `town.toml` supports `includes` (per-rig overlay files) and `--env` flag overlays. Explicit
  merge semantics prevent silent configuration drift.
- `version` field enforces a compatibility contract between manifest schema and `town-ctl`.
- Secrets resolved by `town-ctl` at apply time via env-var interpolation; never written to
  Dolt or `town.toml`.
- `town-ctl apply` launches the Surveyor process if `[town.agents] surveyor = true` — it
  ensures the process is running but has no knowledge of its internal operation.

## Capabilities

### New Capabilities

- `town-toml-manifest`: Declarative TOML manifest format covering town metadata, rigs, per-rig
  agent config, Formula schedules, secrets, includes, and `[town.agents]` process lifecycle.
  Includes Go struct definitions and JSON Schema for validation.
- `town-ctl-actuator`: Standalone binary that parses/validates `town.toml`, resolves overlays
  and secrets, diffs against current Dolt `desired_topology`, writes an atomic transaction.
  Supports `--dry-run` and `--env` flags. Ensures declared agent processes (Surveyor) are
  running. No knowledge of Gas Town internals — speaks only SQL to Dolt.
- `dolt-desired-topology`: Dolt SQL schema for `desired_topology` tables written by `town-ctl`
  and read by the Surveyor. Defines the versioned contract. Tables: `desired_rigs`,
  `desired_agent_config`, `desired_formulas`. Includes `schema_version` column.
- `manifest-includes`: Merge semantics for composing multi-rig topologies from per-rig TOML
  files and environment-specific overlays. Explicit rules — no silent coercions.

### Modified Capabilities

<!-- None — this is entirely new infrastructure. No existing Gas Town code is modified. -->

## Impact

- New binary: `town-ctl` (Go, separate from and independent of `gt`)
- New Dolt tables: `desired_topology` schema (`desired_rigs`, `desired_agent_config`,
  `desired_formulas`)
- `gt` binary: **no modification required**. `town-ctl` writes desired state to Dolt.
  Convergence is handled by the Surveyor agent (see `surveyor-topology-reconciler` change).
- Existing `gt` imperative CLI remains fully functional — this adds a declarative path
- `town.toml` is safe to commit to git (no secrets); pairs with gitignored `secrets.toml`
- The Dolt `desired_topology` schema becomes a versioned public contract — breaking changes
  require a migration path enforced by `schema_version` columns
