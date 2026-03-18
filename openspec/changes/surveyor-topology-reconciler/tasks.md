## 1. actual_topology Dolt Schema

- [ ] 1.1 Design `actual_rigs` table schema (name, repo, branch, enabled, status, last_seen, schema_version) [dgt-fkm]
- [ ] 1.2 Design `actual_agent_config` table schema (rig_name, role, pid, model, status, last_seen, schema_version) [dgt-fkm]
- [ ] 1.3 Design `actual_worktrees` table schema (rig_name, path, branch, clean, last_seen, schema_version) [dgt-fkm]
- [ ] 1.4 Define staleness TTL defaults and configuration surface [dgt-fkm]
- [ ] 1.5 Write Dolt SQL migration files for all three tables [dgt-7ve]
- [ ] 1.6 Define which Gas Town agent role writes which table (Deacon: rigs + worktrees; Polecats/Dogs: agent_config) [dgt-fkm]

## 2. Reconcile Branch Protocol

- [ ] 2.1 Define `reconcile_log` Dolt table schema (reconcile_uuid, timestamp, bead_ids, desired_snapshot, verify_result, convergence_score, reason) [dgt-wv5]
- [ ] 2.2 Define branch naming convention (`reconcile/<uuid-v4>`) and lifecycle states [dgt-wv5]
- [ ] 2.3 Define merge commit message format (UUID, score, duration, op count, timestamp) [dgt-wv5]
- [ ] 2.4 Define abandoned branch retention policy (retain all? TTL-based cleanup?) [dgt-wv5]
- [ ] 2.5 Define stale-branch TTL defaults and detection logic for crash recovery [dgt-wv5]
- [ ] 2.6 Design `surveyor_lock` advisory lock table (instance_id, locked_at, schema_version) [dgt-wv5]
- [ ] 2.7 Define concurrent reconcile guard: lock acquisition, stale lock claim, timeout [dgt-wv5]

## 3. Convergence Verification Scoring

- [ ] 3.1 Define convergence criteria per resource type: rig (status + process health) [dgt-fqg]
- [ ] 3.2 Define Polecat pool convergence: acceptable range [min, max] relative to desired [dgt-fqg]
- [ ] 3.3 Define Formula convergence: schedule entry existence check [dgt-fqg]
- [ ] 3.4 Define convergence score formula (weighted fraction) [dgt-fqg]
- [ ] 3.5 Define configurable thresholds (production=1.0, development=0.9) [dgt-fqg]
- [ ] 3.6 Define retry/backoff parameters (N retries, base delay, multiplier, max delay) [dgt-fqg]
- [ ] 3.7 Define regression detection logic (score decreasing between retries → immediate escalation) [dgt-fqg]

## 4. Surveyor CLAUDE.md

- [ ] 4.1 Draft Surveyor CLAUDE.md: identity, role, GUPP startup protocol [dgt-9tj]
- [ ] 4.2 Specify Dolt change-feed subscription and reconnect behaviour in CLAUDE.md [dgt-9tj]
- [ ] 4.3 Specify delta reasoning protocol (what context to read: desired, actual, active Beads, operational state) [dgt-9tj]
- [ ] 4.4 Specify Bead creation protocol: one Bead per atomic op, reconcile UUID in description, operation ordering via bd dep add [dgt-9tj]
- [ ] 4.5 Specify mid-reconcile desired_topology change handling (complete current pass, queue follow-up) [dgt-9tj]
- [ ] 4.6 Specify verify loop protocol: retry count, backoff, regression detection, escalation format [dgt-9tj]
- [ ] 4.7 Specify Mayor reporting: plan summary Bead, convergence Bead, escalation Bead format [dgt-9tj]
- [ ] 4.8 Specify stale-branch cleanup on startup: TTL check, abandonment write, fresh reconcile start [dgt-9tj]
- [ ] 4.9 Specify context-reset protocol (token budget management for long-running agent) [dgt-9tj]
- [ ] 4.10 Specify operation Bead schema: fields required for Dogs to execute without ambiguity [dgt-9tj]

## 5. Surveyor Lifecycle

- [ ] 5.1 Write systemd unit file for Surveyor process (dev/initial deployment) [dgt-xxh]
- [ ] 5.2 Implement process-liveness check in `town-ctl apply` for `[town.agents] surveyor = true` [dgt-q8q]
- [ ] 5.3 Document long-term path: Deacon Formula for Surveyor health monitoring [dgt-q8q]

## 6. Integration and Testing

- [ ] 6.1 End-to-end test: town-ctl apply → Surveyor reconcile → Dogs execute → verify loop passes [dgt-3aa]
- [ ] 6.2 Test crash recovery: kill Surveyor mid-reconcile, restart, verify stale branch abandoned and reconcile resumed [dgt-3aa]
- [ ] 6.3 Test escalation path: introduce a failing Dog Bead, verify Mayor receives escalation Bead with full context [dgt-3aa]
- [ ] 6.4 Test concurrent guard: start two Surveyor instances, verify only one reconciles [dgt-3aa]
