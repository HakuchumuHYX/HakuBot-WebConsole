package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"hakubot-webconsole/internal/timefmt"
)

var ErrBusy = errors.New("storage maintenance is already running")

type Manager struct {
	db            *sql.DB
	databasePath  string
	spoolPath     string
	retentionDays int
	maintenance   sync.Mutex
}

func NewManager(
	db *sql.DB,
	databasePath, spoolPath string,
	retentionDays int,
) *Manager {
	return &Manager{
		db:            db,
		databasePath:  databasePath,
		spoolPath:     spoolPath,
		retentionDays: retentionDays,
	}
}

type FileUsage struct {
	Bytes   int64  `json:"bytes"`
	Display string `json:"display"`
}

type Stats struct {
	Database           FileUsage `json:"database"`
	WAL                FileUsage `json:"wal"`
	SHM                FileUsage `json:"shm"`
	Spool              FileUsage `json:"spool"`
	SpoolFiles         int64     `json:"spool_files"`
	ResponseCount      int64     `json:"response_count"`
	DiagnosticCount    int64     `json:"diagnostic_count"`
	ResponseEarliest   string    `json:"response_earliest,omitempty"`
	ResponseLatest     string    `json:"response_latest,omitempty"`
	DiagnosticEarliest string    `json:"diagnostic_earliest,omitempty"`
	DiagnosticLatest   string    `json:"diagnostic_latest,omitempty"`
	RetentionDays      int       `json:"retention_days"`
}

type Preview struct {
	CutoffDisplay             string `json:"cutoff_display"`
	ResponseCount             int64  `json:"response_count"`
	DiagnosticCount           int64  `json:"diagnostic_count"`
	CurrentFreePages          int64  `json:"current_free_pages"`
	EstimatedReclaimablePages int64  `json:"estimated_reclaimable_pages"`
}

type CleanupResult struct {
	CutoffDisplay      string `json:"cutoff_display"`
	ResponsesDeleted   int64  `json:"responses_deleted"`
	DiagnosticsDeleted int64  `json:"diagnostics_deleted"`
	Before             Stats  `json:"before"`
	After              Stats  `json:"after"`
}

type ReclaimResult struct {
	Before Stats `json:"before"`
	After  Stats `json:"after"`
}

func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	result := Stats{
		Database:      fileUsage(m.databasePath),
		WAL:           fileUsage(m.databasePath + "-wal"),
		SHM:           fileUsage(m.databasePath + "-shm"),
		RetentionDays: m.retentionDays,
	}
	spoolBytes, spoolFiles, err := directoryUsage(m.spoolPath)
	if err != nil {
		return Stats{}, fmt.Errorf("inspect spool: %w", err)
	}
	result.Spool = usage(spoolBytes)
	result.SpoolFiles = spoolFiles
	if err := m.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*), MIN(started_at_ms), MAX(started_at_ms) "+
			"FROM response_events",
	).Scan(
		&result.ResponseCount,
		newDisplayScanner(&result.ResponseEarliest),
		newDisplayScanner(&result.ResponseLatest),
	); err != nil {
		return Stats{}, fmt.Errorf("count response events: %w", err)
	}
	if err := m.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*), MIN(created_at_ms), MAX(created_at_ms) "+
			"FROM diagnostic_logs",
	).Scan(
		&result.DiagnosticCount,
		newDisplayScanner(&result.DiagnosticEarliest),
		newDisplayScanner(&result.DiagnosticLatest),
	); err != nil {
		return Stats{}, fmt.Errorf("count diagnostic logs: %w", err)
	}
	return result, nil
}

