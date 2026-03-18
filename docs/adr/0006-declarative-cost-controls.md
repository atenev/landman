# ADR-0006: Declarative Cost Controls for Gas Town

- **Status**: Proposed
- **Date**: 2026-03-17
- **Beads issue**: dgt-afk
- **Deciders**: Aleksandar Tenev
- **Extends**: ADR-0001 (town.toml manifest format), ADR-0002 (Surveyor/Deacon enforcement),
  ADR-0003 (desired_topology_versions versioning contract)

---

## Context

Gas Town burns ~$100/hr at peak (12–30 agents on API billing). There are no built-in cost
controls. Spend is bounded only by `max_polecats` — a topology constraint, not a cost
constraint. An operator who forgets to set `max_polecats` or who runs many rigs has no
mechanism to prevent runaway spend.

ADR-0001 reserved `[rig.cost]` as an extension slot in the manifest skeleton with a
forward reference to this ADR. This ADR fills that slot.

**Two user populations exist with fundamentally different cost signals:**

1. **API billing users** (pay-per-token): cost is measurable in USD. The signal is
   `token_count × model_price`. Hard dollar caps are the right governance mechanism.

2. **Subscription users** (Claude Max / Claude Code flat-rate): cost is not measurable in
   USD. The signal is message count (what Anthropic rate-limits on) or total token volume.
   A dollar-only governance model is useless for these users.

The governance design must accommodate both populations without requiring users to
configure a billing mode globally — the budget type is declared per policy, not per
installation.

**Default behaviour when no policy is declared must be unrestricted.** Gas Town must
not silently impose cost limits on existing installations. Governance is opt-in.

---

## Decisions

### Decision 1: `[rig.cost]` fills the reserved ADR-0001 extension slot; `[defaults.cost]` provides a town-wide safety net

**Chosen**: Cost policy is declared at the rig level via `[rig.cost]`. An optional
`[defaults.cost]` sub-table under `[defaults]` provides a town-wide policy that applies
to all rigs that do not declare their own.

```toml
[defaults]
mayor_model  = "claude-opus-4-6"
max_polecats = 20

  [defaults.cost]
  # Optional town-wide safety net. Absent = unrestricted for all rigs by default.
  daily_budget_usd = 200.0
  warn_at_pct      = 80

[[rig]]
name = "backend"

  [rig.cost]
  # Overrides [defaults.cost] for this rig.
  daily_budget_usd = 50.0
  warn_at_pct      = 75

[[rig]]
name = "docs"
  # No [rig.cost] block — inherits [defaults.cost] if present.
  # If [defaults.cost] also absent → unrestricted.
```

**Inheritance chain** (evaluated by `town-ctl` at apply time):

```
rig [rig.cost] present?
  YES → use rig policy (write row to desired_cost_policy)
  NO  → [defaults.cost] present?
          YES → use defaults policy (write row to desired_cost_policy)
          NO  → UNRESTRICTED (write NO row to desired_cost_policy)
```

The absence of a row in `desired_cost_policy` is the definitive signal for "unrestricted".
Deacon skips cost patrol for rigs with no row. No sentinel value, no `enabled` boolean.

**Rationale**: This pattern is identical to how `max_polecats` and `polecat_model` inherit
from `[defaults]` (ADR-0001). Consistent inheritance reduces cognitive load. The absence-
means-unrestricted invariant prevents any silent default from surprising existing users.

---

### Decision 2: Three mutually exclusive budget types — `usd`, `messages`, `tokens`

**Chosen**: Each cost policy declares exactly one of three budget fields:

| Field | Type | Budget type | Intended for |
|-------|------|-------------|--------------|
| `daily_budget_usd` | `float64` | `usd` | API billing users |
| `daily_budget_messages` | `int64` | `messages` | Subscription users (flat-rate) |
| `daily_budget_tokens` | `int64` | `tokens` | Subscription users (token-aware) |

`town-ctl` enforces mutual exclusion: if more than one budget field is set in the same
`[rig.cost]` or `[defaults.cost]` block, it exits non-zero with a clear error:

