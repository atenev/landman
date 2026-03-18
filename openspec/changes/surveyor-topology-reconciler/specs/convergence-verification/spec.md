## ADDED Requirements

### Requirement: Two-layer convergence check before branch merge
The Surveyor SHALL verify convergence at two layers before merging a reconcile branch:
1. **Dolt state layer**: `actual_topology` rows match `desired_topology` rows for each resource
2. **Process health layer**: agents are running, worktrees exist, Dolt connections are healthy

Both layers must pass for a resource to count as converged.

#### Scenario: Resource passes both layers
- **WHEN** `actual_rigs` matches `desired_rigs` for rig `backend` AND the Mayor process
  for that rig is alive
- **THEN** rig `backend` counts as fully converged

#### Scenario: Resource fails process health layer
- **WHEN** `actual_rigs` matches `desired_rigs` for rig `frontend` BUT no Mayor process
  is running for it
- **THEN** rig `frontend` does NOT count as converged despite the Dolt state matching

### Requirement: Convergence score is a weighted fraction of converged resources
The convergence score SHALL be computed as the fraction of desired resources that pass both
layers. The score SHALL be between 0.0 and 1.0. Each resource type (rig, Polecat pool,
Formula) SHALL contribute equally unless weighted differently in configuration.

#### Scenario: Partial convergence scored correctly
- **WHEN** 9 of 10 desired rigs pass both convergence layers
- **THEN** the convergence score is 0.9

### Requirement: Convergence thresholds are configurable per environment
The minimum convergence score required to merge a reconcile branch SHALL be configurable.
Default values: `1.0` for production, `0.9` for development. The Surveyor SHALL NOT merge
a branch whose final score is below the configured threshold.

#### Scenario: Score at threshold allows merge
- **WHEN** threshold is 0.9 and score is 0.9
- **THEN** the branch is merged

#### Scenario: Score below threshold prevents merge
- **WHEN** threshold is 1.0 and score is 0.9
- **THEN** the branch is NOT merged; retry loop is entered

### Requirement: Polecat pool convergence uses a range, not an exact count
A Polecat pool SHALL be considered converged when the actual count is within `[min, max]`
range relative to the desired count. Exact count matching is not required (normal churn).

#### Scenario: Polecat pool within acceptable range
- **WHEN** desired `max_polecats = 20` and actual active Polecats = 18
- **THEN** the Polecat pool is considered converged (normal churn tolerance)

#### Scenario: Polecat pool below range fails convergence
- **WHEN** desired `max_polecats = 20` and actual active Polecats = 5
- **THEN** the Polecat pool fails convergence and the Surveyor investigates further

### Requirement: Formula convergence checks schedule entry existence
A Formula SHALL be considered converged when a matching schedule entry exists in the
Deacon config with the correct cron expression.

#### Scenario: Matching formula schedule is converged
- **WHEN** `desired_formulas` has `name="nightly-tests" schedule="0 2 * * *"` and Deacon
  config contains the matching entry
- **THEN** that formula counts as converged

### Requirement: Retry with exponential backoff before escalating
After a failed verify loop, the Surveyor SHALL wait and retry up to N times (configurable)
with exponential backoff before escalating. Backoff parameters (base delay, multiplier,
max delay) SHALL be configurable.

#### Scenario: Score improves on retry
- **WHEN** initial verify score is 0.8 but after one retry is 1.0
- **THEN** the branch is merged; no escalation is filed

#### Scenario: Score regresses between retries
- **WHEN** score goes from 0.9 to 0.7 between retries (active failure)
- **THEN** the Surveyor escalates immediately rather than waiting for all retries to exhaust

### Requirement: Regressing score triggers immediate escalation
If the convergence score decreases between verify retries (indicating an active failure, not
slow convergence), the Surveyor SHALL escalate immediately without waiting for N retries.

#### Scenario: Regression detected and escalated
- **WHEN** verify retry 2 score (0.7) is lower than retry 1 score (0.9)
- **THEN** the Surveyor abandons the branch and files an escalation Bead to Mayor immediately
