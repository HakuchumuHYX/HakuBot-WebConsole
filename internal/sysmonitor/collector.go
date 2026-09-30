package sysmonitor

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"hakubot-webconsole/internal/timefmt"
)

const (
	sampleInterval = 3 * time.Second
	// A host_metrics row aggregates this many samples (30 seconds).
	samplesPerRow = 10
	historyWindow = 7 * 24 * time.Hour
)

type Collector struct {
	db        *sql.DB
	diskPath  string
	startTime time.Time
	hostname  string
	kernel    string
	cpuModel  string

	// Touched only by the sampling goroutine.
	lastTicks      cpuTicks
	lastNet        netCounters
	lastSampleTime time.Time
	pendingCPU     []float64

	mu     sync.RWMutex
	status SystemStatus
}

func NewCollector(db *sql.DB, diskPath string) *Collector {
	hostname, _ := os.Hostname()
	c := &Collector{
		db:             db,
		diskPath:       diskPath,
		startTime:      time.Now(),
		hostname:       hostname,
		kernel:         readKernelVersion(),
		cpuModel:       readCPUModel(),
		lastSampleTime: time.Now(),
	}
	c.lastTicks, _ = readCPUTicks()
	c.lastNet, _ = readNetCounters()
	c.sampleOnce()
	return c
}

func (c *Collector) Start(ctx context.Context) {
	ticker := time.NewTicker(sampleInterval)
	cleanTicker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	defer cleanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample := c.sampleOnce()
			c.pendingCPU = append(c.pendingCPU, sample.CPUPercent)
			if len(c.pendingCPU) >= samplesPerRow {
				c.writeRow(ctx, sample)
				c.pendingCPU = c.pendingCPU[:0]
			}
		case <-cleanTicker.C:
			cutoff := time.Now().Add(-historyWindow).UnixMilli()
			if _, err := c.db.ExecContext(
				ctx,
				"DELETE FROM host_metrics WHERE timestamp_ms < ?",
				cutoff,
			); err != nil {
				slog.Warn("clean old host metrics failed", "error", err)
			}
		}
	}
}

func (c *Collector) sampleOnce() MetricSample {
	now := time.Now()

	cpuPct := 0.0
	if ticks, err := readCPUTicks(); err == nil {
		cpuPct = calculateCPUPercent(c.lastTicks, ticks)
		c.lastTicks = ticks
	}

	rxRate, txRate := 0.0, 0.0
	if counters, err := readNetCounters(); err == nil {
		seconds := now.Sub(c.lastSampleTime).Seconds()
		if seconds > 0 {
			if counters.rxBytes >= c.lastNet.rxBytes {
				rxRate = float64(counters.rxBytes-c.lastNet.rxBytes) / seconds
			}
			if counters.txBytes >= c.lastNet.txBytes {
				txRate = float64(counters.txBytes-c.lastNet.txBytes) / seconds
			}
		}
		c.lastNet = counters
	}
	c.lastSampleTime = now

	mem, err := readMemStats()
	if err != nil {
		slog.Debug("read mem stats failed", "error", err)
	}
	disk, err := readDiskStats(c.diskPath)
	if err != nil {
		slog.Debug("read disk stats failed", "error", err)
	}
	load, err := readLoadStats()
	if err != nil {
		slog.Debug("read load stats failed", "error", err)
	}
	uptimeSec := readUptime()

	sample := MetricSample{
		TimestampMs:      now.UnixMilli(),
		TimeDisplay:      timefmt.FormatTime(now),
		CPUPercent:       cpuPct,
		MemoryUsedBytes:  mem.used,
		MemoryTotalBytes: mem.total,
		MemoryPercent:    mem.percent,
		SwapUsedBytes:    mem.swapUsed,
		SwapTotalBytes:   mem.swapTotal,
		DiskUsedBytes:    disk.used,
		DiskTotalBytes:   disk.total,
		DiskPercent:      disk.percent,
		Load1:            load.load1,
		Load5:            load.load5,
		Load15:           load.load15,
		UptimeSeconds:    uptimeSec,
		NetRxBytesPerSec: rxRate,
		NetTxBytesPerSec: txRate,
		ProcessCount:     load.processes,
	}

	var memRuntime runtime.MemStats
	runtime.ReadMemStats(&memRuntime)
	procUptimeSec := int64(time.Since(c.startTime).Seconds())

	c.mu.Lock()
	c.status = SystemStatus{
		Hostname:          c.hostname,
		OS:                "Linux",
		Kernel:            c.kernel,
		Arch:              runtime.GOARCH,
		CPUModel:          c.cpuModel,
		CPUCores:          runtime.NumCPU(),
		SystemUptimeDesc:  formatDuration(uptimeSec),
		ProcessUptimeDesc: formatDuration(procUptimeSec),
		SystemUptimeSec:   uptimeSec,
		ProcessUptimeSec:  procUptimeSec,
		Goroutines:        runtime.NumGoroutine(),
		GoHeapAllocBytes:  memRuntime.Alloc,
		Current:           sample,
	}
	c.mu.Unlock()
	return sample
}

