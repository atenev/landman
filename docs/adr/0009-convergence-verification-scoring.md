# ADR-0009: Convergence Verification Scoring and Thresholds

- **Status**: Proposed
- **Date**: 2026-03-18
- **Beads issue**: dgt-fqg
- **Deciders**: Aleksandar Tenev
- **Depends on**: ADR-0002 (Decision 4), ADR-0007 (actual_topology schema)

---

## Context

ADR-0002 Decision 4 established a two-layer convergence model (Dolt state layer +
process health layer) and a configurable convergence score threshold. It explicitly
deferred formal scoring definitions to this ADR:

> "Convergence score: fraction of desired rigs that pass both layers. Configurable
>  threshold — 1.0 for critical environments, 0.9 acceptable for development.
>  If score < threshold after N retries with backoff, the branch is abandoned and
>  an escalation Bead is filed to Mayor."
> — ADR-0002, Decision 4

ADR-0007 designed the `actual_topology` tables. The Surveyor now has a concrete
schema to diff against. This ADR answers the questions ADR-0002 left open:

1. What does "converged" mean per resource type?
2. How are individual resource states combined into a single score?
3. What are the retry and backoff parameters?
4. When does the Surveyor escalate rather than retry?

---

## Decisions

### Decision 1: Two-layer convergence model per resource

**Chosen**: Each resource type is evaluated in two layers. A resource passes only
if it passes **both** layers. Layer 1 checks Dolt consistency (immediate); Layer 2
checks runtime health (eventual).

| Layer | Name | Consistency | What it checks |
|-------|------|-------------|----------------|
| 1 | Dolt state | Strong — immediate after Dogs write `actual_topology` | `actual_topology` rows match `desired_topology` rows exactly |
| 2 | Process health | Eventual — may lag 30–60s after Dolt converges | Agents running, worktrees exist, Dolt connections healthy |

**Rationale**: Dolt convergence is a necessary precondition for process convergence.
A rig that is listed as `running` in Dolt but has no live agent process is not
converged. A rig that has a live process but has not written its `actual_rigs` row
is also not converged. Both layers must pass.

---

### Decision 2: Per-resource convergence criteria

#### 2a. Rig convergence

A rig is **converged** when all of the following hold:

| Check | Layer | Criterion |
|-------|-------|-----------|
| Row exists in `actual_rigs` | 1 | `actual_rigs` has a row with `name = desired.name` |
| `enabled` matches desired | 1 | `actual_rigs.enabled = desired_rigs.enabled` |
| Status is running | 1 | `actual_rigs.status = 'running'` |
| Heartbeat is fresh | 2 | `actual_rigs.last_seen >= NOW() - stale_ttl` |
| Mayor process is alive | 2 | At least one `actual_agent_config` row for this rig with `role = 'mayor'`, `status = 'running'`, and fresh `last_seen` |

**Disabled rigs**: a rig with `desired_rigs.enabled = FALSE` is converged when
`actual_rigs.enabled = FALSE` and `actual_rigs.status IN ('stopped', 'draining')`.
A disabled rig does not require a live Mayor process.

**Absent desired row**: if a rig exists in `actual_rigs` but NOT in `desired_rigs`,
it is a surplus rig. Surplus rigs count as non-converged for scoring purposes and
trigger a remove operation in the reconcile plan.

#### 2b. Polecat pool convergence

A rig's Polecat pool is **converged** when:

| Check | Layer | Criterion |
|-------|-------|-----------|
| Active count in range | 1 | `COUNT(actual_worktrees WHERE rig_name = ? AND status = 'active') BETWEEN desired_agent_config.min_count AND desired_agent_config.max_count` |
| No stale worktrees | 2 | Zero rows with `actual_worktrees.status = 'stale'` for this rig |
| Witness alive | 2 | At least one `actual_agent_config` row for this rig with `role = 'witness'`, `status = 'running'`, and fresh `last_seen` |

