package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
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
