package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"go.yaml.in/yaml/v3"
)

type routingPolicy struct {
	Enabled         bool   `json:"enabled"`
	Root            string `json:"shellcrash_root"`
	Provider        string `json:"provider"`
	CandidateFilter string `json:"candidate_filter"`
	CheckSeconds    int    `json:"check_seconds"`
	QualitySeconds  int    `json:"quality_seconds"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	DailyBytes      int64  `json:"daily_bytes"`
	ProbeBytes      int64  `json:"probe_bytes"`
	ProbeURL        string `json:"probe_url"`
	Timezone        string `json:"timezone,omitempty"`
	location        *time.Location
	Groups          []routingGroupPolicy `json:"groups"`
}
type routingGroupPolicy struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Target   string   `json:"target"`
	Expected int      `json:"expected"`
	Nodes    []string `json:"nodes"`
}

func loadRoutingPolicy(path string) (routingPolicy, error) {
	var p routingPolicy
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if len(b) > 65536 {
		return p, errors.New("选路策略文件过大")
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, errors.New("选路策略文件格式错误")
	}
	if p.Timezone == "" {
		p.Timezone = "Asia/Shanghai"
	}
	p.location, err = time.LoadLocation(p.Timezone)
	if err != nil {
		return p, errors.New("选路时区不正确")
	}
	if p.Provider == "" {
		p.Provider = "household"
	}
	if !regexp.MustCompile("^[A-Za-z0-9_]+$").MatchString(p.Provider) {
		return p, errors.New("订阅名称不正确")
	}
	if p.CheckSeconds == 0 {
		p.CheckSeconds = 60
	}
	if p.QualitySeconds == 0 {
		p.QualitySeconds = 1800
	}
	if p.CooldownSeconds == 0 {
		p.CooldownSeconds = 300
	}
	if p.DailyBytes == 0 {
		p.DailyBytes = 256 << 20
	}
	if p.ProbeBytes == 0 {
		p.ProbeBytes = 8 << 20
	}
	if p.ProbeURL == "" {
		p.ProbeURL = "https://dl.google.com/android/repository/platform-tools-latest-linux.zip"
	}
	if !filepath.IsAbs(p.Root) || strings.ContainsAny(p.Root, "\r\n") || p.Root == "/" {
		return p, errors.New("ShellCrash 路径不正确")
	}
	if p.CheckSeconds < 30 || p.QualitySeconds < 300 || p.CooldownSeconds < 300 || p.DailyBytes < p.ProbeBytes || p.DailyBytes > 1<<30 || p.ProbeBytes < 1<<20 || p.ProbeBytes > 16<<20 {
		return p, errors.New("检测周期或测速预算超出范围")
	}
	if _, err = regexp.Compile(p.CandidateFilter); err != nil || p.CandidateFilter == "" {
		return p, errors.New("候选过滤表达式不正确")
	}
	allowed := map[string]bool{"YOUTUBE-2K": true, "X-STABLE": true, "GOOGLE-AUTO": true, "AUTO": true}
	seen := map[string]bool{}
	if len(p.Groups) == 0 || len(p.Groups) > 4 {
		return p, errors.New("选路分组数量不正确")
	}
	for _, g := range p.Groups {
		if !allowed[g.Name] || seen[g.Name] || len(g.Nodes) < 1 || len(g.Nodes) > 3 {
			return p, errors.New("分组或初始候选不正确")
		}
		seen[g.Name] = true
		if !secureProbeURL(g.Target) || g.Expected < 200 || g.Expected > 399 {
			return p, errors.New("检测地址不正确")
		}
		for _, n := range g.Nodes {
			if strings.TrimSpace(n) == "" || strings.ContainsAny(n, "\r\n") {
				return p, errors.New("候选名称不正确")
			}
		}
	}
	if !secureProbeURL(p.ProbeURL) {
		return p, errors.New("带宽检测地址不正确")
	}
	return p, nil
}
func secureProbeURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
func routingChild(name string, i int) string {
	return fmt.Sprintf("__SMART_%s_%d", strings.ReplaceAll(name, "-", "_"), i+1)
}
func yamlField(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func yamlSet(n *yaml.Node, key string, v any) {
	var x yaml.Node
	_ = x.Encode(v)
	if old := yamlField(n, key); old != nil {
		*old = x
		return
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &x)
}
func routingRules() []string {
	return []string{
		"DOMAIN-SUFFIX,gemini.google.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,aistudio.google.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,makersuite.google.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,generativelanguage.googleapis.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,aiplatform.googleapis.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,cloudaicompanion.googleapis.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,notebooklm.google.com,AI-US-STABLE",
		"DOMAIN-SUFFIX,notebooklm.google,AI-US-STABLE",
		"DOMAIN-SUFFIX,ai.google.dev,AI-US-STABLE",
		"DOMAIN-SUFFIX,x.com,X-STABLE", "DOMAIN-SUFFIX,twitter.com,X-STABLE",
		"DOMAIN-SUFFIX,twimg.com,X-STABLE", "DOMAIN-SUFFIX,t.co,X-STABLE",
		"DOMAIN-SUFFIX,pscp.tv,X-STABLE", "DOMAIN-SUFFIX,periscope.tv,X-STABLE",
		"DOMAIN-SUFFIX,google.com,GOOGLE-AUTO", "DOMAIN-SUFFIX,google.com.hk,GOOGLE-AUTO",
		"DOMAIN-SUFFIX,google.co.jp,GOOGLE-AUTO", "DOMAIN-SUFFIX,google.co.uk,GOOGLE-AUTO",
		"DOMAIN-SUFFIX,google.com.sg,GOOGLE-AUTO", "DOMAIN-SUFFIX,gstatic.com,GOOGLE-AUTO",
		"DOMAIN-SUFFIX,googleapis.com,GOOGLE-AUTO", "DOMAIN-SUFFIX,googleusercontent.com,GOOGLE-AUTO",
	}
}

// Hidden single-node selectors give fallback a deterministic, ranked list without
// downloading twelve copies of the subscription or pinning an automatic group.
func renderRoutingConfig(original []byte, p routingPolicy, pools map[string][]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(original, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("配置不是映射")
	}
	root := doc.Content[0]
	groups := yamlField(root, "proxy-groups")
	providers := yamlField(root, "proxy-providers")
	if groups == nil || groups.Kind != yaml.SequenceNode || providers == nil || yamlField(providers, p.Provider) == nil {
		return nil, errors.New("配置缺少分组或订阅")
	}
	ai := false
	for _, g := range groups.Content {
		n := yamlField(g, "name")
		if n != nil && n.Value == "AI-US-STABLE" {
			ai = true
		}
	}
	if !ai {
		return nil, errors.New("AI 美国分组缺失，拒绝写入")
	}
	managed := map[string]routingGroupPolicy{}
	for _, g := range p.Groups {
		managed[g.Name] = g
	}
	keep := []*yaml.Node{}
	for _, g := range groups.Content {
		n := yamlField(g, "name")
		if n == nil {
			return nil, errors.New("分组缺少名称")
		}
		if _, ok := managed[n.Value]; ok || strings.HasPrefix(n.Value, "__SMART_") {
			continue
		}
		keep = append(keep, g)
	}
	groups.Content = keep
	for _, g := range p.Groups {
		nodes := pools[g.Name]
		if len(nodes) == 0 || len(nodes) > 3 {
			return nil, errors.New("拒绝空候选池")
		}
		children := []string{}
		for i, name := range nodes {
			child := routingChild(g.Name, i)
			children = append(children, child)
			var n yaml.Node
			_ = n.Encode(map[string]any{"name": child, "type": "select", "use": []string{p.Provider}, "filter": "^" + regexp.QuoteMeta(name) + "$", "hidden": true, "empty-fallback": "REJECT"})
			groups.Content = append(groups.Content, &n)
		}
		var n yaml.Node
		_ = n.Encode(map[string]any{"name": g.Name, "type": "fallback", "proxies": children, "url": g.Target, "expected-status": g.Expected, "interval": p.CheckSeconds, "timeout": 4000, "max-failed-times": 2, "lazy": false, "empty-fallback": "REJECT"})
		groups.Content = append(groups.Content, &n)
	}
	rules := yamlField(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return nil, errors.New("配置缺少规则")
	}
	inserts := routingRules()
	known := map[string]bool{}
	for _, x := range inserts {
		known[x] = true
	}
	filtered := []*yaml.Node{}
	for _, n := range rules.Content {
		if !known[n.Value] {
			filtered = append(filtered, n)
		}
	}
	pos := len(filtered)
	for i, n := range filtered {
		if strings.HasPrefix(n.Value, "RULE-SET,") || strings.HasPrefix(n.Value, "GEOIP,") || strings.HasPrefix(n.Value, "GEOSITE,") || strings.HasPrefix(n.Value, "MATCH,") {
			pos = i
			break
		}
	}
	added := make([]*yaml.Node, 0, len(inserts))
	for _, s := range inserts {
		added = append(added, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
	}
	content := append([]*yaml.Node{}, filtered[:pos]...)
	content = append(content, added...)
	content = append(content, filtered[pos:]...)
	rules.Content = content
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return out.Bytes(), nil
}
func (g *routingGuard) applyPools(ctx context.Context, pools map[string][]string) error {
	if !g.enabled() || ctx.Err() != nil {
		return errors.New("自动选路已暂停")
	}
	if !g.app.actionMu.TryLock() {
		return errors.New("代理模式正在切换，延后选路更新")
	}
	defer g.app.actionMu.Unlock()
	lock, err := os.OpenFile("/tmp/router-ai-node-guard.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("AI 守护正在更新配置，稍后重试")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	paths := []string{filepath.Join(g.policy.Root, "yamls/config.yaml"), filepath.Join(g.policy.Root, "yamls/config.yaml.template")}
	originals := make([][]byte, 2)
	next := make([][]byte, 2)
	changed := false
	for i, p := range paths {
		originals[i], err = os.ReadFile(p)
		if err != nil {
			return err
		}
		next[i], err = renderRoutingConfig(originals[i], g.policy, pools)
		if err != nil {
			return err
		}
		changed = changed || !bytes.Equal(next[i], originals[i])
	}
	if !changed {
		return g.verifyPools(ctx, pools)
	}
	candidates := make([]string, 2)
	defer func() {
		for _, p := range candidates {
			if p != "" {
				_ = os.Remove(p)
			}
		}
	}()
	for i, p := range paths {
		f, e := os.CreateTemp(filepath.Dir(p), ".smart-routing-*.yaml")
		if e != nil {
			return e
		}
		candidates[i] = f.Name()
		_ = f.Close()
		if err = writeFileAtomic(f.Name(), next[i], 0600); err != nil {
			return err
		}
		c, cancel := context.WithTimeout(ctx, 25*time.Second)
		err = exec.CommandContext(c, "/tmp/ShellCrash/CrashCore", "-t", "-d", g.policy.Root, "-f", f.Name()).Run()
		cancel()
		if err != nil {
			return errors.New("Mihomo 配置校验失败，未应用候选")
		}
	}
	for i, p := range paths {
		current, e := os.ReadFile(p)
		if !g.enabled() || ctx.Err() != nil {
			return errors.New("自动选路已暂停，未替换配置")
		}
		if e != nil || !bytes.Equal(current, originals[i]) {
			return errors.New("配置被其他任务修改，拒绝覆盖")
		}
	}
	backup := filepath.Join(g.policy.Root, "backups", "smart-routing-"+time.Now().Format("20060102-150405.000"))
	if err = os.MkdirAll(backup, 0700); err != nil {
		return err
	}
	for i, p := range paths {
		if err = writeFileAtomic(filepath.Join(backup, filepath.Base(p)), originals[i], 0600); err != nil {
			return err
		}
	}
	rollback := func(cause error) error {
		for i, p := range paths {
			if e := writeFileAtomic(p, originals[i], 0600); e != nil {
				return fmt.Errorf("%w；配置回滚失败", cause)
			}
		}
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if e := g.controller(c, "PUT", "/configs?force=true", map[string]string{"path": paths[0]}, nil); e != nil {
			return fmt.Errorf("%w；热加载回滚失败", cause)
		}
		return fmt.Errorf("%w；已回滚", cause)
	}
	for i, p := range paths {
		if err = os.Rename(candidates[i], p); err != nil {
			return rollback(err)
		}
	}
	if err = g.controller(ctx, "PUT", "/configs?force=true", map[string]string{"path": paths[0]}, nil); err != nil {
		return rollback(errors.New("热加载失败"))
	}
	for _, group := range g.policy.Groups {
		if err = g.controller(ctx, "DELETE", "/proxies/"+url.PathEscape(group.Name), nil, nil); err != nil {
			return rollback(errors.New("解除手动固定失败"))
		}
	}
	if err = g.verifyPools(ctx, pools); err != nil {
		return rollback(err)
	}
	g.mu.Lock()
	g.state.Backup = backup
	g.mu.Unlock()
	pruneRoutingBackups(filepath.Dir(backup))
	return nil
}

func pruneRoutingBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := []string{}
	pattern := regexp.MustCompile("^smart-routing-[0-9]{8}-[0-9]{6}[.][0-9]{3}$")
	for _, e := range entries {
		if e.IsDir() && pattern.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 3 {
		p := filepath.Join(dir, names[0])
		_ = os.Remove(filepath.Join(p, "config.yaml"))
		_ = os.Remove(filepath.Join(p, "config.yaml.template"))
		_ = os.Remove(p)
		names = names[1:]
	}
}
func (g *routingGuard) verifyPools(ctx context.Context, pools map[string][]string) error {
	var s routingProxySnapshot
	if err := g.controller(ctx, "GET", "/proxies", nil, &s); err != nil {
		return err
	}
	for name, nodes := range pools {
		outer, ok := s.Proxies[name]
		if !ok || outer.Type != "Fallback" || outer.Fixed != "" || len(outer.All) != len(nodes) {
			return fmt.Errorf("%s 自动回退状态不正确", name)
		}
		for i, node := range nodes {
			child := routingChild(name, i)
			v, ok := s.Proxies[child]
			if !ok || v.Now != node || !reflect.DeepEqual(v.All, []string{node}) {
				return fmt.Errorf("%s 候选不匹配，拒绝直连回退", name)
			}
		}
	}
	return nil
}
