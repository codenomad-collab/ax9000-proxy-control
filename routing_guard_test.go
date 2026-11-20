package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func routingTestPolicy(root string) routingPolicy {
	node := "🇺🇸美国04-0.1倍 | 电信联通移动推荐"
	p := routingPolicy{Enabled: true, Root: root, Provider: "household", CandidateFilter: "电信|三网推荐", CheckSeconds: 60, QualitySeconds: 1800, CooldownSeconds: 300, DailyBytes: 32 << 20, ProbeBytes: 8 << 20, ProbeURL: "https://dl.google.com/test.zip"}
	for _, name := range []string{"YOUTUBE-2K", "X-STABLE", "GOOGLE-AUTO", "AUTO"} {
		p.Groups = append(p.Groups, routingGroupPolicy{Name: name, Label: name, Target: "https://www.gstatic.com/generate_204", Expected: 204, Nodes: []string{node}})
	}
	return p
}
func routingOriginal() []byte {
	return []byte("secret: test-only-secret\nproxy-providers:\n  household:\n    type: file\n    path: ./providers/household.yaml\n  ai_us_primary:\n    type: file\n    path: ./providers/ai.yaml\n    filter: '^美国$'\nproxy-groups:\n  - name: AI-US-STABLE\n    type: fallback\n    use: [ai_us_primary]\n  - name: PROXY\n    type: select\n    proxies: [AUTO]\n  - name: AUTO\n    type: url-test\n    use: [household]\n  - name: YOUTUBE-2K\n    type: url-test\n    use: [household]\nrules:\n  - DOMAIN-SUFFIX,github.com,AI-US-STABLE\n  - DOMAIN-SUFFIX,youtube.com,YOUTUBE-2K\n  - DOMAIN,youtubei.googleapis.com,YOUTUBE-2K\n  - DOMAIN-SUFFIX,cloud-backup.cn,DIRECT\n  - RULE-SET,cn,DIRECT\n  - MATCH,PROXY\n")
}
func TestRoutingRenderPreservesAIAndSpecificRules(t *testing.T) {
	p := routingTestPolicy(t.TempDir())
	pools := map[string][]string{}
	for _, g := range p.Groups {
		pools[g.Name] = g.Nodes
	}
	b, e := renderRoutingConfig(routingOriginal(), p, pools)
	if e != nil {
		t.Fatal(e)
	}
	var before, after map[string]any
	_ = yaml.Unmarshal(routingOriginal(), &before)
	_ = yaml.Unmarshal(b, &after)
	oldProviders := before["proxy-providers"]
	if !reflect.DeepEqual(oldProviders, after["proxy-providers"]) {
		t.Fatal("changed subscription or AI filters")
	}
	find := func(v any, name string) any {
		for _, x := range v.([]any) {
			m := x.(map[string]any)
			if m["name"] == name {
				return x
			}
		}
		return nil
	}
	if !reflect.DeepEqual(find(before["proxy-groups"], "AI-US-STABLE"), find(after["proxy-groups"], "AI-US-STABLE")) {
		t.Fatal("AI group changed")
	}
	if !bytes.Contains(b, []byte("  ai_us_primary:\n")) || !bytes.Contains(b, []byte("    filter:")) {
		t.Fatal("legacy AI guard rewrite contract broken")
	}
	rules := after["rules"].([]any)
	index := func(s string) int {
		for i, x := range rules {
			if x == s {
				return i
			}
		}
		return -1
	}
	if index("DOMAIN,youtubei.googleapis.com,YOUTUBE-2K") > index("DOMAIN-SUFFIX,googleapis.com,GOOGLE-AUTO") {
		t.Fatal("YouTube API stolen by Google")
	}
	if index("DOMAIN-SUFFIX,gemini.google.com,AI-US-STABLE") > index("DOMAIN-SUFFIX,google.com,GOOGLE-AUTO") {
		t.Fatal("AI exception lower priority")
	}
	if index("DOMAIN-SUFFIX,cloud-backup.cn,DIRECT") < 0 {
		t.Fatal("LAN/direct rule lost")
	}
	for _, g := range p.Groups {
		m := find(after["proxy-groups"], g.Name).(map[string]any)
		if m["type"] != "fallback" || m["empty-fallback"] != "REJECT" {
			t.Fatal("no safe kernel fallback")
		}
		child := find(after["proxy-groups"], routingChild(g.Name, 0)).(map[string]any)
		if child["empty-fallback"] != "REJECT" || child["hidden"] != true {
			t.Fatal("missing candidate could leak direct")
		}
	}
	again, e := renderRoutingConfig(b, p, pools)
	if e != nil || !bytes.Equal(b, again) {
		t.Fatal("reconcile is not idempotent")
	}
}
func TestRoutingRejectsEmptyOrMissingManagedPrerequisites(t *testing.T) {
	p := routingTestPolicy(t.TempDir())
	if _, e := renderRoutingConfig(routingOriginal(), p, map[string][]string{}); e == nil {
		t.Fatal("empty pools accepted")
	}
	b := bytes.ReplaceAll(routingOriginal(), []byte("AI-US-STABLE"), []byte("OTHER"))
	pools := map[string][]string{}
	for _, g := range p.Groups {
		pools[g.Name] = g.Nodes
	}
	if _, e := renderRoutingConfig(b, p, pools); e == nil {
		t.Fatal("missing AI contract accepted")
	}
}
func TestRoutingPromotionRequiresTwoWindowsAndCooldown(t *testing.T) {
	now := time.Now()
	s := &routingGroupState{}
	cur := routingMeasurement{Name: "current", Delay: 200, Good: true}
	best := routingMeasurement{Name: "better", Delay: 100, Good: true}
	if routingShouldPromote(s, cur, best, now, 5*time.Minute) {
		t.Fatal("single good window switched")
	}
	if !routingShouldPromote(s, cur, best, now.Add(time.Minute), 5*time.Minute) {
		t.Fatal("two good windows did not qualify")
	}
	s.LastSwitch = now
	if routingShouldPromote(s, cur, best, now.Add(time.Minute), 5*time.Minute) {
		t.Fatal("cooldown ignored")
	}
	s.LastSwitch = time.Time{}
	best.Delay = 170
	if routingShouldPromote(s, cur, best, now, 5*time.Minute) {
		t.Fatal("minor jitter switched")
	}
	s.Failures = 1
	cur.Good = false
	if routingShouldPromote(s, cur, best, now, 5*time.Minute) {
		t.Fatal("one failure switched")
	}
	s.Failures = 2
	if !routingShouldPromote(s, cur, best, now, 5*time.Minute) {
		t.Fatal("hard failure blocked by cooldown")
	}
}
func routingTestGuard(t *testing.T) *routingGuard {
	t.Helper()
	root := t.TempDir()
	p := routingTestPolicy(root)
	path := filepath.Join(root, "policy.json")
	b, _ := json.Marshal(p)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	app := &App{cfg: Config{RoutingGuardPolicyPath: path}, audit: newAuditLog(10)}
	return newRoutingGuard(app)
}
func TestRoutingBudgetSurvivesRestartAndFailsClosed(t *testing.T) {
	g := routingTestGuard(t)
	for i := 0; i < 4; i++ {
		if e := g.reserveProbe(); e != nil {
			t.Fatal(e)
		}
	}
	again := newRoutingGuard(g.app)
	if e := again.reserveProbe(); e == nil {
		t.Fatal("restart reset budget")
	}
	if e := os.WriteFile(g.statePath, []byte("{bad"), 0600); e != nil {
		t.Fatal(e)
	}
	broken := newRoutingGuard(g.app)
	if e := broken.reserveProbe(); e == nil {
		t.Fatal("corrupt budget failed open")
	}
	broken.state.Day = "1999-01-01"
	if e := broken.reserveProbe(); e == nil {
		t.Fatal("day reset bypassed corrupted ledger")
	}
}
func TestRoutingBudgetMustPersistBeforeTraffic(t *testing.T) {
	g := routingTestGuard(t)
	g.statePath = filepath.Join(t.TempDir(), "missing", "state")
	if e := g.reserveProbe(); e == nil {
		t.Fatal("unwritable state allowed probe")
	}
	if g.state.Spent != 0 || !g.budgetError {
		t.Fatal("failed reservation state incorrect")
	}
}
func TestRoutingCandidateFilterExcludesMetadataAndUnsuitableNodes(t *testing.T) {
	g := routingTestGuard(t)
	nodes := []routingProxy{
		{Name: "日本05 | 电信推荐", Type: "Vless", Alive: true},
		{Name: "美国 | 三网推荐", Type: "Hysteria2", Alive: true},
		{Name: "台湾 | 电信推荐 避免晚高峰", Type: "Hysteria2"},
		{Name: "美国 | 电信推荐 合适下载", Type: "Vless"},
		{Name: "剩余流量：1GB", Type: "Shadowsocks"},
		{Name: "其他普通", Type: "Vless"},
	}
	got := g.eligible(nodes)
	if len(got) != 2 {
		t.Fatalf("eligible=%v", got)
	}
}

