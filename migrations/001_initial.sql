PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

BEGIN IMMEDIATE;

CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE response_events (
    id INTEGER PRIMARY KEY,
    run_id TEXT NOT NULL UNIQUE,
    started_at_ms INTEGER NOT NULL,
    finished_at_ms INTEGER,
    duration_ms INTEGER,
    status TEXT NOT NULL CHECK (status IN ('success', 'failure')),
    plugin_name TEXT,
    plugin_id TEXT,
    module_name TEXT,
    matcher_type TEXT,
    matcher_lineno INTEGER,
    bot_id TEXT,
    event_name TEXT,
    group_id TEXT,
    user_id TEXT,
    source_message_id TEXT,
    request_summary TEXT,
    response_summary TEXT,
    send_count INTEGER NOT NULL DEFAULT 0 CHECK (send_count >= 0),
    send_success_count INTEGER NOT NULL DEFAULT 0 CHECK (send_success_count >= 0),
    send_failure_count INTEGER NOT NULL DEFAULT 0 CHECK (send_failure_count >= 0),
    max_log_level TEXT,
    error_type TEXT,
    error_message TEXT,
    has_full_diagnostics INTEGER NOT NULL DEFAULT 0
        CHECK (has_full_diagnostics IN (0, 1)),
    request_raw_gzip BLOB,
    response_raw_gzip BLOB,
    logs_raw_gzip BLOB,
    request_raw_sha256 TEXT,
    response_raw_sha256 TEXT,
    logs_raw_sha256 TEXT,
    created_at_ms INTEGER NOT NULL,
    CHECK (finished_at_ms IS NULL OR finished_at_ms >= started_at_ms),
    CHECK (duration_ms IS NULL OR duration_ms >= 0),
    CHECK (send_success_count + send_failure_count <= send_count),
    CHECK (
        has_full_diagnostics = 0
        OR (
            request_raw_gzip IS NOT NULL
            AND response_raw_gzip IS NOT NULL
            AND logs_raw_gzip IS NOT NULL
            AND length(request_raw_sha256) = 64
            AND length(response_raw_sha256) = 64
            AND length(logs_raw_sha256) = 64
        )
    )
) STRICT;

CREATE TABLE diagnostic_logs (
    id INTEGER PRIMARY KEY,
    run_id TEXT,
    created_at_ms INTEGER NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('WARNING', 'ERROR', 'CRITICAL')),
    logger_name TEXT,
    module_name TEXT,
    plugin_name TEXT,
    message_summary TEXT NOT NULL DEFAULT '',
    full_log_gzip BLOB NOT NULL,
    raw_sha256 TEXT NOT NULL CHECK (length(raw_sha256) = 64)
) STRICT;

CREATE TABLE bot_status (
    bot_id TEXT PRIMARY KEY,
    adapter TEXT NOT NULL,
    connected INTEGER NOT NULL DEFAULT 0 CHECK (connected IN (0, 1)),
    connection_started_at_ms INTEGER,
    last_heartbeat_at_ms INTEGER,
    heartbeat_online INTEGER CHECK (heartbeat_online IN (0, 1)),
    heartbeat_good INTEGER CHECK (heartbeat_good IN (0, 1)),
    collector_heartbeat_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_response_events_started
    ON response_events(started_at_ms DESC, id DESC);
CREATE INDEX idx_response_events_status_started
    ON response_events(status, started_at_ms DESC, id DESC);
CREATE INDEX idx_response_events_group_started
    ON response_events(group_id, started_at_ms DESC, id DESC);
CREATE INDEX idx_response_events_plugin_started
    ON response_events(plugin_name, started_at_ms DESC, id DESC);

CREATE INDEX idx_diagnostic_logs_created
    ON diagnostic_logs(created_at_ms DESC, id DESC);
CREATE INDEX idx_diagnostic_logs_level_created
    ON diagnostic_logs(level, created_at_ms DESC, id DESC);
CREATE INDEX idx_diagnostic_logs_run
    ON diagnostic_logs(run_id);

INSERT INTO schema_migrations(version, name, applied_at_ms)
VALUES (
    1,
    'initial',
    CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
);

COMMIT;

