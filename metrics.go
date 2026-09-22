package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	CPU        CPUStats                   `json:"cpu"`
	Memory     MemoryStats                `json:"memory"`
	Storage    []StorageStats             `json:"storage"`
	Links      []LinkStats                `json:"links"`
	Mesh       MeshStats                  `json:"mesh"`
	Interfaces []InterfaceStats           `json:"interfaces"`
	Extended   map[string]CollectorResult `json:"extended"`
	Timestamp  time.Time                  `json:"timestamp"`
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

// monitoredInterface 描述一个需要采样速率的网络接口。
type monitoredInterface struct {
	Name  string
	Label string
}

// virtualInterfacePrefixes 是需要排除的非物理接口前缀。
// 这些接口由内核或用户态程序创建，不是可插拔的以太网口。
var virtualInterfacePrefixes = []string{
	"lo", "br-", "bond", "wl", "tun", "utun", "pppoe", "veth",
	"ifb", "sit", "ip6tnl", "ip6gre", "gre", "erspan", "teql", "dummy", "wds", "ap",
	"wifi", "soc", "miireg",
}

func isVirtualInterface(name string) bool {
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func sysfsEntryExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// discoverEthernetInterfaces 扫描 sysfs，返回物理以太网口名称。
// 判据是接口具备 speed 或 carrier 属性，这能排除纯软件接口，
// 同时不依赖具体接口命名——eth0/eth4 之类的编号因机型而异。
func discoverEthernetInterfaces(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if isVirtualInterface(name) {
			continue
		}
		base := filepath.Join(root, name)
		if !sysfsEntryExists(filepath.Join(base, "speed")) && !sysfsEntryExists(filepath.Join(base, "carrier")) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// discoverPrefixedInterfaces 返回指定前缀的接口名，用于无线接口与 bond 逻辑口。
func discoverPrefixedInterfaces(root, prefix string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// readUCIInterfaceDevice 从 UCI network 配置里读取某个 interface 段绑定的设备名。
// 同时兼容 option ifname（旧写法）与 option device（新写法）。
func readUCIInterfaceDevice(path, section string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	inSection := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "config ") {
			inSection = strings.HasPrefix(line, "config interface") &&
				(strings.Contains(line, "'"+section+"'") || strings.Contains(line, "\""+section+"\""))
			continue
		}
		if !inSection {
			continue
		}
		for _, key := range []string{"ifname", "device"} {
			value := uciOptionValue(line, key)
			if value == "" {
				continue
			}
			// ifname 可能是空格分隔的多个设备，取第一个
			if fields := strings.Fields(value); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func uciOptionValue(line, key string) string {
	prefix := "option " + key
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "'\"")
}

// detectWANInterface 解析 WAN 口绑定的物理设备。
// 优先用显式配置覆盖，其次读 UCI network，最后尝试常见拨号段名。
func detectWANInterface(networkConfigPath, override string) string {
	if override != "" {
		return override
	}
	if device := readUCIInterfaceDevice(networkConfigPath, "wan"); device != "" {
		return device
	}
	for _, section := range []string{"wan6", "pppoe", "wan_pppoe"} {
		if device := readUCIInterfaceDevice(networkConfigPath, section); device != "" {
			return device
		}
	}
	return ""
}

// buildMonitoredInterfaces 动态构建需要采样速率的接口列表，不依赖具体机型的接口命名。
func buildMonitoredInterfaces(root, wanInterface string) []monitoredInterface {
	items := make([]monitoredInterface, 0, 12)

	if sysfsEntryExists(filepath.Join(root, "pppoe-wan")) {
		items = append(items, monitoredInterface{Name: "pppoe-wan", Label: "公网 WAN"})
	} else if wanInterface != "" && sysfsEntryExists(filepath.Join(root, wanInterface)) {
		items = append(items, monitoredInterface{Name: wanInterface, Label: "公网 WAN"})
	}

	if sysfsEntryExists(filepath.Join(root, "br-lan")) {
		items = append(items, monitoredInterface{Name: "br-lan", Label: "家庭 LAN"})
	}

	if sysfsEntryExists(filepath.Join(root, "utun")) {
		items = append(items, monitoredInterface{Name: "utun", Label: "ShellCrash 隧道"})
	}
	if sysfsEntryExists(filepath.Join(root, "tun_Game")) {
		items = append(items, monitoredInterface{Name: "tun_Game", Label: "雷神游戏隧道"})
	}

	for _, name := range discoverPrefixedInterfaces(root, "wl") {
		items = append(items, monitoredInterface{Name: name, Label: "无线接口 " + name})
	}

	return items
}

func (a *App) systemMetrics() SystemMetricsResponse {
	a.metrics.mu.Lock()
	defer a.metrics.mu.Unlock()

	now := time.Now()
	system := readSystemState()
	wanInterface := a.capabilities.WANInterface
	if wanInterface == "" {
		wanInterface = detectWANInterface(a.cfg.NetworkConfigPath, a.cfg.WANInterface)
	}
	// Mesh 缓存路径在运行期惰性解析：控制台可能早于固件生成缓存文件启动，
	// 且该文件位于 tmpfs，重启后会被重建。
	meshNodesPath := ""
	if a.meshNodes != nil {
		meshNodesPath = a.meshNodes.Resolve()
	}

	response := SystemMetricsResponse{
		CPU: CPUStats{
			Cores:  runtime.NumCPU(),
			Load1:  system.Load1,
			Load5:  system.Load5,
			Load15: system.Load15,
		},
		Memory:    memoryStats(system),
		Storage:   readStorageStats(a.cfg.ExternalStorage),
		Links:     readEthernetLinks("/sys/class/net", wanInterface),
		Mesh:      readMeshStats(a.cfg.MeshConfigPath, meshNodesPath, now),
		Timestamp: now,
	}
	if a.collectors != nil {
		response.Extended = a.collectors.Snapshot()
	}

	if current, ok := readCPUCounters("/proc/stat"); ok {
		response.CPU.UsagePercent, response.CPU.SampleReady = calculateCPUUsage(a.metrics.lastCPU, current, a.metrics.cpuReady)
		a.metrics.lastCPU = current
		a.metrics.cpuReady = true
	}

	currentNetwork := readNetworkCounters("/proc/net/dev")
	deltaSeconds := now.Sub(a.metrics.lastNetAt).Seconds()
	mode := readMode(a.cfg.ModeFile)
	for _, item := range buildMonitoredInterfaces("/sys/class/net", wanInterface) {
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

// readEthernetLinks 自动发现物理以太网口，并依据 UCI 解析出的 WAN 口标注角色。
// 接口数量与命名不再写死，因此同一份程序可跑在千兆机型与 2.5G/万兆机型上。
func readEthernetLinks(root, wanInterface string) []LinkStats {
	names := discoverEthernetInterfaces(root)
	result := make([]LinkStats, 0, len(names)+1)

	for _, name := range names {
		target := LinkStats{Name: name}
		master := interfaceMaster(root, name)
		switch {
		case strings.HasPrefix(master, "bond"):
			target.Label = "聚合成员 " + name
			target.Role = master
		case name == wanInterface:
			target.Label = "WAN 上联"
			target.Role = "拨号物理口"
		default:
			target.Label = "LAN 端口 " + name
			target.Role = "独立 LAN"
		}
		if !fillLinkState(root, &target) {
			continue
		}
		result = append(result, target)
	}

	for _, name := range discoverPrefixedInterfaces(root, "bond") {
		if !bondHasMembers(root, name) {
			continue
		}
		target := LinkStats{Name: name, Label: "LAN 聚合接口", Role: "逻辑聚合"}
		if fillLinkState(root, &target) {
			result = append(result, target)
		}
	}

	return result
}

func bondHasMembers(root, name string) bool {
	data, err := os.ReadFile(filepath.Join(root, name, "bonding", "slaves"))
	return err == nil && len(strings.Fields(string(data))) > 0
}

// fillLinkState 读取单个接口的链路状态；接口不存在时返回 false。
func fillLinkState(root string, target *LinkStats) bool {
	base := filepath.Join(root, target.Name)
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return false
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
	return true
}

func interfaceMaster(root, name string) string {
	target, err := os.Readlink(filepath.Join(root, name, "master"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
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
	switch {
	case strings.HasPrefix(name, "utun"):
		return mode == "shellcrash"
	case name == "tun_Game":
		return mode == "leigod"
	case strings.HasPrefix(name, "wl"):
		return interfaceUp(name) || counters.RXBytes > 0 || counters.TXBytes > 0
	default:
		return true
	}
}

type storageTarget struct {
	Name string
	Path string
}

// readStorageStats 返回内部数据分区与外接存储的用量。
// 外接存储优先使用配置覆盖，否则从挂载表选择可写的持久化文件系统。
func readStorageStats(externalOverride string) []StorageStats {
	targets := []storageTarget{
		{Name: "内部数据存储", Path: "/data"},
	}
	external := externalOverride
	if external == "" {
		external = discoverExternalStorage()
	}
	if external != "" {
		targets = append(targets, storageTarget{Name: "外接存储", Path: external})
	}

	result := make([]StorageStats, 0, len(targets))
	for _, target := range targets {
		result = append(result, statFilesystem(target.Name, target.Path))
	}
	return result
}

// discoverExternalStorage 使用与启动能力探测相同的确定性选择规则。
func discoverExternalStorage() string {
	capability := detectExternalStorage("", "/proc/mounts")
	return capability.Path
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
