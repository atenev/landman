-- gastown_init_v1.sql — Gas Town schema initialisation
--
-- Embedded by DoltInstanceController (go:embed) and applied via the
-- gastown-dolt-ddlinit Job on every fresh DoltInstance. All statements
-- use IF NOT EXISTS / ON DUPLICATE KEY UPDATE so the script is idempotent
-- and safe to re-run on an already-initialised instance.
--
-- Includes all tables from migrations 001–005. DOWN migration statements
-- are excluded intentionally; see the individual migration files for rollback.

-- ============================================================================
-- desired_topology_versions  (migration 001)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_topology_versions (
    table_name     VARCHAR(128)  NOT NULL,
    schema_version INT           NOT NULL,
    written_by     VARCHAR(128),
    written_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (table_name)
);

-- ============================================================================
-- desired_rigs  (migration 001)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_rigs (
    name       VARCHAR(128)  NOT NULL,
    repo       TEXT          NOT NULL,
    branch     VARCHAR(256)  NOT NULL,
    enabled    BOOLEAN       NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (name)
);

-- ============================================================================
-- desired_agent_config  (migration 001)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_agent_config (
    rig_name       VARCHAR(128)  NOT NULL,
    role           VARCHAR(64)   NOT NULL,
    enabled        BOOLEAN       NOT NULL DEFAULT TRUE,
    model          VARCHAR(256),
    max_polecats   INT,
    claude_md_path TEXT,
    PRIMARY KEY (rig_name, role),
    CONSTRAINT fk_agent_config_rig
        FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT chk_role_enum
        CHECK (role IN ('mayor', 'witness', 'refinery', 'deacon', 'polecat'))
);

-- ============================================================================
-- desired_formulas  (migration 001)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_formulas (
    rig_name VARCHAR(128) NOT NULL,
    name     VARCHAR(128) NOT NULL,
    schedule VARCHAR(128) NOT NULL,
    PRIMARY KEY (rig_name, name),
    CONSTRAINT fk_formulas_rig
        FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE
);

-- ============================================================================
-- desired_custom_roles  (migration 002)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_custom_roles (
    name             VARCHAR(128)                                          NOT NULL,
    description      TEXT,
    scope            ENUM('town', 'rig')                                   NOT NULL,
    lifespan         ENUM('ephemeral', 'persistent')                       NOT NULL DEFAULT 'ephemeral',
    trigger_type     ENUM('bead_assigned', 'schedule', 'event', 'manual') NOT NULL,
    trigger_schedule VARCHAR(64),
    trigger_event    VARCHAR(128),
    claude_md_path   VARCHAR(512)                                          NOT NULL,
    model            VARCHAR(128),
    parent_role      VARCHAR(128)                                          NOT NULL,
    reports_to       VARCHAR(128),
    max_instances    INT                                                   NOT NULL DEFAULT 1,
    PRIMARY KEY (name),
    CONSTRAINT chk_custom_role_name_not_builtin
        CHECK (name NOT IN ('mayor', 'polecat', 'witness', 'refinery', 'deacon', 'dog', 'crew')),
    CONSTRAINT chk_trigger
        CHECK (
            (trigger_type = 'schedule' AND trigger_schedule IS NOT NULL)
            OR (trigger_type = 'event'    AND trigger_event    IS NOT NULL)
            OR (trigger_type IN ('bead_assigned', 'manual'))
        )
);

-- ============================================================================
-- desired_rig_custom_roles  (migration 002)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_rig_custom_roles (
    rig_name  VARCHAR(128) NOT NULL,
    role_name VARCHAR(128) NOT NULL,
    enabled   BOOLEAN      NOT NULL DEFAULT TRUE,
    PRIMARY KEY (rig_name, role_name),
    CONSTRAINT fk_rig_custom_roles_rig
        FOREIGN KEY (rig_name)  REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_rig_custom_roles_role
        FOREIGN KEY (role_name) REFERENCES desired_custom_roles(name)
            ON DELETE CASCADE ON UPDATE CASCADE
);

-- ============================================================================
-- cost_ledger  (migration 003)
-- ============================================================================
CREATE TABLE IF NOT EXISTS cost_ledger (
    id             BIGINT         NOT NULL AUTO_INCREMENT,
    rig_name       VARCHAR(128)   NOT NULL,
    polecat_id     VARCHAR(64)    NOT NULL,
    model          VARCHAR(128)   NOT NULL,
    input_tokens   INT            NOT NULL DEFAULT 0,
    output_tokens  INT            NOT NULL DEFAULT 0,
    cost_usd       DECIMAL(12,6),
    message_count  INT            NOT NULL DEFAULT 1,
    recorded_at    TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    INDEX idx_cost_ledger_rig_recorded (rig_name, recorded_at)
);

