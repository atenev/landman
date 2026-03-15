# ADR-0002: The Surveyor — Topology Reconciler Design

- **Status**: Proposed
- **Date**: 2026-03-15
- **Beads issue**: dgt-aj7
- **Deciders**: Aleksandar Tenev

---

## Context

ADR-0001 established that `town-ctl` writes desired topology state to Dolt
`desired_topology` tables, and that Gas Town converges to match. However,
ADR-0001 left the convergence mechanism as an unstated assumption: "Mayor and
Deacon watch those tables via Dolt change feed and converge to match."

This ADR designs that mechanism explicitly. The core questions are:

1. Where does the reconciler live — inside `gt`, as a standalone service, or as
   a new Gas Town agent role?
2. How is it triggered?
3. How does it guarantee convergence, not just execution?
4. How is it introduced without forking or modifying the `gt` binary?

Gas Town's architecture is AI-native: every actor is a Claude Code instance
identified by its `CLAUDE.md`. The coordination plane between agents is Dolt
(shared state) and Beads (work items). `gt` is the agent runtime, but agent
identity and coordination are independent of the `gt` binary.

---

## Decisions

### Decision 1: The reconciler is an external Claude Code agent — the Surveyor

**Chosen**: A long-lived Claude Code process with a dedicated `CLAUDE.md` that
defines its identity as the topology reconciler. Deployed alongside `gt`, not
inside it. No `gt` modification required.

**Alternatives considered**:

| Option | Reason rejected |
|--------|-----------------|
| Deacon patrol inside `gt` | Couples topology control-plane logic to the agent runtime binary. The reconciler becomes permanently tied to Gas Town's release cycle — the exact problem ADR-0001 solved for `town-ctl`. |
| Standalone Go service writing to `action_queue` Dolt tables | `gt` agents must consume `action_queue`, which requires modifying the `gt` binary to add a new Dolt change-feed subscription. A fork in disguise — the coupling is on the consumption side rather than the generation side. |
| Go service writing Beads (no action_queue) | Deterministic only at the Bead-generation layer. Execution is still AI-driven by Dogs/Polecats. "Bead closed" ≠ "desired state reached" — no proof of convergence. Go service is architecturally foreign to an AI-native system with no Witness, no Mayor escalation path, and no place in the agent hierarchy. |
| Surveyor as new `gt`-managed role | Would require `gt` to enumerate, launch, and supervise an 8th role — a binary modification. |

**Rationale**:

Gas Town's role boundary is the `CLAUDE.md`, not the `gt` binary. Any Claude
Code process running with a well-formed `CLAUDE.md` that speaks Dolt SQL and
uses `bd` is a first-class Gas Town participant. `gt` does not enumerate or
register agents — coordination is through Dolt and Beads, which are both
accessible from outside `gt`.

The Surveyor requires exactly four capabilities, all externally available:

| Capability | Mechanism | `gt` change? |
|------------|-----------|--------------|
| Read `desired_topology` | Dolt SQL (external) | No |
| Read actual state | Dolt SQL (external) | No |
| Watch for state changes | Dolt change feed / binlog (external) | No |
| File escalations | `bd create` | No |

**The Surveyor is invisible to `gt`.** It participates in the Gas Town
ecosystem through the same Dolt and Beads surfaces every other participant uses.

---

### Decision 2: The Surveyor is self-triggering via Dolt change feed

**Chosen**: The Surveyor is long-lived and subscribes to `desired_topology`
changes directly via Dolt's change feed. It is self-triggering — no external
daemon, no `town-ctl` involvement.

**Alternatives considered**:

**Option A — `town-ctl` spawns the Surveyor after writing to Dolt**

Rejected because:
- ADR-0001 Decision 3 states explicitly: "`town-ctl` has no knowledge of Gas
  Town internals." Spawning a Gas Town agent is Gas Town-internal knowledge.
- `town-ctl` is a one-shot CLI. If it crashes after writing to Dolt but before
  spawning the Surveyor, reconcile is silently lost. No GUPP compliance.
- If a K8s operator (dgt-3j8) or human writes directly to `desired_topology`,
  no Surveyor is ever spawned — the trigger is coupled to the tool, not the
  state change.

