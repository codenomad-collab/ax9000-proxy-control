package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const minimumExternalStorageBytes = 64 << 20

type StorageCapability struct {
	Available      bool   `json:"available"`
	Path           string `json:"path,omitempty"`
	Device         string `json:"device,omitempty"`
	Filesystem     string `json:"filesystem,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	Reason         string `json:"reason"`
}

type DockerCapability struct {
	Installed bool   `json:"installed"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Socket    string `json:"socket,omitempty"`
	Reason    string `json:"reason"`
}

type DeviceCapabilities struct {
	DetectedAt         time.Time         `json:"detected_at"`
	ExternalStorage    StorageCapability `json:"external_storage"`
	WANInterface       string            `json:"wan_interface,omitempty"`
	PhysicalInterfaces []string          `json:"physical_interfaces"`
	Docker             DockerCapability  `json:"docker"`
	TemperatureSensors []string          `json:"temperature_sensors"`
	FanSensors         []string          `json:"fan_sensors"`
	MeshConfigPath     string            `json:"mesh_config_path,omitempty"`
	MeshNodesPath      string            `json:"mesh_nodes_path,omitempty"`
	Warnings           []string          `json:"warnings"`
}

type mountRecord struct {
	Device     string
	Path       string
	Filesystem string
	Options    []string
}

type storageCandidate struct {
	mountRecord
	AvailableBytes uint64
	Writable       bool
}

func detectCapabilities(ctx context.Context, cfg Config) (result DeviceCapabilities) {
	result.DetectedAt = time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("capability detection recovered from an internal error: %v", recovered))
		}
	}()

	result.ExternalStorage = detectExternalStorage(cfg.ExternalStorage, "/proc/mounts")
	result.WANInterface = detectWANInterface(cfg.NetworkConfigPath, cfg.WANInterface)
	result.PhysicalInterfaces = discoverEthernetInterfaces("/sys/class/net")
	result.Docker = detectDocker(ctx, "/var/run/docker.sock")
	result.TemperatureSensors = discoverSensorFiles(
		[]string{"/sys/class/thermal/thermal_zone*/temp", "/sys/class/hwmon/hwmon*/temp*_input"},
	)
	result.FanSensors = discoverSensorFiles([]string{"/sys/class/hwmon/hwmon*/fan*_input"})
	result.MeshConfigPath = firstReadableFile(cfg.MeshConfigPath, []string{"/etc/config/xiaoqiang"})
	result.MeshNodesPath = discoverMeshNodesPath(cfg.MeshNodesPath)

	if !result.ExternalStorage.Available {
		result.Warnings = append(result.Warnings, result.ExternalStorage.Reason)
	}
	if result.WANInterface == "" {
		result.Warnings = append(result.Warnings, "WAN interface was not detected; configure wan_interface to override")
	}
	if len(result.PhysicalInterfaces) == 0 {
		result.Warnings = append(result.Warnings, "no physical Ethernet interfaces were detected")
	}
	if result.MeshNodesPath == "" {
		result.Warnings = append(result.Warnings, "Mesh node cache was not detected; Mesh metrics are unavailable")
	}
	return result
}

