package main

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization[=: ]+bearer[ ]+)[^ ]+`),
	regexp.MustCompile(`(?i)((?:token|secret|password|passwd|pwd)[=:][ ]*)[^ ,;]+`),
	regexp.MustCompile(`(?i)([?&](?:token|secret|key|auth)=)[^& ]+`),
}

func redactSensitive(value string) string {
	result := value
	for _, pattern := range sensitivePatterns {
		result = pattern.ReplaceAllString(result, `${1}[REDACTED]`)
	}
	return strings.TrimSpace(result)
}

type LogsResponse struct {
	Service   string       `json:"service"`
	Audit     []AuditEntry `json:"audit"`
	System    []string     `json:"system"`
	Timestamp time.Time    `json:"timestamp"`
}

func (a *App) logs(service string) LogsResponse {
	if service != "shellcrash" && service != "leigod" && service != "all" {
		service = "all"
	}
	return LogsResponse{
		Service:   service,
		Audit:     a.audit.snapshot(a.cfg.MaxLogLines),
		System:    a.systemLogs(service),
		Timestamp: time.Now(),
	}
}

func (a *App) systemLogs(service string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, a.cfg.LogreadCommand, "-l", "800")
	output, err := command.Output()
	if err != nil || ctx.Err() != nil {
		return []string{}
	}
	keywords := []string{"router-proxy-mode", "router-proxy-web"}
	switch service {
	case "shellcrash":
		keywords = append(keywords, "shellcrash", "crashcore", "mihomo")
	case "leigod":
		keywords = append(keywords, "leigod", "acc-gw", "gameacc", "tun_game")
	default:
		keywords = append(keywords, "shellcrash", "crashcore", "mihomo", "leigod", "acc-gw", "gameacc", "tun_game")
	}
	lines := bytes.Split(output, []byte{'\n'})
	result := make([]string, 0, a.cfg.MaxLogLines)
	for _, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" || !containsAnyFold(line, keywords) {
			continue
		}
		result = append(result, redactSensitive(line))
	}
	if len(result) > a.cfg.MaxLogLines {
		result = result[len(result)-a.cfg.MaxLogLines:]
	}
	return result
}

func containsAnyFold(value string, keywords []string) bool {
	lower := strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}
