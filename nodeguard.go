package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	nodeGuardCronComment = "# AX9000 AI node guard (managed by ax9000-proxy-control)"
	nodeGuardSchedule    = "every 30 minutes"
)

type NodeGuardNodes struct {
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Backup    string `json:"backup"`
}

type NodeGuardProbe struct {
	SamplesMS []int `json:"samples_ms"`
	Successes int   `json:"successes"`
	MedianMS  int   `json:"median_ms,omitempty"`
	SpreadMS  int   `json:"spread_ms,omitempty"`
}

type nodeGuardState struct {
	SchemaVersion       int                       `json:"schema_version"`
	Version             string                    `json:"guard_version"`
	LastCheck           string                    `json:"last_check"`
	Status              string                    `json:"status"`
	ConsecutiveFailures int                       `json:"consecutive_failures"`
	ExpectedNodes       NodeGuardNodes            `json:"expected_nodes"`
	LastIssue           string                    `json:"last_issue,omitempty"`
	LastRepair          string                    `json:"last_repair,omitempty"`
	Checks              map[string]NodeGuardProbe `json:"checks,omitempty"`
}

type NodeGuardStatus struct {
	Installed           bool                      `json:"installed"`
	Version             string                    `json:"version,omitempty"`
	Running             bool                      `json:"running"`
	CronEnabled         bool                      `json:"cron_enabled"`
	SchedulerRunning    bool                      `json:"scheduler_running"`
	Schedule            string                    `json:"schedule"`
	StateAvailable      bool                      `json:"state_available"`
	Status              string                    `json:"status"`
	LastCheck           string                    `json:"last_check,omitempty"`
	ConsecutiveFailures int                       `json:"consecutive_failures"`
	ExpectedNodes       NodeGuardNodes            `json:"expected_nodes"`
	LastIssue           string                    `json:"last_issue,omitempty"`
	LastRepair          string                    `json:"last_repair,omitempty"`
	Checks              map[string]NodeGuardProbe `json:"checks"`
	Logs                []string                  `json:"logs"`
	Message             string                    `json:"message,omitempty"`
	Timestamp           time.Time                 `json:"timestamp"`
}

type nodeGuardActionResult struct {
	Action     string          `json:"action"`
	Successful bool            `json:"successful"`
	Message    string          `json:"message"`
	Status     NodeGuardStatus `json:"status"`
}

func (a *App) handleNodeGuard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, a.nodeGuardStatus())
}

func (a *App) handleNodeGuardAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var request actionRequest
	if err := decodeJSON(r, &request, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "操作请求格式错误")
		return
	}
	action := strings.TrimSpace(request.Action)
	if action != "run" && action != "enable" && action != "disable" {
		writeError(w, http.StatusBadRequest, "不支持的节点守护操作")
		return
	}

	if !a.nodeGuardMu.TryLock() {
		writeError(w, http.StatusConflict, "节点守护已有操作正在执行")
		return
	}
	if action == "run" {
		if _, err := os.Stat(a.cfg.NodeGuardCommand); err != nil {
			a.nodeGuardMu.Unlock()
			writeError(w, http.StatusConflict, "节点守护程序尚未安装")
			return
		}
		a.audit.add("info", "node-guard", "已从控制台启动立即检查")
		go a.runNodeGuard()
		writeJSON(w, http.StatusAccepted, nodeGuardActionResult{
			Action: "run", Successful: true, Message: "节点检查已启动，状态会自动刷新", Status: a.nodeGuardStatus(),
		})
		return
	}

	enable := action == "enable"
	if enable {
		if info, err := os.Stat(a.cfg.NodeGuardCommand); err != nil || info.Mode()&0o111 == 0 {
			a.nodeGuardMu.Unlock()
			writeError(w, http.StatusConflict, "节点守护程序不存在或不可执行")
			return
		}
	}
	if err := a.setNodeGuardCron(enable); err != nil {
		a.audit.add("error", "node-guard", "更新定时调度失败: "+err.Error())
		a.nodeGuardMu.Unlock()
		writeJSON(w, http.StatusConflict, nodeGuardActionResult{
			Action: action, Message: "定时调度更新失败: " + err.Error(), Status: a.nodeGuardStatus(),
		})
		return
	}
	message := "已启用每 30 分钟检查"
	if !enable {
		message = "已暂停定时检查；仍可手动立即检查"
	}
	a.audit.add("success", "node-guard", message)
	a.nodeGuardMu.Unlock()
	writeJSON(w, http.StatusOK, nodeGuardActionResult{
		Action: action, Successful: true, Message: message, Status: a.nodeGuardStatus(),
	})
}

func (a *App) runNodeGuard() {
	defer a.nodeGuardMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, a.cfg.NodeGuardCommand).CombinedOutput()
	message := strings.TrimSpace(redactSensitive(string(output)))
	if ctx.Err() != nil {
		a.audit.add("error", "node-guard", "立即检查超时")
		return
	}
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		a.audit.add("error", "node-guard", "立即检查失败: "+message)
		return
	}
	if message == "" {
		message = "检查完成"
	}
	a.audit.add("success", "node-guard", "立即检查完成: "+message)
}

