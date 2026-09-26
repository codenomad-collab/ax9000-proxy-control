package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const procTCPHeader = "  sl  local_address rem_address   st\n"

func testSSHManager(t *testing.T) (*sshManager, *string) {
	t.Helper()
	directory := t.TempDir()
	m := newSSHManager("192.168.50.1:9098")
	m.guardPath = filepath.Join(directory, "be10000-ssh-persist.sh")
	m.markerPath = filepath.Join(directory, "be10000-ssh-disabled")
	m.initPath = filepath.Join(directory, "dropbear")
	m.lockPath = filepath.Join(directory, "guard.lock")
	m.procTCPPath = filepath.Join(directory, "tcp")
	m.procTCP6Path = filepath.Join(directory, "tcp6")
	m.isRoot = func() bool { return true }
	for _, path := range []string{m.guardPath, m.initPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+sshMarkerLine+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeProc := func(running bool) {
		t.Helper()
		data := procTCPHeader
		if running {
			data += "   0: 0132A8C0:0016 00000000:0000 0A\n"
		}
		if err := os.WriteFile(m.procTCPPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProc(true)
	if err := os.WriteFile(m.procTCP6Path, []byte(procTCPHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	nvram := "1"
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nvram" {
			switch strings.Join(args, " ") {
			case "get model":
				return []byte("RC01\n"), nil
			case "get ssh_en":
				return []byte(nvram + "\n"), nil
			case "set ssh_en=0":
				nvram = "0"
				return nil, nil
			case "commit":
				return nil, nil
			}
		}
		if name == m.initPath && strings.Join(args, " ") == "stop" {
			writeProc(false)
			return nil, nil
		}
		if name == m.guardPath {
			if _, err := os.Stat(m.markerPath); err == nil {
				return nil, nil
			}
			nvram = "1"
			writeProc(true)
			return nil, nil
		}
		return nil, os.ErrInvalid
	}
	return m, &nvram
}

func TestSSHDisableSurvivesGuardAndCanReenable(t *testing.T) {
	m, nvram := testSSHManager(t)
	if s := m.status(); !s.Supported || !s.Enabled || !s.SafeListener || !s.NVRAMEnabled {
		t.Fatalf("unexpected initial SSH state: %+v", s)
	}
	if err := m.setEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if s := m.status(); s.Enabled || s.Running || s.NVRAMEnabled || *nvram != "0" {
		t.Fatalf("SSH did not turn off: %+v", s)
	}
	if _, err := m.run(context.Background(), m.guardPath); err != nil {
		t.Fatal(err)
	}
	if s := m.status(); s.Running || s.NVRAMEnabled {
		t.Fatalf("scheduled guard restarted disabled SSH: %+v", s)
	}
	if err := m.setEnabled(context.Background(), false); err != nil {
		t.Fatalf("repeated disable should be idempotent: %v", err)
	}
	if err := m.setEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if s := m.status(); !s.Enabled || !s.SafeListener || !s.NVRAMEnabled || *nvram != "1" {
		t.Fatalf("SSH did not recover: %+v", s)
	}
}

func TestSSHRejectsMissingGuardAndUnsafeListener(t *testing.T) {
	m, _ := testSSHManager(t)
	if err := os.WriteFile(m.guardPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if s := m.status(); s.Supported {
		t.Fatalf("unpatched guard was accepted: %+v", s)
	}
	if err := m.setEnabled(context.Background(), false); err != errSSHUnsupported {
		t.Fatalf("unexpected error for unsupported guard: %v", err)
	}
	if err := os.WriteFile(m.guardPath, []byte("#!/bin/sh\n"+sshMarkerLine+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.procTCPPath, []byte(procTCPHeader+"   0: 00000000:0016 00000000:0000 0A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := m.status(); !s.Running || s.SafeListener || !strings.Contains(s.Message, "异常") {
		t.Fatalf("broad SSH listener was not flagged: %+v", s)
	}
}

func TestSSHAPIRequiresSessionAndCSRF(t *testing.T) {
	cfg := testConfig("password")
	a := newApp(cfg)
	m, _ := testSSHManager(t)
	a.ssh = m
	handler := a.routes()
	request := httptest.NewRequest(http.MethodGet, "/api/ssh", nil)
	request.Host = cfg.Listen
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status returned %d", recorder.Code)
	}
	id, csrf, _, err := a.auth.create()
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(actionRequest{Action: "disable"})
		r := httptest.NewRequest(http.MethodPost, "/api/ssh/action", bytes.NewReader(body))
		r.Host = cfg.Listen
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(""); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF token returned %d", w.Code)
	}
	if w := call(csrf); w.Code != http.StatusOK || m.status().Running {
		t.Fatalf("authorized SSH disable failed: status=%d body=%s", w.Code, w.Body.String())
	}
}
