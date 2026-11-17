package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type routingProxy struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Now     string   `json:"now"`
	Fixed   string   `json:"fixed"`
	All     []string `json:"all"`
	Alive   bool     `json:"alive"`
	History []struct {
		Delay int `json:"delay"`
	} `json:"history"`
}
type routingProxySnapshot struct {
	Proxies map[string]routingProxy `json:"proxies"`
}
type routingSample struct {
	Mbps   float64   `json:"mbps"`
	At     time.Time `json:"at"`
	Rounds []float64 `json:"rounds"`
}
type routingGroupState struct {
	Label       string    `json:"label"`
	Current     string    `json:"current"`
	Nodes       []string  `json:"nodes"`
	DelayMS     int       `json:"delay_ms"`
	Failures    int       `json:"failures"`
	Active      bool      `json:"active"`
	Mbps        float64   `json:"mbps"`
	SampledAt   time.Time `json:"sampled_at"`
	LastSwitch  time.Time `json:"last_switch"`
	Reason      string    `json:"reason"`
	Challenger  string    `json:"challenger,omitempty"`
	Streak      int       `json:"streak,omitempty"`
	WeakWindows int       `json:"weak_windows,omitempty"`
}
type routingEvent struct {
	Time    time.Time `json:"time"`
	Group   string    `json:"group"`
	Message string    `json:"message"`
}
type routingState struct {
	Schema    int                           `json:"schema"`
	Enabled   bool                          `json:"enabled"`
	Day       string                        `json:"day"`
	Spent     int64                         `json:"spent_bytes"`
	LastCheck time.Time                     `json:"last_check"`
	Backup    string                        `json:"backup,omitempty"`
	Message   string                        `json:"message"`
	Groups    map[string]*routingGroupState `json:"groups"`
	Samples   map[string]routingSample      `json:"samples"`
	Events    []routingEvent                `json:"events"`
}
type routingGuard struct {
	app                   *App
	policy                routingPolicy
	policyPath, statePath string
	mu                    sync.RWMutex
	runMu                 sync.Mutex
	state                 routingState
	configured            bool
	running               bool
	initError             string
	budgetError           bool
	initialized           bool
	lastQuality           time.Time
	lastRefresh           time.Time
	traffic               map[string]int64
	turn                  int
	trafficAt             time.Time
}
type routingMeasurement struct {
	Name  string
	Delay int
	Good  bool
}

