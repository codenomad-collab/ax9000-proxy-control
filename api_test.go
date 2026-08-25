package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoginAndAuthenticatedStatus(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("router-console-password")
	cfg.Listen = listener.Addr().String()
	app := newApp(cfg)
	server := httptest.NewUnstartedServer(app.routes())
	server.Listener = listener
	server.Start()
	defer server.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, _ := json.Marshal(loginRequest{Password: "router-console-password"})
	response, err := client.Post(server.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected login status: %d", response.StatusCode)
	}

	statusResponse, err := client.Get(server.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status response: %d", statusResponse.StatusCode)
	}
}

func TestFailedLogin(t *testing.T) {
	cfg := testConfig("right-password")
	app := newApp(cfg)
	request := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"password":"wrong"}`))
	request.Host = cfg.Listen
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected response: %d", recorder.Code)
	}
}

func TestRejectsUnexpectedHost(t *testing.T) {
	cfg := testConfig("password")
	app := newApp(cfg)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "attacker.example"
	recorder := httptest.NewRecorder()
	app.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unexpected response: %d", recorder.Code)
	}
}

func TestPasswordChangePersistsAndInvalidatesSessions(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("old-password-123")
	cfg.Listen = listener.Addr().String()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	app := newAppWithConfigPath(cfg, configPath)
	server := httptest.NewUnstartedServer(app.routes())
	server.Listener = listener
	server.Start()
	defer server.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	login := func(password string) (*http.Response, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(loginRequest{Password: password})
		response, loginErr := client.Post(server.URL+"/api/login", "application/json", bytes.NewReader(body))
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		var result map[string]any
		_ = json.NewDecoder(response.Body).Decode(&result)
		_ = response.Body.Close()
		return response, result
	}
	change := func(current, next, csrf string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(passwordChangeRequest{CurrentPassword: current, NewPassword: next})
		request, requestErr := http.NewRequest(http.MethodPost, server.URL+"/api/password", bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		response, changeErr := client.Do(request)
		if changeErr != nil {
			t.Fatal(changeErr)
		}
		_ = response.Body.Close()
		return response
	}

	response, loginResult := login("old-password-123")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected login status: %d", response.StatusCode)
	}
	csrf, _ := loginResult["csrf"].(string)
	if csrf == "" {
		t.Fatal("login did not return a CSRF token")
	}

	if response = change("wrong-current", "memorable-pass-88", csrf); response.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected wrong-current response: %d", response.StatusCode)
	}
	if response = change("old-password-123", "short", csrf); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected policy response: %d", response.StatusCode)
	}
	if response = change("old-password-123", "memorable-pass-88", csrf); response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected password-change response: %d", response.StatusCode)
	}

	statusResponse, err := client.Get(server.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session remained valid: %d", statusResponse.StatusCode)
	}

	response, _ = login("old-password-123")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password remained valid: %d", response.StatusCode)
	}
	response, _ = login("memorable-pass-88")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("new password was rejected: %d", response.StatusCode)
	}

	persisted, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.passwordMatches("memorable-pass-88") || persisted.passwordMatches("old-password-123") {
		t.Fatal("persisted password hash does not match the new password")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected config permissions: %o", info.Mode().Perm())
	}
}
