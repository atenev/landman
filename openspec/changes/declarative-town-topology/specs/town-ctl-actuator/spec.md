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
