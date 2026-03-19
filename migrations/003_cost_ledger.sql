-- Migration: 003_cost_ledger
-- Issue:     dgt-5bc, dgt-i71
-- Purpose:   Create the cost_ledger operational table.
--            Polecats append one row on exit (GUPP: write-before-exit invariant).
--            Deacon reads the cost_ledger_24h view (defined in migration 004 after
--            desired_cost_policy is created) to enforce the policies in
--            desired_cost_policy (ADR-0006).
--
-- Note: cost_ledger is an operational table, NOT a desired_topology table.
-- It does NOT go through desired_topology_versions and is never written by
-- town-ctl's apply transaction.
--
-- Note: the cost_ledger_24h view is defined in migration 004_desired_cost_policy.sql
-- because it is logically paired with desired_cost_policy (Deacon joins the two).
-- Apply this migration (003) before migration 004.

-- ============================================================================
-- UP migration
-- ============================================================================

-- ---------------------------------------------------------------------------
-- cost_ledger
--   Append-only audit log of every Claude invocation.
--   One row per Polecat exit (GUPP: write-before-exit invariant).
--   cost_usd is NULL for subscription users who have no per-token billing.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS cost_ledger (
    id             BIGINT         NOT NULL AUTO_INCREMENT,
    rig_name       VARCHAR(128)   NOT NULL,
    polecat_id     VARCHAR(64)    NOT NULL,
    model          VARCHAR(128)   NOT NULL,
    input_tokens   INT            NOT NULL DEFAULT 0,
    output_tokens  INT            NOT NULL DEFAULT 0,
    cost_usd       DECIMAL(12,6),           -- NULL for subscription (non-metered) users
    message_count  INT            NOT NULL DEFAULT 1,
    recorded_at    TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    INDEX idx_cost_ledger_rig_recorded (rig_name, recorded_at)
);

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements to roll back this migration (apply 004 DOWN first):
--
--   DROP TABLE IF EXISTS cost_ledger;