type routingRoundTripper func(*http.Request) (*http.Response, error)

func (f routingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type routingSlowBody struct{ left, read int64 }

func (b *routingSlowBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	if len(p) > 65536 {
		p = p[:65536]
	}
	n := len(p)
	if int64(n) > b.left {
		n = int(b.left)
	}
	b.left -= int64(n)
	b.read += int64(n)
	time.Sleep(time.Millisecond)
	return n, nil
}
func (b *routingSlowBody) Close() error { return nil }
func TestRoutingPayloadRejectsTinyAndErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		code   int
		length int64
	}{{200, 24}, {429, 1}, {403, 10000000}} {
		client := &http.Client{Transport: routingRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.code, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader("bad"))}, nil
		})}
		if _, e := measureRoutingBody(context.Background(), client, "https://test.invalid/data", 2<<20); e == nil {
			t.Fatal("false speed accepted", tc)
		}
	}
}
func TestRoutingPayloadBoundWhenServerIgnoresRange(t *testing.T) {
	body := &routingSlowBody{left: 16 << 20}
	client := &http.Client{Transport: routingRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "bytes=0-2097151" {
			t.Error("range missing")
		}
		return &http.Response{StatusCode: 200, ContentLength: 16 << 20, Body: body}, nil
	})}
	speed, e := measureRoutingBody(context.Background(), client, "https://test.invalid/data", 2<<20)
	if e != nil || speed <= 0 {
		t.Fatal(speed, e)
	}
	if body.read != 2<<20 {
		t.Fatal("payload exceeded cap", body.read)
	}
}
func TestRoutingReconnectNeverClosesAIOrOtherServices(t *testing.T) {
	g := routingTestGuard(t)
	secret := filepath.Join(t.TempDir(), "secret.yaml")
	_ = os.WriteFile(secret, []byte("secret: test-token\n"), 0600)
	g.app.cfg.ShellCrashConfigPaths = []string{secret}
	var mu sync.Mutex
	deleted := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("auth missing")
		}
		if r.Method == "DELETE" {
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(204)
			return
		}
		_, _ = io.WriteString(w, "{\"connections\":[{\"id\":\"yt\",\"chains\":[\"old\",\"YOUTUBE-2K\"]},{\"id\":\"ai\",\"chains\":[\"old\",\"AI-US-STABLE\"]},{\"id\":\"other\",\"chains\":[\"old\",\"AUTO\"]},{\"id\":\"new\",\"chains\":[\"new\",\"YOUTUBE-2K\"]}]}")
	}))
	defer srv.Close()
	g.app.cfg.MihomoControllerURL = srv.URL
	g.reconnectGroup(context.Background(), "YOUTUBE-2K", "old")
	if !reflect.DeepEqual(deleted, []string{"/connections/yt"}) {
		t.Fatalf("closed wrong flows: %v", deleted)
	}
}
func TestRoutingPrunePreservesOtherBackups(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"smart-routing-20261117-100000.000", "smart-routing-20261117-110000.000", "smart-routing-20261117-120000.000", "smart-routing-20261117-130000.000", "youtube-original"} {
		p := filepath.Join(dir, name)
		_ = os.Mkdir(p, 0700)
		_ = os.WriteFile(filepath.Join(p, "config.yaml"), []byte("original"), 0600)
	}
	pruneRoutingBackups(dir)
	if _, e := os.Stat(filepath.Join(dir, "youtube-original/config.yaml")); e != nil {
		t.Fatal("unrelated backup removed")
	}
	if _, e := os.Stat(filepath.Join(dir, "smart-routing-20261117-100000.000")); !os.IsNotExist(e) {
		t.Fatal("old owned backup retained")
	}
}
func TestRoutingEndpointsRequireAuthAndCSRF(t *testing.T) {
	cfg := testConfig("test-password")
	app := newApp(cfg)
	h := app.routes()
	req := httptest.NewRequest("GET", "/api/routing-guard", nil)
	req.Host = cfg.Listen
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatal("unauth status exposed", res.Code)
	}
	id, _, _, e := app.auth.create()
	if e != nil {
		t.Fatal(e)
	}
	req = httptest.NewRequest("POST", "/api/routing-guard/action", strings.NewReader("{\"action\":\"restore\"}"))
	req.Host = cfg.Listen
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatal("CSRF missing", res.Code)
	}
}

