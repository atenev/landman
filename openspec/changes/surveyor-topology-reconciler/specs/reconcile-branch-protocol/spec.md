## ADDED Requirements

### Requirement: Each reconcile attempt opens a Dolt branch named reconcile/<uuid>
The Surveyor SHALL open a Dolt branch named `reconcile/<uuid>` (UUID v4) at the start of each
reconcile pass. The branch SHALL be used for plan metadata and audit records — not as a
transaction isolation scope for Dog Bead execution (Dogs write to `main`).

#### Scenario: Branch created at reconcile start
- **WHEN** the Surveyor starts a new reconcile pass
- **THEN** a Dolt branch named `reconcile/<uuid>` is created from current `main`

### Requirement: Reconcile branch records plan metadata and verification result
The reconcile branch SHALL contain:
- Plan metadata: reconcile UUID, timestamp, list of operation Bead IDs, desired state snapshot
- Verification result (written after verify loop): actual state snapshot, convergence score,
  duration, operation count

These records SHALL be written as rows in a `reconcile_log` Dolt table on the branch.

#### Scenario: Plan metadata written to branch
- **WHEN** the Surveyor files operation Beads
- **THEN** a `reconcile_log` row is written on the branch with the Bead IDs and desired snapshot

### Requirement: Successful convergence merges the branch to main with a structured commit message
When the verify loop passes, the Surveyor SHALL merge the `reconcile/<uuid>` branch to `main`.
The merge commit message SHALL include: reconcile UUID, convergence score, duration (seconds),
operation count, and timestamp.

#### Scenario: Merge commit is queryable in Dolt log
- **WHEN** a reconcile converges successfully
- **THEN** `dolt log` on `main` shows a merge commit with the reconcile UUID and score

### Requirement: Failed reconcile abandons the branch with a reason
When a reconcile fails (Dog Bead failure or verify loop exhausted), the Surveyor SHALL abandon
the branch by writing a failure reason to `reconcile_log` on the branch and then closing the
branch without merging. The branch SHALL be retained for audit.

#### Scenario: Abandoned branch retained after failure
- **WHEN** a Dog Bead fails during reconcile
- **THEN** the `reconcile/<uuid>` branch is not merged; it remains in Dolt with the failure
  reason recorded on it

### Requirement: Stale branches are detected and abandoned on Surveyor startup
On startup, the Surveyor SHALL query for any open `reconcile/*` branches. Any branch whose
plan metadata timestamp is older than the configured stale-branch TTL SHALL be marked
abandoned with reason `surveyor-crash` before starting a fresh reconcile.

#### Scenario: Stale branch from crashed reconcile cleaned up
- **WHEN** the Surveyor restarts and finds a `reconcile/<uuid>` branch 2× older than TTL
- **THEN** it writes reason `surveyor-crash` to that branch's `reconcile_log` and starts fresh

### Requirement: Concurrent reconcile guard via advisory lock
The Surveyor SHALL check for an advisory lock in Dolt (a `surveyor_lock` table row) before
starting a reconcile pass. If a lock exists from a different instance, it SHALL wait and retry.
If the lock is stale (older than TTL), it SHALL claim it and proceed.

#### Scenario: Two Surveyor instances started simultaneously
- **WHEN** two Surveyor processes start at the same time
- **THEN** only one proceeds with the reconcile; the other waits until the lock is released

#### Scenario: Stale lock claimed
- **WHEN** a `surveyor_lock` row exists but is older than TTL
- **THEN** the Surveyor overwrites the lock and proceeds with reconcile