func newRoutingGuard(app *App) *routingGuard {
	g := &routingGuard{app: app, policyPath: app.cfg.RoutingGuardPolicyPath, traffic: map[string]int64{}}
	if g.policyPath == "" {
		return g
	}
	p, err := loadRoutingPolicy(g.policyPath)
	if err != nil {
		g.initError = err.Error()
		return g
	}
	g.policy = p
	g.configured = true
	g.statePath = g.policyPath + ".state.json"
	g.state = routingState{Schema: 1, Enabled: p.Enabled, Groups: map[string]*routingGroupState{}, Samples: map[string]routingSample{}, Events: []routingEvent{}}
	if b, e := os.ReadFile(g.statePath); e == nil {
		var saved routingState
		if json.Unmarshal(b, &saved) != nil || saved.Schema != 1 || saved.Spent < 0 || saved.Groups == nil || saved.Samples == nil {
			g.budgetError = true
			g.state.Message = "持久状态损坏，带宽测速已停止；轻量检测继续"
		} else {
			g.state = saved
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		g.budgetError = true
		g.state.Message = "无法读取测速预算，带宽测速已停止"
	}
	for _, p := range p.Groups {
		if g.state.Groups[p.Name] == nil {
			g.state.Groups[p.Name] = &routingGroupState{Label: p.Label, Nodes: append([]string{}, p.Nodes...), Reason: "等待首次检查"}
		}
		g.state.Groups[p.Name].Label = p.Label
	}
	for _, sample := range g.state.Samples {
		if sample.At.After(g.lastQuality) && !sample.At.After(time.Now().Add(time.Minute)) {
			g.lastQuality = sample.At
		}
	}
	return g
}
func (g *routingGuard) start(ctx context.Context) {
	if !g.configured {
		return
	}
	go func() {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		g.run(ctx, false)
		ticker := time.NewTicker(time.Duration(g.policy.CheckSeconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.run(ctx, false)
			}
		}
	}()
}
func (g *routingGuard) controller(ctx context.Context, method, path string, body, out any) error {
	secret, err := g.app.readMihomoSecret()
	if err != nil {
		return errors.New("无法读取 Mihomo 认证配置")
	}
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.app.cfg.MihomoControllerURL, "/")+path, data)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return errors.New("Mihomo 控制器连接失败")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Mihomo HTTP %d", res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
	}
	return nil
}
func (g *routingGuard) status() map[string]any {
	g.mu.RLock()
	defer g.mu.RUnlock()
	b, _ := json.Marshal(g.state)
	s := map[string]any{}
	_ = json.Unmarshal(b, &s)
	s["configured"] = g.configured
	s["running"] = g.running
	s["check_seconds"] = g.policy.CheckSeconds
	s["quality_seconds"] = g.policy.QualitySeconds
	s["budget_bytes"] = g.policy.DailyBytes
	s["cooldown_seconds"] = g.policy.CooldownSeconds
	s["probe_bytes"] = g.policy.ProbeBytes
	if g.initError != "" {
		s["message"] = g.initError
	}
	if g.state.Day != g.day() {
		s["spent_bytes"] = int64(0)
	}
	return s
}
func (g *routingGuard) saveLocked() error {
	b, e := json.MarshalIndent(g.state, "", "  ")
	if e != nil {
		return e
	}
	return writeFileAtomic(g.statePath, append(b, '\n'), 0600)
}
func (g *routingGuard) event(name, msg string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state.Events = append(g.state.Events, routingEvent{Time: time.Now(), Group: name, Message: msg})
	if len(g.state.Events) > 60 {
		g.state.Events = g.state.Events[len(g.state.Events)-60:]
	}
	g.app.audit.add("info", "smart-routing", msg)
}
func (g *routingGuard) reserveProbe() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.budgetError {
		return errors.New("测速预算不可用")
	}
	day := g.day()
	if g.state.Day != day {
		g.state.Day = day
		g.state.Spent = 0
	}
	if g.state.Spent+g.policy.ProbeBytes > g.policy.DailyBytes {
		return errors.New("今日测速预算已用完，继续使用轻量探测和内核故障回退")
	}
	// Reserve before any network request. Failed/partial probes remain charged:
	// restart or power loss cannot silently reset the daily cap.
	old := g.state.Spent
	g.state.Spent += g.policy.ProbeBytes
	if e := g.saveLocked(); e != nil {
		g.state.Spent = old
		g.budgetError = true
		return errors.New("预算无法持久化，停止带宽测速")
	}
	return nil
}
func (g *routingGuard) setEnabled(enabled bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	old := g.state.Enabled
	g.state.Enabled = enabled
	if err := g.saveLocked(); err != nil {
		g.state.Enabled = old
		return err
	}
	return nil
}
func (g *routingGuard) enabled() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.state.Enabled }
func (g *routingGuard) pools() map[string][]string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p := map[string][]string{}
	for _, x := range g.policy.Groups {
		p[x.Name] = append([]string{}, g.state.Groups[x.Name].Nodes...)
	}
	return p
}
func routingActual(snapshot routingProxySnapshot, name string) string {
	for i := 0; i < 8; i++ {
		p, ok := snapshot.Proxies[name]
		if !ok && i == 0 {
			return ""
		}
		if !ok || len(p.All) == 0 {
			return name
		}
		if p.Now == "" || p.Now == name {
			return ""
		}
		name = p.Now
	}
	return ""
}
func (g *routingGuard) eligible(nodes []routingProxy) []routingProxy {
	filter := regexp.MustCompile(g.policy.CandidateFilter)
	out := []routingProxy{}
	for _, n := range nodes {
		if n.Type != "Vless" && n.Type != "Hysteria2" {
			continue
		}
		if !filter.MatchString(n.Name) || strings.Contains(n.Name, "避免晚高峰") || strings.Contains(n.Name, "合适下载") {
			continue
		}
		out = append(out, n)
	}
	delay := func(n routingProxy) int {
		if !n.Alive {
			return 100000
		}
		if len(n.History) == 0 {
			return 10000
		}
		v := n.History[len(n.History)-1].Delay
		if v <= 0 {
			return 100000
		}
		return v
	}
	sort.SliceStable(out, func(i, j int) bool { return delay(out[i]) < delay(out[j]) })
	return out
}
func (g *routingGuard) measure(ctx context.Context, p routingGroupPolicy, names []string) []routingMeasurement {
	out := make([]routingMeasurement, len(names))
	var wg sync.WaitGroup
	sema := make(chan struct{}, 3)
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			select {
			case sema <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sema }()
			values := []int{}
			for k := 0; k < 2; k++ {
				var d struct {
					Delay int `json:"delay"`
				}
				q := url.Values{"url": {p.Target}, "timeout": {"3500"}, "expected": {fmt.Sprint(p.Expected)}}
				c, cancel := context.WithTimeout(ctx, 4500*time.Millisecond)
				err := g.controller(c, "GET", "/providers/proxies/"+url.PathEscape(g.policy.Provider)+"/"+url.PathEscape(name)+"/healthcheck?"+q.Encode(), nil, &d)
				cancel()
				if err == nil && d.Delay > 0 {
					values = append(values, d.Delay)
				}
			}
			m := routingMeasurement{Name: name}
			if len(values) == 2 {
				sort.Ints(values)
				m.Good = true
				m.Delay = (values[0]+values[1])/2 + (values[1]-values[0])/2
			}
			out[i] = m
		}(i, name)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Good != out[j].Good {
			return out[i].Good
		}
		return out[i].Delay < out[j].Delay
	})
	return out
}
func uniqueNames(names []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
func routingCountry(n string) string {
	for _, c := range []string{"美国", "日本", "新加坡", "香港", "台湾", "韩国"} {
		if strings.Contains(n, c) {
			return c
		}
	}
	return ""
}
func routingShouldPromote(s *routingGroupState, current, best routingMeasurement, now time.Time, cooldown time.Duration) bool {
	if !best.Good {
		return false
	}
	if !current.Good {
		return s.Failures >= 2
	} // hard failure may bypass cooldown
	if best.Name == current.Name || now.Sub(s.LastSwitch) < cooldown || float64(best.Delay) > float64(current.Delay)*.7 {
		s.Challenger = ""
		s.Streak = 0
		return false
	}
	if s.Challenger == best.Name {
		s.Streak++
	} else {
		s.Challenger = best.Name
		s.Streak = 1
	}
	return s.Streak >= 2
}
func (g *routingGuard) run(parent context.Context, force bool) {
	if !g.configured || !g.runMu.TryLock() {
		return
	}
	defer g.runMu.Unlock()
	g.mu.Lock()
	if !g.state.Enabled && !force {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.running = false; g.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	fail := func(msg string) { g.mu.Lock(); g.state.Message = msg; g.state.LastCheck = time.Now(); g.mu.Unlock() }
	if b, e := os.ReadFile(g.app.cfg.ModeFile); e == nil && strings.Contains(string(b), "leigod") {
		fail("雷神模式下暂停质量探测")
		return
	}
	var source struct {
		Proxies []routingProxy `json:"proxies"`
	}
	if err := g.controller(ctx, "GET", "/providers/proxies/"+url.PathEscape(g.policy.Provider), nil, &source); err != nil {
		fail(err.Error())
		return
	}
	nodes := g.eligible(source.Proxies)
	if len(nodes) == 0 {
		fail("没有符合电信筛选条件的候选，保留现有配置")
		return
	}
	// The source already has a periodic subscription refresh. Also recheck on a
	// six-hour schedule; a failed refresh never clears the current pool.
	if !g.lastRefresh.IsZero() && time.Since(g.lastRefresh) > 6*time.Hour {
		if err := g.controller(ctx, "PUT", "/providers/proxies/"+url.PathEscape(g.policy.Provider), nil, nil); err == nil {
			g.lastRefresh = time.Now()
		}
	} else if g.lastRefresh.IsZero() {
		g.lastRefresh = time.Now()
	}
	var snap routingProxySnapshot
	if err := g.controller(ctx, "GET", "/proxies", nil, &snap); err != nil {
		fail(err.Error())
		return
	}
	var connections mihomoConnections
	for _, p := range g.policy.Groups {
		if x, ok := snap.Proxies[p.Name]; ok && x.Fixed != "" && g.enabled() {
			if err := g.controller(ctx, "DELETE", "/proxies/"+url.PathEscape(p.Name), nil, nil); err != nil {
				fail("无法恢复内核自动选路")
				return
			}
			g.event(p.Name, "已解除 "+p.Label+" 的手动固定，恢复内核自动回退")
		}
	}
	_ = g.controller(ctx, "GET", "/connections", nil, &connections)
	active := map[string]bool{}
	trafficBytes := map[string]int64{}
	for _, c := range connections.Connections {
		old, seen := g.traffic[c.ID]
		if (!seen && c.Download > 4096) || (seen && c.Download-old > 4096) {
			for _, p := range g.policy.Groups {
				for _, chain := range c.Chains {
					if chain == p.Name {
						active[p.Name] = true
						if seen && c.Download > old {
							trafficBytes[p.Name] += c.Download - old
						}
					}
				}
			}
		}
	}
	g.traffic = map[string]int64{}
	for _, c := range connections.Connections {
		g.traffic[c.ID] = c.Download
	}
	trafficSeconds := time.Since(g.trafficAt).Seconds()
	firstTraffic := g.trafficAt.IsZero()
	g.trafficAt = time.Now()
	pools := g.pools()
	allNames := map[string]bool{}
	for _, n := range nodes {
		allNames[n.Name] = true
	}
	measurements := map[string][]routingMeasurement{}
	for _, p := range g.policy.Groups {
		current := routingActual(snap, p.Name)
		names := append([]string{current}, pools[p.Name]...)
		limit := 1
		if force || !g.initialized {
			limit = 6
		}
		for i := 0; i < limit && i < len(nodes); i++ {
			index := i
			if limit == 1 {
				index = g.turn % len(nodes)
			}
			names = append(names, nodes[index].Name)
		}
		names = uniqueNames(names)
		filtered := []string{}
		for _, n := range names {
			if allNames[n] {
				filtered = append(filtered, n)
			}
		}
		results := g.measure(ctx, p, filtered)
		measurements[p.Name] = results
		good := []string{}
		cur := routingMeasurement{Name: current}
		for _, m := range results {
			if m.Name == current {
				cur = m
			}
			if m.Good {
				good = append(good, m.Name)
			}
		}
		g.mu.Lock()
		s := g.state.Groups[p.Name]
		s.Current = current
		s.Active = active[p.Name]
		if !firstTraffic && trafficSeconds > 20 && active[p.Name] && trafficBytes[p.Name] > 4096 && float64(trafficBytes[p.Name])*8/trafficSeconds/1e6 < 4 {
			s.WeakWindows++
		} else {
			s.WeakWindows = 0
		}
		s.DelayMS = cur.Delay
		if cur.Good {
			s.Failures = 0
		} else {
			s.Failures++
		}
		next := []string{}
		if p.Name == "YOUTUBE-2K" {
			verified := []string{}
			// Unknown video nodes need a payload measurement before they enter the pool.
			for _, n := range good {
				if v, ok := g.state.Samples[n]; ok && time.Since(v.At) < time.Hour && v.Mbps > 0 {
					verified = append(verified, n)
				}
			}
			next = routingStablePool(pools[p.Name], "", allNames, verified)
		} else {
			best := routingMeasurement{}
			for _, m := range results {
				if !m.Good {
					continue
				}
				if p.Name == "X-STABLE" && cur.Good && routingCountry(current) != routingCountry(m.Name) {
					continue
				}
				best = m
				break
			}
			promoted := ""
			if routingShouldPromote(s, cur, best, time.Now(), time.Duration(g.policy.CooldownSeconds)*time.Second) {
				promoted = best.Name
				s.Reason = "两轮检测确认线路更好"
			}
			next = routingStablePool(pools[p.Name], promoted, allNames, good)
		}
		next = uniqueNames(next)
		if len(next) > 3 {
			next = next[:3]
		}
		if len(next) > 0 {
			pools[p.Name] = next
		}
		g.mu.Unlock()
	}
	g.turn++
	// Payload tests run only with real recent traffic (or an explicit check), and
	// are shared between services. Idle connection presence is not slow bandwidth.
	qualityDue := force || g.lastQuality.IsZero() || time.Since(g.lastQuality) >= time.Duration(g.policy.QualitySeconds)*time.Second
	g.mu.RLock()
	for _, s := range g.state.Groups {
		if (s.Failures >= 2 || s.WeakWindows >= 2) && time.Since(g.lastQuality) >= 5*time.Minute {
			qualityDue = true
		}
	}
	g.mu.RUnlock()
	if qualityDue {
		qualityGroups := append([]routingGroupPolicy{}, g.policy.Groups...)
		g.mu.RLock()
		sort.SliceStable(qualityGroups, func(i, j int) bool {
			a, b := g.state.Groups[qualityGroups[i].Name], g.state.Groups[qualityGroups[j].Name]
			urgentA, urgentB := a.Failures >= 2 || a.WeakWindows >= 2, b.Failures >= 2 || b.WeakWindows >= 2
			if urgentA != urgentB {
				return urgentA
			}
			return g.state.Samples[a.Current].At.Before(g.state.Samples[b.Current].At)
		})
		g.mu.RUnlock()
		for _, p := range qualityGroups {
			if !force && !active[p.Name] {
				continue
			}
			candidates := []string{}
			g.mu.RLock()
			current := g.state.Groups[p.Name].Current
			g.mu.RUnlock()
			if current != "" && allNames[current] {
				candidates = append(candidates, current)
			}
			for _, m := range measurements[p.Name] {
				if m.Good {
					candidates = append(candidates, m.Name)
				}
			}
			candidates = uniqueNames(candidates)
			if len(candidates) > 3 {
				candidates = candidates[:3]
			}
			if len(candidates) > 0 {
				g.quality(ctx, candidates)
				g.lastQuality = time.Now()
			}
			break // at most one bounded batch per cycle, not four parallel speed tests
		}
	}
	// Reorder video only when BOTH paired download rounds beat current by 30%.
	g.mu.Lock()
	for _, p := range g.policy.Groups {
		s := g.state.Groups[p.Name]
		sample := g.state.Samples[s.Current]
		s.Mbps = sample.Mbps
		s.SampledAt = sample.At
		if p.Name != "YOUTUBE-2K" {
			continue
		}
		old := sample
		if time.Since(s.LastSwitch) < time.Duration(g.policy.CooldownSeconds)*time.Second {
			continue
		}
		best := s.Current
		bestSample := old
		if s.Failures >= 2 {
			bestSample.Mbps = 0
		}
		for n, v := range g.state.Samples {
			if !allNames[n] || time.Since(v.At) > 45*time.Minute || len(v.Rounds) != 2 || v.Mbps <= 0 {
				continue
			}
			// Only candidates validated against the YouTube target can be promoted.
			healthy := false
			for _, m := range measurements[p.Name] {
				if m.Name == n && m.Good {
					healthy = true
				}
			}
			if !healthy {
				continue
			}
			wins := len(old.Rounds) == 2 && v.Rounds[0] > old.Rounds[0]*1.3 && v.Rounds[1] > old.Rounds[1]*1.3
			if (wins || s.Failures >= 2) && v.Mbps > bestSample.Mbps {
				best = n
				bestSample = v
			}
		}
		if best != "" && best != s.Current {
			pools[p.Name] = uniqueNames(append([]string{best}, pools[p.Name]...))
			if len(pools[p.Name]) > 3 {
				pools[p.Name] = pools[p.Name][:3]
			}
			s.Reason = "两轮下载均优于当前线路30%以上"
			if s.Failures >= 2 {
				s.Reason = "当前线路连续异常，选择已验证候选"
			}
		}
		clean := []string{}
		for _, n := range pools[p.Name] {
			if allNames[n] {
				clean = append(clean, n)
			}
		}
		if len(clean) > 0 {
			pools[p.Name] = clean
		}
	}
	g.mu.Unlock()
	if g.enabled() {
		if err := g.applyPools(ctx, pools); err != nil {
			fail(err.Error())
			return
		}
		changed := false
		reconnect := map[string]string{}
		g.mu.Lock()
		for _, p := range g.policy.Groups {
			s := g.state.Groups[p.Name]
			if !sameStrings(s.Nodes, pools[p.Name]) {
				old := append([]string{}, s.Nodes...)
				s.Nodes = append([]string{}, pools[p.Name]...)
				changed = true
				if len(old) > 0 && old[0] != s.Nodes[0] {
					s.LastSwitch = time.Now()
					if s.Failures >= 2 || p.Name == "YOUTUBE-2K" {
						reconnect[p.Name] = s.Current
					}
					g.state.Events = append(g.state.Events, routingEvent{Time: time.Now(), Group: p.Name, Message: s.Label + " 候选首选改为 " + s.Nodes[0]})
				}
			}
		}
		if len(g.state.Events) > 60 {
			g.state.Events = g.state.Events[len(g.state.Events)-60:]
		}
		if changed {
			if e := g.saveLocked(); e != nil {
				g.state.Message = "候选已更新，但状态持久化失败"
			}
		}
		g.mu.Unlock()
		for group, oldNode := range reconnect {
			g.reconnectGroup(ctx, group, oldNode)
		}
	}
	var final routingProxySnapshot
	if g.controller(ctx, "GET", "/proxies", nil, &final) == nil {
		g.mu.Lock()
		for _, p := range g.policy.Groups {
			g.state.Groups[p.Name].Current = routingActual(final, p.Name)
			s := g.state.Groups[p.Name]
			if s.Reason == "等待首次检查" || strings.HasPrefix(s.Reason, "正式部署") {
				s.Reason = "内核自动回退就绪"
			}
		}
		g.mu.Unlock()
	}
	g.initialized = true
	g.mu.Lock()
	g.state.LastCheck = time.Now()
	if !g.budgetError && g.state.Spent+2*g.policy.ProbeBytes <= g.policy.DailyBytes {
		g.state.Message = "轻量检测运行中；带宽检测按活跃业务、间隔和每日预算触发"
		if !g.state.Enabled {
			g.state.Message = "手动检测完成；质量调整已暂停，未修改候选池"
		}
	}
	g.mu.Unlock()
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A rotating probe is evidence, not permission to reshuffle a healthy pool.
// Only a confirmed promotion or a disappeared subscription member changes it.
func routingStablePool(previous []string, promoted string, valid map[string]bool, healthy []string) []string {
	out := []string{}
	for _, n := range append(append([]string{promoted}, previous...), healthy...) {
		if n != "" && valid[n] {
			out = append(out, n)
		}
	}
	out = uniqueNames(out)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func (g *routingGuard) reconnectGroup(ctx context.Context, group, oldNode string) {
	if oldNode == "" {
		return
	}
	var cs mihomoConnections
	if g.controller(ctx, "GET", "/connections", nil, &cs) != nil {
		return
	}
	count := 0
	for _, c := range cs.Connections {
		hasGroup, hasNode := false, false
		for _, v := range c.Chains {
			hasGroup = hasGroup || v == group
			hasNode = hasNode || v == oldNode
		}
		if hasGroup && hasNode && count < 100 {
			if g.controller(ctx, "DELETE", "/connections/"+url.PathEscape(c.ID), nil, nil) == nil {
				count++
			}
		}
	}
	if count > 0 {
		g.event(group, fmt.Sprintf("仅重连 %s 旧线路上的 %d 条连接", group, count))
	}
}
func (a *App) handleRoutingGuard(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "只支持 GET")
		return
	}
	writeJSON(w, 200, a.routing.status())
}
func (a *App) handleRoutingGuardAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "只支持 POST")
		return
	}
	var req actionRequest
	if decodeJSON(r, &req, 4096) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	g := a.routing
	if !g.configured {
		writeError(w, 409, "质量守护尚未配置")
		return
	}
	switch req.Action {
	case "disable":
		if e := g.setEnabled(false); e != nil {
			writeError(w, 409, "暂停状态保存失败")
			return
		}
	case "enable", "restore":
		if e := g.setEnabled(true); e != nil {
			writeError(w, 409, "启用状态保存失败")
			return
		}
		go g.run(context.Background(), false)
	case "run":
		if !g.runMu.TryLock() {
			writeError(w, 409, "质量检测正在执行")
			return
		}
		g.runMu.Unlock()
		go g.run(context.Background(), true)
	default:
		writeError(w, 400, "不支持的质量守护操作")
		return
	}
	a.audit.add("info", "smart-routing", "控制台操作: "+req.Action)
	writeJSON(w, 202, map[string]any{"message": "操作已提交，状态会自动刷新", "status": g.status()})
}

func (g *routingGuard) day() string {
	location := g.policy.location
	if location == nil {
		location = time.Local
	}
	return time.Now().In(location).Format("2006-01-02")
}