-- ============================================================================
-- desired_cost_policy  (migration 004)
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_cost_policy (
    rig_name     VARCHAR(128)                       NOT NULL,
    budget_type  ENUM('usd', 'messages', 'tokens')  NOT NULL,
    daily_budget DECIMAL(16,4)                      NOT NULL,
    warn_at_pct  TINYINT                            NOT NULL DEFAULT 80,
    PRIMARY KEY (rig_name),
    CONSTRAINT chk_warn_at_pct_range
        CHECK (warn_at_pct BETWEEN 1 AND 99),
    CONSTRAINT chk_daily_budget_positive
        CHECK (daily_budget > 0)
);

-- ============================================================================
-- cost_ledger_24h view  (migration 004)
-- ============================================================================
CREATE OR REPLACE VIEW cost_ledger_24h AS
SELECT
    rig_name,
    SUM(cost_usd)                     AS spend_usd,
    SUM(message_count)                AS spend_messages,
    SUM(input_tokens + output_tokens) AS spend_tokens
FROM cost_ledger
WHERE recorded_at >= NOW() - INTERVAL 1 DAY
GROUP BY rig_name;

-- ============================================================================
-- actual_topology_versions  (migration 005)
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_topology_versions (
    table_name     VARCHAR(128)  NOT NULL,
    schema_version INT           NOT NULL,
    written_by     VARCHAR(128),
    written_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (table_name)
);

-- ============================================================================
-- actual_rigs  (migration 005)
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_rigs (
    name       VARCHAR(128)                                                  NOT NULL,
    repo       TEXT                                                          NOT NULL,
    branch     VARCHAR(256)                                                  NOT NULL,
    enabled    BOOLEAN                                                       NOT NULL DEFAULT TRUE,
    status     ENUM('starting', 'running', 'draining', 'stopped', 'failed') NOT NULL DEFAULT 'starting',
    last_seen  TIMESTAMP                                                     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP                                                     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (name)
);

-- ============================================================================
-- actual_agent_config  (migration 005)
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_agent_config (
    rig_name   VARCHAR(128)                                                       NOT NULL,
    role       VARCHAR(128)                                                       NOT NULL,
    pid        BIGINT,
    model      VARCHAR(256),
    status     ENUM('starting', 'running', 'stopped', 'failed', 'crashed')       NOT NULL DEFAULT 'starting',
    last_seen  TIMESTAMP                                                          NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rig_name, role)
);

-- ============================================================================
-- actual_worktrees  (migration 005)
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_worktrees (
    rig_name   VARCHAR(128)                        NOT NULL,
    path       TEXT                                NOT NULL,
    branch     VARCHAR(256)                        NOT NULL,
    clean      BOOLEAN                             NOT NULL DEFAULT TRUE,
    status     ENUM('active', 'idle', 'stale')     NOT NULL DEFAULT 'idle',
    bead_id    VARCHAR(128),
    last_seen  TIMESTAMP                           NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rig_name, path(512))
);

-- ============================================================================
-- actual_custom_roles  (migration 005)
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
        CHECK (rig_name != '')
);

-- ============================================================================
-- Seed desired_topology_versions (idempotent)
-- ============================================================================
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES
    ('desired_rigs',             1, 'gastown-operator/init'),
    ('desired_agent_config',     1, 'gastown-operator/init'),
    ('desired_formulas',         1, 'gastown-operator/init'),
    ('desired_custom_roles',     1, 'gastown-operator/init'),
    ('desired_rig_custom_roles', 1, 'gastown-operator/init'),
    ('desired_cost_policy',      1, 'gastown-operator/init')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- Seed actual_topology_versions (idempotent)
-- ============================================================================
INSERT INTO actual_topology_versions (table_name, schema_version, written_by)
VALUES
    ('actual_rigs',          1, 'gastown-operator/init'),
    ('actual_agent_config',  1, 'gastown-operator/init'),
    ('actual_worktrees',     1, 'gastown-operator/init'),
    ('actual_custom_roles',  1, 'gastown-operator/init')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;
