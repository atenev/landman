## Context

ADR-0001 created `town-ctl` and `desired_topology` as the desired-state write path. The
convergence mechanism — how Gas Town actually reaches that desired state — was left implicit.
ADR-0002 specifies it: an external Claude Code agent (the Surveyor) watches `desired_topology`,
reasons about the delta, and coordinates execution via Beads.

The core constraint is identical to ADR-0001: **no modification to the `gt` binary**. The
Surveyor participates through Dolt SQL (external) and Beads (`bd create`, external). It is
invisible to `gt` — it uses the same Dolt and Beads surfaces every other Gas Town participant
uses.

Gas Town is AI-native: agent identity is the `CLAUDE.md`, not a process registered with `gt`.
Any Claude Code process with a well-formed `CLAUDE.md` that speaks Dolt SQL and uses `bd` is a
first-class participant. The Surveyor is exactly this.

Depends on: `declarative-town-topology` (desired_topology tables, town-ctl).

## Goals / Non-Goals

**Goals:**
- Design and specify the Surveyor `CLAUDE.md` (identity, protocol, GUPP startup, escalation)
- Design `actual_topology` Dolt schema (mirrors `desired_topology` for direct diff)
- Define the Dolt branch reconcile protocol (naming, merge commit, abandoned branch, stale TTL)
- Define convergence scoring per resource type and configurable thresholds
- Ensure GUPP compliance throughout (crash = restart from current state, no event replay)

**Non-Goals:**
- Modifying `gt` binary — prohibited
- Implementing a deterministic diff engine — the Surveyor uses LLM reasoning, not a state machine
- K8s operator integration — deferred (`dgt-3j8`)
- Deacon Formula for Surveyor health monitoring — deferred (pending Formula semantics)

## Decisions

### D1: Surveyor as an external Claude Code agent, not a gt role

The Surveyor is a long-lived Claude Code process with a dedicated `CLAUDE.md`. It is deployed
alongside `gt` (via systemd initially) but is not registered with or managed by `gt`.

Alternatives rejected:
- Deacon patrol inside `gt`: couples reconcile logic to `gt` release cycle
- Standalone Go service + `action_queue` Dolt tables: `gt` agents must consume `action_queue` —
  a binary modification on the consumption side
- Go service + Beads only: "Bead closed ≠ state reached" — no convergence proof
- Surveyor as a new `gt`-managed role: requires `gt` to enumerate/launch an 8th role

The Surveyor needs five capabilities, all externally available (zero `gt` changes):
read `desired_topology`, read `actual_topology` + filesystem, watch Dolt change feed,
delegate operations via `bd create`, escalate via `bd create` to Mayor.

### D2: AI reasoning for the delta, not a hardcoded diff

A row-by-row SQL diff identifies *what* differs. It cannot reason about *whether and how* to
act. Examples a hardcoded diff cannot handle:
- 20 desired Polecats, 18 actual — churn or failure?
- Remove rig X, but X has a Witness-escalated Polecat Mayor hasn't resolved — wait or force?
- Enable rig Y, but Y's repo has an active Refinery conflict — start agents or block?
- Drain running 40 minutes with 3 remaining Polecats — expected or hung?

The Surveyor uses LLM reasoning over desired + actual state, active Beads, and Dolt
operational state to plan the minimal safe set of operations. This replaces an unbounded
`if/else` rule engine.

### D3: Dogs as the execution layer, Surveyor as the planner

The Surveyor creates one Bead per atomic operation. Dogs/Deacon pick them up and execute.
Execution ordering is enforced through Bead dependencies (`bd dep add`): removes/drains before
adds, drain must complete before remove, independent rigs run in parallel.

This preserves Gas Town's existing agent hierarchy. The Surveyor does not know how to execute
operations — Dogs do. Existing Dogs already handle maintenance Beads; operation Beads from the
Surveyor use the same mechanism.

### D4: Reconcile branch as planning and audit artifact (not transaction isolation)

Each reconcile attempt opens a Dolt branch `reconcile/<uuid>`. The branch records:
- Plan metadata: UUID, timestamp, list of operation Bead IDs, desired state snapshot
- Verification result: actual state snapshot post-verify, convergence score

Beads write to Dolt `main`. Dogs write `actual_topology` to `main`. The branch is for
planning records and the final merge commit — not for isolating in-flight writes.

