package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type loginRequest struct {
	Password string `json:"password"`
}

type actionRequest struct {
	Action string `json:"action"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	ip := clientIP(r)
	if !a.auth.allowLogin(ip) {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请五分钟后重试")
		return
	}
	var request loginRequest
	if err := decodeJSON(r, &request, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "登录请求格式错误")
		return
	}
	if !a.passwordMatches(request.Password) {
		a.auth.recordFailure(ip)
		a.audit.add("warning", "auth", "来自 "+ip+" 的登录失败")
		time.Sleep(350 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "密码错误")
		return
	}
	a.auth.clearFailures(ip)
	id, csrf, expires, err := a.auth.create()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}
	setSessionCookie(w, id, expires)
	a.audit.add("info", "auth", "管理员已从 "+ip+" 登录")
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": csrf, "expires_at": expires})
}

func (a *App) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var request passwordChangeRequest
	if err := decodeJSON(r, &request, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "修改密码请求格式错误")
		return
	}
	if err := a.changePassword(request.CurrentPassword, request.NewPassword); err != nil {
		switch {
		case errors.Is(err, errCurrentPassword):
			a.audit.add("warning", "auth", "来自 "+clientIP(r)+" 的密码修改验证失败")
			time.Sleep(350 * time.Millisecond)
			writeError(w, http.StatusForbidden, "当前密码不正确")
		case errors.Is(err, errPasswordPolicy), errors.Is(err, errPasswordSame):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			a.audit.add("error", "auth", "密码配置保存失败")
			writeError(w, http.StatusInternalServerError, "密码保存失败，请查看服务日志")
		}
		return
	}
	a.audit.add("info", "auth", "管理员已从 "+clientIP(r)+" 修改控制台密码，所有会话已注销")
	a.auth.clearSessions()
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauthenticate": true})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	session, _ := a.requestSession(r)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": session.CSRF, "expires_at": session.ExpiresAt})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.auth.delete(cookie.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, a.currentStatus())
}

func (a *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, a.sessions(r.URL.Query().Get("service")))
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, a.logs(r.URL.Query().Get("service")))
}

func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	conntrackPath := "unavailable"
	conntrackBytes := int64(0)
	for _, candidate := range a.cfg.ConntrackPaths {
		if info, err := os.Stat(candidate); err == nil {
			conntrackPath = candidate
			conntrackBytes = info.Size()
			break
		}
	}
	controllerConfig := false
	for _, candidate := range a.cfg.ShellCrashConfigPaths {
		if _, err := os.Stat(candidate); err == nil {
			controllerConfig = true
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":                 version,
		"service_uptime_seconds":  time.Since(a.startedAt).Seconds(),
		"status":                  a.currentStatus(),
		"conntrack_path":          conntrackPath,
		"conntrack_bytes":         conntrackBytes,
		"mihomo_config_available": controllerConfig,
		"listen":                  a.cfg.Listen,
		"auto_rollback":           a.cfg.AutoRollback,
		"maximum_session_rows":    a.cfg.MaxSessions,
		"audit_entries":           len(a.audit.snapshot(0)),
	})
}

func (a *App) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var request actionRequest
	if err := decodeJSON(r, &request, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "操作请求格式错误")
		return
	}
	result := a.performAction(strings.TrimSpace(request.Action))
	status := http.StatusOK
	if !result.Successful {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		session, ok := a.requestSession(r)
		if !ok {
			clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "需要登录")
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			provided := r.Header.Get("X-CSRF-Token")
			if len(provided) != len(session.CSRF) || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRF)) != 1 {
				writeError(w, http.StatusForbidden, "CSRF 校验失败")
				return
			}
		}
		next(w, r)
	}
}

func (a *App) requestSession(r *http.Request) (authSession, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authSession{}, false
	}
	return a.auth.get(cookie.Value)
}

func decodeJSON(r *http.Request, target any, limit int64) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "status": status})
}
