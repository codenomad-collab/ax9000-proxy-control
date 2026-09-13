package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type CPUStats struct {
	Cores        int     `json:"cores"`
	UsagePercent float64 `json:"usage_percent"`
	SampleReady  bool    `json:"sample_ready"`
	Load1        float64 `json:"load_1"`
	Load5        float64 `json:"load_5"`
	Load15       float64 `json:"load_15"`
}

type MemoryStats struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

type StorageStats struct {
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Mounted        bool    `json:"mounted"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

type InterfaceStats struct {
	Name             string  `json:"name"`
	Label            string  `json:"label"`
	Up               bool    `json:"up"`
	RXBytes          uint64  `json:"rx_bytes"`
	TXBytes          uint64  `json:"tx_bytes"`
	RXBytesPerSecond float64 `json:"rx_bytes_per_second"`
	TXBytesPerSecond float64 `json:"tx_bytes_per_second"`
	SampleReady      bool    `json:"sample_ready"`
}

type LinkStats struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Role      string `json:"role"`
	Up        bool   `json:"up"`
	SpeedMbps int    `json:"speed_mbps"`
	Duplex    string `json:"duplex"`
}

type MeshNodeStats struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Backhaul string `json:"backhaul"`
	Quality  string `json:"quality"`
	Online   bool   `json:"online"`
}

type MeshStats struct {
	Enabled       bool            `json:"enabled"`
	Role          string          `json:"role"`
	Version       string          `json:"version"`
	Fresh         bool            `json:"fresh"`
	NodeCount     int             `json:"node_count"`
	UpdatedAtUnix int64           `json:"updated_at_unix"`
	Nodes         []MeshNodeStats `json:"nodes"`
}

type SystemMetricsResponse struct {
	CPU        CPUStats         `json:"cpu"`
	Memory     MemoryStats      `json:"memory"`
	Storage    []StorageStats   `json:"storage"`
	Links      []LinkStats      `json:"links"`
	Mesh       MeshStats        `json:"mesh"`
	Interfaces []InterfaceStats `json:"interfaces"`
	Timestamp  time.Time        `json:"timestamp"`
}

type cpuCounters struct {
	Total uint64
	Idle  uint64
}

type networkCounters struct {
	RXBytes uint64
	TXBytes uint64
}

type systemMetricsSampler struct {
	mu          sync.Mutex
	lastCPU     cpuCounters
	cpuReady    bool
	lastNetwork map[string]networkCounters
	lastNetAt   time.Time
}

var monitoredInterfaces = []struct {
	Name  string
	Label string
}{
	{Name: "pppoe-wan", Label: "公网 WAN"},
	{Name: "br-lan", Label: "家庭 LAN"},
	{Name: "utun", Label: "ShellCrash 隧道"},
	{Name: "tun_Game", Label: "雷神游戏隧道"},
	{Name: "wl0", Label: "无线接口 wl0"},
	{Name: "wl1", Label: "无线接口 wl1"},
	{Name: "wl2", Label: "无线接口 wl2"},
	{Name: "wl7", Label: "无线接口 wl7"},
}

func (a *App) systemMetrics() SystemMetricsResponse {
	a.metrics.mu.Lock()
	defer a.metrics.mu.Unlock()

	now := time.Now()
	system := readSystemState()
	response := SystemMetricsResponse{
		CPU: CPUStats{
			Cores:  runtime.NumCPU(),
			Load1:  system.Load1,
			Load5:  system.Load5,
			Load15: system.Load15,
		},
		Memory:    memoryStats(system),
		Storage:   readStorageStats(),
		Links:     readEthernetLinks("/sys/class/net"),
		Mesh:      readMeshStats("/etc/config/xiaoqiang", "/tmp/xq_whc_quire", now),
		Timestamp: now,
	}

	if current, ok := readCPUCounters("/proc/stat"); ok {
		response.CPU.UsagePercent, response.CPU.SampleReady = calculateCPUUsage(a.metrics.lastCPU, current, a.metrics.cpuReady)
		a.metrics.lastCPU = current
		a.metrics.cpuReady = true
	}

	currentNetwork := readNetworkCounters("/proc/net/dev")
	deltaSeconds := now.Sub(a.metrics.lastNetAt).Seconds()
	mode := readMode(a.cfg.ModeFile)
	for _, item := range monitoredInterfaces {
		current, exists := currentNetwork[item.Name]
		if !exists || !shouldShowInterface(item.Name, mode, current) {
			continue
		}
		stats := InterfaceStats{
			Name:    item.Name,
			Label:   item.Label,
			Up:      interfaceUp(item.Name),
			RXBytes: current.RXBytes,
			TXBytes: current.TXBytes,
		}
		if previous, ok := a.metrics.lastNetwork[item.Name]; ok && deltaSeconds >= 0.25 && deltaSeconds <= 30 && current.RXBytes >= previous.RXBytes && current.TXBytes >= previous.TXBytes {
			stats.RXBytesPerSecond = float64(current.RXBytes-previous.RXBytes) / deltaSeconds
			stats.TXBytesPerSecond = float64(current.TXBytes-previous.TXBytes) / deltaSeconds
			stats.SampleReady = true
		}
		response.Interfaces = append(response.Interfaces, stats)
	}
	if len(currentNetwork) > 0 {
		a.metrics.lastNetwork = currentNetwork
		a.metrics.lastNetAt = now
	}

	return response
}

func readEthernetLinks(root string) []LinkStats {
	targets := []LinkStats{
		{Name: "eth4", Label: "WAN 上联", Role: "PPPoE 物理口"},
		{Name: "eth2", Label: "LAN 端口 eth2", Role: "家庭 LAN"},
		{Name: "eth3", Label: "LAN 端口 eth3", Role: "家庭 LAN"},
		{Name: "eth0", Label: "聚合成员 eth0", Role: "bond0"},
		{Name: "eth1", Label: "聚合成员 eth1", Role: "bond0"},
		{Name: "bond0", Label: "LAN 聚合接口", Role: "逻辑聚合"},
	}
	result := make([]LinkStats, 0, len(targets))
	for _, target := range targets {
		base := filepath.Join(root, target.Name)
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			continue
		}
		carrier := strings.TrimSpace(readSmallFile(filepath.Join(base, "carrier")))
		operState := strings.TrimSpace(readSmallFile(filepath.Join(base, "operstate")))
		target.Up = carrier == "1" && operState != "down"
		if target.Up {
			target.SpeedMbps, _ = strconv.Atoi(strings.TrimSpace(readSmallFile(filepath.Join(base, "speed"))))
			duplex := strings.ToLower(strings.TrimSpace(readSmallFile(filepath.Join(base, "duplex"))))
			if duplex == "full" || duplex == "half" {
				target.Duplex = duplex
			}
		}
		result = append(result, target)
	}
	return result
}

func readSmallFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readMeshStats(configPath, nodesPath string, now time.Time) MeshStats {
	options := readUCIOptions(configPath)
	stats := MeshStats{
		Version: options["MESH_VERSION"],
		Nodes:   []MeshNodeStats{},
	}
	switch options["NETMODE"] {
	case "whc_cap":
		stats.Enabled = true
		stats.Role = "cap"
	case "whc_re":
		stats.Enabled = true
		stats.Role = "re"
	}

	if info, err := os.Stat(nodesPath); err == nil {
		stats.UpdatedAtUnix = info.ModTime().Unix()
		age := now.Sub(info.ModTime())
		stats.Fresh = age >= -time.Minute && age <= 5*time.Minute
	}

	file, err := os.Open(nodesPath)
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var record struct {
				Backhauls   string `json:"backhauls"`
				BackhaulsQA string `json:"backhauls_qa"`
				Locale      string `json:"locale"`
				Initted     string `json:"initted"`
				Result      string `json:"return"`
				IP          string `json:"ip"`
			}
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Result != "success" {
				continue
			}
			backhaulMask, _ := strconv.Atoi(record.Backhauls)
			qualityMask, _ := strconv.Atoi(record.BackhaulsQA)
			name := strings.TrimSpace(record.Locale)
			if name == "" {
				name = "Mesh 子节点 " + strconv.Itoa(len(stats.Nodes)+1)
			}
			stats.Nodes = append(stats.Nodes, MeshNodeStats{
				Name:     name,
				IP:       strings.TrimSpace(record.IP),
				Backhaul: decodeMeshBackhaul(backhaulMask),
				Quality:  decodeMeshQuality(backhaulMask, qualityMask),
				Online:   record.Initted == "1" && stats.Fresh,
			})
		}
	}
	if len(stats.Nodes) > 0 {
		stats.Enabled = true
	}
	if stats.Enabled {
		stats.NodeCount = len(stats.Nodes) + 1
	}
	return stats
}

func readUCIOptions(path string) map[string]string {
	result := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return result
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != "option" {
			continue
		}
		result[fields[1]] = strings.Trim(strings.Join(fields[2:], " "), "'\"")
	}
	return result
}

func decodeMeshBackhaul(mask int) string {
	links := make([]string, 0, 3)
	if mask&8 != 0 {
		links = append(links, "ethernet")
	}
	if mask&2 != 0 {
		links = append(links, "5ghz")
	}
	if mask&1 != 0 {
		links = append(links, "2.4ghz")
	}
	if len(links) == 0 {
		return "unknown"
	}
	if len(links) > 1 {
		return "hybrid"
	}
	return links[0]
}

func decodeMeshQuality(backhaulMask, qualityMask int) string {
	if backhaulMask == 0 {
		return "unknown"
	}
	matched := backhaulMask & qualityMask
	if matched == backhaulMask {
		return "good"
	}
	if matched != 0 {
		return "mixed"
	}
	return "poor"
}

func memoryStats(system SystemState) MemoryStats {
	total := uint64(max(system.MemTotalKiB, 0)) * 1024
	available := uint64(max(system.MemAvailableKiB, 0)) * 1024
	if available > total {
		available = total
	}
	used := total - available
	return MemoryStats{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: available,
		UsagePercent:   percentage(used, total),
	}
}

func readCPUCounters(path string) (cpuCounters, bool) {
	file, err := os.Open(path)
	if err != nil {
		return cpuCounters{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return cpuCounters{}, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, false
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuCounters{}, false
		}
		values = append(values, value)
	}
	result := cpuCounters{}
	for _, value := range values {
		result.Total += value
	}
	result.Idle = values[3]
	if len(values) > 4 {
		result.Idle += values[4]
	}
	return result, true
}

func calculateCPUUsage(previous, current cpuCounters, ready bool) (float64, bool) {
	if !ready || current.Total <= previous.Total || current.Idle < previous.Idle {
		return 0, false
	}
	deltaTotal := current.Total - previous.Total
	deltaIdle := current.Idle - previous.Idle
	if deltaIdle > deltaTotal {
		return 0, false
	}
	return clampPercent(float64(deltaTotal-deltaIdle) * 100 / float64(deltaTotal)), true
}

func readNetworkCounters(path string) map[string]networkCounters {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	result := make(map[string]networkCounters)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if name == "" || len(fields) < 16 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil && txErr == nil {
			result[name] = networkCounters{RXBytes: rx, TXBytes: tx}
		}
	}
	return result
}

func shouldShowInterface(name, mode string, counters networkCounters) bool {
	switch name {
	case "utun":
		return mode == "shellcrash"
	case "tun_Game":
		return mode == "leigod"
	case "wl0", "wl1", "wl2", "wl7":
		return interfaceUp(name) || counters.RXBytes > 0 || counters.TXBytes > 0
	default:
		return true
	}
}

func readStorageStats() []StorageStats {
	targets := []struct {
		Name string
		Path string
	}{
		{Name: "内部数据存储", Path: "/data"},
		{Name: "ShellCrash 外接存储", Path: "/extdisks/sda1"},
	}
	result := make([]StorageStats, 0, len(targets))
	for _, target := range targets {
		result = append(result, statFilesystem(target.Name, target.Path))
	}
	return result
}

func statFilesystem(name, path string) StorageStats {
	result := StorageStats{Name: name, Path: path}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return result
	}
	blockSize := uint64(stat.Bsize)
	result.Mounted = true
	result.TotalBytes = uint64(stat.Blocks) * blockSize
	freeBytes := uint64(stat.Bfree) * blockSize
	result.AvailableBytes = uint64(stat.Bavail) * blockSize
	if freeBytes <= result.TotalBytes {
		result.UsedBytes = result.TotalBytes - freeBytes
	}
	result.UsagePercent = percentage(result.UsedBytes, result.TotalBytes)
	return result
}

func percentage(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampPercent(float64(value) * 100 / float64(total))
}

func clampPercent(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}
