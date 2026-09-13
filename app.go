package main

import (
	"embed"
	"io/fs"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type App struct {
	cfg          Config
	configPath   string
	credentialMu sync.RWMutex
	auth         *authStore
	audit        *AuditLog
	actionMu     sync.Mutex
	nodeGuardMu  sync.Mutex
	targetMu     sync.Mutex
	targets      []netip.Prefix
	targetAt     time.Time
	startedAt    time.Time
	metrics      systemMetricsSampler
	last         actionRecord
}

type actionRecord struct {
	mu         sync.RWMutex
	Action     string
	Successful bool
	Message    string
	Time       time.Time
}

type ActionSummary struct {
	Action     string    `json:"action"`
	Successful bool      `json:"successful"`
	Message    string    `json:"message"`
	Time       time.Time `json:"time"`
}

func (r *actionRecord) set(action string, successful bool, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Action = action
	r.Successful = successful
	r.Message = redactSensitive(message)
	r.Time = time.Now()
}

func (r *actionRecord) snapshot() ActionSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ActionSummary{
		Action:     r.Action,
		Successful: r.Successful,
		Message:    r.Message,
		Time:       r.Time,
	}
}

func newApp(cfg Config) *App {
	return newAppWithConfigPath(cfg, "")
}

func newAppWithConfigPath(cfg Config, configPath string) *App {
	return &App{
		cfg:        cfg,
		configPath: configPath,
		auth:       newAuthStore(time.Duration(cfg.SessionTTLMinutes) * time.Minute),
		audit:      newAuditLog(500),
		startedAt:  time.Now(),
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/me", a.requireAuth(a.handleMe))
	mux.HandleFunc("/api/logout", a.requireAuth(a.handleLogout))
	mux.HandleFunc("/api/password", a.requireAuth(a.handlePasswordChange))
	mux.HandleFunc("/api/status", a.requireAuth(a.handleStatus))
	mux.HandleFunc("/api/system-metrics", a.requireAuth(a.handleSystemMetrics))
	mux.HandleFunc("/api/sessions", a.requireAuth(a.handleSessions))
	mux.HandleFunc("/api/logs", a.requireAuth(a.handleLogs))
	mux.HandleFunc("/api/diagnostics", a.requireAuth(a.handleDiagnostics))
	mux.HandleFunc("/api/action", a.requireAuth(a.handleAction))
	mux.HandleFunc("/api/node-guard", a.requireAuth(a.handleNodeGuard))
	mux.HandleFunc("/api/node-guard/action", a.requireAuth(a.handleNodeGuardAction))

	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(assets))
	allowedAssets := map[string]struct{}{
		"/":                     {},
		"/index.html":           {},
		"/app.js":               {},
		"/style.css":            {},
		"/favicon-16.png":       {},
		"/favicon-32.png":       {},
		"/apple-touch-icon.png": {},
		"/icon-192.png":         {},
		"/icon-512.png":         {},
		"/manifest.webmanifest": {},
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "静态资源只支持 GET 和 HEAD")
			return
		}
		if _, ok := allowedAssets[r.URL.Path]; !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))

	return securityHeaders(a.cfg.Listen, mux)
}

func securityHeaders(allowedHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != allowedHost {
			writeError(w, http.StatusMisdirectedRequest, "请求 Host 不在允许列表中")
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
