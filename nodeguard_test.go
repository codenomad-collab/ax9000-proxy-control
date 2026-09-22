package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateNodeGuardCron(t *testing.T) {
	command := "/opt/node-guard"
	original := "SHELL=/bin/sh\n5 * * * * /usr/bin/other-task\n*/10 * * * * /opt/node-guard >/tmp/old.log 2>&1\n"

	enabled := updateNodeGuardCron(original, command, true)
	if !strings.Contains(enabled, "5 * * * * /usr/bin/other-task") {
		t.Fatal("unrelated cron entry was not preserved")
	}
	if cronContainsCommand("*/5 * * * * /opt/node-guard-backup\n", command) {
		t.Fatal("similarly named command was treated as the managed guard")
	}
	if count := strings.Count(enabled, command); count != 1 {
		t.Fatalf("expected one managed command, got %d", count)
	}
	if !cronContainsCommand(enabled, command) {
		t.Fatal("enabled cron was not detected")
	}
	if enabledAgain := updateNodeGuardCron(enabled, command, true); enabledAgain != enabled {
		t.Fatal("enabling an existing entry was not idempotent")
	}

	disabled := updateNodeGuardCron(enabled, command, false)
	if cronContainsCommand(disabled, command) || strings.Contains(disabled, nodeGuardCronComment) {
		t.Fatal("managed cron entry was not removed")
	}
	if !strings.Contains(disabled, "5 * * * * /usr/bin/other-task") {
		t.Fatal("unrelated cron entry was removed")
	}
}

func TestReadNodeGuardState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := `{
  "schema_version": 2,
  "guard_version": "1.1.0",
  "initialized": true,
  "baseline_at": "2026-09-12T14:00:00Z",
  "last_check": "2026-09-12T14:31:46Z",
  "status": "healthy",
  "consecutive_failures": 0,
  "expected_nodes": {"primary":"US-1","secondary":"US-2","backup":"US-3"},
  "last_rollback": "2026-09-12T14:20:00Z",
  "rollback_status": "successful",
  "last_exercise": "2026-09-12T14:25:00Z",
  "exercise_status": "passed",
  "checks": {"openai":{"samples_ms":[180,190],"successes":2,"median_ms":185,"spread_ms":10}}
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := readNodeGuardState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != "1.1.0" || !state.Initialized || state.BaselineAt == "" || state.Status != "healthy" || state.ExpectedNodes.Primary != "US-1" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.RollbackStatus != "successful" || state.LastRollback == "" {
		t.Fatalf("rollback state was not decoded: %+v", state)
	}
	if state.ExerciseStatus != "passed" || state.LastExercise == "" {
		t.Fatalf("exercise state was not decoded: %+v", state)
	}
	if state.Checks["openai"].MedianMS != 185 {
		t.Fatalf("unexpected probe data: %+v", state.Checks["openai"])
	}
}

func TestTailTextFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := tailTextFile(path, 2, 1024)
	if strings.Join(lines, ",") != "three,four" {
		t.Fatalf("unexpected tail: %v", lines)
	}
}
