# town-ctl Design: Manifest-to-Dolt Actuator

- **Issue**: dgt-i36
- **Status**: Design complete; implementation in dgt-apu
- **ADR references**: ADR-0001, ADR-0003, ADR-0006
- **Blocks**: dgt-apu (Implement town-ctl binary)

---

## Overview

`town-ctl` is a thin, standalone Go binary that translates a `town.toml` declarative
manifest into an atomic write against Gas Town's Dolt control plane. It has no knowledge
of Gas Town internals — it speaks only SQL to Dolt.

```
town.toml  →  town-ctl  →  desired_topology (Dolt)  →  Surveyor reconciles
```

`town-ctl` is independently versioned, independently deployable, and independently
testable. It does not import `gt` packages and does not require `gt` to be installed.

---

## Binary Structure

### Package layout

```
cmd/
  town-ctl/
    main.go          # cobra root command; wires subcommands
pkg/
  manifest/          # TownManifest Go structs + Parse() + Validate() (already exists)
  townctl/
    apply.go         # apply pipeline: resolve → diff → write
    costpolicy.go    # ResolveCostPolicies, ApplySQL (already exists)
    diff.go          # diff logic for desired_rigs, desired_agent_config, desired_formulas,
                     # desired_custom_roles, desired_rig_custom_roles
    dolt.go          # Dolt connection, transaction helpers
    includes.go      # glob resolution, include merge, --env overlay
    secrets.go       # env-var interpolation, secrets file loading
    process.go       # liveness check and launch for [town.agents] entries
```

### Cobra CLI layout

```
town-ctl
  apply <manifest-path>
    --dry-run          print plan without writing to Dolt
    --env <name>       apply <manifest-dir>/town.<name>.toml overlay
    --dolt-host        Dolt host (default: localhost)
    --dolt-port        Dolt port (default: 3306)
    --dolt-db          Dolt database name (default: gas_town)
    --dolt-user        Dolt user (default: root)
    --dolt-password    Dolt password (default: empty)
  version              print town-ctl version and exit
```

All flags also accept environment variables with prefix `TOWN_CTL_` (e.g.,
`TOWN_CTL_DOLT_HOST`). Explicit flags take precedence over env vars.

---

## Apply Pipeline

`town-ctl apply` executes the following steps in order. Any failure exits non-zero.
No partial state is written to Dolt (the Dolt write is atomic).

### Step 1 — Parse manifest

```
manifest, err := manifest.Parse(readFile(path))
```

- Read `town.toml` from disk.
- Decode TOML into `TownManifest` struct (`pelletier/go-toml`).
- Run structural validation (`go-validator` struct tags).
- Run cross-field validation (cost policy mutual exclusion, role references, etc.).
- Exit non-zero on any error, printing `<path>: <field>: <constraint>` to stderr.

### Step 2 — Resolve version compatibility

```
if manifest.Version != "1" {
    exit non-zero: "unsupported manifest version %q — upgrade town-ctl"
}
```

Refuse unknown manifest versions with a clear error. Version is a string, not an
integer, to allow pre-release designators in future (e.g., `"2-beta"`).

### Step 3 — Resolve includes

Load each file matched by the `includes` glob patterns (relative to the directory of
`town.toml`). Merge rules (per ADR-0001 Decision 6, ADR-0008):

- `[[rig]]` entries: append included rigs to the base list.
- `[[role]]` entries: append included roles to the base list.
- Scalar top-level fields (`[town]`, `[defaults]`, `[secrets]`): ignored in included
  files — base manifest wins.
- Duplicate rig names across base + includes: hard error, no silent last-wins.
- Duplicate role names across base + includes: hard error.

### Step 4 — Apply --env overlay

If `--env <name>` is given, load `town.<name>.toml` from the same directory as
`town.toml`. The overlay is applied last — it overrides everything (base + includes).

- Overlay may set any field (including scalars).
- Overlay `[[rig]]` entries override the rig with the same name (not append).
- If the named overlay file does not exist: exit non-zero.

### Step 5 — Resolve secrets