**Why range, not exact count**: Polecats are ephemeral. They spawn for a task and
die. At any instant the pool may have N−1 Polecats while one just exited and a
replacement is starting. An exact count check would cause perpetual non-convergence
on a healthy active rig. The range `[min_count, max_count]` reflects operational
intent: the pool is healthy if it has at least min_count workers and has not
exceeded max_count capacity.

`min_count` defaults to 0 (no Polecats required for convergence if none are
declared). `max_count` defaults to `desired_agent_config.max_count`.

#### 2c. Custom role convergence

A custom role instance is **converged** when:

| Check | Layer | Criterion |
|-------|-------|-----------|
| Row exists in `actual_custom_roles` | 1 | Row with matching `rig_name`, `role_name`, `instance_index` |
| Status is running | 1 | `actual_custom_roles.status = 'running'` |
| Heartbeat is fresh | 2 | `actual_custom_roles.last_seen >= NOW() - stale_ttl` |

**Instance count**: for `max_instances > 1`, convergence requires all N instances
(indices 0 through max_instances-1) to pass both checks. A missing instance index
is a non-converged resource.

**Town-scoped roles**: evaluated the same way but using `rig_name = '__town__'`
sentinel as per ADR-0007 Decision 5.

#### 2d. Formula convergence

A Formula is **converged** when:

| Check | Layer | Criterion |
|-------|-------|-----------|
| Schedule entry exists | 1 | Deacon's cron schedule has a live entry for this formula (read from Deacon state) |
| Most-recent scheduled Bead closed | 2 | The most recently due Bead for this formula was closed within `formula_grace_period` of its scheduled time |

**`formula_grace_period`** (default: 2× schedule interval, minimum 5 minutes):
the window after a scheduled trigger within which the corresponding Bead must be
closed. A Bead that is still open after the grace period is a convergence failure
for that Formula.

**Why process state, not process health**: Formulas are schedules, not persistent
processes. There is no Formula "process" to measure. Convergence is defined by
whether the schedule is executing correctly — the Bead lifecycle is the proxy.

**No `actual_formulas` table**: Formula convergence is checked by querying Deacon
state and Beads lifecycle, not by a dedicated actual_topology table. This is
consistent with ADR-0007 Decision 1 ("desired_formulas has no actual counterpart").

---

### Decision 3: Convergence score formula

**Chosen**: The convergence score is a weighted average across all desired resources.
Resources of higher operational criticality carry higher weight.

```
score = sum(weight_i * pass_i) / sum(weight_i)
```

Where `pass_i` is 1 if resource i passes both layers, 0 otherwise, and `weight_i`
is the resource weight from the table below.

| Resource type | Weight | Rationale |
|---------------|--------|-----------|
| Rig (enabled) | 3 | Rig convergence is prerequisite for all other resource convergence |
| Polecat pool | 2 | Active work capacity; non-convergence means tasks cannot execute |
| Custom role (rig-scoped) | 2 | Role is declared per-rig; absence blocks workflow insertion |
| Custom role (town-scoped) | 3 | Global role; absence affects all rigs |
| Formula | 1 | Schedule execution is best-effort; a missed cycle retries next interval |

**Alternatives considered**:

| Option | Reason rejected |
|--------|----------------|
| Unweighted fraction (equal weight) | A town with 10 rigs and 1 Formula would need all 11 resources at 1.0 threshold — a stale Formula blocks convergence equally to a down rig. Weights capture operational criticality. |
| Binary (0 or 1, all-or-nothing) | Too strict for development environments. A 0.9 threshold on a 10-rig cluster tolerates one rig being in a transient restart state, which is normal during a rolling upgrade. |
| Per-resource-type sub-scores | Produces a vector of scores, not a single go/no-go for merge. Operators need one number to reason about convergence. Sub-scores are surfaced in the escalation Bead for diagnostics. |

**Minimum score for merge**: the reconcile branch is merged when
`score >= convergence_threshold`. See Decision 4 for threshold values.

---

### Decision 4: Configurable convergence thresholds

**Chosen**: Two named threshold profiles with full numeric override support.

| Profile | `convergence_threshold` | `formula_grace_period` | Use case |
|---------|------------------------|----------------------|----------|
| `production` | 1.0 | 2× schedule interval | Any production or staging environment |
| `development` | 0.9 | 5× schedule interval | Local development, ephemeral test clusters |

