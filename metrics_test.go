package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadCPUCountersAndUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte("cpu  100 20 30 400 50 6 7 8 0 0\ncpu0 1 2 3 4 5 6 7 8 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	counters, ok := readCPUCounters(path)
	if !ok {
		t.Fatal("failed to parse aggregate CPU counters")
	}
	if counters.Total != 621 || counters.Idle != 450 {
		t.Fatalf("unexpected counters: %+v", counters)
	}

	usage, ready := calculateCPUUsage(cpuCounters{Total: 500, Idle: 380}, counters, true)
	if !ready || math.Abs(usage-42.14876033057851) > 0.0001 {
		t.Fatalf("unexpected usage %.5f ready=%v", usage, ready)
	}
	if _, ready = calculateCPUUsage(counters, counters, true); ready {
		t.Fatal("zero-length sample should not be ready")
	}
}

func TestReadNetworkCounters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "net-dev")
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
pppoe-wan: 12345 1 0 0 0 0 0 0 67890 2 0 0 0 0 0 0
  br-lan: 333 3 0 0 0 0 0 0 444 4 0 0 0 0 0 0
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	counters := readNetworkCounters(path)
	if counters["pppoe-wan"].RXBytes != 12345 || counters["pppoe-wan"].TXBytes != 67890 {
		t.Fatalf("unexpected WAN counters: %+v", counters["pppoe-wan"])
	}
	if counters["br-lan"].RXBytes != 333 || counters["br-lan"].TXBytes != 444 {
		t.Fatalf("unexpected LAN counters: %+v", counters["br-lan"])
	}
}

func TestMemoryAndFilesystemStats(t *testing.T) {
	memory := memoryStats(SystemState{MemTotalKiB: 1024, MemAvailableKiB: 256})
	if memory.TotalBytes != 1024*1024 || memory.UsedBytes != 768*1024 || memory.UsagePercent != 75 {
		t.Fatalf("unexpected memory stats: %+v", memory)
	}

	storage := statFilesystem("temporary", t.TempDir())
	if !storage.Mounted || storage.TotalBytes == 0 || storage.Path == "" {
		t.Fatalf("unexpected filesystem stats: %+v", storage)
	}
}

