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

### Requirement: Stale-branch TTL default is 30 minutes; lock TTL default is 15 minutes
The stale-branch TTL (time after which an open `reconcile/*` branch is presumed from a crashed
Surveyor) SHALL default to 30 minutes. The advisory-lock TTL SHALL default to 15 minutes.
Both values SHALL be overridable via the Surveyor's `CLAUDE.md` configuration block.

Rationale for defaults: a reconcile involving a slow drain of 30 Polecats may take 10–20 minutes
end-to-end. 30 minutes gives headroom for legitimate long reconciles while ensuring a genuinely
crashed Surveyor is detected within one reconcile cycle. The lock TTL is shorter because a live
Surveyor refreshes the lock at each reconcile stage; a 15-minute dead lock is unambiguously stale.

#### Scenario: TTL default is respected
- **WHEN** no TTL override is configured and a branch timestamp is 31 minutes old
- **THEN** the Surveyor treats it as stale and abandons it with reason `surveyor-crash`

#### Scenario: TTL override is respected
- **WHEN** the Surveyor `CLAUDE.md` sets `stale_branch_ttl_minutes = 60`
- **THEN** a branch 31 minutes old is NOT treated as stale; a branch 61 minutes old IS

### Requirement: Abandoned branches are pruned after a configurable retention window
Abandoned `reconcile/*` branches (both `surveyor-crash` and Dog-failure cases) SHALL be pruned
from Dolt after a configurable retention window. The default retention window is 7 days.
Pruning SHALL occur at Surveyor startup, after stale-branch cleanup, before the first reconcile.
Pruned branches SHALL first be summarised (UUID, reason, timestamp) into a `reconcile_archive`
row on `main` so the audit record survives branch deletion.

Rationale: Dolt branch count grows unboundedly without pruning. Retaining branches for 7 days
gives operators time to inspect failures. The `reconcile_archive` table preserves queryability
of historical failures indefinitely without the storage cost of full branch retention.

#### Scenario: Abandoned branch within retention window is kept
- **WHEN** an abandoned branch timestamp is 3 days old and retention window is 7 days
- **THEN** the branch is NOT pruned; it remains queryable in `dolt branch -a`

#### Scenario: Abandoned branch past retention window is archived and deleted
- **WHEN** an abandoned branch timestamp is 8 days old and retention window is 7 days
- **THEN** the Surveyor writes a summary row to `reconcile_archive` on `main` and deletes the branch

#### Scenario: Successfully merged branches are not subject to retention
- **WHEN** a `reconcile/<uuid>` branch has been merged to `main`
- **THEN** the branch is deleted immediately after merge (Dolt retains the merge commit on `main`)

### Requirement: Dolt branch provides plan-isolation reads for the Surveyor's own writes
The Surveyor MAY write plan metadata to the `reconcile/<uuid>` branch mid-reconcile and read
back those writes on the same branch before merging. These reads are branch-isolated: they see
the branch's own uncommitted rows but NOT other branches' concurrent writes.

Dogs write `actual_topology` updates to `main`, NOT to the reconcile branch. The Surveyor reads
`actual_topology` from `main` during the verify loop (after switching its Dolt session to `main`
for that query). This means the reconcile branch is append-only for plan metadata; the Surveyor
never relies on branch isolation for operational correctness — only for audit record coherence.

#### Scenario: Plan metadata readable on branch before merge
- **WHEN** the Surveyor writes a `reconcile_log` row on the `reconcile/<uuid>` branch
- **THEN** a SELECT on that branch returns the row immediately (branch-local read)

#### Scenario: Dog writes to main are not visible on the reconcile branch
- **WHEN** a Dog updates `actual_topology` on `main` while the Surveyor holds the reconcile branch
- **THEN** a SELECT on the reconcile branch does NOT see the Dog's write (branch isolation)

#### Scenario: Verify loop reads actual_topology from main
- **WHEN** the Surveyor runs its verify loop
- **THEN** it queries `actual_topology` against `main`, not against the reconcile branch
