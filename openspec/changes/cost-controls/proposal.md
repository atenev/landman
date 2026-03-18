## Why

Gas Town burns ~$100/hr at peak with no built-in cost controls. Spend is bounded only by
`max_polecats` — a topology constraint, not a financial one. Operators on API billing have
no mechanism to prevent runaway spend. Operators on Claude Max / subscription plans have
no mechanism to govern their rate-limited message or token consumption.

## What Changes

- Introduce `[rig.cost]` block in `town.toml` (filling the reserved ADR-0001 extension
  slot) with three mutually exclusive budget types: `daily_budget_usd` (API billing),
  `daily_budget_messages` (subscription), `daily_budget_tokens` (subscription).
- Introduce `[defaults.cost]` as an optional town-wide cost safety net under `[defaults]`.
  Inheritance: rig policy overrides defaults; both absent = unrestricted (no row written).
- `town-ctl apply` writes `desired_cost_policy` rows to Dolt for rigs with an active
  policy. Rigs with no policy get no row — absence is the canonical "unrestricted" signal.
- Introduce `cost_ledger` Dolt table: each Polecat writes one row before exit (GUPP
  invariant) recording input/output tokens, model, cost_usd (NULL for subscription), and
  message_count. All three metrics are always recorded regardless of active budget type.
- Deacon implements a new cost patrol type: every 5 minutes it queries
  `desired_cost_policy` + `cost_ledger_24h` and enforces via Beads — hard-cap drain Bead
  (priority 0) on 100%+ spend, warning Bead to Mayor at `warn_at_pct`.

## Capabilities

### New Capabilities

- `cost-policy-schema`: `desired_cost_policy` Dolt table (written by `town-ctl`,
  versioned via `desired_topology_versions` per ADR-0003) and `cost_ledger` operational
  table with `cost_ledger_24h` rolling view. Three budget types covering both API and
  subscription billing models.
- `deacon-cost-patrol`: Deacon patrol type that queries rolling 24h spend, compares
  against declared policy, and enforces via Beads. Hard cap → drain. Soft warning → Mayor.
  Duplicate Bead prevention within patrol window. Unrestricted rigs fully skipped.
- `cost-ledger`: Polecat CLAUDE.md update (GUPP write-before-exit) recording per-task
  token and cost data. Static pricing table for USD computation. `GT_BILLING_TYPE`
  env var for subscription users.

### Modified Capabilities

- `town-toml-manifest`: `[rig.cost]` and `[defaults.cost]` blocks added to manifest
  schema. Inheritance chain resolves at apply time. Extension slot `[rig.cost]` from
  ADR-0001 is now live (not a no-op).
- `town-ctl-actuator`: parse and validate cost policy blocks, write `desired_cost_policy`
  rows in existing atomic transaction, delete orphaned rows on rig removal.

## Impact

- New Dolt tables: `desired_cost_policy`, `cost_ledger`, view `cost_ledger_24h`
- `town.toml` schema: `[rig.cost]` and `[defaults.cost]` blocks (Go structs + JSON Schema)
- `town-ctl` binary: cost block parsing, validation, Dolt write — no new commands
- Deacon: new cost patrol type (CLAUDE.md or Formula update)
- Polecat CLAUDE.md: `cost_ledger` write-before-exit instruction added
- `gt` binary: **no modification required**
- Existing installations without `[rig.cost]`: **no behavioral change** — unrestricted