**Option B — Separate watcher daemon tailing Dolt binlog**

Rejected because it is a third deployed component with its own crash-failure
mode. To be GUPP-compliant, it must persist a record before acting — introducing
a `reconcile_queue` table, the same complexity as `action_queue`.

**Rationale for self-triggering**:

The trigger should be coupled to the state change, not to the tool that caused
it. The Surveyor watches `desired_topology` directly. Any writer — `town-ctl`,
K8s operator, direct SQL — triggers reconcile automatically.

GUPP compliance is natural: on crash and restart, the Surveyor unconditionally
re-reads current desired and actual state and reconciles whatever delta exists.
No events are lost, because the state itself is the event.

```
Surveyor startup → read desired_topology → read actual_topology → reconcile delta
Desired_topology changes → Dolt change feed fires → Surveyor reconcile loop
```

---

### Decision 3: Dolt branch as the reconcile transaction

**Chosen**: Each reconcile attempt operates on a Dolt branch
`reconcile/<uuid>`. The branch is merged to `main` on successful convergence
and abandoned on failure. Rollback is free — branch abandonment requires no
compensating operations.

**Rationale**:

Dolt is git-for-SQL. Every reconcile attempt becomes a reviewable, auditable
Dolt commit with full history. The merge commit is the proof of convergence:
it contains the desired state snapshot, the actual state snapshot after
verification, and the convergence score at close time. Failed attempts
(abandoned branches) are also retained — the full history of reconcile
attempts is queryable.

Partial failure semantics: if any operation in the reconcile plan fails, the
entire branch is abandoned. `desired_topology` main is unchanged. The
Surveyor files an escalation Bead to Mayor and exits the reconcile loop.
This is the transactional option — one bad rig does not partially apply
the rest.

---

### Decision 4: Verify loop with two-layer convergence check

**Chosen**: After applying all operations, the Surveyor runs a verify loop
before merging the reconcile branch. Convergence is checked at two layers
with different consistency properties.

| Layer | What it checks | Consistency |
|-------|---------------|-------------|
| Dolt state | `actual_topology` rows match `desired_topology` rows | Strong — immediate after operations complete |
| Process health | Agents running, worktrees exist, connections healthy | Eventual — may lag 30–60s after Dolt converges |

The reconcile branch is only merged when both layers converge within
configured thresholds.

**Convergence score**: fraction of desired rigs that pass both layers.
Configurable threshold — `1.0` for critical environments, `0.9` acceptable
for development. If score < threshold after N retries with backoff, the
branch is abandoned and an escalation Bead is filed to Mayor.

**Why this is stronger than Go+Beads**:

A Go service writes Beads and considers its job done when Beads are closed.
"Bead closed" means "agent decided it was done" — not "desired state reached."
The verify loop checks actual state directly, not Bead lifecycle. The Surveyor
provides proof of convergence; a Go service provides proof of task assignment.

---

### Decision 5: Beads as the escalation channel — no action_queue

**Chosen**: The Surveyor uses `bd create` to file escalation Beads to Mayor
when it cannot converge. No `action_queue` Dolt tables are introduced.

**Rationale**:

Introducing `action_queue` requires `gt` agents to consume it — a binary
modification. Beads are the existing, external-writable coordination primitive
in Gas Town. Any agent (including the Surveyor) can file a Bead. Mayor
already processes Beads. No new infrastructure, no new `gt` contract.

For normal operations (rig add, drain, scale), the Surveyor executes directly
rather than delegating to Beads. Beads are used only for escalation: stuck
drains, partial failure requiring human judgement, repeated convergence
failures.

---

### Decision 6: Surveyor lifecycle via `town.toml` with systemd fallback

**Chosen**: The Surveyor's existence is declared in `town.toml` under
`[town.agents]`. `town-ctl` ensures the Surveyor process is running as part
of `apply`. The host process supervisor (systemd) handles restart on crash.

```toml
[town.agents]
surveyor = true   # enable topology reconciler
```

**Phases**:

- **Short term (development)**: launch Surveyor manually or via systemd unit
  alongside `gt`. No `town.toml` integration required during prototyping.
