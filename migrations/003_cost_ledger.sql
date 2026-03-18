-- Migration: 003_cost_ledger
-- Issue:     dgt-5bc
-- Purpose:   Create the cost_ledger operational table and cost_ledger_24h view.
--            Polecats append one row on exit; Deacon reads the view to enforce
--            cost policies declared in desired_cost_policy (ADR-0006).
--
-- Note: cost_ledger is an operational table, NOT a desired_topology table.
-- It does NOT go through desired_topology_versions and is never written by
-- town-ctl's apply transaction.

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

-- ---------------------------------------------------------------------------
-- cost_ledger_24h
--   Rolling 24-hour aggregate per rig. Read by Deacon's cost patrol.
--   Deacon enforces the policy declared in desired_cost_policy by comparing
--   these totals against the budget thresholds.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW cost_ledger_24h AS
SELECT
    rig_name,
    SUM(cost_usd)                        AS total_cost_usd,
    SUM(message_count)                   AS total_messages,
    SUM(input_tokens + output_tokens)    AS total_tokens
FROM cost_ledger
WHERE recorded_at >= NOW() - INTERVAL 1 DAY
GROUP BY rig_name;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements to roll back this migration:
--
--   DROP VIEW  IF EXISTS cost_ledger_24h;
--   DROP TABLE IF EXISTS cost_ledger;