// writeRow stores the average and peak CPU of the pending samples; the other
// fields are taken from the latest sample.
func (c *Collector) writeRow(ctx context.Context, latest MetricSample) {
	cpuSum, cpuMax := 0.0, 0.0
	for _, value := range c.pendingCPU {
		cpuSum += value
		cpuMax = max(cpuMax, value)
	}
	_, err := c.db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO host_metrics (
			timestamp_ms, cpu_avg, cpu_max, memory_percent,
			memory_used_bytes, memory_total_bytes, disk_percent, load1,
			net_rx_bytes_per_sec, net_tx_bytes_per_sec
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		latest.TimestampMs,
		cpuSum/float64(len(c.pendingCPU)),
		cpuMax,
		latest.MemoryPercent,
		int64(latest.MemoryUsedBytes),
		int64(latest.MemoryTotalBytes),
		latest.DiskPercent,
		latest.Load1,
		latest.NetRxBytesPerSec,
		latest.NetTxBytesPerSec,
	)
	if err != nil {
		slog.Warn("insert host metrics failed", "error", err)
	}
}

func (c *Collector) Status() SystemStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

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

// History buckets host_metrics into roughly 60-120 points for the window.
func (c *Collector) History(
	ctx context.Context,
	window string,
) (HistoryResponse, error) {
	var duration time.Duration
	var bucketMs int64
	switch window {
	case "30m":
		duration, bucketMs = 30*time.Minute, 30_000
	case "6h":
		duration, bucketMs = 6*time.Hour, 180_000
	case "24h":
		duration, bucketMs = 24*time.Hour, 720_000
	case "7d":
		duration, bucketMs = 7*24*time.Hour, 5_040_000
	default:
		window = "1h"
		duration, bucketMs = time.Hour, 30_000
	}

	now := time.Now()
	response := HistoryResponse{
		Window: window,
		FromMs: now.Add(-duration).UnixMilli(),
		ToMs:   now.UnixMilli(),
		Points: make([]HistoryPoint, 0, 128),
	}
	rows, err := c.db.QueryContext(
		ctx,
		`SELECT (timestamp_ms / ?) * ? AS bucket_time,
		        ROUND(AVG(cpu_avg), 2), ROUND(MAX(cpu_max), 2),
		        ROUND(AVG(memory_percent), 2),
		        CAST(AVG(memory_used_bytes) AS INTEGER),
		        CAST(AVG(memory_total_bytes) AS INTEGER),
		        ROUND(AVG(disk_percent), 2), ROUND(AVG(load1), 2),
		        ROUND(AVG(net_rx_bytes_per_sec), 2),
		        ROUND(AVG(net_tx_bytes_per_sec), 2)
		   FROM host_metrics
		  WHERE timestamp_ms >= ?
		  GROUP BY bucket_time
		  ORDER BY bucket_time`,
		bucketMs,
		bucketMs,
		response.FromMs,
	)
	if err != nil {
		return HistoryResponse{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var point HistoryPoint
		var memUsed, memTotal int64
		if err := rows.Scan(
			&point.TimestampMs,
			&point.CPUPercent,
			&point.CPUMax,
			&point.MemoryPercent,
			&memUsed,
			&memTotal,
			&point.DiskPercent,
			&point.Load1,
			&point.NetRxBytesPerSec,
			&point.NetTxBytesPerSec,
		); err != nil {
			return HistoryResponse{}, err
		}
		point.MemoryUsedBytes = uint64(memUsed)
		point.MemoryTotalBytes = uint64(memTotal)
		local := time.UnixMilli(point.TimestampMs).In(timefmt.Location)
		point.TimeDisplay = timefmt.FormatTime(local)
		if window == "24h" || window == "7d" {
			point.TimeLabel = local.Format("01-02 15:04")
		} else {
			point.TimeLabel = local.Format("15:04:05")
		}
		response.Points = append(response.Points, point)
	}
	return response, rows.Err()
}
