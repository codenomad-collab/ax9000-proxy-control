const state = {
  csrf: "",
  status: null,
  sessions: [],
  sessionService: "auto",
  logService: "all",
  busy: false,
  visible: true,
  toastTimer: null,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

async function api(path, options = {}) {
  const headers = { Accept: "application/json", ...(options.headers || {}) };
  if (options.body) headers["Content-Type"] = "application/json";
  if (options.method && options.method !== "GET") headers["X-CSRF-Token"] = state.csrf;
  const response = await fetch(path, { credentials: "same-origin", cache: "no-store", ...options, headers });
  let data = {};
  try { data = await response.json(); } catch (_) { data = { error: `HTTP ${response.status}` }; }
  if (response.status === 401) {
    showLogin();
    throw new Error("登录已失效");
  }
  if (!response.ok) throw new Error(data.message || data.error || `HTTP ${response.status}`);
  return data;
}

function showLogin(message = "") {
  state.csrf = "";
  $("#app-view").classList.add("hidden");
  $("#login-view").classList.remove("hidden");
  closePasswordModal();
  $("#login-message").textContent = message;
  setTimeout(() => $("#password").focus(), 50);
}

function showApp() {
  $("#login-view").classList.add("hidden");
  $("#app-view").classList.remove("hidden");
  $("#login-message").textContent = "";
}

async function restoreSession() {
  try {
    const me = await api("/api/me");
    state.csrf = me.csrf;
    showApp();
    await refreshAll();
  } catch (_) {
    showLogin();
  }
}

$("#login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const error = $("#login-error");
  error.textContent = "";
  $("#login-message").textContent = "";
  const button = event.currentTarget.querySelector("button");
  button.disabled = true;
  try {
    const result = await api("/api/login", { method: "POST", body: JSON.stringify({ password: $("#password").value }) });
    state.csrf = result.csrf;
    $("#password").value = "";
    showApp();
    await refreshAll();
  } catch (err) {
    error.textContent = err.message;
  } finally {
    button.disabled = false;
  }
});

$("#logout-button").addEventListener("click", async () => {
  try { await api("/api/logout", { method: "POST", body: "{}" }); } catch (_) {}
  showLogin();
});

function openPasswordModal() {
  $("#password-form").reset();
  $("#password-error").textContent = "";
  $("#password-modal").classList.remove("hidden");
  document.body.classList.add("modal-open");
  setTimeout(() => $("#current-password").focus(), 50);
}

function closePasswordModal() {
  $("#password-modal").classList.add("hidden");
  document.body.classList.remove("modal-open");
  $("#password-form").reset();
  $("#password-error").textContent = "";
  [$("#current-password"), $("#new-password"), $("#confirm-password")].forEach((input) => { input.type = "password"; });
}

$("#password-button").addEventListener("click", openPasswordModal);
$("#password-close").addEventListener("click", closePasswordModal);
$("#password-cancel").addEventListener("click", closePasswordModal);
$("#password-modal").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closePasswordModal();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#password-modal").classList.contains("hidden")) closePasswordModal();
});
$("#show-passwords").addEventListener("change", (event) => {
  const type = event.currentTarget.checked ? "text" : "password";
  [$("#current-password"), $("#new-password"), $("#confirm-password")].forEach((input) => { input.type = type; });
});

$("#password-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const error = $("#password-error");
  const currentPassword = $("#current-password").value;
  const newPassword = $("#new-password").value;
  const confirmPassword = $("#confirm-password").value;
  const button = event.currentTarget.querySelector('button[type="submit"]');
  error.textContent = "";

  if ([...newPassword].length < 8 || [...newPassword].length > 128) {
    error.textContent = "新密码需要 8 到 128 个字符";
    return;
  }
  if (newPassword !== confirmPassword) {
    error.textContent = "两次输入的新密码不一致";
    return;
  }

  button.disabled = true;
  try {
    await api("/api/password", {
      method: "POST",
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    });
    closePasswordModal();
    showLogin("密码修改成功，请使用新密码重新登录。");
  } catch (err) {
    error.textContent = err.message;
  } finally {
    button.disabled = false;
  }
});

$("#refresh-button").addEventListener("click", refreshAll);

$$('[data-action]').forEach((button) => button.addEventListener("click", () => runAction(button.dataset.action)));