```
[rig.backend.cost] sets both daily_budget_usd and daily_budget_messages.
Exactly one budget type may be set per cost policy block.
```

If zero budget fields are set but a `[rig.cost]` block is present (e.g., only
`warn_at_pct` is set), `town-ctl` also exits non-zero — a cost block with no budget
is a misconfiguration.

**Alternatives considered**:

**Option A — Single `daily_budget` with a `budget_type` enum field**

```toml
[rig.cost]
budget_type    = "usd"
daily_budget   = 50.0
```

Rejected because: TOML has no union type. The `daily_budget` field must be numeric, but
the unit (USD vs count vs tokens) changes what "50.0" means by three orders of magnitude.
A reviewer reading the manifest cannot immediately understand the unit without reading the
`budget_type`. Explicit field names (`daily_budget_usd`) are self-documenting.

**Option B — Three explicit fields, mutually exclusive (chosen)**

Self-documenting, type-safe (different field types: `float64` for USD, `int64` for
counts), and compatible with `go-validator` mutual-exclusion tags.

---

### Decision 3: Unrestricted is the explicit default — no row in `desired_cost_policy` means no enforcement

**Chosen**: When the inheritance chain resolves to "no policy" (no `[rig.cost]` and no
`[defaults.cost]`), `town-ctl` writes **no row** for that rig in `desired_cost_policy`.
Deacon's cost patrol query only returns rigs that have a policy row. Rigs absent from
`desired_cost_policy` are not patrolled.

**Why this is correct**:

1. **No regression**: existing Gas Town installations have no cost policy. Adding
   `desired_cost_policy` to the schema cannot silently impose limits — there are no rows
   to enforce against.

2. **Explicit opt-in**: an operator adds `[rig.cost]` or `[defaults.cost]` to
   consciously enable governance. The action of adding the block is the intent signal.

3. **No sentinel values**: there is no `enabled = false` flag, no `daily_budget_usd = 0`
   meaning "unlimited", no null budget. Absence of a row is the one canonical
   representation of "unrestricted". This prevents ambiguity in Deacon's patrol logic.

4. **Cleanup on rig removal**: when a rig is removed from `town.toml`, `town-ctl`'s
   apply transaction deletes the corresponding `desired_cost_policy` row. Orphaned policy
   rows for removed rigs do not exist.

---

### Decision 4: `cost_ledger` as the spend tracking table — written by Polecats before exit

**Chosen**: Each Polecat writes one row to `cost_ledger` immediately before its process
exits. This is a GUPP invariant: the write happens before exit, ensuring no spend goes
unrecorded even if the Polecat is killed or crashes.

**Schema**:

```sql
CREATE TABLE cost_ledger (
  id            BIGINT         AUTO_INCREMENT PRIMARY KEY,
  rig_name      VARCHAR(128)   NOT NULL,
  polecat_id    VARCHAR(64)    NOT NULL,
  model         VARCHAR(128)   NOT NULL,
  input_tokens  INT            NOT NULL,
  output_tokens INT            NOT NULL,
  cost_usd      DECIMAL(12,6)  NULL,       -- NULL for subscription users
  message_count INT            NOT NULL DEFAULT 1,
  recorded_at   TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX (rig_name, recorded_at)   -- supports the 24h rolling window query
);

CREATE VIEW cost_ledger_24h AS
  SELECT
    rig_name,
    SUM(cost_usd)                          AS spend_usd,
    SUM(message_count)                     AS spend_messages,
    SUM(input_tokens + output_tokens)      AS spend_tokens
  FROM cost_ledger
  WHERE recorded_at >= NOW() - INTERVAL 1 DAY
  GROUP BY rig_name;
```

The Polecat CLAUDE.md is updated to include:
- The SQL INSERT statement to execute before exit.
- A static model pricing table for computing `cost_usd` (API billing users). The table
  is embedded in the CLAUDE.md — no external pricing API call required.
- The rule: if `GT_BILLING_TYPE=subscription` env var is set, write `cost_usd = NULL`.

