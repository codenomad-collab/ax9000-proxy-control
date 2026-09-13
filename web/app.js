const state = {
  csrf: "",
  status: null,
  metrics: null,
  activeView: "overview",
  sessions: [],
  sessionService: "auto",
  logService: "all",
  busy: false,
  guardBusy: false,
  visible: true,
  toastTimer: null,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

const pageMeta = {
  overview: ["COMMAND CENTER", "运行总览", "先确认当前代理模式和服务健康，再进入具体栏目处理问题。"],
  resources: ["ROUTER HEALTH", "资源监控", "查看 CPU、内存、存储、有线协商状态和关键接口实时速率。"],
  guard: ["AI ROUTE HEALTH", "节点守护", "管理 ChatGPT、Claude 与 GitHub 使用的优质美国线路。"],
  sessions: ["TRAFFIC INSPECTOR", "网络会话", "核对设备、目标域名、命中规则和实际出口路径。"],
  logs: ["OPERATIONS", "运行日志", "查看控制操作、服务运行和路由器故障信息。"],
};

function requestedView() {
  const value = window.location.hash.replace(/^#\/?/, "");
  return pageMeta[value] ? value : "overview";
}

function selectView(view, options = {}) {
  const next = pageMeta[view] ? view : "overview";
  const { updateHash = true, refresh = true } = options;
  state.activeView = next;
  $$('[data-page]').forEach((page) => page.classList.toggle("active", page.dataset.page === next));
  $$('[data-nav-view]').forEach((button) => {
    const active = button.dataset.navView === next;
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  const meta = pageMeta[next];
  $("#page-eyebrow").textContent = meta[0];
  $("#page-title").textContent = meta[1];
  $("#page-description").textContent = meta[2];
  document.title = `${meta[1]} · AX9000 代理控制台`;
  if (updateHash) window.history.replaceState(null, "", `#${next}`);
  window.scrollTo({ top: 0, behavior: "auto" });
  if (refresh && state.csrf) refreshActiveView();
}

$$('[data-nav-view]').forEach((button) => button.addEventListener("click", () => selectView(button.dataset.navView)));
$$('[data-open-view]').forEach((button) => button.addEventListener("click", () => selectView(button.dataset.openView)));
window.addEventListener("hashchange", () => selectView(requestedView(), { updateHash: false }));

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
  selectView(requestedView(), { updateHash: false, refresh: false });
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
$$('[data-guard-action]').forEach((button) => button.addEventListener("click", () => runGuardAction(button.dataset.guardAction)));
$("#guard-refresh").addEventListener("click", refreshNodeGuard);

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

async function runGuardAction(action) {
  if (state.guardBusy) return;
  const labels = { run: "立即检查", enable: "启用每 30 分钟检查", disable: "暂停定时检查" };
  if (action === "disable" && !window.confirm("确认暂停节点定时检查？\n\n暂停后仍可在控制台手动立即检查。")) return;
  setGuardBusy(true);
  toast(`正在${labels[action]}…`);
  try {
    const result = await api("/api/node-guard/action", { method: "POST", body: JSON.stringify({ action }) });
    renderNodeGuard(result.status);
    toast(result.message || "操作完成");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setGuardBusy(false);
    await refreshNodeGuard();
  }
}

function setGuardBusy(value) {
  state.guardBusy = value;
  $$('[data-guard-action]').forEach((button) => { button.disabled = value; });
}

async function refreshNodeGuard() {
  try {
    renderNodeGuard(await api("/api/node-guard"));
  } catch (err) {
    if (!err.message.includes("登录")) toast(`节点守护状态刷新失败：${err.message}`, true);
  }
}

function renderNodeGuard(data) {
  const statusMap = {
    healthy: ["运行正常", "online"],
    degraded: ["线路降级", "warning"],
    attention: ["需要处理", "warning"],
    unknown: ["等待首次检查", "offline"],
    not_installed: ["尚未安装", "offline"],
  };
  const current = statusMap[data.status] || [data.status || "未知", "offline"];
  $("#guard-state").textContent = current[0];
  $("#guard-state").className = `status-pill ${current[1]}`;
  $("#guard-running").textContent = data.running ? "检查中" : "空闲";
  $("#guard-running").className = `status-pill ${data.running ? "warning" : "offline"}`;
  $("#guard-version").textContent = data.version ? `v${data.version}` : data.installed ? "已安装" : "—";
  $("#guard-last-check").textContent = data.last_check ? formatTime(data.last_check) : "尚未检查";
  $("#guard-failures").textContent = data.consecutive_failures || 0;
  $("#guard-schedule").textContent = data.cron_enabled
    ? (data.scheduler_running ? "每 30 分钟 · 生效" : "已配置 · cron 异常")
    : "已暂停";

  $("#overview-guard-summary").textContent = current[0];
  $("#overview-guard-detail").innerHTML = data.last_check
    ? `最近检查 ${escapeHTML(formatTime(data.last_check))} <b>→</b>`
    : '查看优先线路 <b>→</b>';

  const nodes = data.expected_nodes || {};
  $("#guard-primary").textContent = nodes.primary || "—";
  $("#guard-secondary").textContent = nodes.secondary || "—";
  $("#guard-backup").textContent = nodes.backup || "—";
  renderProbe("openai", data.checks && data.checks.openai);
  renderProbe("anthropic", data.checks && data.checks.anthropic);
  renderProbe("github", data.checks && data.checks.github);

  const issue = $("#guard-issue");
  const issueText = data.last_issue || data.message || "";
  issue.textContent = issueText;
  issue.classList.toggle("hidden", !issueText);

  const logs = data.logs || [];
  $("#guard-log-output").innerHTML = logs.length
    ? logs.map((line) => `<div>${escapeHTML(line)}</div>`).join("")
    : '<div class="muted">暂无守护日志；健康检查通常只更新状态文件。</div>';

  const runButton = $('[data-guard-action="run"]');
  runButton.disabled = state.guardBusy || data.running || !data.installed;
  $('[data-guard-action="enable"]').disabled = state.guardBusy || data.cron_enabled || !data.installed;
  $('[data-guard-action="disable"]').disabled = state.guardBusy || !data.cron_enabled;
}

function renderProbe(name, probe) {
  const element = $(`#guard-${name}`);
  if (!probe || !probe.successes) {
    element.textContent = "未通过";
    element.className = "probe-bad";
    return;
  }
  const samples = Array.isArray(probe.samples_ms) ? probe.samples_ms.length : 0;
  element.textContent = `${probe.median_ms || 0} ms · ${probe.successes}/${samples || probe.successes}`;
  element.className = "probe-good";
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
  $("#top-mode").className = `top-mode ${current[3]}`;
  $("#top-mode span").textContent = `${current[0]} · ${data.shellcrash.healthy || data.leigod.healthy ? "运行正常" : mode === "off" ? "服务已停止" : "需要检查"}`;

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
  $("#overview-resource-summary").textContent = `可用 ${formatKiB(data.system.mem_available_kib)} · 负载 ${data.system.load_1.toFixed(2)}`;
}

function setServiceState(name, running, healthy) {
  const element = $(`#${name}-state`);
  element.textContent = running ? (healthy ? "运行正常" : "运行异常") : "已停止";
  element.className = `status-pill ${running ? (healthy ? "online" : "warning") : "offline"}`;
  $(`#${name}-card`).classList.toggle("active-service", running);
}

async function refreshSystemMetrics() {
  try {
    const data = await api("/api/system-metrics");
    state.metrics = data;
    renderSystemMetrics(data);
  } catch (err) {
    const sampleState = $("#resource-sample-state");
    sampleState.textContent = "读取失败";
    sampleState.className = "status-pill warning";
    if (!err.message.includes("登录")) toast(`资源监控刷新失败：${err.message}`, true);
  }
}

function renderSystemMetrics(data) {
  const cpu = data.cpu || {};
  const memory = data.memory || {};
  const interfaces = data.interfaces || [];
  const sampleReady = Boolean(cpu.sample_ready) && interfaces.some((item) => item.sample_ready);
  const sampleState = $("#resource-sample-state");
  sampleState.textContent = sampleReady ? "实时监控" : "建立基线";
  sampleState.className = `status-pill ${sampleReady ? "online" : "offline"}`;

  $("#resource-cpu").textContent = cpu.sample_ready ? formatPercent(cpu.usage_percent) : "采样中";
  $("#resource-cpu-detail").textContent = `${cpu.cores || 0} 核 · 负载 ${Number(cpu.load_1 || 0).toFixed(2)} / ${Number(cpu.load_5 || 0).toFixed(2)} / ${Number(cpu.load_15 || 0).toFixed(2)}`;
  setUsageBar($("#resource-cpu-bar"), cpu.sample_ready ? cpu.usage_percent : 0);

  $("#resource-memory").textContent = formatPercent(memory.usage_percent || 0);
  $("#resource-memory-detail").textContent = `已用 ${formatBytes(memory.used_bytes || 0)} · 可用 ${formatBytes(memory.available_bytes || 0)} · 共 ${formatBytes(memory.total_bytes || 0)}`;
  setUsageBar($("#resource-memory-bar"), memory.usage_percent || 0);
  $("#overview-resource-summary").textContent = `${cpu.sample_ready ? `CPU ${formatPercent(cpu.usage_percent)}` : "CPU 采样中"} · 内存 ${formatPercent(memory.usage_percent || 0)}`;

  const storage = data.storage || [];
  const mounted = storage.filter((item) => item.mounted);
  $("#resource-storage-summary").textContent = mounted.length === storage.length ? `${mounted.length} 个挂载点正常` : `${mounted.length}/${storage.length} 正常`;
  $("#resource-storage-list").innerHTML = storage.length ? storage.map((item) => {
    const percent = Number(item.usage_percent || 0);
    return `<div class="storage-row">
      <div class="storage-name"><strong>${escapeHTML(item.name)}</strong><span>${escapeHTML(item.path)}</span></div>
      <div class="storage-bar"><span class="${usageClass(percent)}" style="width:${item.mounted ? clampPercent(percent) : 0}%"></span></div>
      <div class="storage-value">${item.mounted ? `${formatBytes(item.used_bytes)} / ${formatBytes(item.total_bytes)} · ${formatPercent(percent)}` : "未挂载"}</div>
    </div>`;
  }).join("") : '<div class="resource-detail">未发现存储挂载点</div>';

  const links = data.links || [];
  $("#ethernet-link-list").innerHTML = links.length ? links.map((item) => {
    const slow = item.up && item.speed_mbps > 0 && item.speed_mbps <= 100;
    const duplex = item.duplex === "full" ? "全双工" : item.duplex === "half" ? "半双工" : "双工未知";
    return `<div class="ethernet-link-card${item.up ? "" : " down"}${slow ? " slow" : ""}">
      <div class="ethernet-link-name"><strong>${escapeHTML(item.label || item.name)}</strong><span>${escapeHTML(item.name)} · ${escapeHTML(item.role || "有线接口")}</span></div>
      <div class="ethernet-link-state"><strong>${item.up && item.speed_mbps > 0 ? `${item.speed_mbps}M` : "未连接"}</strong><span>${item.up ? duplex : "无载波"}</span></div>
    </div>`;
  }).join("") : '<div class="network-empty muted">未发现有线互联接口</div>';

  const list = $("#network-interface-list");
  if (!interfaces.length) {
    list.innerHTML = '<div class="network-empty muted">未发现关键网络接口</div>';
    return;
  }
  list.innerHTML = interfaces.map((item) => `<div class="network-interface-row">
    <div class="network-interface-name">
      <span class="interface-state ${item.up ? "up" : ""}"></span>
      <div><strong>${escapeHTML(item.label || item.name)}</strong><span>${escapeHTML(item.name)}</span></div>
    </div>
    <div class="network-rate download"><span>接收 RX</span><strong>${item.sample_ready ? formatRate(item.rx_bytes_per_second) : "采样中"}</strong></div>
    <div class="network-rate upload"><span>发送 TX</span><strong>${item.sample_ready ? formatRate(item.tx_bytes_per_second) : "采样中"}</strong></div>
    <div class="network-total"><span>累计接收 ${formatBytes(item.rx_bytes || 0)}</span><span>累计发送 ${formatBytes(item.tx_bytes || 0)}</span></div>
  </div>`).join("");
}

function setUsageBar(element, value) {
  const percent = clampPercent(Number(value || 0));
  element.style.width = `${percent}%`;
  element.className = usageClass(percent);
}

function usageClass(percent) {
  if (percent >= 90) return "critical";
  if (percent >= 75) return "warning";
  return "";
}

function clampPercent(value) { return Math.max(0, Math.min(100, Number(value) || 0)); }
function formatPercent(value) { return `${clampPercent(value).toFixed(1)}%`; }
function formatRate(value) { return `${formatBytes(value || 0)}/s`; }

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

async function refreshActiveView() {
  switch (state.activeView) {
    case "overview":
      await Promise.all([refreshSystemMetrics(), refreshNodeGuard()]);
      break;
    case "resources":
      await refreshSystemMetrics();
      break;
    case "guard":
      await refreshNodeGuard();
      break;
    case "sessions":
      await refreshSessions();
      break;
    case "logs":
      await refreshLogs();
      break;
  }
}

async function refreshAll() {
  await Promise.all([refreshStatus(), refreshActiveView()]);
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
setInterval(() => { if (state.visible && state.csrf && !state.busy && ["overview", "resources"].includes(state.activeView)) refreshSystemMetrics(); }, 2000);
setInterval(() => { if (state.visible && state.csrf && !state.busy && state.activeView === "sessions") refreshSessions(); }, 2000);
setInterval(() => { if (state.visible && state.csrf && !state.busy && state.activeView === "logs") refreshLogs(); }, 5000);
setInterval(() => { if (state.visible && state.csrf && !state.guardBusy && ["overview", "guard"].includes(state.activeView)) refreshNodeGuard(); }, 5000);

restoreSession();
