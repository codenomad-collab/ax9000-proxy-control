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

	links := readEthernetLinks(root)
	if len(links) != 3 {
		t.Fatalf("unexpected link count: %d", len(links))
	}
	if links[0].Name != "eth4" || !links[0].Up || links[0].SpeedMbps != 1000 || links[0].Duplex != "full" {
		t.Fatalf("unexpected WAN link: %+v", links[0])
	}
	if links[1].Name != "eth2" || links[1].SpeedMbps != 100 || links[1].Duplex != "half" {
		t.Fatalf("unexpected LAN link: %+v", links[1])
	}
	if links[2].Name != "eth0" || links[2].Up || links[2].SpeedMbps != 0 || links[2].Duplex != "" {
		t.Fatalf("down link should not expose stale negotiation: %+v", links[2])
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
