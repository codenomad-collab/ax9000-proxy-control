package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	sshPersistScript = "/data/etc/be10000-ssh-persist.sh"
	sshDisabledFile  = "/data/etc/be10000-ssh-disabled"
	sshInitScript    = "/etc/init.d/dropbear"
	sshPersistLock   = "/tmp/be10000-ssh-persist.lock"
	sshMarkerLine    = "[ -e /data/etc/be10000-ssh-disabled ] && exit 0"
)

var errSSHUnsupported = errors.New("当前设备没有安装受控 SSH 守护")

type SSHStatus struct {
	Supported       bool      `json:"supported"`
	Enabled         bool      `json:"enabled"`
	Running         bool      `json:"running"`
	SafeListener    bool      `json:"safe_listener"`
	ListenerChecked bool      `json:"listener_checked"`
	NVRAMEnabled    bool      `json:"nvram_enabled"`
	Listen          string    `json:"listen,omitempty"`
	Message         string    `json:"message"`
	Timestamp       time.Time `json:"timestamp"`
}

type sshActionResult struct {
	Action     string    `json:"action"`
	Successful bool      `json:"successful"`
	Message    string    `json:"message"`
	Status     SSHStatus `json:"status"`
}

type sshManager struct {
	lanIP        string
	guardPath    string
	markerPath   string
	initPath     string
	lockPath     string
	procTCPPath  string
	procTCP6Path string
	run          func(context.Context, string, ...string) ([]byte, error)
	isRoot       func() bool
}

func newSSHManager(listen string) *sshManager {
	host, _, _ := net.SplitHostPort(listen)
	return &sshManager{
		lanIP:        host,
		guardPath:    sshPersistScript,
		markerPath:   sshDisabledFile,
		initPath:     sshInitScript,
		lockPath:     sshPersistLock,
		procTCPPath:  "/proc/net/tcp",
		procTCP6Path: "/proc/net/tcp6",
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		isRoot: func() bool { return os.Geteuid() == 0 },
	}
}

func (a *App) handleSSHStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, a.ssh.status())
}

func (a *App) handleSSHAction(w http.ResponseWriter, r *http.Request) {
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
	if action != "enable" && action != "disable" {
		writeError(w, http.StatusBadRequest, "不支持的 SSH 操作")
		return
	}
	if !a.sshMu.TryLock() {
		writeError(w, http.StatusConflict, "SSH 开关已有操作正在执行")
		return
	}
	defer a.sshMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := a.ssh.setEnabled(ctx, action == "enable")
	status := a.ssh.status()
	if err != nil {
		a.audit.add("error", "ssh", "SSH "+action+" 失败: "+err.Error())
		writeJSON(w, http.StatusConflict, sshActionResult{Action: action, Message: err.Error(), Status: status})
		return
	}
	message := "SSH 已关闭，重启后保持关闭"
	if action == "enable" {
		message = "SSH 已开启，仅监听路由器 LAN IPv4"
	}
	a.audit.add("success", "ssh", message)
	writeJSON(w, http.StatusOK, sshActionResult{Action: action, Successful: true, Message: message, Status: status})
}

func (m *sshManager) status() SSHStatus {
	status := SSHStatus{Timestamp: time.Now()}
	if m == nil {
		status.Message = "SSH 状态不可用"
		return status
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model, modelErr := m.run(ctx, "nvram", "get", "model")
	guard, guardErr := os.ReadFile(m.guardPath)
	guardInfo, guardStatErr := os.Stat(m.guardPath)
	initInfo, initErr := os.Stat(m.initPath)
	status.Supported = modelErr == nil && strings.TrimSpace(string(model)) == "RC01" &&
		guardErr == nil && guardStatErr == nil && guardInfo.Mode()&0o111 != 0 &&
		strings.Contains(string(guard), sshMarkerLine) &&
		initErr == nil && initInfo.Mode()&0o111 != 0 && net.ParseIP(m.lanIP).To4() != nil
	if !status.Supported {
		status.Message = "当前设备未安装受控 SSH 守护"
		return status
	}
	_, markerErr := os.Stat(m.markerPath)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		status.Message = "无法读取 SSH 开关状态"
		return status
	}
	status.Enabled = os.IsNotExist(markerErr)
	nvram, nvramErr := m.run(ctx, "nvram", "get", "ssh_en")
	status.NVRAMEnabled = nvramErr == nil && strings.TrimSpace(string(nvram)) == "1"
	listeners, listenErr := m.listeners()
	if listenErr != nil {
		status.Message = "无法读取 SSH 监听状态"
		return status
	}
	status.ListenerChecked = true
	if len(listeners) == 1 && listeners[0] == net.JoinHostPort(m.lanIP, "22") {
		status.SafeListener = true
		status.Listen = listeners[0]
	}
	status.Running = len(listeners) > 0
	switch {
	case nvramErr != nil:
		status.Message = "无法读取 SSH 固件开关"
	case !status.Enabled && !status.Running && !status.NVRAMEnabled:
		status.Message = "已关闭，重启后保持关闭"
	case !status.Enabled:
		status.Message = "关闭状态异常：仍有 SSH 监听或固件开关未关闭"
	case status.SafeListener && status.NVRAMEnabled:
		status.Message = "运行中，仅监听路由器 LAN IPv4"
	case status.Running:
		status.Message = "SSH 监听地址异常，请检查服务"
	default:
		status.Message = "已启用，但未检测到 SSH 监听"
	}
	return status
}

