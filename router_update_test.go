package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPerformLeiGodUpdateStartsInstalledCommand(t *testing.T) {
	dir := t.TempDir()
	modeFile := filepath.Join(dir, "mode")
	command := filepath.Join(dir, "manual_update.sh")
	if err := os.WriteFile(modeFile, []byte("shellcrash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\n[ \"$1\" = check ] || exit 2\necho update-started\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.ModeFile = modeFile
	cfg.LeiGodUpdateCommand = command
	app := &App{cfg: cfg, audit: newAuditLog(100), startedAt: time.Now()}

	result := app.performAction("update_leigod")
	if !result.Successful || result.Message != "update-started" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !result.Status.LeiGod.UpdateEnabled {
		t.Fatal("expected update capability to be reported")
	}
}

func TestPerformLeiGodUpdateRejectsActiveLeiGodMode(t *testing.T) {
	dir := t.TempDir()
	modeFile := filepath.Join(dir, "mode")
	command := filepath.Join(dir, "manual_update.sh")
	if err := os.WriteFile(modeFile, []byte("leigod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.ModeFile = modeFile
	cfg.LeiGodUpdateCommand = command
	app := &App{cfg: cfg, audit: newAuditLog(100), startedAt: time.Now()}

	result := app.performAction("update_leigod")
	if result.Successful || result.Message == "" {
		t.Fatalf("expected active-mode rejection: %#v", result)
	}
}
