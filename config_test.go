package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testConfig(password string) Config {
	cfg := defaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.PasswordSalt = "00112233445566778899aabbccddeeff"
	sum := sha256.Sum256([]byte(cfg.PasswordSalt + password))
	cfg.PasswordSHA256 = hex.EncodeToString(sum[:])
	return cfg
}

func TestLoadLegacyConfigAddsNodeGuardDefaults(t *testing.T) {
	cfg := testConfig("password")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "node_guard_command")
	delete(legacy, "node_guard_state_path")
	delete(legacy, "node_guard_log_path")
	delete(legacy, "node_guard_cron_path")
	data, _ = json.Marshal(legacy)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := defaultConfig()
	if loaded.NodeGuardCommand != defaults.NodeGuardCommand || loaded.NodeGuardCronPath != defaults.NodeGuardCronPath {
		t.Fatalf("node guard defaults were not applied: %+v", loaded)
	}
}

func TestPasswordMatches(t *testing.T) {
	cfg := testConfig("correct horse battery staple")
	if !cfg.passwordMatches("correct horse battery staple") {
		t.Fatal("expected password to match")
	}
	if cfg.passwordMatches("wrong") {
		t.Fatal("unexpected password match")
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	cfg := testConfig("password")
	cfg.Listen = ""
	if err := cfg.validate(); err == nil {
		t.Fatal("expected missing listen address to fail")
	}
}