async function runAction(action) {
  if (state.busy) return;
  const labels = {
    start_shellcrash: "切换到 ShellCrash",
    restart_shellcrash: "重启 ShellCrash",
    start_leigod: "切换到雷神加速器",
    restart_leigod: "重启雷神加速器",
    stop_all: "停止全部代理服务",
  };
  const warning = action === "stop_all"
    ? "停止后，依赖代理的设备可能暂时无法访问部分网络。"
    : "切换期间网络可能中断数秒；目标服务启动失败时会自动回滚。";
  if (!window.confirm(`确认${labels[action]}？\n\n${warning}`)) return;
  setBusy(true);
  toast(`正在${labels[action]}…`);
  try {
    const result = await api("/api/action", { method: "POST", body: JSON.stringify({ action }) });
    toast(result.message || "操作完成");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(false);
    await refreshAll();
  }
}

function setBusy(value) {
  state.busy = value;
  $$('[data-action]').forEach((button) => { button.disabled = value; });
}

async function refreshStatus() {
  try {
    const data = await api("/api/status");
    state.status = data;
    renderStatus(data);
  } catch (err) {
    if (!err.message.includes("登录")) toast(`状态刷新失败：${err.message}`, true);
  }
}

function renderStatus(data) {
  const mode = data.mode;
  const modeMap = {
    shellcrash: ["ShellCrash", "透明代理正在接管局域网流量", "S", "shellcrash"],
    leigod: ["雷神加速器", "游戏设备策略路由正在工作", "L", "leigod"],
    off: ["全部停止", "当前没有代理服务运行", "○", "off"],
    unknown: ["状态异常", "无法读取工作模式，请查看日志", "!", "neutral"],
  };
  const current = modeMap[mode] || modeMap.unknown;
  $("#mode-title").textContent = current[0];
  $("#mode-description").textContent = current[1];
  $("#mode-icon").textContent = current[2];
  $("#mode-badge").textContent = mode;
  $("#mode-badge").className = `badge ${current[3]}`;

  setServiceState("shellcrash", data.shellcrash.running, data.shellcrash.healthy);
  $("#shellcrash-processes").textContent = data.shellcrash.process_count;
  $("#shellcrash-memory").textContent = formatKiB(data.shellcrash.rss_kib);
  $("#shellcrash-health").textContent = data.shellcrash.healthy ? "正常" : data.shellcrash.running ? "异常" : "—";

  setServiceState("leigod", data.leigod.running, data.leigod.healthy);
  $("#leigod-targets").textContent = data.leigod.target_count;
  $("#leigod-memory").textContent = formatKiB(data.leigod.rss_kib);
  $("#leigod-tun").textContent = data.leigod.tun_game_up ? "UP" : data.leigod.game_running ? "异常" : "未激活";

  $("#memory-metric").textContent = formatKiB(data.system.mem_available_kib);
  $("#controller-memory").textContent = formatKiB(data.system.controller_rss_kib);
  $("#load-metric").textContent = `${data.system.load_1.toFixed(2)} / ${data.system.load_5.toFixed(2)}`;
  $("#uptime-metric").textContent = formatUptime(data.system.uptime_seconds);
}

function setServiceState(name, running, healthy) {
  const element = $(`#${name}-state`);
  element.textContent = running ? (healthy ? "运行正常" : "运行异常") : "已停止";
  element.className = `status-pill ${running ? (healthy ? "online" : "warning") : "offline"}`;
  $(`#${name}-card`).classList.toggle("active-service", running);
}

async function refreshSessions() {
  try {
    const data = await api(`/api/sessions?service=${encodeURIComponent(state.sessionService)}`);
    state.sessions = data.sessions || [];
    $("#session-count").textContent = data.count || 0;
    $("#session-upload").textContent = formatBytes(data.total_upload || 0);
    $("#session-download").textContent = formatBytes(data.total_download || 0);
    $("#session-message").textContent = data.message || "";
    renderSessions();
  } catch (err) {
    $("#session-message").textContent = err.message;
  }
}

function renderSessions() {
  const filter = $("#session-filter").value.trim().toLowerCase();
  const rows = state.sessions.filter((session) => JSON.stringify(session).toLowerCase().includes(filter));
  const body = $("#session-table-body");
  if (!rows.length) {
    body.innerHTML = `<tr><td colspan="5" class="empty-state">${filter ? "没有匹配的会话" : "当前没有活动会话"}</td></tr>`;
    return;
  }
  body.innerHTML = rows.map((session) => {
    const target = session.host || session.destination_ip || "—";
    const targetAddress = session.host && session.destination_ip ? session.destination_ip : "";
    const route = (session.chains || []).join(" → ") || session.rule || "—";
    const traffic = session.service === "shellcrash"
      ? `↑ ${formatBytes(session.upload || 0)} · ↓ ${formatBytes(session.download || 0)}`
      : `${session.timeout_seconds || 0}s`;
    return `<tr>
      <td><span class="address">${escapeHTML((session.network || "—").toUpperCase())}</span><span class="subvalue">${escapeHTML(session.state || session.service)}</span></td>
      <td><span class="address">${escapeHTML(session.source_ip || "—")}:${escapeHTML(session.source_port || "")}</span></td>
      <td><span class="address">${escapeHTML(target)}:${escapeHTML(session.destination_port || "")}</span><span class="subvalue">${escapeHTML(targetAddress)}</span></td>
      <td>${escapeHTML(route)}<span class="subvalue">${escapeHTML(session.rule_payload || "")}</span></td>
      <td>${escapeHTML(traffic)}<span class="subvalue">${escapeHTML(formatTime(session.started_at))}</span></td>
    </tr>`;
  }).join("");
}