- **Medium term**: `town-ctl apply` checks for and launches the Surveyor
  process if `town.agents.surveyor = true`.
- **Long term**: a Deacon Formula provides Gas Town-native health monitoring
  and restart, once Formula semantics supporting `ensure-process` are
  validated.

**Bootstrapping**: `town-ctl apply` both writes `desired_topology` to Dolt
and ensures the Surveyor is running. The Surveyor then takes over all
subsequent reconcile operations. `town-ctl` has no further involvement in
the reconcile loop — consistent with ADR-0001 Decision 3.

---

## Surveyor State Machine

```
┌─────────────────────────────────────────────────────────┐
│                     SURVEYOR LOOP                       │
│                                                         │
│  startup / change-feed event                            │
│         │                                               │
│         ▼                                               │
│  Read desired_topology (Dolt)                           │
│  Read actual_topology  (Dolt)                           │
│         │                                               │
│         ▼                                               │
│  Compute delta                                          │
│         │                                               │
│    empty?──── yes ──────────────────► idle / wait       │
│         │                                               │
│         ▼ no                                            │
│  Open Dolt branch reconcile/<uuid>                      │
│         │                                               │
│         ▼                                               │
│  Execute plan (removes before adds)                     │
│         │                                               │
│    step fails?──── yes ──► abandon branch               │
│         │                        │                      │
│         ▼ no                     ▼                      │
│  VERIFY LOOP                bd create escalation Bead   │
│    re-query actual state    │ (→ Mayor)                 │
│    compute convergence score◄────────────────────────── │
│         │                                               │
│    score >= threshold? ── yes ── merge branch ──► done  │
│         │                                               │
│    retries < N? ─── yes ──► wait + retry                │
│         │                                               │
│    no ──► abandon branch + bd create escalation Bead    │
└─────────────────────────────────────────────────────────┘
```

---

## Consequences

### What becomes easier

- **No `gt` fork**: the entire reconcile subsystem is external to the `gt`
  binary. It can be versioned, deployed, and replaced independently.
- **Auditable convergence**: every reconcile attempt is a Dolt branch. Success
  is a merge commit with before/after state snapshots. Failure is a retained
  abandoned branch. The full history is queryable.
- **GUPP compliance**: crash recovery requires no event replay — the Surveyor
  re-reads current state on startup and reconciles whatever delta exists.
- **Multi-actuator**: any writer to `desired_topology` triggers reconcile.
  `town-ctl`, K8s operator (dgt-3j8), and direct SQL all work identically.
- **Escalation path**: stuck reconciles become Mayor-level Beads with full
  context — not silent failures or hung processes.

### New constraints introduced

- **`actual_topology` Dolt tables** must exist alongside `desired_topology`
  for the Surveyor to diff against. These tables are written by Gas Town
  agents as they act; schema must be designed (→ new issue).
- **Surveyor `CLAUDE.md`** must precisely specify: Dolt change-feed
  subscription, diff logic, branch naming, verify loop parameters, escalation
  thresholds, and GUPP startup behaviour. Ambiguity in the CLAUDE.md is
  a correctness risk (→ new issue).
- **Dolt branch reconcile protocol** requires formal semantics: naming
  convention, merge commit format, abandoned-branch retention policy,
  concurrent-reconcile guard (→ new issue).
- **Convergence scoring** requires a formal definition per resource type:
  what "converged" means for a rig, for a Polecat pool, for a Formula
  schedule. Approximate convergence thresholds must be configurable
  (→ new issue).
- **`town.toml` schema extension**: `[town.agents]` block must be added to
  the manifest format, Go structs, and JSON Schema (→ affects dgt-4gp,
  dgt-wpk).

### Out of scope for this ADR

- `actual_topology` Dolt table schema (→ new issue from this ADR)
- Surveyor `CLAUDE.md` content (→ new issue from this ADR)
- Dolt branch reconcile transaction protocol (→ new issue from this ADR)
- Convergence verification scoring and thresholds (→ new issue from this ADR)
- `[town.agents]` schema extension (→ new issue from this ADR)
- K8s operator reconcile integration (→ dgt-3j8)
- Deacon Formula for Surveyor health monitoring (→ future, pending Formula
  semantics validation)