func TestRoutingClearsPinBeforeNoopVerification(t *testing.T) {
	g := routingTestGuard(t)
	g.initialized = true
	g.lastQuality = time.Now()
	secret := filepath.Join(t.TempDir(), "secret.yaml")
	_ = os.WriteFile(secret, []byte("secret: dummy-only\n"), 0600)
	g.app.cfg.ShellCrashConfigPaths = []string{secret}
	pools := g.pools()
	cfg, e := renderRoutingConfig(routingOriginal(), g.policy, pools)
	if e != nil {
		t.Fatal(e)
	}
	_ = os.MkdirAll(filepath.Join(g.policy.Root, "yamls"), 0700)
	for _, n := range []string{"config.yaml", "config.yaml.template"} {
		if e = os.WriteFile(filepath.Join(g.policy.Root, "yamls", n), cfg, 0600); e != nil {
			t.Fatal(e)
		}
	}
	var mu sync.Mutex
	fixed := "manual-node"
	deletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "DELETE" && r.URL.Path == "/proxies/YOUTUBE-2K" {
			fixed = ""
			deletes++
			w.WriteHeader(204)
			return
		}
		if strings.Contains(r.URL.Path, "healthcheck") {
			_, _ = io.WriteString(w, "{\"delay\":150}")
			return
		}
		if r.URL.Path == "/connections" {
			_, _ = io.WriteString(w, "{\"connections\":[]}")
			return
		}
		if r.URL.Path == "/providers/proxies/household" {
			_ = json.NewEncoder(w).Encode(map[string]any{"proxies": []routingProxy{{Name: g.policy.Groups[0].Nodes[0], Type: "Hysteria2", Alive: true}}})
			return
		}
		proxies := map[string]routingProxy{}
		for _, p := range g.policy.Groups {
			child := routingChild(p.Name, 0)
			proxies[p.Name] = routingProxy{Type: "Fallback", Now: child, All: []string{child}}
			proxies[child] = routingProxy{Type: "Selector", Now: p.Nodes[0], All: p.Nodes}
		}
		yt := proxies["YOUTUBE-2K"]
		yt.Fixed = fixed
		proxies["YOUTUBE-2K"] = yt
		_ = json.NewEncoder(w).Encode(routingProxySnapshot{Proxies: proxies})
	}))
	defer srv.Close()
	g.app.cfg.MihomoControllerURL = srv.URL
	g.run(context.Background(), false)
	if deletes != 1 || fixed != "" {
		t.Fatalf("pin was not cleared: deletes=%d fixed=%s", deletes, fixed)
	}
	if !g.initialized || g.state.LastCheck.IsZero() {
		t.Fatal("no-op reconciliation failed")
	}
}