$("#session-filter").addEventListener("input", renderSessions);
$$('[data-service]').forEach((button) => button.addEventListener("click", async () => {
  state.sessionService = button.dataset.service;
  $$('[data-service]').forEach((item) => item.classList.toggle("active", item === button));
  await refreshSessions();
}));

async function refreshLogs() {
  try {
    const data = await api(`/api/logs?service=${encodeURIComponent(state.logService)}`);
    renderLogs(data);
  } catch (err) {
    $("#log-output").innerHTML = `<div class="log-line"><span class="log-level error">ERROR</span><span>${escapeHTML(err.message)}</span></div>`;
  }
}

function renderLogs(data) {
  const audit = (data.audit || []).map((entry) => ({ time: entry.time, level: entry.level, source: entry.source, message: entry.message }));
  const system = (data.system || []).map((message) => ({ time: "", level: "system", source: "router", message }));
  const lines = [...audit, ...system];
  const output = $("#log-output");
  if (!lines.length) {
    output.innerHTML = '<div class="log-line muted">暂无相关日志</div>';
    return;
  }
  output.innerHTML = lines.map((entry) => `<div class="log-line ${entry.level === "system" ? "system-log" : ""}">
    <span class="log-time">${escapeHTML(formatTime(entry.time))}</span>
    <span class="log-level ${escapeHTML(entry.level)}">${escapeHTML(entry.level)}</span>
    <span>[${escapeHTML(entry.source)}] ${escapeHTML(entry.message)}</span>
  </div>`).join("");
  output.scrollTop = output.scrollHeight;
}

$$('[data-log-service]').forEach((button) => button.addEventListener("click", async () => {
  state.logService = button.dataset.logService;
  $$('[data-log-service]').forEach((item) => item.classList.toggle("active", item === button));
  await refreshLogs();
}));

$("#diagnostics-button").addEventListener("click", async () => {
  try {
    const data = await api("/api/diagnostics");
    await navigator.clipboard.writeText(JSON.stringify(data, null, 2));
    toast("诊断信息已复制，敏感凭据不会包含在内");
  } catch (err) {
    toast(`复制失败：${err.message}`, true);
  }
});

async function refreshAll() {
  await Promise.all([refreshStatus(), refreshSessions(), refreshLogs()]);
}

function toast(message, isError = false) {
  const element = $("#toast");
  element.textContent = message;
  element.className = `toast visible${isError ? " error" : ""}`;
  clearTimeout(state.toastTimer);
  state.toastTimer = setTimeout(() => { element.className = "toast"; }, 4500);
}

function formatBytes(value) {
  if (!Number.isFinite(Number(value)) || Number(value) <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let number = Number(value), index = 0;
  while (number >= 1024 && index < units.length - 1) { number /= 1024; index += 1; }
  return `${number.toFixed(number >= 100 || index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatKiB(value) { return formatBytes(Number(value || 0) * 1024); }

function formatUptime(seconds) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  return days > 0 ? `${days} 天 ${hours} 小时` : `${hours} 小时`;
}

function formatTime(value) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString("zh-CN", { hour12: false });
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}

document.addEventListener("visibilitychange", () => {
  state.visible = !document.hidden;
  $("#live-indicator").classList.toggle("paused", !state.visible);
  $("#live-indicator").lastChild.textContent = state.visible ? "实时刷新" : "已暂停";
  if (state.visible && state.csrf) refreshAll();
});

setInterval(() => { if (state.visible && state.csrf && !state.busy) refreshStatus(); }, 2000);
setInterval(() => { if (state.visible && state.csrf && !state.busy) refreshSessions(); }, 2000);
setInterval(() => { if (state.visible && state.csrf && !state.busy) refreshLogs(); }, 5000);

restoreSession();
