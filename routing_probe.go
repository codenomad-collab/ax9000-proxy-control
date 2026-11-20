package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type routingBench struct {
	cmd        *exec.Cmd
	dir        string
	controller *http.Client
	proxy      *url.URL
	cancel     context.CancelFunc
	done       chan error
}

func (g *routingGuard) startBench(parent context.Context) (*routingBench, error) {
	ctx, cancel := context.WithTimeout(parent, 110*time.Second)
	dir, e := os.MkdirTemp("", "router-quality-bench-")
	if e != nil {
		cancel()
		return nil, e
	}
	b := &routingBench{dir: dir, cancel: cancel, done: make(chan error, 1)}
	cleanup := true
	defer func() {
		if cleanup {
			b.close()
		}
	}()
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	sock := filepath.Join(dir, "api.sock")
	// The sandbox reads the existing local provider, never downloads another
	// subscription and never configures a TUN, DNS listener or LAN listener.
	cfg := map[string]any{
		"mixed-port": port, "allow-lan": false, "bind-address": "127.0.0.1",
		"external-controller-unix": sock, "mode": "rule", "log-level": "silent", "ipv6": false,
		"dns": map[string]any{"enable": false}, "profile": map[string]any{"store-selected": false},
		"proxy-providers": map[string]any{"bench": map[string]any{"type": "file", "path": filepath.Join(g.policy.Root, "providers", g.policy.Provider+".yaml"), "filter": g.policy.CandidateFilter}},
		"proxy-groups":    []any{map[string]any{"name": "BENCH", "type": "select", "use": []string{"bench"}, "empty-fallback": "REJECT"}},
		"rules":           []string{"MATCH,BENCH"},
	}
	data, _ := json.Marshal(cfg)
	config := filepath.Join(dir, "config.json")
	if e = os.WriteFile(config, data, 0600); e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, "/tmp/ShellCrash/CrashCore", "-d", dir, "-f", config)
	cmd.Env = append(os.Environ(), "SAFE_PATHS="+g.policy.Root)
	routingChildLifetime(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	b.cmd = cmd
	if e = cmd.Start(); e != nil {
		return nil, errors.New("无法启动隔离测速内核")
	}
	go func() { b.done <- cmd.Wait() }()
	b.controller = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "unix", sock)
	}}}
	b.proxy, _ = url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("隔离测速内核启动超时")
		case <-tick.C:
			if _, e = os.Stat(sock); e == nil {
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://unix/version", nil)
				res, err := b.controller.Do(req)
				if err == nil {
					_ = res.Body.Close()
					if res.StatusCode == 200 {
						cleanup = false
						return b, nil
					}
				}
			}
		}
	}
}
func (b *routingBench) close() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		select {
		case <-b.done:
		case <-time.After(3 * time.Second):
		}
	}
	if b.controller != nil {
		b.controller.CloseIdleConnections()
	}
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}
func (b *routingBench) selectNode(ctx context.Context, name string) error {
	data, _ := json.Marshal(map[string]string{"name": name})
	req, e := http.NewRequestWithContext(ctx, "PUT", "http://unix/proxies/BENCH", bytes.NewReader(data))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := b.controller.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("测速候选选择失败")
	}
	return nil
}

// Status and tiny error bodies are not bandwidth samples. Rate excludes DNS,
// CONNECT and TLS first-byte latency, and includes only the bounded body read.
func measureRoutingBody(ctx context.Context, client *http.Client, target string, limit int64) (float64, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", target, nil)
	if e != nil {
		return 0, e
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")
	res, e := client.Do(req)
	if e != nil {
		return 0, errors.New("下载探测连接失败")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return 0, fmt.Errorf("下载探测 HTTP %d", res.StatusCode)
	}
	if res.ContentLength >= 0 && res.ContentLength < 1<<20 {
		return 0, errors.New("下载内容过小，不能当作测速")
	}
	start := time.Now()
	n, e := io.Copy(io.Discard, io.LimitReader(res.Body, limit))
	elapsed := time.Since(start).Seconds()
	if n < 1<<20 || elapsed < .01 {
		return 0, errors.New("下载样本不足")
	}
	// A timeout after >=1MiB is a slow partial sample, not a false zero.
	if e != nil && ctx.Err() == nil {
		return 0, errors.New("下载流异常")
	}
	return float64(n) * 8 / elapsed / 1e6, nil
}
func (g *routingGuard) quality(ctx context.Context, names []string) {
	// Reserve the complete first pair before starting a child process.
	if len(names) == 0 {
		return
	}
	g.mu.RLock()
	available := g.policy.DailyBytes - g.state.Spent
	day := g.state.Day
	bad := g.budgetError
	g.mu.RUnlock()
	if bad {
		return
	}
	if day != g.day() {
		available = g.policy.DailyBytes
	}
	if available < 2*g.policy.ProbeBytes {
		g.mu.Lock()
		g.state.Message = "今日测速预算不足，轻量检测和故障回退继续"
		g.mu.Unlock()
		return
	}
	bench, e := g.startBench(ctx)
	if max := int(available / (2 * g.policy.ProbeBytes)); len(names) > max {
		names = names[:max]
	}
	if e != nil {
		g.event("", e.Error())
		return
	}
	defer bench.close()
	results := map[string][]float64{}
	// Interleave rounds, so all candidates see a comparable time window.
	for round := 0; round < 2; round++ {
		for _, name := range names {
			if e = g.reserveProbe(); e != nil {
				g.event("", e.Error())
				return
			}
			c, cancel := context.WithTimeout(ctx, 14*time.Second)
			e = bench.selectNode(c, name)
			if e != nil {
				cancel()
				continue
			}
			transport := &http.Transport{Proxy: http.ProxyURL(bench.proxy), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, DisableCompression: true, ResponseHeaderTimeout: 6 * time.Second}
			client := &http.Client{Transport: transport, Timeout: 14 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			speed, err := measureRoutingBody(c, client, g.policy.ProbeURL, g.policy.ProbeBytes)
			cancel()
			transport.CloseIdleConnections()
			if err == nil {
				results[name] = append(results[name], speed)
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for name, values := range results {
		if len(values) != 2 {
			continue
		}
		copyValues := append([]float64{}, values...)
		sort.Float64s(copyValues)
		g.state.Samples[name] = routingSample{Mbps: (copyValues[0] + copyValues[1]) / 2, At: time.Now(), Rounds: values}
	}
	if e = g.saveLocked(); e != nil {
		g.budgetError = true
		g.state.Message = "测速状态保存失败，后续带宽检测停止"
	}
}
