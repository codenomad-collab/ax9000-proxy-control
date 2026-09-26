package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type PPPoEStats struct {
	Configured    bool     `json:"configured"`
	Connected     bool     `json:"connected"`
	Device        string   `json:"device,omitempty"`
	IPv4Addresses []string `json:"ipv4_addresses"`
	IPv6Addresses []string `json:"ipv6_addresses"`
	DNSServers    []string `json:"dns_servers"`
	GatewayIPv4   string   `json:"gateway_ipv4,omitempty"`
	GatewayIPv6   string   `json:"gateway_ipv6,omitempty"`
	UptimeSeconds int64    `json:"uptime_seconds"`
}

type pppoeCollector struct {
	networkConfigPath string
	ubusCommand       string
	statusPath        string
}

func (c *pppoeCollector) Name() string { return "pppoe" }

func (c *pppoeCollector) Collect(ctx context.Context) (any, error) {
	options, err := readUCISectionOptions(c.networkConfigPath, "wan")
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(options["proto"], "pppoe") {
		return nil, fmt.Errorf("%w: WAN 未配置为 PPPoE", errCollectorNotSupported)
	}
	var data []byte
	if c.statusPath != "" {
		data, err = os.ReadFile(c.statusPath)
	} else {
		output, commandErr := exec.CommandContext(ctx, c.ubusCommand, "call", "network.interface.wan", "status").CombinedOutput()
		if commandErr != nil {
			return nil, fmt.Errorf("读取 PPPoE 运行状态失败: %w", commandErr)
		}
		data = output
	}
	if err != nil {
		return nil, err
	}
	return parsePPPoEStatus(data)
}

func readUCISectionOptions(path, section string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := map[string]string{}
	inSection := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "config ") {
			inSection = strings.HasPrefix(line, "config interface") && (strings.Contains(line, "'"+section+"'") || strings.Contains(line, "\""+section+"\""))
			continue
		}
		if !inSection || !strings.HasPrefix(line, "option ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			result[fields[1]] = strings.Trim(strings.Join(fields[2:], " "), "'\"")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parsePPPoEStatus(data []byte) (PPPoEStats, error) {
	var raw struct {
		Up     bool   `json:"up"`
		Uptime int64  `json:"uptime"`
		Device string `json:"l3_device"`
		IPv4   []struct {
			Address string `json:"address"`
		} `json:"ipv4-address"`
		IPv6 []struct {
			Address string `json:"address"`
		} `json:"ipv6-address"`
		DNS    []string `json:"dns-server"`
		Routes []struct {
			Target  string `json:"target"`
			NextHop string `json:"nexthop"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return PPPoEStats{}, err
	}
	result := PPPoEStats{Configured: true, Connected: raw.Up, Device: raw.Device, DNSServers: raw.DNS, UptimeSeconds: raw.Uptime, IPv4Addresses: []string{}, IPv6Addresses: []string{}}
	for _, address := range raw.IPv4 {
		result.IPv4Addresses = append(result.IPv4Addresses, address.Address)
	}
	for _, address := range raw.IPv6 {
		result.IPv6Addresses = append(result.IPv6Addresses, address.Address)
	}
	for _, route := range raw.Routes {
		switch route.Target {
		case "0.0.0.0", "0.0.0.0/0":
			result.GatewayIPv4 = route.NextHop
		case "::", "::/0":
			result.GatewayIPv6 = route.NextHop
		}
	}
	return result, nil
}
