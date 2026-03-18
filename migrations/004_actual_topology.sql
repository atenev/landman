-- Migration: 004_actual_topology
-- Issue:     dgt-fkm
-- Purpose:   Create the actual_topology Dolt tables that Gas Town agents write
--            as they act. The Surveyor diffs desired_topology against
--            actual_topology to compute its reconcile plan (ADR-0002).
--
-- Versioning strategy: parallel to ADR-0003 — a single actual_topology_versions
-- table is the versioning authority. The Surveyor reads it before touching any
-- actual_topology data (see Design Notes below).
--
-- Writer model (who writes each table):
--   actual_rigs         — Mayor on rig add/remove; Deacon heartbeat (every ~30s)
--   actual_agent_config — each agent process on startup and shutdown;
--                         Deacon heartbeat covers Mayor/Deacon themselves
--   actual_worktrees    — Polecat/Witness on worktree create and cleanup;
--                         Deacon heartbeat (status+clean refresh)
--   actual_custom_roles — custom role process on spawn and on normal/abnormal exit;
--                         Deacon heartbeat for persistent roles
--
-- Staleness model:
--   All tables with running-process semantics carry a last_seen TIMESTAMP.
--   Deacon refreshes last_seen on every heartbeat (default: 30s interval).
--   A row whose last_seen is older than the stale TTL (default: 2 × heartbeat, 60s)
--   is considered stale and treated as an abnormal absence. The Surveyor uses this
--   to distinguish:
--     - status='starting' + fresh last_seen  → agent is launching, not yet running
--     - status='starting' + stale last_seen  → failed to start
--     - row absent entirely                  → agent was never declared or has been
--                                              cleanly removed (Mayor deleted it)
--
-- actual_topology_versions decision (see Design Notes):
--   Deacon writes actual_topology_versions as the first step whenever it updates
--   any actual_topology table. The Surveyor reads it as a pre-flight check — same
--   pattern as desired_topology_versions (ADR-0003). The Surveyor does NOT write
--   actual_topology; it is a read-only consumer.

-- ============================================================================
-- actual_topology_versions
--   One row per actual_topology table. Written by Deacon as the first step of
--   any actual_topology update. Read by the Surveyor before querying any
--   actual_topology data.
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_topology_versions (
    table_name     VARCHAR(128)  NOT NULL,
    schema_version INT           NOT NULL,
    written_by     VARCHAR(128),             -- e.g. "deacon/heartbeat/0.1.0"
    written_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (table_name)
);

-- ============================================================================
-- actual_rigs
--   One row per rig that Gas Town is managing (or attempting to manage).
--   Mirrors desired_rigs shape for direct Surveyor diff.
--
--   status values:
--     starting — Mayor has been launched but has not yet confirmed readiness
--     running  — Mayor is healthy and the Polecat pool is active
--     draining — rig is being gracefully wound down (no new Beads assigned)
--     stopped  — rig is present but all agents are stopped
--     failed   — rig startup/drain exceeded TTL or Mayor reported a fatal error
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_rigs (
    name       VARCHAR(128)                                              NOT NULL,
    repo       TEXT                                                      NOT NULL,
    branch     VARCHAR(256)                                              NOT NULL,
    enabled    BOOLEAN                                                   NOT NULL DEFAULT TRUE,
    status     ENUM('starting', 'running', 'draining', 'stopped', 'failed') NOT NULL DEFAULT 'starting',
    last_seen  TIMESTAMP                                                 NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP                                                 NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (name)
);

