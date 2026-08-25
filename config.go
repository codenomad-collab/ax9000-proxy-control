package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Listen                string   `json:"listen"`
	ModeCommand           string   `json:"mode_command"`
	ModeFile              string   `json:"mode_file"`
	ShellCrashConfigPaths []string `json:"shellcrash_config_paths"`
	MihomoControllerURL   string   `json:"mihomo_controller_url"`
	ConntrackPaths        []string `json:"conntrack_paths"`
	IPSetCommand          string   `json:"ipset_command"`
	LogreadCommand        string   `json:"logread_command"`
	PasswordSalt          string   `json:"password_salt"`
	PasswordSHA256        string   `json:"password_sha256"`
	SessionTTLMinutes     int      `json:"session_ttl_minutes"`
	MaxSessions           int      `json:"max_sessions"`
	MaxLogLines           int      `json:"max_log_lines"`
	AutoRollback          bool     `json:"auto_rollback"`
}

func defaultConfig() Config {
	return Config{
		Listen:      "192.168.1.1:9098",
		ModeCommand: "/usr/bin/router-proxy-mode",
		ModeFile:    "/data/router_proxy_mode",
		ShellCrashConfigPaths: []string{
			"/tmp/ShellCrash/config.yaml",
			"/extdisks/sda1/ShellClash/yamls/config.yaml",
		},
		MihomoControllerURL: "http://127.0.0.1:9097",
		ConntrackPaths: []string{
			"/proc/net/nf_conntrack",
			"/proc/net/ip_conntrack",
		},
		IPSetCommand:      "/usr/sbin/ipset",
		LogreadCommand:    "/sbin/logread",
		SessionTTLMinutes: 720,
		MaxSessions:       300,
		MaxLogLines:       250,
		AutoRollback:      true,
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen address is required")
	}
	if c.ModeCommand == "" || c.ModeFile == "" {
		return errors.New("mode command and mode file are required")
	}
	if c.PasswordSalt == "" || c.PasswordSHA256 == "" {
		return errors.New("password salt and password hash are required")
	}
	if _, err := hex.DecodeString(c.PasswordSalt); err != nil {
		return errors.New("password salt must be hexadecimal")
	}
	hash, err := hex.DecodeString(c.PasswordSHA256)
	if err != nil || len(hash) != sha256.Size {
		return errors.New("password hash must be a SHA-256 hexadecimal string")
	}
	if c.SessionTTLMinutes < 5 || c.SessionTTLMinutes > 10080 {
		return errors.New("session_ttl_minutes must be between 5 and 10080")
	}
	if c.MaxSessions < 10 || c.MaxSessions > 2000 {
		return errors.New("max_sessions must be between 10 and 2000")
	}
	if c.MaxLogLines < 20 || c.MaxLogLines > 2000 {
		return errors.New("max_log_lines must be between 20 and 2000")
	}
	return nil
}

func (c Config) passwordMatches(password string) bool {
	sum := sha256.Sum256([]byte(c.PasswordSalt + password))
	want, err := hex.DecodeString(c.PasswordSHA256)
	if err != nil || len(want) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], want) == 1
}
