package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectExternalStorageFailure(t *testing.T) {
	result := detectExternalStorage("", filepath.Join(t.TempDir(), "missing-mounts"))
	if result.Available || !strings.Contains(result.Reason, "cannot read mount table") {
		t.Fatalf("missing mount table should degrade cleanly: %+v", result)
	}
}

func TestDetectExternalStorageOverride(t *testing.T) {
	root := t.TempDir()
	mounts := filepath.Join(t.TempDir(), "mounts")
	line := "/dev/sdz1 " + root + " ext4 rw,relatime 0 0\n"
	if err := os.WriteFile(mounts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	result := detectExternalStorage(root, mounts)
	if !result.Available || result.Path != root || !strings.Contains(result.Reason, "configured override") {
		t.Fatalf("configured writable path should win: %+v", result)
	}
	if result.Device != "/dev/sdz1" || result.Filesystem != "ext4" {
		t.Fatalf("override should retain matching mount metadata: %+v", result)
	}
}

func TestDetectExternalStorageMultipleCandidates(t *testing.T) {
	root := t.TempDir()
	vfat := filepath.Join(root, "usb-small")
	ext4 := filepath.Join(root, "usb-native")
	docker := filepath.Join(ext4, "mi_docker", "lib", "docker")
	for _, path := range []string{vfat, ext4, docker} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	template, err := os.ReadFile("testdata/capabilities/mounts_multi.txt")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.NewReplacer(
		"{{VFAT}}", vfat,
		"{{EXT4}}", ext4,
		"{{DOCKER}}", docker,
	).Replace(string(template))
	mounts := filepath.Join(root, "mounts")
	if err := os.WriteFile(mounts, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := detectExternalStorage("", mounts)
	if !result.Available || result.Path != ext4 || result.Filesystem != "ext4" {
		t.Fatalf("native Linux filesystem should be selected deterministically: %+v", result)
	}
}

func TestResolveRuntimeConfigFromCapabilities(t *testing.T) {
	cfg := testConfig("password")
	cfg.NodeGuardCommand = ""
	cfg.NodeGuardStatePath = ""
	cfg.NodeGuardLogPath = ""
	cfg.ExternalStorage = ""
	cfg.MeshNodesPath = ""
	cfg.WANInterface = ""
	caps := DeviceCapabilities{
		ExternalStorage: StorageCapability{Available: true, Path: "/media/router"},
		WANInterface:    "eth9",
		MeshNodesPath:   "/run/mesh-nodes",
	}
	resolved := resolveRuntimeConfig(cfg, caps)
	if resolved.ExternalStorage != "/media/router" || resolved.WANInterface != "eth9" || resolved.MeshNodesPath != "/run/mesh-nodes" {
		t.Fatalf("capabilities were not applied: %+v", resolved)
	}
	if resolved.NodeGuardCommand != "/media/router/ShellClash/tools/router-ai-node-guard" {
		t.Fatalf("node guard path was not derived from storage: %q", resolved.NodeGuardCommand)
	}
	if !containsString(resolved.ShellCrashConfigPaths, "/media/router/ShellClash/yamls/config.yaml") {
		t.Fatalf("ShellCrash config path was not derived: %v", resolved.ShellCrashConfigPaths)
	}
}

func TestResolveRuntimeConfigPreservesExplicitLegacyPaths(t *testing.T) {
	cfg := testConfig("password")
	cfg.ExternalStorage = "/extdisks/sda1"
	cfg.WANInterface = "eth4"
	cfg.MeshNodesPath = "/tmp/xq_whc_quire"
	cfg.ShellCrashConfigPaths = []string{
		"/tmp/ShellCrash/config.yaml",
		"/extdisks/sda1/ShellClash/yamls/config.yaml",
	}
	cfg.NodeGuardCommand = "/extdisks/sda1/ShellClash/tools/ax9000-ai-node-guard"
	cfg.NodeGuardStatePath = "/extdisks/sda1/ShellClash/tools/ai-node-guard-state.json"
	cfg.NodeGuardLogPath = "/extdisks/sda1/ShellClash/logs/ai-node-guard.log"

	resolved := resolveRuntimeConfig(cfg, DeviceCapabilities{
		ExternalStorage: StorageCapability{Available: true, Path: "/mnt/usb-new"},
		WANInterface:    "eth9",
		MeshNodesPath:   "/tmp/other-mesh-cache",
	})
	if resolved.ExternalStorage != cfg.ExternalStorage || resolved.WANInterface != cfg.WANInterface || resolved.MeshNodesPath != cfg.MeshNodesPath {
		t.Fatalf("explicit legacy overrides must be preserved: %+v", resolved)
	}
	if resolved.NodeGuardCommand != cfg.NodeGuardCommand || resolved.NodeGuardStatePath != cfg.NodeGuardStatePath || resolved.NodeGuardLogPath != cfg.NodeGuardLogPath {
		t.Fatalf("explicit legacy node guard paths must be preserved: %+v", resolved)
	}
	if len(resolved.ShellCrashConfigPaths) != len(cfg.ShellCrashConfigPaths) {
		t.Fatalf("explicit legacy ShellCrash path should not be duplicated: %v", resolved.ShellCrashConfigPaths)
	}
}

func TestDetectDockerUnavailable(t *testing.T) {
	result := detectDocker(context.Background(), filepath.Join(t.TempDir(), "docker.sock"))
	if result.Installed || result.Available || result.Reason == "" {
		t.Fatalf("missing Docker socket should be unavailable, not fatal: %+v", result)
	}
}