**`cost_ledger` is an operational table, not a `desired_topology` table.** It is written
by Polecats (not `town-ctl`) and read by Deacon (not the Surveyor). It does NOT go
through `desired_topology_versions` (ADR-0003). The Surveyor's reconcile loop does not
read or write `cost_ledger`.

**All three metrics are always recorded**, regardless of the active policy type:
- `cost_usd` is populated if on API billing, NULL if on subscription.
- `message_count` is always 1 per Polecat exit (one task = one Polecat = one record).
- `input_tokens` and `output_tokens` are always recorded.

This means the operator can switch `budget_type` in `town.toml` without losing historical
data — the ledger already has all three signals.

---

### Decision 5: Deacon as the enforcement agent — cost patrol via the existing patrol mechanism

**Chosen**: Deacon implements a new patrol type: `cost`. Every 5 minutes (configurable
per town via `[town.cost] patrol_interval_seconds`), Deacon runs the cost patrol query
against `desired_cost_policy` and `cost_ledger_24h`, then enforces via Beads.

**Patrol query**:

```sql
SELECT
  p.rig_name,
  p.budget_type,
  p.daily_budget,
  p.warn_at_pct,
  CASE p.budget_type
    WHEN 'usd'      THEN l.spend_usd      / p.daily_budget * 100
    WHEN 'messages' THEN l.spend_messages / p.daily_budget * 100
    WHEN 'tokens'   THEN l.spend_tokens   / p.daily_budget * 100
  END AS pct_used
FROM desired_cost_policy p
LEFT JOIN cost_ledger_24h l USING (rig_name);
```

**Enforcement actions**:

| Condition | Action |
|-----------|--------|
| `pct_used >= 100` | `bd create` hard cap drain Bead (priority=0) to Dogs/Surveyor drain path |
| `warn_at_pct <= pct_used < 100` | `bd create` warning Bead (priority=1) assigned to Mayor |
| Rig absent from `desired_cost_policy` | Skip — no enforcement |

**Duplicate Bead prevention**: before filing any Bead, Deacon checks for an existing
open Bead with `tag=cost-cap` and `rig={rig_name}`. If one exists, the patrol skips
filing a new one. This prevents Deacon from flooding Beads on every 5-minute cycle when
a rig stays over budget.

**Why Deacon, not Witness or Surveyor**:

- **Witness** is in the critical Polecat spawn path. Blocking spawn on cost checks
  couples cost governance to agent creation latency and is the wrong layer.
- **Surveyor** is the topology reconciler. Cost enforcement is an operational health
  event, not a desired-vs-actual topology divergence. Mixing concerns corrupts the
  Surveyor's clear reconcile semantics.
- **Deacon** runs periodic health patrols. "Is this rig over budget?" is precisely a
  health question. Deacon already knows how to drain rigs (via Beads to Dogs) and
  escalate (via Beads to Mayor). Cost patrol is a new patrol type in existing
  infrastructure.

---

### Decision 6: `desired_cost_policy` is versioned via `desired_topology_versions` per ADR-0003

**Chosen**: `desired_cost_policy` is a `desired_topology` table. It participates in the
`desired_topology_versions` versioning protocol exactly as specified in ADR-0003:

- `town-ctl` upserts a `desired_topology_versions` row for `desired_cost_policy` as part
  of its first-write-in-transaction invariant.
- The Surveyor reads `desired_topology_versions` pre-flight (ADR-0003 Decision 3).
  Although the Surveyor does not directly enforce cost policy, it reads `desired_cost_policy`
  during delta computation to understand the full desired state context.
- No `schema_version` column on `desired_cost_policy` rows — ADR-0003 explicitly
  prohibits this.

`cost_ledger` is **not** a `desired_topology` table. It is an operational audit table
written by Polecats and read by Deacon. It is not versioned via `desired_topology_versions`.

---

## Consequences

### What becomes easier

- **Runaway spend prevention**: operators who run API-billed Gas Towns can set a
  `[defaults.cost] daily_budget_usd` and know that Deacon will drain rigs automatically
  before spend exceeds the daily cap.