func TestReadEthernetLinks(t *testing.T) {
	root := t.TempDir()
	writeLink := func(name, carrier, state, speed, duplex string) {
		t.Helper()
		base := filepath.Join(root, name)
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		for file, value := range map[string]string{"carrier": carrier, "operstate": state, "speed": speed, "duplex": duplex} {
			if err := os.WriteFile(filepath.Join(base, file), []byte(value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeLink("eth4", "1", "up", "1000", "full")
	writeLink("eth2", "1", "up", "100", "half")
	writeLink("eth0", "0", "down", "10", "half")

	links := readEthernetLinks(root, "eth4")
	if len(links) != 3 {
		t.Fatalf("unexpected link count: %d", len(links))
	}
	// 自动发现按接口名字典序输出：eth0 / eth2 / eth4
	if links[0].Name != "eth0" || links[0].Up || links[0].SpeedMbps != 0 || links[0].Duplex != "" {
		t.Fatalf("down link should not expose stale negotiation: %+v", links[0])
	}
	if links[0].Label != "LAN 端口 eth0" || links[0].Role != "独立 LAN" {
		t.Fatalf("unbonded port should be labeled as independent LAN: %+v", links[0])
	}
	if links[1].Name != "eth2" || links[1].SpeedMbps != 100 || links[1].Duplex != "half" {
		t.Fatalf("unexpected LAN link: %+v", links[1])
	}
	if links[2].Name != "eth4" || !links[2].Up || links[2].SpeedMbps != 1000 || links[2].Duplex != "full" {
		t.Fatalf("unexpected WAN link: %+v", links[2])
	}
	if links[2].Label != "WAN 上联" || links[2].Role != "拨号物理口" {
		t.Fatalf("WAN role should follow the resolved device, not a fixed name: %+v", links[2])
	}

	writeLink("eth1", "1", "up", "1000", "full")
	writeLink("bond0", "1", "up", "1000", "full")
	if err := os.MkdirAll(filepath.Join(root, "bond0", "bonding"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bond0", "bonding", "slaves"), []byte("eth0 eth1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"eth0", "eth1"} {
		if err := os.Symlink("../bond0", filepath.Join(root, name, "master")); err != nil {
			t.Fatal(err)
		}
	}
	bonded := readEthernetLinks(root, "eth4")
	byName := make(map[string]LinkStats, len(bonded))
	for _, link := range bonded {
		byName[link.Name] = link
	}
	if byName["eth0"].Label != "聚合成员 eth0" || byName["eth0"].Role != "bond0" {
		t.Fatalf("bond member was not detected: %+v", byName["eth0"])
	}
	if byName["bond0"].Label != "LAN 聚合接口" {
		t.Fatalf("bond interface was not included: %+v", byName["bond0"])
	}
}

func TestDetectWANInterface(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network")
	legacy := `config interface 'loopback'
	option ifname 'lo'

config interface 'lan'
	option ifname 'eth0 eth1'
	option proto 'static'

config interface 'wan'
	option ifname 'eth4'
	option proto 'pppoe'
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectWANInterface(path, ""); got != "eth4" {
		t.Fatalf("expected eth4 parsed from option ifname, got %q", got)
	}
	// 多设备 ifname 只取第一个
	if got := readUCIInterfaceDevice(path, "lan"); got != "eth0" {
		t.Fatalf("expected first device of multi-value ifname, got %q", got)
	}
	// 显式覆盖优先于 UCI
	if got := detectWANInterface(path, "eth9"); got != "eth9" {
		t.Fatalf("explicit override should win, got %q", got)
	}

	modern := `config interface 'lan'
	option device 'br-lan'

config interface 'wan'
	option device 'eth3'
`
	modernPath := filepath.Join(dir, "network-modern")
	if err := os.WriteFile(modernPath, []byte(modern), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectWANInterface(modernPath, ""); got != "eth3" {
		t.Fatalf("expected eth3 parsed from option device, got %q", got)
	}

	// 配置文件缺失时不应 panic
	if got := detectWANInterface(filepath.Join(dir, "absent"), ""); got != "" {
		t.Fatalf("missing config should yield empty result, got %q", got)
	}
}

func TestDiscoverEthernetInterfaces(t *testing.T) {
	root := t.TempDir()
	create := func(name string, attrs ...string) {
		t.Helper()
		base := filepath.Join(root, name)
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, attr := range attrs {
			if err := os.WriteFile(filepath.Join(base, attr), []byte("1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 物理以太网口：具备 speed / carrier
	for _, name := range []string{"eth0", "eth1", "eth2", "eth3", "eth4"} {
		create(name, "speed", "carrier")
	}
	// 虚拟或逻辑接口：必须被排除
	for _, name := range []string{"br-lan", "pppoe-wan", "wl0", "wl1", "tun0", "utun", "bond0", "lo", "erspan0", "ip6gre0", "miireg", "soc0", "wifi0"} {
		create(name, "carrier")
	}

	got := discoverEthernetInterfaces(root)
	want := []string{"eth0", "eth1", "eth2", "eth3", "eth4"}
	if len(got) != len(want) {
		t.Fatalf("expected only physical ports %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected sorted physical ports %v, got %v", want, got)
		}
	}
}

func TestBuildMonitoredInterfaces(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"eth0", "eth4", "br-lan", "pppoe-wan", "utun", "tun_Game", "wl0", "wl1"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	items := buildMonitoredInterfaces(root, "eth4")
	byName := make(map[string]string, len(items))
	for _, item := range items {
		byName[item.Name] = item.Label
	}
	if byName["pppoe-wan"] != "公网 WAN" {
		t.Fatalf("pppoe-wan should be labelled as public WAN: %v", byName)
	}
	if byName["br-lan"] != "家庭 LAN" {
		t.Fatalf("br-lan should be included: %v", byName)
	}
	if byName["wl0"] != "无线接口 wl0" || byName["wl1"] != "无线接口 wl1" {
		t.Fatalf("wireless interfaces should be auto-discovered: %v", byName)
	}
	if byName["utun"] != "ShellCrash 隧道" || byName["tun_Game"] != "雷神游戏隧道" {
		t.Fatalf("tunnel interfaces should be included: %v", byName)
	}
	// pppoe-wan 已代表 WAN，不应再重复加入物理口
	if _, exists := byName["eth4"]; exists {
		t.Fatalf("physical WAN must not duplicate the pppoe entry: %v", byName)
	}
}

func TestShouldShowInterface(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		counters networkCounters
		want     bool
	}{
		{"utun", "shellcrash", networkCounters{}, true},
		{"utun", "leigod", networkCounters{}, false},
		{"utun", "off", networkCounters{}, false},
		{"tun_Game", "leigod", networkCounters{}, true},
		{"tun_Game", "shellcrash", networkCounters{}, false},
		{"pppoe-wan", "off", networkCounters{}, true},
		{"br-lan", "shellcrash", networkCounters{}, true},
	}
	for _, tc := range cases {
		if got := shouldShowInterface(tc.name, tc.mode, tc.counters); got != tc.want {
			t.Fatalf("shouldShowInterface(%q, %q) = %v, want %v", tc.name, tc.mode, got, tc.want)
		}
	}
}

func TestReadMeshStats(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "xiaoqiang")
	nodesPath := filepath.Join(dir, "xq_whc_quire")
	config := "config common 'common'\n\toption NETMODE 'whc_cap'\n\toption MESH_VERSION '2'\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	nodes := `{"backhauls":"2","backhauls_qa":"2","locale":"节点甲","initted":"1","return":"success","ip":"192.0.2.10"}
{"backhauls":"8","backhauls_qa":"0","locale":"节点乙","initted":"1","return":"success","ip":"192.0.2.11"}
invalid line
`
	if err := os.WriteFile(nodesPath, []byte(nodes), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 20, 0, 0, 0, time.Local)
	updatedAt := now.Add(-90 * time.Second)
	if err := os.Chtimes(nodesPath, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}

	stats := readMeshStats(configPath, nodesPath, now)
	if !stats.Enabled || stats.Role != "cap" || stats.Version != "2" || !stats.Fresh || stats.NodeCount != 3 {
		t.Fatalf("unexpected mesh summary: %+v", stats)
	}
	if len(stats.Nodes) != 2 || stats.Nodes[0].Backhaul != "5ghz" || stats.Nodes[0].Quality != "good" || !stats.Nodes[0].Online {
		t.Fatalf("unexpected wireless node: %+v", stats.Nodes)
	}
	if stats.Nodes[1].Backhaul != "ethernet" || stats.Nodes[1].Quality != "poor" {
		t.Fatalf("unexpected wired node: %+v", stats.Nodes[1])
	}

	staleAt := now.Add(-10 * time.Minute)
	if err := os.Chtimes(nodesPath, staleAt, staleAt); err != nil {
		t.Fatal(err)
	}
	stale := readMeshStats(configPath, nodesPath, now)
	if stale.Fresh || stale.Nodes[0].Online {
		t.Fatalf("stale topology should not report online nodes: %+v", stale)
	}
}
