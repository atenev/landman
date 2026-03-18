## Why

ADR-0001 established that `town-ctl` writes desired topology to Dolt `desired_topology` tables.
But writing desired state is not the same as converging to it. ADR-0001 left the convergence
mechanism as an assumption: "Mayor and Deacon watch those tables and converge to match." This
ADR designs that mechanism explicitly — without modifying the `gt` binary.

## What Changes

- Introduce the **Surveyor**: a long-lived Claude Code agent with a dedicated `CLAUDE.md`
  defining its identity as the topology reconciler. Deployed alongside `gt`, not inside it.
- The Surveyor watches `desired_topology` via Dolt change feed, reasons about the delta using
  LLM judgement (not a hardcoded diff), and files one Bead per atomic operation for
  Dogs/Deacon to execute.
- Introduce `actual_topology` Dolt tables written by Gas Town agents (Mayor, Deacon, Dogs) as
  they act. The Surveyor diffs these against `desired_topology` to verify convergence.
- Each reconcile attempt operates on a Dolt branch `reconcile/<uuid>` — a planning and audit
  artifact. Successful convergence merges the branch; failure abandons it (retained for audit).
- The Surveyor verifies convergence at two layers (Dolt state + process health) before merging.
  If convergence fails after N retries, it escalates to Mayor via a high-priority Bead.
- **No `gt` modification required.** The Surveyor participates through Dolt SQL and Beads —
  the same surfaces every existing Gas Town agent uses.

## Capabilities

### New Capabilities

- `surveyor-agent`: The Surveyor Claude Code agent — its `CLAUDE.md` identity, startup
  (GUPP-compliant), Dolt change-feed subscription, AI-reasoned delta logic, Bead creation
  protocol for operation delegation, mid-reconcile change handling, and Mayor reporting.
- `actual-topology-schema`: Dolt `actual_topology` tables (`actual_rigs`, `actual_agent_config`,
  `actual_worktrees`) written by Gas Town agents. Schema mirrors `desired_topology` for direct
  diffing. Includes staleness model (TTL, `last_seen`).
- `reconcile-branch-protocol`: Formal semantics for `reconcile/<uuid>` Dolt branches: what
  the branch contains (plan metadata + verification result), merge commit format, abandoned-branch
  retention, stale-branch TTL handling, concurrent-reconcile guard.
- `convergence-verification`: Per-resource-type convergence definition and scoring. Two-layer
  model (Dolt state + process health). Configurable thresholds. Escalation conditions.

### Modified Capabilities

- `town-toml-manifest`: `[town.agents] surveyor = true` block — already added in
  `declarative-town-topology` change. No additional schema changes needed here.

## Impact

- New Claude Code agent: Surveyor (`CLAUDE.md` + systemd unit)
- New Dolt tables: `actual_topology` schema (`actual_rigs`, `actual_agent_config`,
  `actual_worktrees`)
- Gas Town agents (Mayor, Deacon, Dogs): write `actual_topology` as they act — this is
  normal agent behaviour, not a `gt` binary modification
- Dogs: execute operation Beads filed by the Surveyor (Beads with `gt rig add`, drain
  instructions) — uses the existing Beads coordination mechanism
- No modification to the `gt` binary itself
- Depends on `declarative-town-topology` change: `desired_topology` tables and `town-ctl`
  must be deployed first
