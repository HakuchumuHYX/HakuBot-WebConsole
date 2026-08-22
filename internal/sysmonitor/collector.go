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

type Collector struct {
	db          *sql.DB
	databaseDir string
	startTime   time.Time

	mu             sync.RWMutex
	lastTicks      cpuTicks
	lastNet        netCounters
	lastSampleTime time.Time
	hasPrev        bool
	currentSample  MetricSample
	cachedStatus   SystemStatus
}

func NewCollector(db *sql.DB, databaseDir string) *Collector {
	c := &Collector{
		db:          db,
		databaseDir: databaseDir,
		startTime:   time.Now(),
	}
	// Initial warm-up read
	if ticks, err := readCPUTicks(); err == nil {
		c.lastTicks = ticks
	}
	if net, err := readNetCounters(); err == nil {
		c.lastNet = net
	}
	c.lastSampleTime = time.Now()
	c.hasPrev = true
	c.sampleOnce(context.Background())
	return c
}

func (c *Collector) Start(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	cleanTicker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	defer cleanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sampleOnce(ctx)
		case <-cleanTicker.C:
			c.cleanOldMetrics(ctx)
		}
	}
}

func (c *Collector) sampleOnce(ctx context.Context) {
	now := time.Now()
	timestampMs := now.UnixMilli()

	// 1. CPU
	currTicks, err := readCPUTicks()
	cpuPct := 0.0
	if err == nil && c.hasPrev {
		cpuPct = calculateCPUPercent(c.lastTicks, currTicks)
		c.lastTicks = currTicks
	} else if err == nil {
		c.lastTicks = currTicks
	}

	// 2. Memory
	mem, err := readMemStats()
	if err != nil {
		slog.Debug("read mem stats failed", "error", err)
	}

	// 3. Disk
	disk, err := readDiskStats(c.databaseDir)
	if err != nil {
		slog.Debug("read disk stats failed", "error", err)
	}

	// 4. Load
	load, err := readLoadStats()
	if err != nil {
		slog.Debug("read load stats failed", "error", err)
	}

	// 5. Uptime
	uptimeSec := readUptime()

	// 6. Network rate
	currNet, err := readNetCounters()
	rxRate := 0.0
	txRate := 0.0
	if err == nil && c.hasPrev {
		durationSec := now.Sub(c.lastSampleTime).Seconds()
		if durationSec > 0 {
			if currNet.rxBytes >= c.lastNet.rxBytes {
				rxRate = float64(currNet.rxBytes-c.lastNet.rxBytes) / durationSec
			}
			if currNet.txBytes >= c.lastNet.txBytes {
				txRate = float64(currNet.txBytes-c.lastNet.txBytes) / durationSec
			}
		}
		c.lastNet = currNet
	} else if err == nil {
		c.lastNet = currNet
	}
	c.lastSampleTime = now
	c.hasPrev = true

	sample := MetricSample{
		TimestampMs:      timestampMs,
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

	// Insert into DB
	if c.db != nil {
		_, err := c.db.ExecContext(
			ctx,
			`INSERT INTO system_metrics (
				timestamp_ms, cpu_percent, memory_used_bytes, memory_total_bytes, memory_percent,
				swap_used_bytes, swap_total_bytes, disk_used_bytes, disk_total_bytes, disk_percent,
				load1, load5, load15, uptime_seconds, net_rx_bytes_per_sec, net_tx_bytes_per_sec, process_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sample.TimestampMs,
			sample.CPUPercent,
			sample.MemoryUsedBytes,
			sample.MemoryTotalBytes,
			sample.MemoryPercent,
			sample.SwapUsedBytes,
			sample.SwapTotalBytes,
			sample.DiskUsedBytes,
			sample.DiskTotalBytes,
			sample.DiskPercent,
			sample.Load1,
			sample.Load5,
			sample.Load15,
			sample.UptimeSeconds,
			sample.NetRxBytesPerSec,
			sample.NetTxBytesPerSec,
			sample.ProcessCount,
		)
		if err != nil {
			slog.Debug("insert system metrics failed", "error", err)
		}
	}

	// Update cached status
	var memRuntime runtime.MemStats
	runtime.ReadMemStats(&memRuntime)

	hostname, _ := os.Hostname()
	procUptimeSec := int64(time.Since(c.startTime).Seconds())

	status := SystemStatus{
		Hostname:          hostname,
		OS:                "Linux",
		Kernel:            readKernelVersion(),
		Arch:              runtime.GOARCH,
		CPUModel:          readCPUModel(),
		CPUCores:          runtime.NumCPU(),
		SystemUptimeDesc:  formatDuration(uptimeSec),
		ProcessUptimeDesc: formatDuration(procUptimeSec),
		SystemUptimeSec:   uptimeSec,
		ProcessUptimeSec:  procUptimeSec,
		Goroutines:        runtime.NumGoroutine(),
		GoHeapAllocBytes:  memRuntime.Alloc,
		Current:           sample,
	}

	c.mu.Lock()
	c.currentSample = sample
	c.cachedStatus = status
	c.mu.Unlock()
}

func (c *Collector) GetStatus() SystemStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedStatus
}

func (c *Collector) cleanOldMetrics(ctx context.Context) {
	if c.db == nil {
		return
	}
	// Retain 7 days of high-res system metrics
	cutoffMs := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	_, err := c.db.ExecContext(
		ctx,
		"DELETE FROM system_metrics WHERE timestamp_ms < ?",
		cutoffMs,
	)
	if err != nil {
		slog.Warn("clean old system metrics failed", "error", err)
	}
}
