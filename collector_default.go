package main

import "time"

// defaultCollectorRegistrations 注册默认采集器。
// meshResolver 由 main.go 创建并共享，使扩展采集器与顶层 system-metrics
// 使用同一个 Mesh 路径解析结果；为 nil 时回退到配置中的静态路径。
func defaultCollectorRegistrations(cfg Config, capabilities DeviceCapabilities, meshResolver *meshNodesResolver) []collectorRegistration {
	wan := capabilities.WANInterface
	if wan == "" {
		wan = cfg.WANInterface
	}
	return []collectorRegistration{
		{collector: &ethernetCollector{sysClassNet: "/sys/class/net", wanInterface: wan}, timeout: time.Second, interval: 5 * time.Second},
		{collector: &temperatureCollector{thermalRoot: "/sys/class/thermal", hwmonRoot: "/sys/class/hwmon"}, timeout: time.Second, interval: 10 * time.Second},
		{collector: &fanCollector{hwmonRoot: "/sys/class/hwmon", ubusCommand: "/bin/ubus"}, timeout: 2 * time.Second, interval: 10 * time.Second},
		{collector: &usbCollector{mountsPath: "/proc/mounts", sysBlockRoot: "/sys/block"}, timeout: 2 * time.Second, interval: 15 * time.Second},
		{collector: &dockerCollector{socketPath: "/var/run/docker.sock"}, timeout: 3 * time.Second, interval: 15 * time.Second},
		{collector: &swapCollector{meminfoPath: "/proc/meminfo", swapsPath: "/proc/swaps"}, timeout: time.Second, interval: 5 * time.Second},
		{collector: &pppoeCollector{networkConfigPath: cfg.NetworkConfigPath, ubusCommand: "/bin/ubus"}, timeout: 3 * time.Second, interval: 10 * time.Second},
		{collector: &meshCollector{configPath: cfg.MeshConfigPath, nodesPath: cfg.MeshNodesPath, resolver: meshResolver}, timeout: time.Second, interval: 5 * time.Second},
	}
}
