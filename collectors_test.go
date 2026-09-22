package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEthernetCollectorFromFixture(t *testing.T) {
	collector := &ethernetCollector{sysClassNet: "testdata/collectors/ethernet", wanInterface: "eth0"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	links := value.(map[string]any)["links"].([]LinkStats)
	if len(links) != 1 || links[0].SpeedMbps != 10000 || links[0].Duplex != "full" || links[0].Role != "拨号物理口" {
		t.Fatalf("unexpected link result: %+v", links)
	}
}

func TestTemperatureCollectorFromFixture(t *testing.T) {
	collector := &temperatureCollector{thermalRoot: "testdata/collectors/thermal", hwmonRoot: "testdata/collectors/hwmon"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sensors := value.(map[string]any)["sensors"].([]SensorReading)
	if len(sensors) != 2 || sensors[0].Value != 51 || sensors[1].Value != 45.5 {
		t.Fatalf("unexpected temperature result: %+v", sensors)
	}
}

func TestFanCollectorFromFixture(t *testing.T) {
	collector := &fanCollector{hwmonRoot: "testdata/collectors/hwmon"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fans := value.(map[string]any)["fans"].([]SensorReading)
	if len(fans) != 1 || fans[0].Name != "system fan" || fans[0].Value != 1820 {
		t.Fatalf("unexpected fan result: %+v", fans)
	}
}

func TestFanCollectorFallsBackToMitempctrlFixture(t *testing.T) {
	collector := &fanCollector{hwmonRoot: "testdata/collectors/does-not-exist", statusPath: "testdata/collectors/fan/status.json"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fans := value.(map[string]any)["fans"].([]SensorReading)
	if len(fans) != 1 || fans[0].Name != "系统风扇" || fans[0].Value != 0 {
		t.Fatalf("unexpected mitempctrl fallback: %+v", fans)
	}
}

func TestUSBCollectorFromFixture(t *testing.T) {
	collector := &usbCollector{mountsPath: "testdata/collectors/usb/mounts", sysBlockRoot: "testdata/collectors/usb/sys_block"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	devices := value.(map[string]any)["devices"].([]USBDeviceStats)
	if len(devices) != 1 || devices[0].SizeBytes != 1<<30 || devices[0].ReadBytes != 100*512 || devices[0].WrittenBytes != 200*512 {
		t.Fatalf("unexpected USB result: %+v", devices)
	}
}

func TestDockerCollectorFromFixtures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ""
		switch r.URL.Path {
		case "/version":
			name = "version.json"
		case "/containers/json":
			name = "containers.json"
		case "/system/df":
			name = "df.json"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join("testdata/collectors/docker", name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	collector := &dockerCollector{client: server.Client(), baseURL: server.URL}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(DockerStats)
	if stats.Version != "20.10.17" || stats.ContainersTotal != 2 || stats.ContainersRunning != 1 || stats.StorageBytes != 123456789 {
		t.Fatalf("unexpected Docker result: %+v", stats)
	}
}

func TestSwapCollectorFromFixture(t *testing.T) {
	collector := &swapCollector{meminfoPath: "testdata/collectors/swap/meminfo", swapsPath: "testdata/collectors/swap/swaps"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(SwapStats)
	if stats.TotalBytes != 524284*1024 || stats.UsedBytes != (524284-393213)*1024 || len(stats.Entries) != 1 {
		t.Fatalf("unexpected swap result: %+v", stats)
	}
}

func TestPPPoECollectorFromFixture(t *testing.T) {
	collector := &pppoeCollector{networkConfigPath: "testdata/collectors/pppoe/network", statusPath: "testdata/collectors/pppoe/status.json"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(PPPoEStats)
	if !stats.Connected || stats.Device != "pppoe-wan" || len(stats.IPv4Addresses) != 1 || len(stats.IPv6Addresses) != 1 || stats.GatewayIPv4 == "" || stats.GatewayIPv6 == "" {
		t.Fatalf("unexpected PPPoE result: %+v", stats)
	}
}

func TestMeshCollectorFromFixture(t *testing.T) {
	collector := &meshCollector{configPath: "testdata/collectors/mesh/xiaoqiang", nodesPath: "testdata/collectors/mesh/nodes"}
	value, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := value.(MeshStats)
	if !stats.Enabled || stats.Role != "cap" || stats.NodeCount != 2 || len(stats.Nodes) != 1 {
		t.Fatalf("unexpected Mesh result: %+v", stats)
	}
}

type testCollector struct {
	name string
	run  func(context.Context) (any, error)
}

func (c testCollector) Name() string                             { return c.name }
func (c testCollector) Collect(ctx context.Context) (any, error) { return c.run(ctx) }

func TestCollectorManagerIsolatesFailureAndPanic(t *testing.T) {
	manager := newCollectorManager([]collectorRegistration{
		{collector: testCollector{name: "ok", run: func(context.Context) (any, error) { return "ready", nil }}, timeout: time.Second, interval: time.Hour},
		{collector: testCollector{name: "error", run: func(context.Context) (any, error) { return nil, errors.New("fixture failure") }}, timeout: time.Second, interval: time.Hour},
		{collector: testCollector{name: "panic", run: func(context.Context) (any, error) { panic("fixture panic") }}, timeout: time.Second, interval: time.Hour},
		{collector: testCollector{name: "unsupported", run: func(context.Context) (any, error) { return nil, errCollectorNotSupported }}, timeout: time.Second, interval: time.Hour},
	})
	manager.Start()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		results := manager.Snapshot()
		if results["ok"].Available && results["error"].Message != "采集中" && results["panic"].Message != "采集中" && !results["unsupported"].Supported {
			if len(results) != 4 || results["error"].Available || results["panic"].Available {
				t.Fatalf("collector isolation failed: %+v", results)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("collectors did not finish")
}
