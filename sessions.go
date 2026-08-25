package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type NetworkSession struct {
	ID              string   `json:"id"`
	Service         string   `json:"service"`
	Network         string   `json:"network"`
	State           string   `json:"state,omitempty"`
	SourceIP        string   `json:"source_ip"`
	SourcePort      string   `json:"source_port,omitempty"`
	DestinationIP   string   `json:"destination_ip"`
	DestinationPort string   `json:"destination_port,omitempty"`
	Host            string   `json:"host,omitempty"`
	Rule            string   `json:"rule,omitempty"`
	RulePayload     string   `json:"rule_payload,omitempty"`
	Chains          []string `json:"chains,omitempty"`
	Upload          int64    `json:"upload,omitempty"`
	Download        int64    `json:"download,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
}

type SessionsResponse struct {
	Service       string           `json:"service"`
	Available     bool             `json:"available"`
	Message       string           `json:"message,omitempty"`
	Count         int              `json:"count"`
	TotalUpload   int64            `json:"total_upload"`
	TotalDownload int64            `json:"total_download"`
	Sessions      []NetworkSession `json:"sessions"`
	Timestamp     time.Time        `json:"timestamp"`
}

type flexibleString string

func (s *flexibleString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*s = flexibleString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = flexibleString(number.String())
	return nil
}

type mihomoConnections struct {
	Upload      int64 `json:"uploadTotal"`
	Download    int64 `json:"downloadTotal"`
	Connections []struct {
		ID       string `json:"id"`
		Metadata struct {
			Network         string         `json:"network"`
			SourceIP        string         `json:"sourceIP"`
			SourcePort      flexibleString `json:"sourcePort"`
			DestinationIP   string         `json:"destinationIP"`
			DestinationPort flexibleString `json:"destinationPort"`
			Host            string         `json:"host"`
		} `json:"metadata"`
		Upload      int64    `json:"upload"`
		Download    int64    `json:"download"`
		Start       string   `json:"start"`
		Chains      []string `json:"chains"`
		Rule        string   `json:"rule"`
		RulePayload string   `json:"rulePayload"`
	} `json:"connections"`
}

func (a *App) sessions(service string) SessionsResponse {
	status := a.currentStatus()
	if service == "auto" || service == "" {
		service = status.Mode
	}
	switch service {
	case "shellcrash":
		if !status.ShellCrash.Running {
			return SessionsResponse{Service: service, Message: "ShellCrash 未运行", Sessions: []NetworkSession{}, Timestamp: time.Now()}
		}
		return a.shellCrashSessions()
	case "leigod":
		if !status.LeiGod.Running {
			return SessionsResponse{Service: service, Message: "雷神未运行", Sessions: []NetworkSession{}, Timestamp: time.Now()}
		}
		return a.leigodSessions()
	default:
		return SessionsResponse{Service: service, Message: "当前没有运行中的代理服务", Sessions: []NetworkSession{}, Timestamp: time.Now()}
	}
}

func (a *App) shellCrashSessions() SessionsResponse {
	response := SessionsResponse{Service: "shellcrash", Sessions: []NetworkSession{}, Timestamp: time.Now()}
	secret, err := a.readMihomoSecret()
	if err != nil {
		response.Message = err.Error()
		return response
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.cfg.MihomoControllerURL, "/")+"/connections", nil)
	if err != nil {
		response.Message = "无法创建控制器请求"
		return response
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	client := &http.Client{Timeout: 4 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		response.Message = "Mihomo 控制器暂不可用: " + err.Error()
		return response
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		response.Message = fmt.Sprintf("Mihomo 控制器返回 HTTP %d", res.StatusCode)
		return response
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 8<<20))
	var data mihomoConnections
	if err := decoder.Decode(&data); err != nil {
		response.Message = "无法解析 Mihomo 会话: " + err.Error()
		return response
	}
	response.Available = true
	response.TotalUpload = data.Upload
	response.TotalDownload = data.Download
	limit := a.cfg.MaxSessions
	if len(data.Connections) < limit {
		limit = len(data.Connections)
	}
	for _, connection := range data.Connections[:limit] {
		response.Sessions = append(response.Sessions, NetworkSession{
			ID:              connection.ID,
			Service:         "shellcrash",
			Network:         connection.Metadata.Network,
			SourceIP:        connection.Metadata.SourceIP,
			SourcePort:      string(connection.Metadata.SourcePort),
			DestinationIP:   connection.Metadata.DestinationIP,
			DestinationPort: string(connection.Metadata.DestinationPort),
			Host:            connection.Metadata.Host,
			Rule:            connection.Rule,
			RulePayload:     connection.RulePayload,
			Chains:          connection.Chains,
			Upload:          connection.Upload,
			Download:        connection.Download,
			StartedAt:       connection.Start,
		})
	}
	response.Count = len(data.Connections)
	return response
}

func (a *App) readMihomoSecret() (string, error) {
	for _, path := range a.cfg.ShellCrashConfigPaths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "secret:") {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "secret:"))
			value = strings.Trim(value, "\"'")
			_ = file.Close()
			if value != "" {
				return value, nil
			}
			break
		}
		_ = file.Close()
	}
	return "", errors.New("没有找到 Mihomo 控制器密钥")
}

func (a *App) leigodTargets(parent context.Context) []netip.Prefix {
	a.targetMu.Lock()
	defer a.targetMu.Unlock()
	if time.Since(a.targetAt) < time.Second {
		return append([]netip.Prefix(nil), a.targets...)
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, a.cfg.IPSetCommand, "save", "target_Game").Output()
	if err != nil {
		a.targets = nil
		a.targetAt = time.Now()
		return nil
	}
	var targets []netip.Prefix
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != "add" || fields[1] != "target_Game" {
			continue
		}
		value := strings.SplitN(fields[2], ",", 2)[0]
		if address, err := netip.ParseAddr(value); err == nil {
			targets = append(targets, netip.PrefixFrom(address, address.BitLen()))
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			targets = append(targets, prefix)
		}
	}
	a.targets = append([]netip.Prefix(nil), targets...)
	a.targetAt = time.Now()
	return append([]netip.Prefix(nil), targets...)
}

func (a *App) leigodSessions() SessionsResponse {
	response := SessionsResponse{Service: "leigod", Sessions: []NetworkSession{}, Timestamp: time.Now()}
	targets := a.leigodTargets(context.Background())
	if len(targets) == 0 {
		response.Available = true
		response.Message = "雷神当前没有登记加速设备"
		return response
	}
	path := ""
	for _, candidate := range a.cfg.ConntrackPaths {
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		response.Message = "内核连接跟踪接口不可用"
		return response
	}
	file, err := os.Open(path)
	if err != nil {
		response.Message = "无法读取内核连接跟踪: " + err.Error()
		return response
	}
	defer file.Close()
	response.Available = true
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 256*1024)
	for scanner.Scan() {
		session, ok := parseConntrackLine(scanner.Text(), targets)
		if !ok {
			continue
		}
		response.Count++
		if len(response.Sessions) < a.cfg.MaxSessions {
			response.Sessions = append(response.Sessions, session)
		}
	}
	if err := scanner.Err(); err != nil {
		response.Message = "连接跟踪读取不完整: " + err.Error()
	}
	return response
}

func parseConntrackLine(line string, targets []netip.Prefix) (NetworkSession, bool) {
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return NetworkSession{}, false
	}
	session := NetworkSession{Service: "leigod", Network: fields[2]}
	if timeout, err := strconv.Atoi(fields[4]); err == nil {
		session.TimeoutSeconds = timeout
	}
	if fields[2] == "tcp" && len(fields) > 5 && !strings.Contains(fields[5], "=") {
		session.State = fields[5]
	}
	tuple := 0
	for _, field := range fields[5:] {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == "src" {
			tuple++
			if tuple > 1 {
				break
			}
			session.SourceIP = parts[1]
			continue
		}
		if tuple != 1 {
			continue
		}
		switch parts[0] {
		case "dst":
			session.DestinationIP = parts[1]
		case "sport":
			session.SourcePort = parts[1]
		case "dport":
			session.DestinationPort = parts[1]
		}
	}
	address, err := netip.ParseAddr(session.SourceIP)
	if err != nil || !prefixContainsAny(targets, address) {
		return NetworkSession{}, false
	}
	idSource := strings.Join([]string{session.Network, session.SourceIP, session.SourcePort, session.DestinationIP, session.DestinationPort}, "|")
	hash := sha256.Sum256([]byte(idSource))
	session.ID = hex.EncodeToString(hash[:8])
	return session, true
}

func prefixContainsAny(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
