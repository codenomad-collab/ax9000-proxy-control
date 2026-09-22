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
	capabilities DeviceCapabilities
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
	collectors   *collectorManager
	meshNodes    *meshNodesResolver
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
	return newAppWithMeshResolver(cfg, "", DeviceCapabilities{}, nil)
}

func newAppWithConfigPath(cfg Config, configPath string) *App {
	return newAppWithMeshResolver(cfg, configPath, DeviceCapabilities{}, nil)
}

func newAppWithConfigPathAndCapabilities(cfg Config, configPath string, capabilities DeviceCapabilities) *App {
	return newAppWithMeshResolver(cfg, configPath, capabilities, nil)
}

// newAppWithMeshResolver 供 main.go 传入共享的 Mesh 路径解析器。
// 顶层 system-metrics 与扩展采集器必须引用同一个解析器实例，
// 否则两处的 Mesh 数据可能不一致。
func newAppWithMeshResolver(cfg Config, configPath string, capabilities DeviceCapabilities, meshResolver *meshNodesResolver) *App {
	app := &App{
		cfg:          cfg,
		capabilities: capabilities,
		configPath:   configPath,
		auth:         newAuthStore(time.Duration(cfg.SessionTTLMinutes) * time.Minute),
		audit:        newAuditLog(500),
		startedAt:    time.Now(),
		meshNodes:    meshResolver,
	}
	app.collectors = newCollectorManager(defaultCollectorRegistrations(cfg, capabilities, meshResolver))
	app.collectors.Start()
	return app
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