- **Subscription-safe governance**: flat-rate users can govern by `daily_budget_messages`
  or `daily_budget_tokens` — signals that are meaningful on their billing model.
- **GitOps-auditable spend policy**: `[rig.cost]` lives in `town.toml`. A PR changing a
  rig's daily budget is a reviewable, git-tracked change. Every `town-ctl apply` is a Dolt
  commit — the history of policy changes is queryable.
- **No-op for existing users**: no `[rig.cost]` block = no `desired_cost_policy` row =
  Deacon skips cost patrol for that rig. Existing Gas Town setups are unaffected.
- **Cost observability is free**: `cost_ledger` is a Dolt table. `SELECT * FROM
  cost_ledger WHERE recorded_at > ...` gives auditable, per-Polecat spend history
  without a separate monitoring tool.

### New constraints introduced

- **Polecat CLAUDE.md must be updated** to include the `cost_ledger` write instruction.
  This is a CLAUDE.md change, not a `gt` binary change — but it must be deployed to all
  Polecat instances. Deacon's cost patrol is only accurate if Polecats reliably write
  their ledger rows.
- **Static pricing table in Polecat CLAUDE.md will drift** as Anthropic changes model
  prices. Operators on API billing must update the CLAUDE.md when pricing changes.
  This is a known limitation of embedding prices in config rather than fetching from an
  API. Mitigated by: prices change infrequently; the cost governance signal does not need
  to be cent-accurate — it needs to be order-of-magnitude correct for budget enforcement.
- **`cost_ledger` grows unboundedly** without a retention policy. A `cost_ledger_cleanup`
  Deacon patrol (or Formula) should archive or delete rows older than a configurable
  retention window. Deferred to a follow-up task.
- **`desired_cost_policy` rows for removed rigs** must be cleaned up by `town-ctl` apply.
  This is specified in Decision 3 and must be implemented — orphaned rows cause Deacon to
  patrol rigs that no longer exist.
- **Subscription users with `cost_usd = NULL`** cannot switch to a `usd` budget type
  without backfilling `cost_usd` in historical `cost_ledger` rows. The 24h rolling window
  mitigates this — after 24h of API-billed Polecats writing non-null `cost_usd`, the
  `usd` budget type becomes accurate.

### Out of scope for this ADR

- Model routing based on remaining budget (e.g., switch from Opus to Haiku at 80% spend)
  — deferred (V2, requires Witness integration)
- Per-Formula sub-budgets — deferred (schema extension, non-breaking)
- Rate limiting (max new Polecats per hour) — deferred; `max_polecats` + `daily_budget`
  covers the common case
- `gt cost report` CLI / Prometheus metrics export — deferred (dgt-cost-obs, V2)
- `cost_ledger` retention / archival policy — deferred (follow-up task)
- Configurable rolling window (non-24h) — deferred (simple patrol query change, V2)
- Billing type auto-detection (vs. `GT_BILLING_TYPE` env var) — deferred

---

## Reference: Canonical Manifest Skeleton with Cost Controls

```toml
# town.toml — cost controls examples
version = "1"

[town]
name = "my-town"
home = "${GT_HOME}"

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

  # Optional town-wide cost safety net.
  # Remove this block entirely for unrestricted operation.
  [defaults.cost]
  daily_budget_usd = 200.0   # API billing users
  # daily_budget_messages = 2000  # subscription users (pick one)
  # daily_budget_tokens   = 5_000_000  # subscription users (pick one)
  warn_at_pct = 80

[[rig]]
name    = "backend"
repo    = "${PROJECTS_DIR}/backend"
branch  = "main"
enabled = true

  [rig.agents]
  mayor        = true
  max_polecats = 30

  [rig.cost]
  # Overrides [defaults.cost] for this rig.
  daily_budget_usd = 50.0
  warn_at_pct      = 75

[[rig]]
name    = "docs"
repo    = "${PROJECTS_DIR}/docs"
branch  = "main"
enabled = true

  [rig.agents]
  max_polecats = 5
  # No [rig.cost] block → inherits [defaults.cost].
  # If [defaults.cost] is also absent → unrestricted.
```
