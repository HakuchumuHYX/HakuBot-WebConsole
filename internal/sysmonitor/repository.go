package sysmonitor

import (
	"context"
	"database/sql"
	"time"

	"hakubot-webconsole/internal/timefmt"
)

type HistoryPoint struct {
	TimestampMs      int64   `json:"timestamp_ms"`
	TimeLabel        string  `json:"time_label"`
	TimeDisplay      string  `json:"time_display"`
	CPUPercent       float64 `json:"cpu_percent"`
	CPUMax           float64 `json:"cpu_max"`
	MemoryPercent    float64 `json:"memory_percent"`
	MemoryUsedBytes  uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes uint64  `json:"memory_total_bytes"`
	DiskPercent      float64 `json:"disk_percent"`
	Load1            float64 `json:"load1"`
	NetRxBytesPerSec float64 `json:"net_rx_bytes_per_sec"`
	NetTxBytesPerSec float64 `json:"net_tx_bytes_per_sec"`
}

type HistoryResponse struct {
	Window string         `json:"window"`
	FromMs int64          `json:"from_ms"`
	ToMs   int64          `json:"to_ms"`
	Points []HistoryPoint `json:"points"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetHistory(ctx context.Context, window string) (HistoryResponse, error) {
	var (
		windowDuration time.Duration
		bucketMs       int64
	)

	switch window {
	case "30m":
		windowDuration = 30 * time.Minute
		bucketMs = 15 * 1000 // 15s buckets (~120 points)
	case "6h":
		windowDuration = 6 * time.Hour
		bucketMs = 180 * 1000 // 3m buckets (~120 points)
	case "24h":
		windowDuration = 24 * time.Hour
		bucketMs = 720 * 1000 // 12m buckets (~120 points)
	case "7d":
		windowDuration = 7 * 24 * time.Hour
		bucketMs = 5040 * 1000 // 1.4h buckets (~120 points)
	case "1h":
		fallthrough
	default:
		window = "1h"
		windowDuration = 1 * time.Hour
		bucketMs = 30 * 1000 // 30s buckets (~120 points)
	}

	now := time.Now()
	toMs := now.UnixMilli()
	fromMs := now.Add(-windowDuration).UnixMilli()

	query := `
		SELECT 
			(timestamp_ms / ?) * ? AS bucket_time,
			ROUND(AVG(cpu_percent), 2) AS avg_cpu,
			ROUND(MAX(cpu_percent), 2) AS max_cpu,
			ROUND(AVG(memory_percent), 2) AS avg_mem,
			CAST(AVG(memory_used_bytes) AS INTEGER) AS avg_mem_used,
			CAST(AVG(memory_total_bytes) AS INTEGER) AS mem_total,
			ROUND(AVG(disk_percent), 2) AS avg_disk,
			ROUND(AVG(load1), 2) AS avg_load,
			ROUND(AVG(net_rx_bytes_per_sec), 2) AS avg_rx,
			ROUND(AVG(net_tx_bytes_per_sec), 2) AS avg_tx
		FROM system_metrics
		WHERE timestamp_ms >= ?
		GROUP BY bucket_time
		ORDER BY bucket_time ASC
	`

	rows, err := r.db.QueryContext(ctx, query, bucketMs, bucketMs, fromMs)
	if err != nil {
		return HistoryResponse{
			Window: window,
			FromMs: fromMs,
			ToMs:   toMs,
			Points: []HistoryPoint{},
		}, err
	}
	defer rows.Close()

	points := make([]HistoryPoint, 0, 128)
	for rows.Next() {
		var p HistoryPoint
		var memUsed, memTotal int64
		if err := rows.Scan(
			&p.TimestampMs,
			&p.CPUPercent,
			&p.CPUMax,
			&p.MemoryPercent,
			&memUsed,
			&memTotal,
			&p.DiskPercent,
			&p.Load1,
			&p.NetRxBytesPerSec,
			&p.NetTxBytesPerSec,
		); err != nil {
			return HistoryResponse{
				Window: window,
				FromMs: fromMs,
				ToMs:   toMs,
				Points: points,
			}, err
		}
		p.MemoryUsedBytes = uint64(memUsed)
		p.MemoryTotalBytes = uint64(memTotal)

		t := time.UnixMilli(p.TimestampMs).In(timefmt.Location)
		p.TimeDisplay = timefmt.FormatTime(t)
		if window == "24h" || window == "7d" {
			p.TimeLabel = t.Format("01-02 15:04")
		} else {
			p.TimeLabel = t.Format("15:04:05")
		}

		points = append(points, p)
	}

	return HistoryResponse{
		Window: window,
		FromMs: fromMs,
		ToMs:   toMs,
		Points: points,
	}, rows.Err()
}