func (m *sshManager) setEnabled(ctx context.Context, enable bool) error {
	if !m.isRoot() {
		return errors.New("控制台需要 root 权限管理 SSH")
	}
	before := m.status()
	if !before.Supported {
		return errSSHUnsupported
	}
	if !before.ListenerChecked {
		return errors.New("无法读取 SSH 监听状态")
	}
	if enable {
		if before.Enabled && before.SafeListener && before.NVRAMEnabled {
			return nil
		}
		if err := os.Remove(m.markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清除 SSH 关闭标记: %w", err)
		}
		if _, err := m.run(ctx, m.guardPath); err != nil {
			m.rollbackEnable()
			return fmt.Errorf("启动 SSH 守护: %w", err)
		}
		after := m.status()
		if !after.SafeListener || !after.NVRAMEnabled {
			m.rollbackEnable()
			return errors.New("SSH 未建立安全的 LAN IPv4 监听，已尝试回滚")
		}
		return nil
	}
	if !before.Enabled && !before.Running && !before.NVRAMEnabled {
		return nil
	}
	if err := writeSSHMarker(m.markerPath); err != nil {
		return fmt.Errorf("保存 SSH 关闭标记: %w", err)
	}
	for {
		_, err := os.Stat(m.lockPath)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("检查 SSH 守护锁: %w", err)
		}
		select {
		case <-ctx.Done():
			return errors.New("等待当前 SSH 守护结束超时；关闭标记已保留")
		case <-time.After(100 * time.Millisecond):
		}
	}
	var failures []string
	if _, err := m.run(ctx, "nvram", "set", "ssh_en=0"); err != nil {
		failures = append(failures, "设置固件 SSH 开关失败")
	}
	if _, err := m.run(ctx, "nvram", "commit"); err != nil {
		failures = append(failures, "保存固件 SSH 开关失败")
	}
	if _, err := m.run(ctx, m.initPath, "stop"); err != nil {
		failures = append(failures, "停止 Dropbear 失败")
	}
	for i := 0; i < 20; i++ {
		after := m.status()
		if after.ListenerChecked && !after.Running && !after.NVRAMEnabled {
			if len(failures) == 0 {
				return nil
			}
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return errors.New("SSH 关闭后仍检测到监听或固件开关未关闭")
}

func (m *sshManager) rollbackEnable() {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = writeSSHMarker(m.markerPath)
	_, _ = m.run(cleanupCtx, "nvram", "set", "ssh_en=0")
	_, _ = m.run(cleanupCtx, "nvram", "commit")
	_, _ = m.run(cleanupCtx, m.initPath, "stop")
}

func writeSSHMarker(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.New("SSH 关闭标记不是普通文件")
		}
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func (m *sshManager) listeners() ([]string, error) {
	var result []string
	for _, source := range []struct {
		path string
		v6   bool
	}{{m.procTCPPath, false}, {m.procTCP6Path, true}} {
		data, err := os.ReadFile(source.path)
		if os.IsNotExist(err) && source.v6 {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) != 2 || parts[1] != "0016" {
				continue
			}
			if source.v6 {
				result = append(result, "IPv6:22")
				continue
			}
			bytes, err := hex.DecodeString(parts[0])
			if err != nil || len(bytes) != 4 {
				continue
			}
			ip := net.IPv4(bytes[3], bytes[2], bytes[1], bytes[0]).String()
			result = append(result, net.JoinHostPort(ip, "22"))
		}
	}
	return result, nil
}