Scan all string fields in the resolved manifest for `${VAR}` expressions.
Substitute each with the corresponding environment variable value.

If `[secrets] file` is set:
- Load the secrets TOML file from that path (after path interpolation).
- For each `${VAR}` still unresolved, look it up in the secrets file.

If any `${VAR}` remains unresolved after both passes: exit non-zero.

Secrets are never written to Dolt or logged.

### Step 6 — Connect to Dolt

Open a standard MySQL-protocol connection to Dolt:

```
dsn = "<user>:<pass>@tcp(<host>:<port>)/<db>?parseTime=true"
conn, err = sql.Open("mysql", dsn)
```

Ping to verify connectivity. Exit non-zero with the connection error if Dolt is
unreachable. Use `go-sql-driver/mysql` (Dolt speaks the MySQL wire protocol).

### Step 7 — Diff against current desired_topology

Read the current desired state from Dolt:

```sql
SELECT name, repo, branch, enabled FROM desired_rigs;
SELECT rig_name, role, enabled, model, max_count, claude_md_path FROM desired_agent_config;
SELECT rig_name, name, schedule FROM desired_formulas;
SELECT name, scope, lifespan, trigger_type, ... FROM desired_custom_roles;
SELECT rig_name, role_name, enabled FROM desired_rig_custom_roles;
SELECT rig_name, budget_type, daily_budget, warn_at_pct FROM desired_cost_policy;
```

Compute the delta between current state and the resolved manifest:
- **add**: row in desired manifest but not in Dolt
- **update**: row in both but with different field values
- **remove**: row in Dolt but not in desired manifest
- **no-op**: row in both, identical — skip

### Step 8 — Dry run (if --dry-run)

If `--dry-run` is set:
- Print the delta to stdout (one operation per line).
- Format: `+ <table>: <key>=<val> ...` / `~ <table>: ...` / `- <table>: ...`
- Exit zero. No Dolt write. No process launch.

### Step 9 — Write atomic Dolt transaction

Begin a Dolt transaction. Execute in this order (ADR-0003 contract):

1. **desired_topology_versions upserts** — one per table touched, written first.
2. **desired_custom_roles** — INSERT/UPDATE/DELETE (no FK deps on other tables).
3. **desired_rigs** — INSERT/UPDATE; DELETE rows not in manifest (cascades to
   desired_agent_config, desired_formulas, desired_rig_custom_roles).
4. **desired_agent_config** — INSERT/UPDATE per (rig, role) pair.
5. **desired_formulas** — INSERT/UPDATE per (rig, formula) pair.
6. **desired_rig_custom_roles** — INSERT/UPDATE per (rig, role_name) pair.
7. **desired_cost_policy** — UPSERT rows with active policy; DELETE rows for
   unrestricted or removed rigs (via `costpolicy.ApplySQL`).

Wrap steps 1–7 in a single `BEGIN` / `COMMIT`. On any error: `ROLLBACK` and exit
non-zero, printing the failed SQL statement to stderr.

### Step 10 — Check and launch agent processes

After a successful Dolt commit, for each entry declared in `[town.agents]`:

- **Surveyor** (`surveyor = true`): check if a process named `surveyor` is alive
  (read `/var/run/gas-town/surveyor.pid` if present; `kill -0` to verify). If not
  alive, execute `surveyor --config <town.toml-dir>` as a detached process.

Process management beyond initial launch is Deacon's / systemd's responsibility.
`town-ctl` does not restart, supervise, or track processes after launch.

---

## Version Enforcement

The `version` field in `town.toml` is the compatibility contract between the manifest
author and `town-ctl`. Rules:

- `town-ctl` knows exactly one version: `"1"`.
- If `manifest.Version != "1"`: exit non-zero before any Dolt connection.
- Error message includes a pointer to the minimum `town-ctl` version required.
- Schema evolution uses version bumps — no silent behavioural changes.

This is independent of the `schema_version` in `desired_topology_versions`, which
governs the Surveyor ↔ Dolt contract.

---

## Dolt Transaction Model

All writes in a single `town-ctl apply` run are committed as one Dolt commit. The
commit message is set via a Dolt-specific SQL statement before COMMIT:

```sql
SET @dolt_transaction_commit_message = 'town-ctl apply: <manifest-path> v<version> [<n>+/<m>~/<k>-]';
```

This gives operators a queryable audit trail via `DOLT_LOG()`.

Each apply is idempotent: running twice with the same manifest produces an empty diff
on the second run and writes nothing (but still sets the commit message with `[0+/0~/0-]`).

---

## Secret Resolution Algorithm

```
1. Scan resolved manifest struct fields for ${VAR} patterns.
2. For each ${VAR}: look up os.Getenv(VAR).
3. If [secrets].file is set:
   a. Path-interpolate the file path (${HOME}, ${GT_HOME}).
   b. Parse the secrets TOML file into a map[string]string.
   c. For each ${VAR} still unresolved: look up in the secrets map.
4. If any ${VAR} is still unresolved after both passes:
   exit non-zero listing all unresolved references.
5. Apply resolved values back into the struct fields.
   Resolved secrets are never written back to disk or to Dolt.
```

---

## Diff Algorithm

For each Dolt table, the diff is computed as a set operation over primary keys:

```
desired_keys = {pk: row for row in resolved_manifest}
current_keys = {pk: row for row in dolt_read}

adds    = desired_keys - current_keys
removes = current_keys - desired_keys
updates = {k for k in desired_keys ∩ current_keys if desired_keys[k] != current_keys[k]}
no_ops  = desired_keys ∩ current_keys - updates
```

Primary keys:
- `desired_rigs`: `name`
- `desired_agent_config`: `(rig_name, role)`
- `desired_formulas`: `(rig_name, name)`
- `desired_custom_roles`: `name`
- `desired_rig_custom_roles`: `(rig_name, role_name)`
- `desired_cost_policy`: `rig_name`

Removes for `desired_rigs` cascade automatically via FK `ON DELETE CASCADE` to
`desired_agent_config`, `desired_formulas`, and `desired_rig_custom_roles`.

---

## Error Handling Conventions

- All errors exit non-zero (any non-zero code is a failure; no specific codes defined).
- Parse/validation errors: `stderr: <file>: <field-path>: <constraint>`
- Secret resolution failure: `stderr: unresolved secret references: ${VAR1}, ${VAR2}`
- Dolt connection failure: `stderr: dolt: connect: <error>`
- Dolt SQL failure: `stderr: dolt: <statement>: <error>`
- Include glob failure: `stderr: includes: <pattern>: <error>`
- Duplicate rig name: `stderr: duplicate rig name "backend" in <file1> and <file2>`
- Unknown version: `stderr: unsupported manifest version "2" — upgrade town-ctl to ≥ 0.2.0`

Stdout is reserved for dry-run plan output. All other output goes to stderr.

---

## Testing Strategy

Unit tests (no Dolt required):
- `manifest.Parse` with valid and invalid manifests
- `includes.Merge` edge cases (duplicate names, scalar ignore, array append)
- `secrets.Resolve` with env vars and secrets file
- `diff.Compute` for each table type (add, update, remove, no-op)
- `costpolicy.ResolveCostPolicies` inheritance chain

Integration tests (Dolt in Docker):
- Full apply cycle: parse → diff → write → re-apply (idempotent)
- `--dry-run`: verify no writes
- `--env`: verify overlay applied
- Unknown version: verify non-zero exit before connection
- Dolt write failure: verify rollback

---

## Open Questions for dgt-apu

1. **Path interpolation scope**: Which fields support `${VAR}` interpolation beyond
   the `[secrets]` block? Currently: `secrets.file`, `claude_md_path`, formula paths.
   Decision: all string fields support interpolation for consistency.

2. **Surveyor PID file convention**: Where does `town-ctl` look for the Surveyor PID?
   Proposed: `${GT_HOME}/run/surveyor.pid`. Fallback: process name scan via `/proc`.

3. **`town-ctl export` command**: Should `town-ctl` generate a `town.toml` skeleton
   from the current `desired_topology` rows? Deferred — not in scope for dgt-apu.