**Configuration surface** (in Surveyor `[verify]` config block, referenced by
`docs/agents/surveyor.CLAUDE.md`):

```toml
[verify]
profile                  = "production"  # or "development"
convergence_threshold    = 1.0           # overrides profile value if set
formula_grace_period_s   = 0             # 0 = use profile default (2× or 5×)
stale_ttl_s              = 60            # 2× Deacon heartbeat interval (30s)
```

**Explicit `convergence_threshold` overrides the profile**. This allows
operators to set `profile = "production"` and `convergence_threshold = 0.95`
for an intermediate threshold without defining a new profile.

**Rationale for 1.0 production default**: in production, a rig that is listed in
`desired_topology` but is not running is a real incident. The Surveyor should not
merge a branch that records a "mostly converged" state in production — the
reconcile should be retried until convergence is full or escalated to Mayor.

**Rationale for 0.9 development default**: a 10-resource cluster at 0.9 allows
one resource to be in a transient restart state. This prevents spurious escalation
during local development where Deacon may be slow or temporarily absent.

---

### Decision 5: Retry with exponential backoff

**Chosen**: The verify loop retries with exponential backoff. The Surveyor waits
between verify attempts; it does not poll continuously.

```
retry_delay_seconds = base_delay * (2 ^ retry_count)
capped at: max_delay_seconds
```

| Parameter | Default | Config key |
|-----------|---------|------------|
| `base_delay` | 5s | `verify.base_delay_s` |
| `max_delay` | 120s | `verify.max_delay_s` |
| `max_retries` | 10 | `verify.max_retries` |

**Example backoff sequence** (base=5s, max=120s):
```
Attempt 1: wait 5s
Attempt 2: wait 10s
Attempt 3: wait 20s
Attempt 4: wait 40s
Attempt 5: wait 80s
Attempts 6–10: wait 120s each (capped)
Total max wait: 5+10+20+40+80+(5×120) = 755s ≈ 12.6 minutes before escalation
```

**Rationale for 10 retries**: a slow drain (30 Polecats each finishing a
multi-file refactor) may take 10–20 minutes. 10 retries with capped 120s delay
gives up to 13 minutes of retry time before escalation — sufficient for normal
long operations without masking genuinely stuck reconciles indefinitely.

**Jitter**: the Surveyor adds ±10% random jitter to each delay to prevent
synchronized retry storms when multiple Surveyors start simultaneously (e.g.
after a cluster reboot). Jitter is applied after capping:
`actual_delay = delay * (0.9 + 0.2 * random())`.

---

### Decision 6: Score regression detection — immediate escalation

**Chosen**: If the convergence score **decreases** between two consecutive verify
attempts, the Surveyor escalates immediately without exhausting retries.

**Regression definition**: `score[N] < score[N-1]` where N > 1.

**Rationale**: a score decrease between retries means something is actively
failing — a Dog Bead completed but left the cluster in a worse state, or an agent
that was healthy is now crashing. Waiting for more retries only delays discovering
an active incident. Immediate escalation gives Mayor the earliest possible signal.

**Score plateau (stuck)**: if the score does not change between two retries AND is
below threshold, the Surveyor continues retrying up to `max_retries`. A plateau
could be a slow-starting agent (e.g. Mayor on a large repo takes 20s to
initialise). Regression is distinct from plateau.

---

### Decision 7: Escalation Bead format

**Chosen**: On escalation (verify-exhausted, regression, or Dog failure), the
Surveyor files a high-priority Bead to Mayor with a structured description
containing all diagnostic context. The Bead title encodes the reconcile UUID
for correlation.

**Bead title format**:
```
RECONCILE ESCALATION: <uuid> score=<score> reason=<reason>
```