func resolveRuntimeConfig(cfg Config, caps DeviceCapabilities) Config {
	if cfg.ExternalStorage == "" && caps.ExternalStorage.Available {
		cfg.ExternalStorage = caps.ExternalStorage.Path
	}
	if cfg.WANInterface == "" {
		cfg.WANInterface = caps.WANInterface
	}
	if cfg.MeshConfigPath == "" {
		cfg.MeshConfigPath = caps.MeshConfigPath
	}
	if cfg.MeshNodesPath == "" {
		cfg.MeshNodesPath = caps.MeshNodesPath
	}
	if cfg.ExternalStorage == "" {
		return cfg
	}

	shellCrashRoot := filepath.Join(cfg.ExternalStorage, "ShellClash")
	persistentConfig := filepath.Join(shellCrashRoot, "yamls", "config.yaml")
	if !containsString(cfg.ShellCrashConfigPaths, persistentConfig) {
		cfg.ShellCrashConfigPaths = append(cfg.ShellCrashConfigPaths, persistentConfig)
	}
	if cfg.NodeGuardCommand == "" {
		cfg.NodeGuardCommand = filepath.Join(shellCrashRoot, "tools", "router-ai-node-guard")
	}
	if cfg.NodeGuardStatePath == "" {
		cfg.NodeGuardStatePath = filepath.Join(shellCrashRoot, "tools", "ai-node-guard-state.json")
	}
	if cfg.NodeGuardLogPath == "" {
		cfg.NodeGuardLogPath = filepath.Join(shellCrashRoot, "logs", "ai-node-guard.log")
	}
	return cfg
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func parseMounts(reader io.Reader) ([]mountRecord, error) {
	scanner := bufio.NewScanner(reader)
	records := make([]mountRecord, 0, 16)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		records = append(records, mountRecord{
			Device:     unescapeMountField(fields[0]),
			Path:       unescapeMountField(fields[1]),
			Filesystem: fields[2],
			Options:    strings.Split(fields[3], ","),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func unescapeMountField(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

func detectExternalStorage(override, mountsPath string) StorageCapability {
	if override != "" {
		device, filesystem := mountMetadataForPath(mountsPath, override)
		candidate, reason := inspectStoragePath(override, device, filesystem)
		if reason != "" {
			return StorageCapability{Path: override, Reason: reason}
		}
		return storageCapabilityFromCandidate(candidate, "selected configured override")
	}

	file, err := os.Open(mountsPath)
	if err != nil {
		return StorageCapability{Reason: "cannot read mount table: " + err.Error()}
	}
	defer file.Close()
	records, err := parseMounts(file)
	if err != nil {
		return StorageCapability{Reason: "cannot parse mount table: " + err.Error()}
	}
	candidates := make([]storageCandidate, 0, len(records))
	for _, record := range records {
		if !eligibleExternalMount(record) {
			continue
		}
		candidate, reason := inspectStoragePath(record.Path, record.Device, record.Filesystem)
		if reason == "" {
			candidate.mountRecord = record
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return StorageCapability{Reason: "no writable external filesystem with at least 64 MiB available was detected"}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftNative := nativeLinuxFilesystem(left.Filesystem)
		rightNative := nativeLinuxFilesystem(right.Filesystem)
		if leftNative != rightNative {
			return leftNative
		}
		if left.AvailableBytes != right.AvailableBytes {
			return left.AvailableBytes > right.AvailableBytes
		}
		return left.Path < right.Path
	})
	selected := candidates[0]
	reason := fmt.Sprintf("selected writable %s mount with the largest suitable capacity", selected.Filesystem)
	return storageCapabilityFromCandidate(selected, reason)
}

// mountMetadataForPath enriches an explicit override when the mount table is
// readable. Failure is intentionally ignored because the configured path can
// still be validated directly with statfs and a write probe.
func mountMetadataForPath(mountsPath, target string) (string, string) {
	file, err := os.Open(mountsPath)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	records, err := parseMounts(file)
	if err != nil {
		return "", ""
	}
	cleanTarget := filepath.Clean(target)
	for _, record := range records {
		if filepath.Clean(record.Path) == cleanTarget {
			return record.Device, record.Filesystem
		}
	}
	return "", ""
}

func eligibleExternalMount(record mountRecord) bool {
	if !hasMountOption(record.Options, "rw") || !persistentFilesystem(record.Filesystem) {
		return false
	}
	cleaned := filepath.Clean(record.Path)
	protected := []string{"/", "/data", "/etc", "/tmp", "/proc", "/sys", "/dev", "/rom", "/overlay", "/userdisk"}
	for _, path := range protected {
		if cleaned == path || (path != "/" && strings.HasPrefix(cleaned, path+"/")) {
			return false
		}
	}
	if strings.Contains(cleaned, "/mi_docker/lib/docker") || record.Filesystem == "overlay" {
		return false
	}
	return strings.HasPrefix(record.Device, "/dev/")
}

func persistentFilesystem(name string) bool {
	switch strings.ToLower(name) {
	case "ext2", "ext3", "ext4", "f2fs", "btrfs", "xfs", "exfat", "vfat", "ntfs", "ntfs3":
		return true
	default:
		return false
	}
}

func nativeLinuxFilesystem(name string) bool {
	switch strings.ToLower(name) {
	case "ext2", "ext3", "ext4", "f2fs", "btrfs", "xfs":
		return true
	default:
		return false
	}
}

func hasMountOption(options []string, target string) bool {
	for _, option := range options {
		if option == target {
			return true
		}
	}
	return false
}

func inspectStoragePath(path, device, filesystem string) (storageCandidate, string) {
	result := storageCandidate{mountRecord: mountRecord{Path: path, Device: device, Filesystem: filesystem}}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return result, "external storage path is not an accessible directory: " + path
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return result, "cannot inspect external storage capacity: " + err.Error()
	}
	result.AvailableBytes = uint64(stat.Bavail) * uint64(stat.Bsize)
	if result.AvailableBytes < minimumExternalStorageBytes {
		return result, "external storage has less than 64 MiB available: " + path
	}
	probe, err := os.CreateTemp(path, ".router-proxy-write-probe-")
	if err != nil {
		return result, "external storage is not writable: " + path
	}
	probeName := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probeName)
		return result, "external storage write probe could not be closed: " + path
	}
	if err := os.Remove(probeName); err != nil {
		return result, "external storage write probe could not be removed: " + path
	}
	result.Writable = true
	return result, ""
}

func storageCapabilityFromCandidate(candidate storageCandidate, reason string) StorageCapability {
	return StorageCapability{
		Available:      true,
		Path:           candidate.Path,
		Device:         candidate.Device,
		Filesystem:     candidate.Filesystem,
		AvailableBytes: candidate.AvailableBytes,
		Reason:         reason,
	}
}

func discoverSensorFiles(patterns []string) []string {
	var result []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
				result = append(result, match)
			}
		}
	}
	sort.Strings(result)
	return result
}

func firstReadableFile(override string, candidates []string) string {
	if override != "" {
		if fileReadable(override) {
			return override
		}
		return ""
	}
	for _, candidate := range candidates {
		if fileReadable(candidate) {
			return candidate
		}
	}
	return ""
}

func fileReadable(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func discoverMeshNodesPath(override string) string {
	if override != "" {
		return firstReadableFile(override, nil)
	}
	candidates := []string{"/tmp/xq_whc_quire"}
	patterns := []string{"/tmp/*mesh*", "/tmp/*whc*"}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil {
			candidates = append(candidates, matches...)
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && fileReadable(candidate) {
			return candidate
		}
	}
	return ""
}

func detectDocker(ctx context.Context, socket string) DockerCapability {
	result := DockerCapability{Socket: socket, Reason: "Docker socket is unavailable"}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		return result
	}
	result.Installed = true
	transport := &http.Transport{
		DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(dialContext, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.Reason = "Docker API is not responding: " + err.Error()
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Reason = fmt.Sprintf("Docker API returned HTTP %d", response.StatusCode)
		return result
	}
	var payload struct {
		Version string `json:"Version"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		result.Reason = "Docker API returned invalid JSON"
		return result
	}
	result.Available = true
	result.Version = payload.Version
	result.Reason = "Docker Engine API is available"
	return result
}