func (m *Manager) Preview(
	ctx context.Context,
	cutoff time.Time,
) (Preview, error) {
	if cutoff.After(time.Now().In(timefmt.Location)) {
		return Preview{}, errors.New("cleanup cutoff cannot be in the future")
	}
	cutoffMS := cutoff.UnixMilli()
	var preview Preview
	preview.CutoffDisplay = timefmt.FormatTime(cutoff)
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
	if err := m.db.QueryRowContext(
		ctx,
		"PRAGMA freelist_count",
	).Scan(&preview.CurrentFreePages); err != nil {
		return Preview{}, err
	}
	var totalRows int64
	if err := m.db.QueryRowContext(
		ctx,
		"SELECT (SELECT COUNT(*) FROM response_events) + "+
			"(SELECT COUNT(*) FROM diagnostic_logs)",
	).Scan(&totalRows); err != nil {
		return Preview{}, err
	}
	if totalRows > 0 {
		var pageCount int64
		if err := m.db.QueryRowContext(
			ctx,
			"PRAGMA page_count",
		).Scan(&pageCount); err != nil {
			return Preview{}, err
		}
		deleting := preview.ResponseCount + preview.DiagnosticCount
		preview.EstimatedReclaimablePages =
			(pageCount - preview.CurrentFreePages) * deleting / totalRows
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
	if cutoff.After(time.Now().In(timefmt.Location)) {
		return CleanupResult{}, errors.New(
			"cleanup cutoff cannot be in the future",
		)
	}
	before, err := m.Stats(ctx)
	if err != nil {
		return CleanupResult{}, err
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
	if err := m.reclaimLocked(ctx); err != nil {
		return CleanupResult{}, err
	}
	after, err := m.Stats(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{
		CutoffDisplay:      timefmt.FormatTime(cutoff),
		ResponsesDeleted:   responses,
		DiagnosticsDeleted: diagnostics,
		Before:             before,
		After:              after,
	}, nil
}

func (m *Manager) Reclaim(ctx context.Context) (ReclaimResult, error) {
	if !m.maintenance.TryLock() {
		return ReclaimResult{}, ErrBusy
	}
	defer m.maintenance.Unlock()
	before, err := m.Stats(ctx)
	if err != nil {
		return ReclaimResult{}, err
	}
	if err := m.reclaimLocked(ctx); err != nil {
		return ReclaimResult{}, err
	}
	after, err := m.Stats(ctx)
	if err != nil {
		return ReclaimResult{}, err
	}
	return ReclaimResult{Before: before, After: after}, nil
}

func (m *Manager) RunAutomaticRetention(
	ctx context.Context,
	now time.Time,
) (CleanupResult, bool, error) {
	if m.retentionDays <= 0 {
		return CleanupResult{}, false, nil
	}
	cutoff := now.In(timefmt.Location).AddDate(0, 0, -m.retentionDays)
	result, err := m.Cleanup(ctx, cutoff)
	return result, true, err
}

func NextAutomaticRun(now time.Time) time.Time {
	local := now.In(timefmt.Location)
	next := time.Date(
		local.Year(),
		local.Month(),
		local.Day(),
		4,
		15,
		0,
		0,
		timefmt.Location,
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

func (m *Manager) reclaimLocked(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return fmt.Errorf("WAL checkpoint: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
		return fmt.Errorf("incremental vacuum: %w", err)
	}
	return nil
}

func usage(bytes int64) FileUsage {
	return FileUsage{Bytes: bytes, Display: humanBytes(bytes)}
}

func fileUsage(path string) FileUsage {
	info, err := os.Stat(path)
	if err != nil {
		return usage(0)
	}
	return usage(info.Size())
}

func directoryUsage(path string) (int64, int64, error) {
	var bytes, files int64
	err := filepath.WalkDir(path, func(
		_ string,
		entry fs.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			bytes += info.Size()
			files++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	return bytes, files, err
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor := int64(unit)
	exponent := 0
	for quotient := value / unit; quotient >= unit; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf(
		"%.1f %ciB",
		float64(value)/float64(divisor),
		"KMGTPE"[exponent],
	)
}

type displayScanner struct {
	target *string
}

func newDisplayScanner(target *string) *displayScanner {
	return &displayScanner{target: target}
}

func (s *displayScanner) Scan(value any) error {
	if value == nil {
		*s.target = ""
		return nil
	}
	var milliseconds int64
	switch typed := value.(type) {
	case int64:
		milliseconds = typed
	case int:
		milliseconds = int64(typed)
	default:
		return fmt.Errorf("unexpected timestamp type %T", value)
	}
	*s.target = timefmt.FormatMilliseconds(milliseconds)
	return nil
}