func (a *App) nodeGuardStatus() NodeGuardStatus {
	status := NodeGuardStatus{
		Schedule:  nodeGuardSchedule,
		Checks:    map[string]NodeGuardProbe{},
		Logs:      tailTextFile(a.cfg.NodeGuardLogPath, 60, 128<<10),
		Timestamp: time.Now(),
	}
	if info, err := os.Stat(a.cfg.NodeGuardCommand); err == nil && info.Mode().IsRegular() {
		status.Installed = true
	}
	status.Running = processCommandRunning(a.cfg.NodeGuardCommand)
	if !status.Running && !a.nodeGuardMu.TryLock() {
		status.Running = true
	} else if !status.Running {
		a.nodeGuardMu.Unlock()
	}
	status.SchedulerRunning = processNameRunning("crond")
	if cron, err := os.ReadFile(a.cfg.NodeGuardCronPath); err == nil {
		status.CronEnabled = cronContainsCommand(string(cron), a.cfg.NodeGuardCommand)
	}

	state, err := readNodeGuardState(a.cfg.NodeGuardStatePath)
	if err == nil {
		status.StateAvailable = true
		status.Version = state.Version
		status.Status = state.Status
		status.LastCheck = state.LastCheck
		status.ConsecutiveFailures = state.ConsecutiveFailures
		status.ExpectedNodes = state.ExpectedNodes
		status.LastIssue = redactSensitive(state.LastIssue)
		status.LastRepair = state.LastRepair
		status.Checks = state.Checks
	} else if !errors.Is(err, os.ErrNotExist) {
		status.Message = "状态文件无法读取: " + err.Error()
	}
	if status.Version == "" && status.Installed {
		status.Version = readNodeGuardVersion(a.cfg.NodeGuardCommand)
	}
	if status.Status == "" {
		if status.Installed {
			status.Status = "unknown"
		} else {
			status.Status = "not_installed"
		}
	}
	for index := range status.Logs {
		status.Logs[index] = redactSensitive(status.Logs[index])
	}
	return status
}

func readNodeGuardVersion(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil || ctx.Err() != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func readNodeGuardState(path string) (nodeGuardState, error) {
	file, err := os.Open(path)
	if err != nil {
		return nodeGuardState{}, err
	}
	defer file.Close()
	var state nodeGuardState
	decoder := json.NewDecoder(io.LimitReader(file, 512<<10))
	if err := decoder.Decode(&state); err != nil {
		return nodeGuardState{}, err
	}
	return state, nil
}

func processCommandRunning(command string) bool {
	return processMatches(func(name, cmdline string) bool {
		return strings.Contains(cmdline, command) || (filepath.Base(command) != "" && name == filepath.Base(command))
	})
}

func processNameRunning(name string) bool {
	return processMatches(func(processName, cmdline string) bool {
		return processName == name || strings.Contains(cmdline, "/"+name+" ") || strings.HasSuffix(cmdline, "/"+name)
	})
}

func processMatches(match func(name, cmdline string) bool) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", entry.Name())
		nameBytes, _ := os.ReadFile(filepath.Join(base, "comm"))
		cmdBytes, _ := os.ReadFile(filepath.Join(base, "cmdline"))
		name := strings.TrimSpace(string(nameBytes))
		cmdline := strings.TrimSpace(strings.ReplaceAll(string(cmdBytes), "\x00", " "))
		if match(name, cmdline) {
			return true
		}
	}
	return false
}

func cronContainsCommand(content, command string) bool {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		for _, field := range fields[5:] {
			if strings.Trim(field, "'\";") == command {
				return true
			}
		}
	}
	return false
}

func updateNodeGuardCron(content, command string, enable bool) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == nodeGuardCronComment {
			continue
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && cronContainsCommand(line+"\n", command) {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if enable {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, nodeGuardCronComment, "*/30 * * * * "+command+" >/dev/null 2>&1")
	}
	return strings.Join(kept, "\n") + "\n"
}

func (a *App) setNodeGuardCron(enable bool) error {
	original, err := os.ReadFile(a.cfg.NodeGuardCronPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated := []byte(updateNodeGuardCron(string(original), a.cfg.NodeGuardCommand, enable))
	if bytes.Equal(original, updated) {
		return nil
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(a.cfg.NodeGuardCronPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomic(a.cfg.NodeGuardCronPath, updated, mode); err != nil {
		return err
	}
	if err := restartCron(); err != nil {
		_ = writeFileAtomic(a.cfg.NodeGuardCronPath, original, mode)
		_ = restartCron()
		return err
	}
	return nil
}

func restartCron() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/etc/init.d/cron", "restart").CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("cron restart timeout")
	}
	if err != nil {
		message := strings.TrimSpace(redactSensitive(string(output)))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("cron restart failed: %s", message)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".node-guard-cron-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func tailTextFile(path string, maxLines int, maxBytes int64) []string {
	file, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return []string{}
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return []string{}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), ""), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	result := make([]string, 0, maxLines)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	if len(result) > maxLines {
		result = result[len(result)-maxLines:]
	}
	return result
}