On successful convergence: branch merged to `main`. The merge commit is the durable audit
record containing the full before/after state.
On failure: branch abandoned (retained). The abandoned branch preserves the plan and the
failure reason — queryable after the fact.

### D5: Self-triggering via Dolt change feed, GUPP-compliant startup

The Surveyor subscribes to `desired_topology` changes. Any writer — `town-ctl`, K8s operator,
direct SQL — triggers reconcile automatically. The trigger is coupled to the state change, not
the tool.

GUPP compliance: on startup (or restart after crash), the Surveyor unconditionally reads current
desired and actual state and reconciles whatever delta exists. No event replay needed — state is
the event. Stale `reconcile/*` branches older than TTL are detected, marked abandoned with
reason `surveyor-crash`, and a fresh reconcile starts.

### D6: Two-layer convergence verify before branch merge

After all Dog Beads are closed, the Surveyor runs a verify loop:
1. **Dolt state layer**: `actual_topology` rows match `desired_topology` rows (strong
   consistency — immediate after Dogs complete operations)
2. **Process health layer**: agents running, worktrees exist, connections healthy (eventual
   consistency — may lag 30–60s)

Convergence score = weighted fraction of desired resources passing both layers. Configurable
thresholds: `1.0` for production, `0.9` for development. Score < threshold after N retries
with backoff → abandon branch + escalate to Mayor.

The verify loop closes the "Bead closed ≠ state reached" gap — actual state is checked
directly, not Bead lifecycle.

### D7: Mayor reporting at three points, never in the critical path

1. **Plan filed**: summary Bead to Mayor (inform, not block)
2. **Convergence confirmed**: close plan Bead with score and duration
3. **Escalation**: high-priority Bead with desired snapshot, actual snapshot, delta, and
   list of unresolved Dog Beads

Mayor is consulted only on escalation. For normal reconcile, it is informed asynchronously.

## Risks / Trade-offs

- **LLM non-determinism**: the Surveyor's delta reasoning may vary across runs. Mitigated by:
  the verify loop — convergence is checked against actual state, not the plan's assumptions.
  If the Surveyor reasons incorrectly, the verify loop catches it and escalates.
- **Stale branch TTL**: too short and a slow drain triggers false crash-recovery; too long and
  a crashed reconcile blocks the next one. Configurable in `[town.agents]` / Surveyor CLAUDE.md.
- **actual_topology staleness**: Dogs write `actual_topology` after acting. If a Dog crashes
  mid-write, the actual state row may be stale. Mitigated by `last_seen` TTL — the Surveyor
  treats rows with expired TTL as "unknown" and escalates rather than assuming convergence.
- **Mid-reconcile desired_topology change**: the Surveyor completes the current reconcile
  before re-evaluating. The subsequent change-feed event handles the incremental delta.
  This means a topology change may take up to one reconcile cycle to take effect.
- **Dog Bead partial failure**: one Dog Bead failure abandons the entire reconcile branch
  (transactional semantics). This is deliberate — partial apply can leave topology in an
  inconsistent state. Escalation gives Mayor full context to decide next action.

## Migration Plan

1. Deploy `declarative-town-topology` change first (desired_topology tables + town-ctl)
2. Design and deploy `actual_topology` tables; Gas Town agents (Deacon, Mayor, Dogs) begin
   writing to them as they act — this is additive, not a `gt` fork
3. Author Surveyor `CLAUDE.md` and systemd unit
4. Start Surveyor manually for initial testing (or via `town-ctl apply` with `surveyor = true`)
5. Surveyor begins watching `desired_topology` and filing operation Beads
6. Monitor first reconcile: review Dolt branch, Bead execution, convergence score
7. Tune stale-branch TTL and convergence thresholds

## Open Questions

- **actual_topology write protocol**: which `gt` agent role writes which rows? Proposed:
  Deacon writes `actual_rigs` (heartbeat), Polecats write their own `actual_agent_config` rows
  on spawn/die, Deacon writes `actual_worktrees`. Needs confirmation — see dgt-fkm.
- **Concurrent Surveyor guard**: what if two Surveyor processes start simultaneously (e.g.,
  systemd restart race)? Proposed: advisory lock in Dolt (a `surveyor_lock` row). See dgt-wv5.
- **Surveyor CLAUDE.md token budget**: a long-running agent that reads Dolt state on every
  reconcile may accumulate large context. Define context-reset protocol. See dgt-9tj.
