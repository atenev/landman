-- Migration: 008_reconcile_log
-- Issues:    dgt-13d
-- Purpose:   Create the reconcile_log table written by the Surveyor after each
--            successful reconcile cycle. The rig controller reads it to surface
--            LastConvergedAt in Rig status (pkg/k8s/operator/controllers/rig_controller.go).

-- ---------------------------------------------------------------------------
-- reconcile_log
--   Append-only log of reconcile cycle completions.
--   Written by the Surveyor when a full desired→actual diff is resolved with
--   no remaining divergences (i.e. the rig has converged).
--
--   duration_ms: wall-clock time of the reconcile cycle in milliseconds.
--   divergences: number of drift items the Surveyor found (0 means fully converged).
--   notes:       optional free-text (e.g. error summary on a partial convergence).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reconcile_log (
    id               BIGINT        NOT NULL AUTO_INCREMENT,
    rig_name         VARCHAR(128)  NOT NULL,
    last_converged_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    duration_ms      INT,
    divergences      INT           NOT NULL DEFAULT 0,
    notes            TEXT,
    PRIMARY KEY (id),
    INDEX idx_reconcile_log_rig_converged (rig_name, last_converged_at)
);

-- ---------------------------------------------------------------------------
-- DOWN migration
-- ---------------------------------------------------------------------------
-- DROP TABLE IF EXISTS reconcile_log;
