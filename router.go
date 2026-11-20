package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServiceState struct {
	Running      bool  `json:"running"`
	Healthy      bool  `json:"healthy"`
	ProcessCount int   `json:"process_count"`
	RSSKiB       int64 `json:"rss_kib"`
}

type LeiGodState struct {
	ServiceState
	DaemonRunning bool `json:"daemon_running"`
	WebRunning    bool `json:"web_running"`
	GameRunning   bool `json:"game_running"`
	TunGameUp     bool `json:"tun_game_up"`
	TargetCount   int  `json:"target_count"`
	UpdateEnabled bool `json:"update_enabled"`
}

type SystemState struct {
	UptimeSeconds    float64 `json:"uptime_seconds"`
	MemTotalKiB      int64   `json:"mem_total_kib"`
	MemAvailableKiB  int64   `json:"mem_available_kib"`
	Load1            float64 `json:"load_1"`
	Load5            float64 `json:"load_5"`
	Load15           float64 `json:"load_15"`
	ControllerRSSKiB int64   `json:"controller_rss_kib"`
}

type StatusResponse struct {
	Version    string        `json:"version"`
	Mode       string        `json:"mode"`
	ShellCrash ServiceState  `json:"shellcrash"`
	LeiGod     LeiGodState   `json:"leigod"`
	System     SystemState   `json:"system"`
	LastAction ActionSummary `json:"last_action"`
	Timestamp  time.Time     `json:"timestamp"`
}

type procInfo struct {
	Name    string
	Command string
	RSSKiB  int64
}

func readMode(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	mode := strings.TrimSpace(string(data))
	switch mode {
	case "shellcrash", "leigod", "off":
		return mode
	default:
		return "unknown"
	}
}

func readProcesses() []procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	result := make([]procInfo, 0, 32)
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
		command := strings.TrimSpace(strings.ReplaceAll(string(cmdBytes), "\x00", " "))
		name := strings.TrimSpace(string(nameBytes))
		if !strings.Contains(name, "CrashCore") && !strings.Contains(command, "CrashCore") &&
			!strings.Contains(name, "acc-gw") && !strings.Contains(command, "acc-gw.router.arm64") {
			continue
		}
		result = append(result, procInfo{Name: name, Command: command, RSSKiB: readRSSKiB(filepath.Join(base, "status"))})
	}
	return result
}

func readRSSKiB(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			return value
		}
	}
	return 0
}

func interfaceUp(name string) bool {
	iface, err := net.InterfaceByName(name)
	return err == nil && iface.Flags&net.FlagUp != 0
}

func (a *App) currentStatus() StatusResponse {
	processes := readProcesses()
	shell := ServiceState{}
	leigod := LeiGodState{}
	for _, process := range processes {
		if strings.Contains(process.Name, "CrashCore") || strings.Contains(process.Command, "CrashCore") {
			shell.Running = true
			shell.ProcessCount++
			shell.RSSKiB += process.RSSKiB
		}
		if strings.Contains(process.Name, "acc-gw") || strings.Contains(process.Command, "acc-gw.router.arm64") {
			leigod.Running = true
			leigod.ProcessCount++
			leigod.RSSKiB += process.RSSKiB
			if strings.Contains(process.Command, "-r daemon") {
				leigod.DaemonRunning = true
			}
			if strings.Contains(process.Command, "-r web") {
				leigod.WebRunning = true
			}
			if strings.Contains(process.Command, "-r acc") || strings.Contains(process.Command, "-t Game") {
				leigod.GameRunning = true
			}
		}
	}
	shell.Healthy = shell.Running
	leigod.TunGameUp = interfaceUp("tun_Game")
	leigod.TargetCount = len(a.leigodTargets(context.Background()))
	leigod.Healthy = leigod.DaemonRunning && leigod.WebRunning
	if info, err := os.Stat(a.cfg.LeiGodUpdateCommand); err == nil && info.Mode()&0o111 != 0 {
		leigod.UpdateEnabled = true
	}

	return StatusResponse{
		Version:    version,
		Mode:       readMode(a.cfg.ModeFile),
		ShellCrash: shell,
		LeiGod:     leigod,
		System:     readSystemState(),
		LastAction: a.last.snapshot(),
		Timestamp:  time.Now(),
	}
}

func readSystemState() SystemState {
	state := SystemState{ControllerRSSKiB: readRSSKiB("/proc/self/status")}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			state.UptimeSeconds, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if file, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				state.MemTotalKiB = value
			case "MemAvailable:":
				state.MemAvailableKiB = value
			}
		}
		_ = file.Close()
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			state.Load1, _ = strconv.ParseFloat(fields[0], 64)
			state.Load5, _ = strconv.ParseFloat(fields[1], 64)
			state.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	return state
}

type actionResult struct {
	Action     string         `json:"action"`
	Successful bool           `json:"successful"`
	RolledBack bool           `json:"rolled_back"`
	Message    string         `json:"message"`
	Output     string         `json:"output,omitempty"`
	Status     StatusResponse `json:"status"`
}

