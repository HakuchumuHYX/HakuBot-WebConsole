package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"hakubot-webconsole/migrations"

	_ "modernc.org/sqlite"
)

const CurrentSchemaVersion = 1

func Open(ctx context.Context, databasePath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     databasePath,
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var migrationTableCount int
	err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema
		 WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&migrationTableCount)
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}

	currentVersion := 0
	if migrationTableCount > 0 {
		if err := db.QueryRowContext(
			ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
		).Scan(&currentVersion); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
	}

	// If a higher version was previously written, reset it to CurrentSchemaVersion (1)
	// so external systems (like HakuBot) reading schema_migrations stay compatible.
	if currentVersion > CurrentSchemaVersion {
		if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version > ?", CurrentSchemaVersion); err != nil {
			return fmt.Errorf("normalize schema version: %w", err)
		}
		currentVersion = CurrentSchemaVersion
	}

	if currentVersion == 0 {
		script, err := migrations.Files.ReadFile("001_initial.sql")
		if err != nil {
			return fmt.Errorf("read initial migration: %w", err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("apply initial migration: %w", err)
		}
		currentVersion = 1
	}

	// Apply internal WebConsole migrations (e.g. system_metrics)
	sysMetricsScript, err := migrations.Files.ReadFile("002_system_metrics.sql")
	if err != nil {
		return fmt.Errorf("read system metrics internal migration: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(sysMetricsScript)); err != nil {
		return fmt.Errorf("apply system metrics internal migration: %w", err)
	}

	if currentVersion != CurrentSchemaVersion {
		return errors.New("database schema migration is incomplete")
	}
	return verifySchema(ctx, db)
}

func verifySchema(ctx context.Context, db *sql.DB) error {
	required := []string{
		"response_events",
		"diagnostic_logs",
		"bot_status",
		"system_metrics",
	}
	for _, table := range required {
		var count int
		if err := db.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM sqlite_schema
			 WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			return fmt.Errorf("verify table %s: %w", table, err)
		}
		if count != 1 {
			return fmt.Errorf("required table %s is missing", table)
		}
	}
	return nil
}

func Ready(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		return err
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf(
			"schema version %d, expected %d",
			version,
			CurrentSchemaVersion,
		)
	}
	return nil
}
