package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const meshConfigFixture = "config xiaoqiang 'common'\n\toption NETMODE 'whc_cap'\n\toption MESH_VERSION '4'\n"

const meshNodesFixture = `{"backhauls":"8","backhauls_qa":"8","locale":"节点甲","initted":"1","return":"success","ip":"192.0.2.2"}
`

func writeMeshConfigFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "xiaoqiang")
	if err := os.WriteFile(path, []byte(meshConfigFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMeshNodesFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(meshNodesFixture), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 7.1.1 启动探测为空时，Resolve 返回空字符串。
func TestMeshResolverEmptyWhenNothingDiscovered(t *testing.T) {
	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string { return "" }

	if got := resolver.Resolve(); got != "" {
		t.Fatalf("expected empty path, got %q", got)
	}
}

// 7.1.2 缓存文件稍后生成时，下一次 Resolve 应返回新路径。
func TestMeshResolverPicksUpLateFile(t *testing.T) {
	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "xq_whc_quire")

	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		if regularReadableFile(nodesPath) {
			return nodesPath
		}
		return ""
	}

	if got := resolver.Resolve(); got != "" {
		t.Fatalf("expected empty before file exists, got %q", got)
	}

	writeMeshNodesFile(t, nodesPath)

	if got := resolver.Resolve(); got != nodesPath {
		t.Fatalf("expected %q after file appears, got %q", nodesPath, got)
	}
}

// 7.1.3 显式配置的路径即使当前不存在，也必须原样返回。
func TestMeshResolverKeepsOverrideEvenIfMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-yet-created")
	resolver := newMeshNodesResolver(missing, "")
	resolver.discover = func(string) string { return "" }

	if got := resolver.Resolve(); got != missing {
		t.Fatalf("override should be returned as-is, got %q", got)
	}

	// 文件生成后仍返回同一路径，无需重启。
	writeMeshNodesFile(t, missing)
	if got := resolver.Resolve(); got != missing {
		t.Fatalf("override should stay stable, got %q", got)
	}
}

// 7.1.4 显式配置优先于自动发现结果。
func TestMeshResolverOverrideWinsOverDiscovery(t *testing.T) {
	dir := t.TempDir()
	discovered := filepath.Join(dir, "discovered")
	writeMeshNodesFile(t, discovered)

	override := filepath.Join(dir, "override")
	resolver := newMeshNodesResolver(override, discovered)
	resolver.discover = func(string) string { return discovered }

	if got := resolver.Resolve(); got != override {
		t.Fatalf("override must win, got %q", got)
	}
}

// 7.1.5 自动发现的路径失效后，应重新探测新的候选。
func TestMeshResolverRediscoversAfterInvalidation(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")

	writeMeshNodesFile(t, first)

	calls := 0
	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		calls++
		if regularReadableFile(second) {
			return second
		}
		if regularReadableFile(first) {
			return first
		}
		return ""
	}

	if got := resolver.Resolve(); got != first {
		t.Fatalf("expected initial discovery %q, got %q", first, got)
	}
	before := calls

	// 旧路径失效（例如固件重建缓存后换了位置）。
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	writeMeshNodesFile(t, second)

	if got := resolver.Resolve(); got != second {
		t.Fatalf("expected rediscovery %q, got %q", second, got)
	}
	if calls <= before {
		t.Fatalf("expected an extra discovery call after invalidation, calls=%d", calls)
	}
}

// 7.1.6 并发调用不得产生数据竞争（配合 go test -race）。
func TestMeshResolverConcurrentResolve(t *testing.T) {
	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "nodes")
	writeMeshNodesFile(t, nodesPath)

	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		if regularReadableFile(nodesPath) {
			return nodesPath
		}
		return ""
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if got := resolver.Resolve(); got != nodesPath {
					t.Errorf("unexpected path %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// 7.2 集成：控制台先启动、缓存文件后生成，采集器无需重建即可恢复。
func TestMeshCollectorRecoversWhenCacheAppearsLater(t *testing.T) {
	dir := t.TempDir()
	configPath := writeMeshConfigFile(t, dir)
	nodesPath := filepath.Join(dir, "xq_whc_quire")

	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		if regularReadableFile(nodesPath) {
			return nodesPath
		}
		return ""
	}

	// 采集器只构造一次，之后不再重建。
	collector := &meshCollector{configPath: configPath, resolver: resolver}

	first, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("first collect should still report enabled Mesh: %v", err)
	}
	firstStats := first.(MeshStats)
	if firstStats.NodeCount != 1 || len(firstStats.Nodes) != 0 {
		t.Fatalf("expected only the CAP node before cache exists: %+v", firstStats)
	}
	if firstStats.UpdatedAtUnix != 0 {
		t.Fatalf("expected unknown cache timestamp, got %d", firstStats.UpdatedAtUnix)
	}

	// 固件稍后生成缓存文件。
	writeMeshNodesFile(t, nodesPath)

	second, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondStats := second.(MeshStats)
	if secondStats.NodeCount != 2 || len(secondStats.Nodes) != 1 {
		t.Fatalf("expected the child node after cache appears: %+v", secondStats)
	}
	if !secondStats.Nodes[0].Online {
		t.Fatalf("fresh cache should mark the node online: %+v", secondStats.Nodes[0])
	}
	if secondStats.UpdatedAtUnix == 0 {
		t.Fatalf("expected a real cache timestamp after file appears")
	}
}

