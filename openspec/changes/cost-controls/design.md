## Context

Gas Town has no cost governance. `max_polecats` caps agent count but not spend rate.
Two billing populations exist: API users (pay-per-token, USD signal) and subscription
users (flat-rate, message/token count signal). Any governance design must accommodate
both without requiring a global billing mode flag.

ADR-0001 reserved `[rig.cost]` as a manifest extension slot. ADR-0002 established Deacon
as the health patrol agent. ADR-0003 defines `desired_topology_versions` as the versioning
contract. This change fills the reserved slot and wires cost enforcement into Deacon's
existing patrol mechanism.

Constraint: **no `gt` binary modification**. All changes are in `town-ctl` (parsing +
Dolt writes), Deacon CLAUDE.md (new patrol type), and Polecat CLAUDE.md (ledger write).

## Goals / Non-Goals

**Goals:**
- Define `[rig.cost]` and `[defaults.cost]` TOML schema with three budget types
- Explicit inheritance chain: rig → defaults → unrestricted
- No-row-means-unrestricted invariant in `desired_cost_policy`
- `cost_ledger` schema covering all three budget signal types always
- Deacon cost patrol: hard cap drain + soft warning via Beads
- Polecat GUPP write before exit
- ADR-0003 compliance for `desired_cost_policy`

**Non-Goals:**
- Model routing on budget threshold (V2 — requires Witness integration)
- Per-Formula sub-budgets (non-breaking V2 extension)
- Rate limiting / max-Polecats-per-hour (covered by `max_polecats` + daily cap)
- `gt cost report` CLI or Prometheus export (V2 observability task)
- `cost_ledger` retention / archival policy (follow-up task)
- Billing type auto-detection

## Decisions

### D1: Three explicit budget field names, mutually exclusive

`daily_budget_usd` (float64), `daily_budget_messages` (int64), `daily_budget_tokens`
(int64) — one field, self-documenting unit. Mutex enforced by `town-ctl` at parse time
and by `go-validator` tags. A cost block with no budget field is also a hard error.

Alternative (single `daily_budget` + `budget_type` enum) rejected: the unit ambiguity
(USD vs count vs tokens) is too high for a number field that changes meaning by three
orders of magnitude depending on type.

### D2: Absence-means-unrestricted invariant

No `[rig.cost]` + no `[defaults.cost]` → no row in `desired_cost_policy`. Deacon's
patrol query only returns rigs with a policy row. Absent rows are never patrolled. This
prevents any silent default from affecting existing installations and eliminates the need
for `enabled = false` sentinel values or null budget fields.

`town-ctl` apply deletes `desired_cost_policy` rows for rigs removed from `town.toml`
within the same atomic transaction. Orphaned rows cannot exist after a successful apply.

### D3: `cost_ledger` always records all three metrics

Every Polecat writes `cost_usd` (NULL if subscription), `message_count` (always 1 per
Polecat), `input_tokens`, and `output_tokens`. This decouples data recording from policy
type — operators can change `budget_type` in `town.toml` without losing signal continuity
in the ledger. The 24h rolling window ensures the new policy type becomes accurate within
one day.

`cost_ledger` is an operational table, not a `desired_topology` table. It is NOT versioned
via `desired_topology_versions`. The Surveyor does not read it.

### D4: Deacon as enforcer, not Witness or Surveyor

Witness is in the Polecat spawn critical path — cost checks there add latency and couple
governance to agent creation. Surveyor owns topology reconciliation — cost enforcement
is a health event, not a topology divergence. Deacon runs health patrols and already has
the escalation paths (drain via Dogs, warn via Mayor) cost enforcement needs. New patrol
type in existing infrastructure, no new agent.

### D5: `desired_cost_policy` versioned per ADR-0003

`desired_cost_policy` is a `desired_topology` table. `town-ctl` upserts its
`desired_topology_versions` row first in every transaction. No per-row `schema_version`
column. Surveyor reads `desired_topology_versions` pre-flight and will fail-safe on
unknown cost policy schema version.

## Risks / Trade-offs

- **Static pricing table drift**: model prices embedded in Polecat CLAUDE.md will become
  stale when Anthropic changes pricing. Governance accuracy (not precision) is the goal
  — budget enforcement at order-of-magnitude granularity is acceptable. Operators must
  update CLAUDE.md on price changes. Mitigated by: prices change infrequently; the daily
  budget is a soft safety net, not an accounting system.
- **Deacon patrol lag**: up to 5 minutes between cap breach and drain Bead filing.
  Acceptable — Gas Town is a development orchestrator, not a financial system. Sub-minute
  enforcement would require Witness spawn-path integration (deferred).
- **Polecat crash before ledger write**: if a Polecat is SIGKILL'd before its exit hook
  runs, the spend for that task is unrecorded. Mitigated by: (a) GUPP requires the write
  to be the first exit action — only an unclean kill bypasses it; (b) the governance
  target is preventing budget overruns by 2x, not by 1 task. A few missed records do not
  materially affect a 5-minute patrol cycle.
- **Subscription users with NULL `cost_usd`**: if an operator switches from subscription
  to API billing, historical `cost_ledger` rows have NULL `cost_usd`. The 24h window
  means the transition is seamless after one day.

## Migration Plan

1. Deploy `desired_cost_policy` and `cost_ledger` DDL migrations against existing Dolt
2. Update Polecat CLAUDE.md with `cost_ledger` write instruction (deploy to all Polecats)
3. Author `[rig.cost]` blocks in `town.toml` for rigs where governance is desired
4. Run `town-ctl apply --dry-run` to preview `desired_cost_policy` rows to be written
5. Run `town-ctl apply` — `desired_cost_policy` rows written; Deacon begins patrolling
6. Existing rigs without `[rig.cost]` remain unrestricted — no behavioral change

## Open Questions

- **Patrol interval configuration**: `[town.cost] patrol_interval_seconds = 300` is
  proposed as the config surface. Should this be in `town.toml` or in the Deacon
  CLAUDE.md? Using `town.toml` keeps all operational config in one place; using CLAUDE.md
  keeps Deacon's config with Deacon. Leaning toward `town.toml` for consistency.
- **`cost_ledger` retention**: no retention policy is specified in this change. A follow-up
  task should define a configurable retention window and implement a Deacon cleanup patrol
  or Dolt TTL mechanism.
