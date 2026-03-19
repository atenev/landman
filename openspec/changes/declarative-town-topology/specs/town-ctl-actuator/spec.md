## ADDED Requirements

### Requirement: town-ctl is a standalone binary with no knowledge of gt internals
`town-ctl` SHALL be a Go binary independent of the `gt` binary. It SHALL NOT import `gt`
packages, call `gt` CLI commands, or require `gt` to be installed. Its only external
dependencies are: Dolt (via SQL), the filesystem (for `town.toml` and secrets file), and
environment variables.

#### Scenario: Runs without gt installed
- **WHEN** `gt` is not present on the system and `town-ctl apply town.toml` is run
- **THEN** `town-ctl` completes successfully (given Dolt is accessible)

### Requirement: apply command writes an atomic Dolt transaction
`town-ctl apply <path>` SHALL parse, validate, resolve, diff, and write desired state to
Dolt in a single atomic transaction. If any step fails, no partial state SHALL be written
to Dolt.

#### Scenario: Successful apply commits to Dolt
- **WHEN** `town-ctl apply town.toml` runs against a valid manifest with Dolt accessible
- **THEN** all `desired_topology` rows are written in one Dolt commit and `town-ctl` exits zero

#### Scenario: Dolt write failure rolls back
- **WHEN** a Dolt write fails mid-transaction
- **THEN** no rows are committed and `town-ctl` exits non-zero with the SQL error

### Requirement: apply computes a diff before writing
`town-ctl apply` SHALL read the current `desired_topology` rows from Dolt before writing,
compute the set of additions, updates, and deletions, and write only the delta. Rows
unchanged from current state SHALL NOT be rewritten.

#### Scenario: Idempotent apply
- **WHEN** `town-ctl apply` is run twice with the same `town.toml`
- **THEN** the second apply writes zero rows (empty diff) and exits zero

#### Scenario: Incremental apply on rig addition
- **WHEN** a new `[[rig]]` entry is added to `town.toml` and apply is run
- **THEN** only the new rig's rows are written; existing rows are untouched

### Requirement: --dry-run flag prints structured plan without writing
`town-ctl apply --dry-run <path>` SHALL resolve the full manifest and compute the diff, then
print the planned operations (add / update / remove per row) to stdout in a structured format.
It SHALL write nothing to Dolt and SHALL NOT launch any processes.

#### Scenario: Dry run shows planned changes
- **WHEN** `town-ctl apply --dry-run town.toml` is run with a manifest that would add one rig
- **THEN** stdout shows `+ desired_rigs: name=backend ...` and no Dolt write occurs

### Requirement: --env flag selects an environment overlay
`town-ctl apply --env <name> <path>` SHALL apply the overlay file matching `<name>` on top of
the base manifest before any Dolt write. If the named overlay file does not exist, `town-ctl`
SHALL exit non-zero.

#### Scenario: Overlay applied
- **WHEN** `--env prod` is specified and `town.prod.toml` exists in the same directory
- **THEN** the resolved manifest reflects the prod overlay values

### Requirement: apply ensures declared agent processes are running
After writing to Dolt, `town-ctl apply` SHALL check process liveness for each declared entry
in `[town.agents]` and launch any that are not running. It SHALL not manage process lifecycle
beyond initial launch (no restart, no supervision — that is systemd's or Deacon's job).

#### Scenario: Process not running is launched
- **WHEN** `[town.agents] surveyor = true` and the Surveyor process is not running
- **THEN** `town-ctl` launches the Surveyor and exits zero

### Requirement: Non-zero exit on any failure with human-readable error
`town-ctl` SHALL exit non-zero on any failure (parse error, validation error, secret
resolution failure, Dolt error) and print a human-readable error to stderr identifying
the cause and, where possible, the location in the manifest.

#### Scenario: Missing required field error
- **WHEN** `town.toml` omits the required `[town] name` field
- **THEN** stderr shows: `town.toml: [town.name] is required` and exit code is non-zero