// 7.2 缓存文件被删除后，节点不得继续显示为在线。
func TestMeshCollectorDropsNodesWhenCacheRemoved(t *testing.T) {
	dir := t.TempDir()
	configPath := writeMeshConfigFile(t, dir)
	nodesPath := filepath.Join(dir, "xq_whc_quire")
	writeMeshNodesFile(t, nodesPath)

	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		if regularReadableFile(nodesPath) {
			return nodesPath
		}
		return ""
	}
	collector := &meshCollector{configPath: configPath, resolver: resolver}

	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats := value.(MeshStats); len(stats.Nodes) != 1 || !stats.Nodes[0].Online {
		t.Fatalf("expected one online node: %+v", stats)
	}

	if err := os.Remove(nodesPath); err != nil {
		t.Fatal(err)
	}

	value, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(MeshStats)
	if len(stats.Nodes) != 0 {
		t.Fatalf("removed cache must not keep reporting nodes: %+v", stats.Nodes)
	}
	if stats.NodeCount != 1 {
		t.Fatalf("expected only the CAP node, got %d", stats.NodeCount)
	}
}

// 7.2 过期缓存不得把节点标记为在线。
func TestMeshCollectorStaleCacheKeepsNodesOffline(t *testing.T) {
	dir := t.TempDir()
	configPath := writeMeshConfigFile(t, dir)
	nodesPath := filepath.Join(dir, "xq_whc_quire")
	writeMeshNodesFile(t, nodesPath)

	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(nodesPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	resolver := newMeshNodesResolver("", nodesPath)
	collector := &meshCollector{configPath: configPath, resolver: resolver}

	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(MeshStats)
	if stats.Fresh {
		t.Fatalf("stale cache must not be fresh: %+v", stats)
	}
	if len(stats.Nodes) != 1 || stats.Nodes[0].Online {
		t.Fatalf("stale cache must not report online nodes: %+v", stats.Nodes)
	}
}

// 7.2 顶层 system-metrics 与扩展采集器必须使用同一解析结果。
func TestMeshResolverSharedBetweenTopLevelAndCollector(t *testing.T) {
	dir := t.TempDir()
	configPath := writeMeshConfigFile(t, dir)
	nodesPath := filepath.Join(dir, "xq_whc_quire")

	resolver := newMeshNodesResolver("", "")
	resolver.discover = func(string) string {
		if regularReadableFile(nodesPath) {
			return nodesPath
		}
		return ""
	}

	cfg := Config{MeshConfigPath: configPath, MeshNodesPath: ""}
	// 采集器由构造函数启动且无停止接口（与既有测试一致）；
	// 本用例只关心解析器是否被顶层与扩展采集器共享。
	app := newAppWithMeshResolver(cfg, "", DeviceCapabilities{}, resolver)
	if app.meshNodes != resolver {
		t.Fatalf("app should hold the shared resolver")
	}

	writeMeshNodesFile(t, nodesPath)

	topLevel := readMeshStats(cfg.MeshConfigPath, resolver.Resolve(), time.Now())

	collector := &meshCollector{configPath: configPath, resolver: resolver}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	extended := value.(MeshStats)

	if topLevel.NodeCount != extended.NodeCount {
		t.Fatalf("top-level and extended Mesh disagree: %d vs %d", topLevel.NodeCount, extended.NodeCount)
	}
	if topLevel.NodeCount != 2 {
		t.Fatalf("expected 2 nodes, got %d", topLevel.NodeCount)
	}
	if strings.TrimSpace(resolver.Resolve()) != nodesPath {
		t.Fatalf("resolver should settle on %q", nodesPath)
	}
}
