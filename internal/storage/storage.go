package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	_ "time/tzdata"
)

// Location is GMT+8: automatic retention runs at 04:15 there and counts
// retention days by its calendar.
var Location = mustLocation()

func mustLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return location
}

var (
	ErrBusy         = errors.New("storage maintenance is already running")
	ErrFutureCutoff = errors.New("cleanup cutoff cannot be in the future")
)

type Manager struct {
	db            *sql.DB
	databasePath  string
	retentionDays int
	maintenance   sync.Mutex
}

func NewManager(db *sql.DB, databasePath string, retentionDays int) *Manager {
	return &Manager{
		db:            db,
		databasePath:  databasePath,
		retentionDays: retentionDays,
	}
}

// Stats holds file sizes in bytes; the time ranges are nil when the table has
// no rows.
type Stats struct {
	DatabaseBytes        int64  `json:"database_bytes"`
	WALBytes             int64  `json:"wal_bytes"`
	SHMBytes             int64  `json:"shm_bytes"`
	ResponseCount        int64  `json:"response_count"`
	DiagnosticCount      int64  `json:"diagnostic_count"`
	ResponseEarliestMS   *int64 `json:"response_earliest_ms,omitempty"`
	ResponseLatestMS     *int64 `json:"response_latest_ms,omitempty"`
	DiagnosticEarliestMS *int64 `json:"diagnostic_earliest_ms,omitempty"`
	DiagnosticLatestMS   *int64 `json:"diagnostic_latest_ms,omitempty"`
	RetentionDays        int    `json:"retention_days"`
}

type Preview struct {
	ResponseCount   int64 `json:"response_count"`
	DiagnosticCount int64 `json:"diagnostic_count"`
}

type CleanupResult struct {
	Cutoff             time.Time `json:"-"`
	ResponsesDeleted   int64     `json:"responses_deleted"`
	DiagnosticsDeleted int64     `json:"diagnostics_deleted"`
}

func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	result := Stats{
		DatabaseBytes: fileSize(m.databasePath),
		WALBytes:      fileSize(m.databasePath + "-wal"),
		SHMBytes:      fileSize(m.databasePath + "-shm"),
		RetentionDays: m.retentionDays,
	}
	var err error
	result.ResponseCount, result.ResponseEarliestMS, result.ResponseLatestMS, err =
		m.tableRange(ctx, "response_events", "started_at_ms")
	if err != nil {
		return Stats{}, err
	}
	result.DiagnosticCount, result.DiagnosticEarliestMS, result.DiagnosticLatestMS, err =
		m.tableRange(ctx, "diagnostic_logs", "created_at_ms")
	if err != nil {
		return Stats{}, err
	}
	return result, nil
}

func (m *Manager) tableRange(
	ctx context.Context,
	table, timestampColumn string,
) (int64, *int64, *int64, error) {
	var count int64
	var earliest, latest *int64
	err := m.db.QueryRowContext(
		ctx,
		fmt.Sprintf(
			"SELECT COUNT(*), MIN(%s), MAX(%s) FROM %s",
			timestampColumn,
			timestampColumn,
			table,
		),
	).Scan(&count, &earliest, &latest)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("count %s: %w", table, err)
	}
	return count, earliest, latest, nil
}

func (m *Manager) Preview(
	ctx context.Context,
	cutoff time.Time,
) (Preview, error) {
	if cutoff.After(time.Now()) {
		return Preview{}, ErrFutureCutoff
	}
	cutoffMS := cutoff.UnixMilli()
	var preview Preview
	if err := m.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM response_events WHERE started_at_ms < ?",
		cutoffMS,
	).Scan(&preview.ResponseCount); err != nil {
		return Preview{}, err
	}
	if err := m.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM diagnostic_logs WHERE created_at_ms < ?",
		cutoffMS,
	).Scan(&preview.DiagnosticCount); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func (m *Manager) Cleanup(
	ctx context.Context,
	cutoff time.Time,
) (CleanupResult, error) {
	if !m.maintenance.TryLock() {
		return CleanupResult{}, ErrBusy
	}
	defer m.maintenance.Unlock()
	if cutoff.After(time.Now()) {
		return CleanupResult{}, ErrFutureCutoff
	}
	cutoffMS := cutoff.UnixMilli()
	responses, err := m.deleteBatches(
		ctx,
		"response_events",
		"started_at_ms",
		cutoffMS,
	)
	if err != nil {
		return CleanupResult{}, err
	}
	diagnostics, err := m.deleteBatches(
		ctx,
		"diagnostic_logs",
		"created_at_ms",
		cutoffMS,
	)
	if err != nil {
		return CleanupResult{}, err
	}
	if err := m.reclaim(ctx); err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{
		Cutoff:             cutoff,
		ResponsesDeleted:   responses,
		DiagnosticsDeleted: diagnostics,
	}, nil
}

// RunAutomaticRetention deletes records older than retention_days; callers
// only schedule it when retention is enabled.
func (m *Manager) RunAutomaticRetention(
	ctx context.Context,
	now time.Time,
) (CleanupResult, error) {
	cutoff := now.In(Location).AddDate(0, 0, -m.retentionDays)
	return m.Cleanup(ctx, cutoff)
}

func NextAutomaticRun(now time.Time) time.Time {
	local := now.In(Location)
	next := time.Date(
		local.Year(),
		local.Month(),
		local.Day(),
		4,
		15,
		0,
		0,
		Location,
	)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (m *Manager) deleteBatches(
	ctx context.Context,
	table, timestampColumn string,
	cutoffMS int64,
) (int64, error) {
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE id IN ("+
			"SELECT id FROM %s WHERE %s < ? ORDER BY id LIMIT 1000)",
		table,
		table,
		timestampColumn,
	)
	var total int64
	for {
		transaction, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		result, err := transaction.ExecContext(ctx, query, cutoffMS)
		if err != nil {
			_ = transaction.Rollback()
			return total, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			_ = transaction.Rollback()
			return total, err
		}
		if err := transaction.Commit(); err != nil {
			return total, err
		}
		total += deleted
		if deleted < 1000 {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// reclaim returns the pages freed by the deletes to the filesystem.
func (m *Manager) reclaim(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return fmt.Errorf("WAL checkpoint: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
		return fmt.Errorf("incremental vacuum: %w", err)
	}
	return nil
}

// fileSize reports a missing file (e.g. no WAL yet) as zero.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