**Bead description body**:
```
Reconcile attempt <uuid> failed to converge.

Reason: <verify-exhausted | score-regression | dog-failure>

## Summary
- Convergence score: <score> (threshold: <threshold>)
- Retry attempts: <count>
- Total duration: <seconds>s
- Escalated at: <RFC3339>

## Desired State Snapshot (at plan time)
<JSON summary of desired_topology rows>

## Actual State Snapshot (at escalation)
<JSON summary of actual_topology rows>

## Delta (non-converged resources)
<list of resources that did not pass, with their status and last_seen>

## Sub-scores
- Rigs:               <n_pass>/<n_total> (weight 3)
- Polecat pools:      <n_pass>/<n_total> (weight 2)
- Custom roles (rig): <n_pass>/<n_total> (weight 2)
- Custom roles (town):<n_pass>/<n_total> (weight 3)
- Formulas:           <n_pass>/<n_total> (weight 1)

## Open Dog Beads (unresolved operations)
<list of Bead IDs that are still open, with titles>

## Reconcile Branch
Branch: reconcile/<uuid>
reconcile_log rows on that branch contain the full plan and verify history.
```

**Priority**: 0 (critical) for `dog-failure` and `score-regression`; 1 (high)
for `verify-exhausted`. Mayor must triage the critical cases first.

---

## Configuration Reference

All parameters are in the Surveyor's `[verify]` configuration block (specified
in full in `docs/agents/surveyor.CLAUDE.md`):

```toml
[verify]
# Convergence profile: "production" (1.0) or "development" (0.9)
profile                  = "production"

# Explicit threshold override (0.0–1.0). Overrides profile value if set.
convergence_threshold    = 1.0

# Seconds before a last_seen timestamp is considered stale.
# Default: 60s (2 × Deacon heartbeat).
stale_ttl_s              = 60

# Formula convergence: seconds after scheduled trigger within which the
# Bead must be closed. 0 = use profile default (production: 2× interval, dev: 5×)
formula_grace_period_s   = 0

# Backoff retry parameters
base_delay_s             = 5
max_delay_s              = 120
max_retries              = 10
```

---

## Consequences

### What becomes easier

- **Unambiguous go/no-go for merge**: a single numeric score against a threshold
  gives the Surveyor a deterministic merge decision. No subjective judgment about
  "mostly converged."
- **Tunable for environment**: production gets strict 1.0, development gets 0.9
  with a one-line profile override. Custom thresholds require a single config value
  change, not a code change.
- **Diagnostics in escalation**: the sub-score breakdown in the escalation Bead
  tells Mayor immediately which resource class is failing — rigs, pools, or roles.
- **Regression detection**: a decreasing score is an active incident signal.
  Immediate escalation avoids the Surveyor silently retrying while the cluster
  degrades.
- **Formula correctness**: defining Formula convergence via Bead lifecycle (not a
  missing `actual_formulas` table) is consistent with ADR-0007's decision and
  avoids a schema addition.

### New constraints introduced

- **Surveyor must track score history across retries** to implement regression
  detection. The verify loop must maintain `previous_score` in memory across
  retry iterations.
- **`formula_grace_period` requires Deacon schedule access**: the Surveyor must
  query Deacon configuration to determine the schedule interval for each Formula,
  in order to compute the default grace period. This interface must be specified
  in the Surveyor CLAUDE.md (→ dgt-9tj).
- **Weights are hardcoded, not configurable**: resource type weights (3, 2, 2, 3, 1)
  are not operator-configurable in this design. Operators adjust `convergence_threshold`
  to change the acceptable score; they do not adjust weights per resource.
  If operational experience reveals that Formula failures should be weighted more
  heavily, a new ADR can amend this decision.
- **Escalation Bead title format is a contract**: Mayor and monitoring tools may
  parse the title. Any change to the `RECONCILE ESCALATION: <uuid>` format must
  be treated as a breaking change and versioned.

### Out of scope for this ADR

- Surveyor CLAUDE.md content (→ dgt-9tj)
- `reconcile_log` DDL and reconcile branch protocol (→ ADR-0007b, dgt-wv5)
- Formula actual state tracking schema (→ dgt-fqg covers scoring only; DDL if needed is a separate issue)
- Deacon heartbeat interval configuration (→ Deacon CLAUDE.md)
- K8s operator convergence integration (→ dgt-3j8)
