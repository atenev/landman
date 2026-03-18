## Context

Gas Town (~189k lines of Go) is an AI-native multi-agent coding orchestrator. Setup today is a
sequence of imperative `gt` CLI calls with no single artifact describing desired state. Gas Town
already has partial declarative DNA — TOML Formulas, CLAUDE.md agent identities, Beads issues —
but topology (rigs, agents, capacity) is not yet declarative.

The system uses Dolt (git-for-SQL) as its shared coordination plane, accessible to all agents
and external tools via standard SQL. This is the key integration point: `town-ctl` writes
desired state into Dolt; the Surveyor (separate change) reads it and reconciles.

Constraint: **no modification to the `gt` binary**. The `town-ctl` actuator and all Dolt schema
changes are fully external to `gt`. Gas Town's existing agent roles (Dogs, Deacon, Mayor) pick
up operation Beads through the existing coordination mechanism they already use.

## Goals / Non-Goals

**Goals:**
- Define `town.toml` TOML manifest format with complete schema (Go structs + JSON Schema)
- Implement `town-ctl` binary: parse → validate → resolve → diff → atomic Dolt write
- Define `desired_topology` Dolt schema as the versioned public contract
- Define `includes` and `--env` overlay merge semantics explicitly
- Support `--dry-run` (structured diff output, no writes)
- Ensure secrets are never written to Dolt or `town.toml`
- Version compatibility contract: `town-ctl` refuses unknown manifest versions

**Non-Goals:**
- Convergence execution — delegated to the Surveyor (`surveyor-topology-reconciler` change)
- `actual_topology` Dolt schema — owned by `surveyor-topology-reconciler`
- K8s operator / CRD design — deferred (`dgt-3j8`)
- Cost controls and custom role extensions — deferred (`dgt-afk`, `dgt-bfp`)
- Modifying `gt` in any way

## Decisions

### D1: TOML as the manifest format

TOML over YAML (Norway problem, indentation footguns), JSON (no comments, hostile to editing),
and CUE (steep learning curve, immature Go tooling). Gas Town already uses TOML for Formulas —
consistent ecosystem. `pelletier/go-toml` provides first-class Go support. `[[array-of-tables]]`
syntax gives explicit rig delimiters and clean git diffs. Validation belongs in Go struct tags
(`go-validator`) and JSON Schema, not the config language.

### D2: Dolt as the actuator coupling point — not `gt` CLI

Three options evaluated:
- **Tight `gt apply`**: manifest format permanently coupled to `gt` release cycle. Rejected.
- **Actuator calls `gt` CLI**: `gt status --json` becomes a versioned public contract, fragile
  across versions. Rejected.
- **Dolt as shared state plane (chosen)**: `town-ctl` speaks SQL to Dolt. The `desired_topology`
  schema is the explicit, versioned contract. Natural audit trail (Dolt is git-for-SQL). Reactive
  — Surveyor watches Dolt change feed, no polling. Multi-actuator: `town-ctl`, K8s operator,
  GitOps all write the same schema. Atomic transaction = clean partial-failure rollback.

### D3: `town-ctl` as a standalone binary, independent of `gt`

`town-ctl` has exactly six responsibilities:
1. Parse and validate `town.toml` against JSON Schema
2. Resolve `includes` and `--env` overlays per explicit merge semantics
3. Resolve secrets (env-var interpolation / secrets file) — secrets never leave the process
4. Diff resolved desired state against current `desired_topology` rows in Dolt
5. Write atomic Dolt transaction (single commit)
6. If `[town.agents] surveyor = true`, ensure the Surveyor process is running

`town-ctl` has no knowledge of Gas Town internals beyond: (a) the Dolt `desired_topology`
schema, (b) how to launch the Surveyor process. It cannot introspect `gt` state.

### D4: Secrets resolution at apply time, never persisted

`town.toml` references secrets by env-var name: `anthropic_api_key = "${ANTHROPIC_API_KEY}"`.
`town-ctl` interpolates at apply time, fails fast on unresolvable refs. Secrets injected as
env vars into agent processes — never written to Dolt, logs, or `town.toml`. Optional
`secrets.toml` file (gitignored) for environments where env vars are inconvenient.

### D5: `version` field as the compatibility contract

`version = "1"` at top-level. `town-ctl` refuses unknown versions with a clear error including
the minimum `town-ctl` version required. Schema evolution through version bumps, not silent
behavioural changes. Decoupled from `gt` versioning.

### D6: Explicit `includes` merge semantics

`includes = ["./rigs/*.toml"]` resolved before Dolt write. Merge rules:
- Array fields (e.g., `[[rig]]`): included rigs appended to base list
- Scalar fields: included file wins; base manifest wins if included file is silent
- `--env` overlay applied last, overrides everything
- Conflicts (same rig name defined twice) are a hard error, not silent last-wins

### D7: `desired_topology` schema versioning via `schema_version` column

Each `desired_topology` table carries a `schema_version` integer column. `town-ctl` writes the
version it understands. The Surveyor checks `schema_version` on read and refuses to act on
an unknown version, filing an escalation Bead to Mayor. This enforces that `town-ctl` and the
Surveyor agree on schema without requiring a coordinated deploy.

## Risks / Trade-offs

- **`desired_topology` schema as a public contract**: breaking changes require a migration path.
  Mitigated by `schema_version` column — both `town-ctl` and Surveyor hard-fail on mismatch.
- **`town-ctl` launching the Surveyor**: this is the one place `town-ctl` has operational
  knowledge (process launch). Mitigated by: `town-ctl` only checks if the PID is alive and
  runs the launch command from `[town.agents]`; it does not know what the Surveyor does.
- **`includes` glob ordering**: file system glob order is not guaranteed across OS. Mitigated
  by: `includes` paths must be explicit or the merge result is deterministic (append semantics
  for arrays, explicit error on name conflict).
- **Partial apply on Dolt write failure**: the Dolt transaction is atomic — partial failure
  rolls back entirely. `town-ctl` exits non-zero and prints the failed SQL. Re-running is safe
  (idempotent diff).

## Migration Plan

1. Deploy `town-ctl` binary alongside existing `gt` install — no `gt` change required
2. Author `town.toml` describing current topology (can be generated by `town-ctl export` — TBD)
3. Run `town-ctl apply --dry-run` to validate the manifest and preview the Dolt diff
4. Run `town-ctl apply` — writes `desired_topology` for the first time
5. Surveyor (when deployed) picks up the new `desired_topology` and reconciles
6. Imperative `gt` CLI remains available throughout — no flag day

## Open Questions

- `town-ctl export`: should `town-ctl` be able to generate a `town.toml` skeleton from current
  `gt` state? Useful for migration but adds `gt` read-coupling. Deferred.
- Overlay precedence for three-way conflicts (base + include + `--env` all set the same field):
  currently `--env` wins, include wins over base. Document in spec.