### Requirement: manifest version field is enforced before any Dolt connection
`town-ctl` SHALL check `manifest.version` immediately after parsing. If the version is
not `"1"`, it SHALL exit non-zero before opening a Dolt connection. The error message
SHALL state the unsupported version and recommend upgrading `town-ctl`.

#### Scenario: Unknown manifest version
- **WHEN** `town.toml` declares `version = "2"` and `town-ctl` only understands `"1"`
- **THEN** stderr shows: `unsupported manifest version "2" — upgrade town-ctl` and exit
  code is non-zero, without any Dolt connection being attempted

### Requirement: secrets are resolved at apply time and never written to Dolt
`town-ctl` SHALL resolve `${VAR}` references in manifest fields by reading environment
variables first, then the optional `[secrets] file` if set. All references MUST resolve;
unresolved references SHALL cause a non-zero exit listing all unresolved names. Resolved
secret values SHALL NOT appear in Dolt writes, log output, or the dry-run plan.

#### Scenario: Missing secret reference
- **WHEN** `anthropic_api_key = "${ANTHROPIC_API_KEY}"` and the env var is not set and
  no secrets file is configured
- **THEN** stderr shows: `unresolved secret references: ${ANTHROPIC_API_KEY}` and exit
  code is non-zero before any Dolt write

### Requirement: Dolt writes use a labelled commit message for audit
Each successful `town-ctl apply` transaction SHALL set a Dolt commit message that includes
the manifest path, `town-ctl` version, and a summary of operations performed
(`[<n>+/<m>~/<k>-]` for adds, updates, removes). The commit is queryable via `DOLT_LOG()`.

#### Scenario: Apply commit is visible in Dolt log
- **WHEN** `town-ctl apply town.toml` completes successfully
- **THEN** `SELECT * FROM DOLT_LOG()` shows a commit with a message containing
  `town-ctl apply: town.toml` and the operation summary

### Requirement: apply transaction order is deterministic and FK-safe
Within the Dolt transaction, `town-ctl` SHALL write tables in dependency order:
`desired_topology_versions` upserts first, then `desired_custom_roles`,
then `desired_rigs` (cascade-deletes dependents), then `desired_agent_config`,
`desired_formulas`, `desired_rig_custom_roles`, and finally `desired_cost_policy`.
This order prevents FK constraint violations during the transaction.

#### Scenario: Transaction applies FK-dependent tables in correct order
- **WHEN** `town-ctl apply` adds a new rig with agent config and formulas
- **THEN** the `desired_rigs` row is written before the `desired_agent_config` and
  `desired_formulas` rows in the same transaction (no FK violation)

### Requirement: dry-run stdout uses +/~/- prefix format per table
`town-ctl apply --dry-run` SHALL print planned operations to stdout using the format:
`+ <table>: <key>=<val> [<field>=<val> ...]` for adds, `~ <table>: ...` for updates,
`- <table>: <key>=<val>` for removes. If there are no changes, it SHALL print
`<table>: no changes` for each table. Secrets SHALL NOT appear in dry-run output.

#### Scenario: Dry run add output format
- **WHEN** `--dry-run` is used and a new rig "backend" would be added
- **THEN** stdout contains exactly: `+ desired_rigs: name=backend repo=... branch=main`

### Requirement: --env overlay overrides all base and included values for the same field
The `--env <name>` overlay is applied after all `includes` are merged. It overrides any
field set in the base manifest or any included file. For `[[rig]]` entries, the overlay
matches by `name` and replaces the entire rig spec; it does not append a new rig.

#### Scenario: Overlay overrides rig max_polecats
- **WHEN** the base manifest sets `max_polecats = 5` for rig "backend" and `--env prod`
  overlay sets `max_polecats = 20` for rig "backend"
- **THEN** the resolved manifest has `max_polecats = 20` for rig "backend"
