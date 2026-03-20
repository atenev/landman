-- Migration: 007_desired_topology_lock
-- Issues:    dgt-lc3
-- Purpose:   Introduce an advisory write-lock sentinel row so that town-ctl
--            and the K8s operator can detect concurrent writes to the
--            desired_* tables and fail gracefully (townctl) or requeue
--            (operator) rather than producing interleaved partial state.
--
-- Design (advisory locking, not exclusive):
--   A single row (singleton='X') records which component last acquired the
--   write lock and when.  Before each write, both writers:
--     1. Read the lock row (pre-flight, outside the transaction).
--     2. If a different component holds it and acquired_at is within the TTL
--        window (30 s), return an error / requeue.
--     3. Upsert the lock row inside their Dolt transaction so that, on
--        commit, the lock records the new holder.
--
--   Holder values:
--     "gastown-operator"    — K8s reconciler
--     "town-ctl/<version>"  — town-ctl CLI (BinaryVersion)

CREATE TABLE IF NOT EXISTS desired_topology_lock (
    singleton    CHAR(1)      NOT NULL DEFAULT 'X',
    holder       VARCHAR(128) NOT NULL,
    acquired_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY  (singleton),
    CONSTRAINT chk_singleton CHECK (singleton = 'X')
);

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- DROP TABLE IF EXISTS desired_topology_lock;
