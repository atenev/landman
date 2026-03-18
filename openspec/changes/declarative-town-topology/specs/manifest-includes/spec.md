## ADDED Requirements

### Requirement: includes field specifies glob patterns for overlay files
`town.toml` SHALL support a top-level `includes` array of glob patterns. Each matched file
SHALL be a valid partial TOML manifest (may be missing required top-level fields). Glob
resolution SHALL be relative to the directory containing `town.toml`.

#### Scenario: Glob matches multiple files
- **WHEN** `includes = ["./rigs/*.toml"]` and three files match the glob
- **THEN** all three files are loaded and merged into the base manifest

#### Scenario: Glob with no matches is not an error
- **WHEN** `includes = ["./rigs/*.toml"]` and no files match
- **THEN** `town-ctl` proceeds with the base manifest only, no error

### Requirement: Included rig tables are appended to the base manifest
`[[rig]]` entries from included files SHALL be appended to the `[[rig]]` list from the base
manifest. The order is: base rigs first, then included files in glob sort order.

#### Scenario: Rig from included file is present in resolved manifest
- **WHEN** base `town.toml` has one rig and an included file defines a second rig
- **THEN** the resolved manifest contains both rigs

### Requirement: Duplicate rig names across includes are a hard error
If two sources (base or any included file) define a `[[rig]]` with the same `name`, `town-ctl`
SHALL exit non-zero identifying the conflict. Last-wins silent behaviour is prohibited.

#### Scenario: Duplicate rig name detected
- **WHEN** base `town.toml` and an included file both define `[[rig]]` with `name = "backend"`
- **THEN** `town-ctl` exits non-zero: `duplicate rig name "backend" in base and ./rigs/backend.toml`

### Requirement: Scalar fields in included files do not override base manifest scalars
Top-level scalar fields (`[town]`, `[defaults]`, `[secrets]`) in included files SHALL be
ignored. Only `[[rig]]` arrays are merged from included files. Included files are rig
definition files, not full manifest overrides.

#### Scenario: Included file cannot override town name
- **WHEN** an included file sets `[town] name = "other-town"`
- **THEN** `town-ctl` ignores that field and uses the base `[town] name`

### Requirement: --env overlay is applied last and overrides all sources
When `--env <name>` is specified, `town-ctl` SHALL look for `town.<name>.toml` in the same
directory as `town.toml`. The overlay file SHALL be applied after base + includes merge.
Fields present in the overlay take precedence over base and included values. Arrays in
the overlay replace (not append to) corresponding base/include arrays.

#### Scenario: Env overlay overrides default
- **WHEN** `town.toml` sets `max_polecats = 20` and `town.prod.toml` sets `max_polecats = 30`
  and `--env prod` is specified
- **THEN** the resolved `max_polecats` is 30

#### Scenario: Missing env overlay file is an error
- **WHEN** `--env prod` is specified but `town.prod.toml` does not exist
- **THEN** `town-ctl` exits non-zero: `overlay file "town.prod.toml" not found`

### Requirement: Resolution order is deterministic and documented
The merge resolution order SHALL be: (1) base `town.toml`, (2) included files in glob sort
order, (3) `--env` overlay. `town-ctl` SHALL document this order in its `--help` output.

#### Scenario: Resolution order visible in dry-run output
- **WHEN** `town-ctl apply --dry-run` is run with includes and an env overlay
- **THEN** the output lists the resolution order before the planned operations