-- ============================================================================
-- actual_agent_config
--   One row per (rig, role) agent that is currently running or last known alive.
--   Mirrors desired_agent_config shape for direct Surveyor diff.
--   Roles: mayor, witness, refinery, deacon, polecat, plus any custom role name.
--
--   pid is the OS process ID of the Claude Code instance. NULL means the agent
--   is starting (pid not yet known) or was stopped cleanly (row not deleted yet).
--   A NULL pid with last_seen > stale TTL indicates a failed start.
--
--   status values:
--     starting — agent process launched, waiting for first heartbeat write
--     running  — agent is healthy (last heartbeat within TTL)
--     stopped  — agent exited cleanly (Deacon marked before removing row)
--     failed   — agent exited abnormally or heartbeat TTL exceeded
--     crashed  — no heartbeat + process not found in OS (Deacon detected)
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_agent_config (
    rig_name   VARCHAR(128)                                                       NOT NULL,
    role       VARCHAR(128)                                                       NOT NULL,
    pid        BIGINT,                          -- NULL while starting or after clean stop
    model      VARCHAR(256),                    -- as reported by the running process
    status     ENUM('starting', 'running', 'stopped', 'failed', 'crashed')       NOT NULL DEFAULT 'starting',
    last_seen  TIMESTAMP                                                          NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rig_name, role)
);

-- ============================================================================
-- actual_worktrees
--   One row per active git worktree on a rig.
--   The Surveyor uses this table to verify that Polecat pool capacity matches
--   desired max_polecats and to detect stale/orphaned worktrees.
--
--   clean: TRUE if `git status` reports a clean working tree (no uncommitted
--   changes). FALSE means the worktree has in-progress work.
--   bead_id: the Beads issue ID the Polecat in this worktree is working on.
--   NULL for worktrees that are idle (reserved but no active task).
--
--   status values:
--     active — a Polecat is working in this worktree
--     idle   — worktree exists but no Polecat is currently assigned
--     stale  — worktree last_seen exceeded TTL; likely orphaned after a crash
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_worktrees (
    rig_name   VARCHAR(128)                                NOT NULL,
    path       TEXT                                        NOT NULL,
    branch     VARCHAR(256)                                NOT NULL,
    clean      BOOLEAN                                     NOT NULL DEFAULT TRUE,
    status     ENUM('active', 'idle', 'stale')             NOT NULL DEFAULT 'idle',
    bead_id    VARCHAR(128),                 -- NULL when idle
    last_seen  TIMESTAMP                                   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rig_name, path(512))
);

-- ============================================================================
-- actual_custom_roles
--   One row per running (or last-known) instance of a custom role agent.
--   Mirrors desired_rig_custom_roles + desired_custom_roles shape for diffing.
--
--   For scope=town custom roles, rig_name = '__town__' (sentinel value).
--   The Surveyor treats '__town__' rows as town-scoped when computing its diff
--   against desired_custom_roles (scope='town').
--
--   instance_index distinguishes multiple instances of the same role on the same
--   rig when max_instances > 1 (ADR-0004). Starts at 0.
--
--   status values: same as actual_agent_config.
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_custom_roles (
    rig_name        VARCHAR(128)                                                   NOT NULL,
    role_name       VARCHAR(128)                                                   NOT NULL,
    instance_index  INT                                                            NOT NULL DEFAULT 0,
    pid             BIGINT,
    status          ENUM('starting', 'running', 'stopped', 'failed', 'crashed')   NOT NULL DEFAULT 'starting',
    last_seen       TIMESTAMP                                                      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rig_name, role_name, instance_index),
    CONSTRAINT chk_town_sentinel
        CHECK (rig_name != '' )     -- empty string not allowed; use '__town__' for town-scope
);

-- ============================================================================
-- Seed actual_topology_versions at schema version 1.
-- Idempotent: safe to re-run.
-- ============================================================================
INSERT INTO actual_topology_versions (table_name, schema_version, written_by)
VALUES
    ('actual_rigs',          1, 'migration/004'),
    ('actual_agent_config',  1, 'migration/004'),
    ('actual_worktrees',     1, 'migration/004'),
    ('actual_custom_roles',  1, 'migration/004')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements in reverse dependency order to roll back.
--
--   DELETE FROM actual_topology_versions
--     WHERE table_name IN ('actual_rigs', 'actual_agent_config',
--                          'actual_worktrees', 'actual_custom_roles');
--   DROP TABLE IF EXISTS actual_custom_roles;
--   DROP TABLE IF EXISTS actual_worktrees;
--   DROP TABLE IF EXISTS actual_agent_config;
--   DROP TABLE IF EXISTS actual_rigs;
--   DROP TABLE IF EXISTS actual_topology_versions;
