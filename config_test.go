package main

import (
	"crypto/sha256"
	"encoding/hex"
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
