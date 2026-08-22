package sysmonitor

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type MetricSample struct {
	TimestampMs       int64   `json:"timestamp_ms"`
	TimeDisplay       string  `json:"time_display"`
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	MemoryPercent     float64 `json:"memory_percent"`
	SwapUsedBytes     uint64  `json:"swap_used_bytes"`
	SwapTotalBytes    uint64  `json:"swap_total_bytes"`
	DiskUsedBytes     uint64  `json:"disk_used_bytes"`
	DiskTotalBytes    uint64  `json:"disk_total_bytes"`
	DiskPercent       float64 `json:"disk_percent"`
	Load1             float64 `json:"load1"`
	Load5             float64 `json:"load5"`
	Load15            float64 `json:"load15"`
	UptimeSeconds     int64   `json:"uptime_seconds"`
	NetRxBytesPerSec  float64 `json:"net_rx_bytes_per_sec"`
	NetTxBytesPerSec  float64 `json:"net_tx_bytes_per_sec"`
	ProcessCount      int     `json:"process_count"`
}

type SystemStatus struct {
	Hostname           string       `json:"hostname"`
	OS                 string       `json:"os"`
	Kernel             string       `json:"kernel"`
	Arch               string       `json:"arch"`
	CPUModel           string       `json:"cpu_model"`
	CPUCores           int          `json:"cpu_cores"`
	SystemUptimeDesc   string       `json:"system_uptime_desc"`
	ProcessUptimeDesc  string       `json:"process_uptime_desc"`
	SystemUptimeSec    int64        `json:"system_uptime_sec"`
	ProcessUptimeSec   int64        `json:"process_uptime_sec"`
	Goroutines         int          `json:"goroutines"`
	GoHeapAllocBytes   uint64       `json:"go_heap_alloc_bytes"`
	Current            MetricSample `json:"current"`
}

type cpuTicks struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

func readCPUTicks() (cpuTicks, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTicks{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 8 {
				return cpuTicks{}, fmt.Errorf("insufficient cpu fields")
			}
			var t cpuTicks
			t.user, _ = strconv.ParseUint(fields[1], 10, 64)
			t.nice, _ = strconv.ParseUint(fields[2], 10, 64)
			t.system, _ = strconv.ParseUint(fields[3], 10, 64)
			t.idle, _ = strconv.ParseUint(fields[4], 10, 64)
			t.iowait, _ = strconv.ParseUint(fields[5], 10, 64)
			t.irq, _ = strconv.ParseUint(fields[6], 10, 64)
			t.softirq, _ = strconv.ParseUint(fields[7], 10, 64)
			if len(fields) > 8 {
				t.steal, _ = strconv.ParseUint(fields[8], 10, 64)
			}
			return t, nil
		}
	}
	return cpuTicks{}, fmt.Errorf("cpu line not found")
}

func calculateCPUPercent(prev, curr cpuTicks) float64 {
	prevIdle := prev.idle + prev.iowait
	currIdle := curr.idle + curr.iowait

	prevNonIdle := prev.user + prev.nice + prev.system + prev.irq + prev.softirq + prev.steal
	currNonIdle := curr.user + curr.nice + curr.system + curr.irq + curr.softirq + curr.steal

	prevTotal := prevIdle + prevNonIdle
	currTotal := currIdle + currNonIdle

	totalDelta := float64(currTotal - prevTotal)
	idleDelta := float64(currIdle - prevIdle)

	if totalDelta <= 0 {
		return 0.0
	}
	percent := (1.0 - (idleDelta / totalDelta)) * 100.0
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent
}

type memStats struct {
	total     uint64
	available uint64
	used      uint64
	percent   float64
	swapTotal uint64
	swapFree  uint64
	swapUsed  uint64
}

func readMemStats() (memStats, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return memStats{}, err
	}
	defer file.Close()

	var (
		total, free, available, buffers, cached uint64
		swapTotal, swapFree                     uint64
	)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}
		valKb, _ := strconv.ParseUint(valFields[0], 10, 64)
		valBytes := valKb * 1024

		switch key {
		case "MemTotal":
			total = valBytes
		case "MemFree":
			free = valBytes
		case "MemAvailable":
			available = valBytes
		case "Buffers":
			buffers = valBytes
		case "Cached":
			cached = valBytes
		case "SwapTotal":
			swapTotal = valBytes
		case "SwapFree":
			swapFree = valBytes
		}
	}

	if available == 0 {
		available = free + buffers + cached
	}
	used := uint64(0)
	if total >= available {
		used = total - available
	}
	percent := 0.0
	if total > 0 {
		percent = (float64(used) / float64(total)) * 100.0
	}

	swapUsed := uint64(0)
	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}

	return memStats{
		total:     total,
		available: available,
		used:      used,
		percent:   percent,
		swapTotal: swapTotal,
		swapFree:  swapFree,
		swapUsed:  swapUsed,
	}, nil
}

type diskStats struct {
	total   uint64
	used    uint64
	percent float64
}

func readDiskStats(path string) (diskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		if err2 := syscall.Statfs("/", &stat); err2 != nil {
			return diskStats{}, err
		}
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used := uint64(0)
	if total >= free {
		used = total - free
	}
	percent := 0.0
	if total > 0 {
		percent = (float64(used) / float64(total)) * 100.0
	}
	return diskStats{
		total:   total,
		used:    used,
		percent: percent,
	}, nil
}

type loadStats struct {
	load1     float64
	load5     float64
	load15    float64
	processes int
}

func readLoadStats() (loadStats, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return loadStats{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return loadStats{}, fmt.Errorf("insufficient loadavg fields")
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)

	procs := 0
	procParts := strings.Split(fields[3], "/")
	if len(procParts) == 2 {
		procs, _ = strconv.Atoi(procParts[1])
	}

	return loadStats{
		load1:     l1,
		load5:     l5,
		load15:    l15,
		processes: procs,
	}, nil
}

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		secs, _ := strconv.ParseFloat(fields[0], 64)
		return int64(secs)
	}
	return 0
}

type netCounters struct {
	rxBytes uint64
	txBytes uint64
}

func readNetCounters() (netCounters, error) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return netCounters{}, err
	}
	defer file.Close()

	var totalRx, totalTx uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) >= 9 {
			rx, _ := strconv.ParseUint(fields[0], 10, 64)
			tx, _ := strconv.ParseUint(fields[8], 10, 64)
			totalRx += rx
			totalTx += tx
		}
	}
	return netCounters{rxBytes: totalRx, txBytes: totalTx}, nil
}

func readCPUModel() string {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return fmt.Sprintf("%d Cores", runtime.NumCPU())
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return fmt.Sprintf("%d Cores", runtime.NumCPU())
}

func readKernelVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return runtime.GOOS
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		return fmt.Sprintf("Linux %s", fields[2])
	}
	return "Linux"
}

func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return "0分"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60

	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d天", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
	}
	parts = append(parts, fmt.Sprintf("%d分钟", mins))
	return strings.Join(parts, " ")
}
