-- WebConsole Internal Migration: System Metrics Storage
CREATE TABLE IF NOT EXISTS system_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp_ms INTEGER NOT NULL,
    cpu_percent REAL NOT NULL DEFAULT 0.0,
    memory_used_bytes INTEGER NOT NULL DEFAULT 0,
    memory_total_bytes INTEGER NOT NULL DEFAULT 0,
    memory_percent REAL NOT NULL DEFAULT 0.0,
    swap_used_bytes INTEGER NOT NULL DEFAULT 0,
    swap_total_bytes INTEGER NOT NULL DEFAULT 0,
    disk_used_bytes INTEGER NOT NULL DEFAULT 0,
    disk_total_bytes INTEGER NOT NULL DEFAULT 0,
    disk_percent REAL NOT NULL DEFAULT 0.0,
    load1 REAL NOT NULL DEFAULT 0.0,
    load5 REAL NOT NULL DEFAULT 0.0,
    load15 REAL NOT NULL DEFAULT 0.0,
    uptime_seconds INTEGER NOT NULL DEFAULT 0,
    net_rx_bytes_per_sec REAL NOT NULL DEFAULT 0.0,
    net_tx_bytes_per_sec REAL NOT NULL DEFAULT 0.0,
    process_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_system_metrics_timestamp
    ON system_metrics(timestamp_ms ASC);
