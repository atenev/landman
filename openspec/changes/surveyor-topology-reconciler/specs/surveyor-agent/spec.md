## ADDED Requirements

### Requirement: Surveyor is a Claude Code agent identified by CLAUDE.md
The Surveyor SHALL be a Claude Code process running with a dedicated `CLAUDE.md` that defines
its identity as the topology reconciler. It SHALL NOT be a Go service or any other process
type. It SHALL NOT be registered with or managed by `gt`.

#### Scenario: Surveyor starts without gt
- **WHEN** the Surveyor Claude Code process is launched with its CLAUDE.md
- **THEN** it initialises correctly without `gt` being involved in its registration

### Requirement: GUPP-compliant startup — reconcile from current state on every boot
On every startup (initial launch or restart after crash), the Surveyor SHALL unconditionally:
1. Read current `desired_topology` from Dolt
2. Read current `actual_topology` from Dolt
3. Check for stale `reconcile/*` branches older than the configured TTL and mark them abandoned
4. Compute delta and begin a reconcile pass if delta is non-empty

The Surveyor SHALL NOT skip the startup reconcile pass based on any persisted state.

#### Scenario: Startup reconcile on initial launch
- **WHEN** the Surveyor starts for the first time with non-empty desired_topology
- **THEN** it computes the delta and files operation Beads without waiting for a change-feed event

#### Scenario: Crash recovery reconcile
- **WHEN** the Surveyor restarts after a crash with a stale open `reconcile/<uuid>` branch
- **THEN** it marks the stale branch abandoned with reason `surveyor-crash` and starts a fresh
  reconcile from current state

### Requirement: Dolt change-feed subscription triggers reconcile
The Surveyor SHALL subscribe to `desired_topology` changes via the Dolt change feed. Any
change to `desired_topology` (from any writer) SHALL trigger a new reconcile evaluation.

#### Scenario: town-ctl write triggers reconcile
- **WHEN** `town-ctl apply` writes new rows to `desired_topology`
- **THEN** the Surveyor's change-feed handler triggers a reconcile pass

#### Scenario: Direct SQL write triggers reconcile
- **WHEN** a row is inserted directly into `desired_topology` via SQL
- **THEN** the Surveyor's change-feed handler triggers a reconcile pass

### Requirement: Mid-reconcile change deferred to next pass
If `desired_topology` changes while a reconcile is in progress, the Surveyor SHALL complete
the current reconcile before re-evaluating. It SHALL NOT restart the in-flight reconcile.
The subsequent change-feed event SHALL trigger a new reconcile pass for the incremental delta.

#### Scenario: Change during reconcile is deferred
- **WHEN** `desired_topology` is updated while a reconcile branch is open
- **THEN** the Surveyor finishes the current pass and then starts a new pass for the delta

### Requirement: AI-reasoned delta, not a hardcoded diff
The Surveyor SHALL use LLM reasoning to interpret the delta between desired and actual state
in context of active Beads and operational state, and plan the minimal safe set of operations.
It SHALL NOT implement a deterministic row-by-row diff as the sole decision mechanism.

#### Scenario: Ambiguous Polecat count evaluated in context
- **WHEN** desired has 20 Polecats and actual has 18
- **THEN** the Surveyor reads active Beads to determine if the 2 missing Polecats are normal
  churn or a convergence failure, and acts accordingly

### Requirement: One Bead per atomic operation, filed with reconcile UUID
The Surveyor SHALL create one Bead per atomic operation (`bd create`). Each Bead description
SHALL include the reconcile UUID so the Surveyor can track plan completion. Operation Beads
are filed for Dogs/Deacon to execute — the Surveyor does not execute operations itself.

#### Scenario: Rig add filed as a Bead
- **WHEN** desired has a new rig `backend` and actual does not
- **THEN** the Surveyor files a Bead: `"Add rig: backend"` with description including the
  reconcile UUID and the `gt rig add` command parameters

### Requirement: Operation ordering enforced via Bead dependencies
The Surveyor SHALL enforce operation ordering through `bd dep add`:
- Drain Beads SHALL be dependencies of remove Beads (drain before remove)
- Remove Beads SHALL complete before add Beads for the same resource
- Independent rig operations SHALL have no dependencies (parallel execution)

#### Scenario: Drain blocks remove
- **WHEN** a rig must be drained and removed
- **THEN** the Surveyor creates a drain Bead and a remove Bead, and adds the drain as a
  dependency of the remove via `bd dep add`

### Requirement: Mayor receives plan, convergence, and escalation Beads
The Surveyor SHALL file three categories of Beads to Mayor:
1. **Plan summary** (inform): filed when operation Beads are created, not blocking
2. **Convergence confirmed**: closed when verify loop passes, includes score and duration
3. **Escalation**: high-priority Bead when convergence fails, includes desired snapshot,
   actual snapshot, delta, and list of unresolved Dog Beads

#### Scenario: Mayor informed of plan without blocking
- **WHEN** the Surveyor files operation Beads
- **THEN** a plan summary Bead is filed to Mayor; reconcile proceeds without waiting for Mayor

#### Scenario: Escalation filed on verify failure
- **WHEN** the convergence score remains below threshold after N retries
- **THEN** a high-priority escalation Bead is filed to Mayor with full context and the
  reconcile branch is abandoned