func TestRoutingStablePoolDoesNotChurnOnProbeRotation(t *testing.T) {
	valid := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	previous := []string{"a", "b", "c"}
	for _, healthy := range [][]string{{"d", "c", "b", "a"}, {"b", "d"}, {"a", "c"}} {
		if got := routingStablePool(previous, "", valid, healthy); !reflect.DeepEqual(got, previous) {
			t.Fatalf("routine probe changed pool: %v", got)
		}
	}
	if got := routingStablePool(previous, "d", valid, []string{"d", "a"}); !reflect.DeepEqual(got, []string{"d", "a", "b"}) {
		t.Fatal("confirmed promotion missing", got)
	}
	delete(valid, "b")
	if got := routingStablePool(previous, "", valid, []string{"d"}); !reflect.DeepEqual(got, []string{"a", "c", "d"}) {
		t.Fatal("renamed candidate not replaced", got)
	}
}

func TestRoutingPolicyAcceptsTwoGiBBudgetAndKeepsUsage(t *testing.T) {
	root := t.TempDir()
	policy := routingTestPolicy(root)
	policy.DailyBytes = 2 << 30
	path := filepath.Join(root, "policy.json")
	write := func() {
		t.Helper()
		b, e := json.Marshal(policy)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	loaded, e := loadRoutingPolicy(path)
	if e != nil || loaded.DailyBytes != 2<<30 {
		t.Fatalf("2GiB policy rejected: %v", e)
	}
	app := &App{cfg: Config{RoutingGuardPolicyPath: path}, audit: newAuditLog(10)}
	g := newRoutingGuard(app)
	g.state.Day = g.day()
	g.state.Spent = 256 << 20
	if e = g.saveLocked(); e != nil {
		t.Fatal(e)
	}
	restarted := newRoutingGuard(app)
	if !restarted.configured || restarted.state.Spent != 256<<20 {
		t.Fatal("budget increase reset previous usage")
	}
	if e = restarted.reserveProbe(); e != nil {
		t.Fatal("new allowance unavailable", e)
	}
	if restarted.state.Spent != (256<<20)+(8<<20) {
		t.Fatal("reservation did not retain previous usage")
	}
	policy.DailyBytes = (2 << 30) + 1
	write()
	if _, e = loadRoutingPolicy(path); e == nil {
		t.Fatal("unsupported upper limit accepted")
	}
}