func (a *App) performAction(action string) actionResult {
	a.actionMu.Lock()
	defer a.actionMu.Unlock()

	previous := readMode(a.cfg.ModeFile)
	if action == "update_leigod" {
		return a.performLeiGodUpdate(action, previous)
	}
	target, restart, err := actionTarget(action)
	if err != nil {
		return actionResult{Action: action, Message: err.Error(), Status: a.currentStatus()}
	}
	a.audit.add("info", "action", fmt.Sprintf("请求执行 %s，切换前模式 %s", action, previous))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	var output bytes.Buffer
	if restart && previous == target {
		text, runErr := a.runModeCommand(ctx, "off")
		output.WriteString(text)
		if runErr != nil {
			message := "停止当前服务失败: " + runErr.Error()
			a.audit.add("error", "action", message)
			a.last.set(action, false, message)
			return actionResult{Action: action, Message: message, Output: redactSensitive(output.String()), Status: a.currentStatus()}
		}
		time.Sleep(time.Second)
	}

	text, runErr := a.runModeCommand(ctx, target)
	output.WriteString(text)
	if runErr == nil {
		runErr = a.waitForTarget(ctx, target)
	}
	if runErr == nil {
		message := "操作完成，当前模式为 " + target
		a.audit.add("success", "action", message)
		a.last.set(action, true, message)
		return actionResult{Action: action, Successful: true, Message: message, Output: redactSensitive(output.String()), Status: a.currentStatus()}
	}

	message := fmt.Sprintf("%s 启动验证失败: %v", target, runErr)
	a.audit.add("error", "action", message)
	rolledBack := false
	if a.cfg.AutoRollback && previous != target && isKnownMode(previous) {
		a.audit.add("warning", "rollback", "尝试恢复切换前模式 "+previous)
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 50*time.Second)
		rollbackOutput, rollbackErr := a.runModeCommand(rollbackCtx, previous)
		output.WriteString("\n[rollback]\n" + rollbackOutput)
		if rollbackErr == nil {
			rollbackErr = a.waitForTarget(rollbackCtx, previous)
		}
		rollbackCancel()
		if rollbackErr == nil {
			rolledBack = true
			message += "；已自动恢复 " + previous
			a.audit.add("success", "rollback", "自动回滚成功")
		} else {
			message += "；自动回滚失败: " + rollbackErr.Error()
			a.audit.add("error", "rollback", "自动回滚失败: "+rollbackErr.Error())
		}
	}
	a.last.set(action, false, message)
	return actionResult{Action: action, Successful: false, RolledBack: rolledBack, Message: message, Output: redactSensitive(output.String()), Status: a.currentStatus()}
}

func (a *App) performLeiGodUpdate(action, currentMode string) actionResult {
	a.audit.add("info", "leigod-update", "请求手动检查雷神更新，当前模式 "+currentMode)
	if currentMode == "leigod" {
		message := "请先切换到 ShellCrash 或全部停止，再检查雷神更新"
		a.audit.add("warning", "leigod-update", message)
		a.last.set(action, false, message)
		return actionResult{Action: action, Message: message, Status: a.currentStatus()}
	}
	info, err := os.Stat(a.cfg.LeiGodUpdateCommand)
	if err != nil || info.Mode()&0o111 == 0 {
		message := "雷神手动更新入口尚未安装"
		a.audit.add("error", "leigod-update", message)
		a.last.set(action, false, message)
		return actionResult{Action: action, Message: message, Status: a.currentStatus()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, runErr := exec.CommandContext(ctx, a.cfg.LeiGodUpdateCommand, "check").CombinedOutput()
	redacted := redactSensitive(string(output))
	if ctx.Err() != nil {
		runErr = fmt.Errorf("command timeout: %w", ctx.Err())
	}
	if runErr != nil {
		message := "雷神更新检查启动失败: " + runErr.Error()
		if redacted != "" {
			message += ": " + redacted
		}
		a.audit.add("error", "leigod-update", message)
		a.last.set(action, false, message)
		return actionResult{Action: action, Message: message, Output: redacted, Status: a.currentStatus()}
	}
	message := "雷神更新检查已启动，后台最多运行 3 分钟"
	if redacted != "" {
		message = redacted
	}
	a.audit.add("success", "leigod-update", message)
	a.last.set(action, true, message)
	return actionResult{Action: action, Successful: true, Message: message, Output: redacted, Status: a.currentStatus()}
}

func actionTarget(action string) (target string, restart bool, err error) {
	switch action {
	case "start_shellcrash":
		return "shellcrash", false, nil
	case "restart_shellcrash":
		return "shellcrash", true, nil
	case "start_leigod":
		return "leigod", false, nil
	case "restart_leigod":
		return "leigod", true, nil
	case "stop_all":
		return "off", false, nil
	default:
		return "", false, errors.New("unsupported action")
	}
}

func isKnownMode(mode string) bool {
	return mode == "shellcrash" || mode == "leigod" || mode == "off"
}

func (a *App) runModeCommand(ctx context.Context, target string) (string, error) {
	command := exec.CommandContext(ctx, a.cfg.ModeCommand, target)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("command timeout: %w", ctx.Err())
	}
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}
	return string(output), nil
}

func (a *App) waitForTarget(ctx context.Context, target string) error {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := a.currentStatus()
		ok := false
		switch target {
		case "shellcrash":
			ok = status.Mode == target && status.ShellCrash.Healthy && !status.LeiGod.Running
		case "leigod":
			ok = status.Mode == target && status.LeiGod.Healthy && !status.ShellCrash.Running
		case "off":
			ok = status.Mode == target && !status.ShellCrash.Running && !status.LeiGod.Running
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
