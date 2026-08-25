"use strict";

const state = {
  eventCursor: "",
  diagnosticCursor: "",
  confirmationToken: "",
  previewCutoff: "",
  refreshTimer: null,
  clockTimer: null,
  refreshInFlight: false,
  refreshErrorShown: false,
  systemWindow: "1h",
  systemHistoryData: null,
};

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

function toast(message) {
  const element = $("#toast");
  if (!element) return;
  const msgElement = $("#toast-message") || element;
  msgElement.textContent = message;
  element.classList.remove("hidden");
  clearTimeout(element._timer);
  element._timer = setTimeout(() => element.classList.add("hidden"), 4500);
}

function formatBytes(bytes, decimals = 1) {
  if (bytes === 0 || !bytes) return "0 B";
  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i] || "B"}`;
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    credentials: "same-origin",
    cache: "no-store",
    ...options,
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      message = body.error || message;
    } catch (_) {}
    throw new Error(message);
  }
  return response;
}

function gmt8Parts(date = new Date()) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai",
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  return Object.fromEntries(parts.map(({ type, value }) => [type, value]));
}

function gmt8NowDisplay() {
  const p = gmt8Parts();
  return `${Number(p.year)}-${Number(p.month)}-${Number(p.day)} ${p.hour}:${p.minute}:${p.second}`;
}

function inputToAPI(value) {
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})$/);
  if (!match) return "";
  return `${match[1]}-${Number(match[2])}-${Number(match[3])} ${match[4]}:${match[5]}:${match[6]}`;
}

function partsToInput(parts) {
  return `${parts.year}-${String(parts.month).padStart(2, "0")}-${String(parts.day).padStart(2, "0")}T${String(parts.hour).padStart(2, "0")}:${String(parts.minute).padStart(2, "0")}:${String(parts.second).padStart(2, "0")}`;
}

function shiftedHours(hours, now = new Date()) {
  return partsToInput(gmt8Parts(new Date(now.getTime() + hours * 3600000)));
}

function setQuickRange(value) {
  const form = $("#event-filters");
  if (!form || value === "custom") return;
  const now = new Date();
  const hours = { "1h": -1, "24h": -24, "7d": -168 }[value];
  if (form.elements.from) form.elements.from.value = shiftedHours(hours, now);
  if (form.elements.to) form.elements.to.value = partsToInput(gmt8Parts(now));
}

function filterParams(form, includeCursor = false) {
  const params = new URLSearchParams();
  for (const name of ["status", "group_id", "plugin"]) {
    if (form.elements[name]?.value) params.set(name, form.elements[name].value);
  }
  const from = inputToAPI(form.elements.from?.value || "");
  const to = inputToAPI(form.elements.to?.value || "");
  if (from) params.set("from", from);
  if (to) params.set("to", to);
  params.set("limit", "50");
  if (includeCursor && state.eventCursor) params.set("cursor", state.eventCursor);
  return params;
}

function textCell(value, className = "") {
  const cell = document.createElement("td");
  if (className) cell.className = className;
  const str = value || "—";
  cell.textContent = str;
  cell.title = str;
  return cell;
}

function statusBadge(status) {
  const span = document.createElement("span");
  span.className = `badge ${status}`;
  span.textContent = status === "success" ? "成功" : "失败";
  return span;
}

async function loadEvents({ append = false } = {}) {
  const form = $("#event-filters");
  if (!append && form.elements.quick_time.value !== "custom") {
    setQuickRange(form.elements.quick_time.value);
  }
  const params = filterParams(form, append);
  const [eventsResponse, statsResponse] = await Promise.all([
    api(`/api/events?${params}`),
    append ? Promise.resolve(null) : api(`/api/stats?${params}`),
  ]);
  const page = await eventsResponse.json();
  const body = $("#event-rows");
  if (!append) body.replaceChildren();
  for (const item of page.items || []) {
    const row = document.createElement("tr");
    const status = document.createElement("td");
    status.append(statusBadge(item.status));
    row.append(
      textCell(item.time_display),
      status,
      textCell(item.plugin_name || item.module_name),
      textCell(item.group_id ? `${item.group_id} / ${item.user_id || "—"}` : `私聊 / ${item.user_id || "—"}`),
      textCell(item.request_summary, "summary"),
      textCell(item.response_summary, "summary"),
      textCell(item.duration_ms == null ? "—" : `${item.duration_ms} ms`),
    );
    row.addEventListener("click", () => showEventDetail(item.id));
    body.append(row);
  }
  state.eventCursor = page.next_cursor || "";
  $("#load-more").classList.toggle("hidden", !state.eventCursor);
  $("#event-empty").classList.toggle("hidden", body.children.length > 0);
  if (statsResponse) {
    const stats = await statsResponse.json();
    $("#stat-total").textContent = stats.total;
    $("#stat-success").textContent = stats.success;
    $("#stat-failure").textContent = stats.failure;
    $("#stat-rate").textContent = stats.total ? `${(stats.success_rate * 100).toFixed(1)}%` : "—";
  }
}

function createDetailCard(title, content, options = {}) {
  const card = document.createElement("div");
  card.className = `detail-card ${options.className || ""}`;

  const header = document.createElement("div");
  header.className = "detail-card-header";

  const titleEl = document.createElement("span");
  titleEl.className = "detail-card-title";
  titleEl.textContent = title;
  header.append(titleEl);

  if (options.copyable !== false && content && content !== "—") {
    const copyBtn = document.createElement("button");
    copyBtn.className = "copy-btn";
    copyBtn.type = "button";
    copyBtn.innerHTML = `
      <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
      复制
    `;
    copyBtn.addEventListener("click", () => {
      navigator.clipboard.writeText(content).then(() => toast("已复制到剪贴板")).catch(() => toast("复制失败"));
    });
    header.append(copyBtn);
  }

  const body = document.createElement("div");
  body.className = `detail-card-body ${options.mono ? "font-mono" : ""}`;
  body.textContent = content || "—";

  card.append(header, body);
  return card;
}

async function showEventDetail(id) {
  const detail = await (await api(`/api/events/${id}`)).json();
  if ($("#detail-eyebrow")) $("#detail-eyebrow").textContent = "RESPONSE DETAIL";
  $("#detail-title").textContent = `${detail.plugin_name || detail.module_name || "未知插件"} · ${detail.status === "success" ? "成功" : "失败"}`;
  
  // 1. Meta Grid
  const fields = {
    "开始时间（GMT+8）": detail.time_display,
    "结束时间（GMT+8）": detail.finished_display,
    "模块": detail.module_name,
    "Matcher": `${detail.matcher_type || "—"}${detail.matcher_lineno ? `:${detail.matcher_lineno}` : ""}`,
    "群 / 用户": `${detail.group_id || "私聊"} / ${detail.user_id || "—"}`,
    "耗时": detail.duration_ms != null ? `${detail.duration_ms} ms` : "—",
    "发送状态": `${detail.send_success_count}/${detail.send_count} 成功${detail.send_failure_count ? `，${detail.send_failure_count} 失败` : ""}`,
    "消息 ID": detail.source_message_id,
    "run_id": detail.run_id,
  };
  const meta = $("#detail-meta");
  meta.replaceChildren();
  for (const [label, value] of Object.entries(fields)) {
    const box = document.createElement("div");
    const title = document.createElement("span");
    title.textContent = label;
    box.append(title, document.createTextNode(value || "—"));
    meta.append(box);
  }

  // 2. Sections: Full Input & Output Cards
  const sections = $("#detail-sections");
  if (sections) {
    sections.replaceChildren();
    sections.append(createDetailCard("📥 用户输入内容 (Request)", detail.request_summary || "（无输入内容）"));
    sections.append(createDetailCard("📤 机器人响应内容 (Response)", detail.response_summary || "（无响应内容）"));

    if (detail.status === "failure" || detail.error_type || detail.error_message) {
      const errorText = [detail.error_type, detail.error_message].filter(Boolean).join(": ") || "未知错误";
      sections.append(createDetailCard("⚠️ 错误详情 (Error Detail)", errorText, { className: "error-card", mono: true }));
    }
  }

  // 3. Raw Diagnostics Container (only show when has_full_diagnostics is true!)
  const rawActions = $("#raw-actions");
  const rawWrapper = $("#raw-content-wrapper");
  if (rawActions) rawActions.replaceChildren();

  if (detail.has_full_diagnostics && rawActions && rawWrapper) {
    rawActions.classList.remove("hidden");
    rawWrapper.classList.remove("hidden");
    if ($("#raw-toolbar-title")) $("#raw-toolbar-title").textContent = "Payload & Logs";
    $("#raw-content").textContent = "点击上方按钮查看完整原始数据。";
    if ($("#copy-raw-btn")) $("#copy-raw-btn").classList.add("hidden");

    for (const [part, label] of [["input", "完整输入"], ["output", "完整输出"], ["logs", "完整日志"]]) {
      const view = document.createElement("button");
      view.textContent = `查看${label}`;
      view.addEventListener("click", () => loadRaw(`/api/events/${id}/raw/${part}`));
      const download = document.createElement("a");
      download.href = `/api/events/${id}/raw/${part}`;
      download.textContent = `下载${label}`;
      download.className = "button-link";
      rawActions.append(view, download);
    }
  } else if (rawActions && rawWrapper) {
    rawActions.classList.add("hidden");
    rawWrapper.classList.add("hidden");
  }

  $("#detail-dialog").showModal();
}

async function loadRaw(path) {
  const target = $("#raw-content");
  const copyBtn = $("#copy-raw-btn");
  target.textContent = "正在加载完整内容…";
  if (copyBtn) copyBtn.classList.add("hidden");
  try {
    const text = await (await api(path)).text();
    try {
      const json = JSON.parse(text);
      target.textContent = JSON.stringify(json, null, 2);
    } catch (_) {
      target.textContent = text;
    }
    if (copyBtn) copyBtn.classList.remove("hidden");
  } catch (error) {
    target.textContent = `加载失败：${error.message}`;
  }
}

async function loadFilters() {
  const data = await (await api("/api/filters")).json();
  for (const selector of ['#event-filters select[name="group_id"]']) {
    const select = $(selector);
    if (!select) continue;
    for (const value of data.groups || []) select.add(new Option(value, value));
  }
  for (const selector of ['#event-filters select[name="plugin"]', '#diagnostic-filters select[name="plugin"]']) {
    const select = $(selector);
    if (!select) continue;
    for (const value of data.plugins || []) select.add(new Option(value, value));
  }
}

async function loadBotStatus() {
  const data = await (await api("/api/bot-status")).json();
  const element = $("#bot-status");
  element.className = `status ${data.online ? "online" : "offline"}`;
  const textSpan = element.querySelector(".status-text") || element.lastElementChild;
  textSpan.textContent = data.online ? "在线" : "离线";
}

async function loadDiagnostics({ append = false } = {}) {
  const form = $("#diagnostic-filters");
  const params = new URLSearchParams({ limit: "50" });
  if (form.elements.level.value) params.set("level", form.elements.level.value);
  if (form.elements.plugin.value) params.set("plugin", form.elements.plugin.value);
  if (append && state.diagnosticCursor) params.set("cursor", state.diagnosticCursor);
  const page = await (await api(`/api/diagnostics?${params}`)).json();
  const body = $("#diagnostic-rows");
  if (!append) body.replaceChildren();
  for (const item of page.items || []) {
    const row = document.createElement("tr");
    const level = document.createElement("td");
    const badge = document.createElement("span");
    badge.className = `badge ${item.level}`;
    badge.textContent = item.level;
    level.append(badge);
    row.append(
      textCell(item.time_display),
      level,
      textCell(item.plugin_name || item.module_name || item.logger_name),
      textCell(item.message_summary, "summary"),
    );
    row.addEventListener("click", async () => {
      if ($("#detail-eyebrow")) $("#detail-eyebrow").textContent = "DIAGNOSTIC LOG";
      $("#detail-title").textContent = `${item.level} · ${item.plugin_name || item.module_name || "系统"}`;
      
      const meta = $("#detail-meta");
      meta.replaceChildren();
      const fields = {
        "记录时间（GMT+8）": item.time_display,
        "日志级别": item.level,
        "插件 / 模块": item.plugin_name || item.module_name || "—",
        "Logger": item.logger_name || "—",
      };
      for (const [label, value] of Object.entries(fields)) {
        const box = document.createElement("div");
        const title = document.createElement("span");
        title.textContent = label;
        box.append(title, document.createTextNode(value || "—"));
        meta.append(box);
      }

      const sections = $("#detail-sections");
      if (sections) sections.replaceChildren();

      const rawActions = $("#raw-actions");
      const rawWrapper = $("#raw-content-wrapper");
      if (rawActions) {
        rawActions.replaceChildren();
        rawActions.classList.add("hidden");
      }
      if (rawWrapper) {
        rawWrapper.classList.remove("hidden");
      }
      if ($("#raw-toolbar-title")) $("#raw-toolbar-title").textContent = "完整诊断记录 (Traceback & Logs)";

      $("#detail-dialog").showModal();
      await loadRaw(`/api/diagnostics/${item.id}`);
    });
    body.append(row);
  }
  state.diagnosticCursor = page.next_cursor || "";
  $("#diagnostic-more").classList.toggle("hidden", !state.diagnosticCursor);
  $("#diagnostic-empty").classList.toggle("hidden", body.children.length > 0);
}

function updateClock() {
  $("#current-time").textContent = `${gmt8NowDisplay()} · GMT+8`;
}

function startClock() {
  clearInterval(state.clockTimer);
  updateClock();
  state.clockTimer = setInterval(updateClock, 1000);
}

async function loadStorage() {
  const data = await (await api("/api/storage")).json();
  $("#size-db").textContent = data.database.display;
  $("#size-wal").textContent = `${data.wal.display} / ${data.shm.display}`;
  $("#size-spool").textContent = `${data.spool.display} · ${data.spool_files} 个文件`;
  $("#record-count").textContent = `${data.response_count} 响应 / ${data.diagnostic_count} 诊断`;
}

async function loadSystemStatus() {
  const status = await (await api("/api/system/status")).json();
  if ($("#sys-hostname")) $("#sys-hostname").textContent = status.hostname || "—";
  if ($("#sys-os-kernel")) $("#sys-os-kernel").textContent = `${status.os} / ${status.kernel} (${status.arch})`;
  if ($("#sys-cpu-model")) $("#sys-cpu-model").textContent = status.cpu_model || `${status.cpu_cores} 核`;
  if ($("#sys-uptime-system")) $("#sys-uptime-system").textContent = status.system_uptime_desc || "—";
  if ($("#sys-uptime-proc")) $("#sys-uptime-proc").textContent = status.process_uptime_desc || "—";

  const curr = status.current || {};
  if ($("#sys-cpu-pct")) $("#sys-cpu-pct").textContent = `${(curr.cpu_percent || 0).toFixed(1)}%`;
  if ($("#sys-cpu-bar")) $("#sys-cpu-bar").style.width = `${Math.min(100, Math.max(0, curr.cpu_percent || 0))}%`;
  if ($("#sys-cpu-cores-badge")) $("#sys-cpu-cores-badge").textContent = `${status.cpu_cores} 核`;

  if ($("#sys-mem-pct")) $("#sys-mem-pct").textContent = `${(curr.memory_percent || 0).toFixed(1)}%`;
  if ($("#sys-mem-bar")) $("#sys-mem-bar").style.width = `${Math.min(100, Math.max(0, curr.memory_percent || 0))}%`;
  if ($("#sys-mem-text")) $("#sys-mem-text").textContent = `${formatBytes(curr.memory_used_bytes)} / ${formatBytes(curr.memory_total_bytes)}`;
  if ($("#sys-swap-text")) $("#sys-swap-text").textContent = `${formatBytes(curr.swap_used_bytes)} / ${formatBytes(curr.swap_total_bytes)}`;

  if ($("#sys-disk-pct")) $("#sys-disk-pct").textContent = `${(curr.disk_percent || 0).toFixed(1)}%`;
  if ($("#sys-disk-bar")) $("#sys-disk-bar").style.width = `${Math.min(100, Math.max(0, curr.disk_percent || 0))}%`;
  if ($("#sys-disk-text")) $("#sys-disk-text").textContent = `${formatBytes(curr.disk_used_bytes)} / ${formatBytes(curr.disk_total_bytes)}`;

  if ($("#sys-load-text")) $("#sys-load-text").textContent = `${(curr.load1 || 0).toFixed(2)} / ${(curr.load5 || 0).toFixed(2)} / ${(curr.load15 || 0).toFixed(2)}`;
  if ($("#sys-procs-text")) $("#sys-procs-text").textContent = `${curr.process_count || 0}`;
  if ($("#sys-goroutines-text")) $("#sys-goroutines-text").textContent = `${status.goroutines || 0} (${formatBytes(status.go_heap_alloc_bytes)})`;

  if ($("#sys-net-rx")) $("#sys-net-rx").textContent = `↓ ${formatBytes(curr.net_rx_bytes_per_sec)}/s`;
  if ($("#sys-net-tx")) $("#sys-net-tx").textContent = `↑ ${formatBytes(curr.net_tx_bytes_per_sec)}/s`;
}

async function loadSystemHistory(window = state.systemWindow) {
  const data = await (await api(`/api/system/history?window=${window}`)).json();
  state.systemHistoryData = data;
  const points = data.points || [];
  const fromMs = data.from_ms;
  const toMs = data.to_ms;

  if (points.length > 0) {
    const last = points[points.length - 1];
    let maxCPU = 0;
    for (const p of points) {
      if (p.cpu_max > maxCPU) maxCPU = p.cpu_max;
      if (p.cpu_percent > maxCPU) maxCPU = p.cpu_percent;
    }
    if ($("#chart-cpu-stat")) $("#chart-cpu-stat").textContent = `当前: ${last.cpu_percent.toFixed(1)}% | 峰值: ${maxCPU.toFixed(1)}%`;
    if ($("#chart-mem-stat")) $("#chart-mem-stat").textContent = `当前: ${last.memory_percent.toFixed(1)}% (${formatBytes(last.memory_used_bytes)})`;
    if ($("#chart-net-stat")) $("#chart-net-stat").textContent = `下行: ${formatBytes(last.net_rx_bytes_per_sec)}/s | 上行: ${formatBytes(last.net_tx_bytes_per_sec)}/s`;
  }

  // Render CPU Chart
  renderSVGChart("chart-cpu-container", {
    points,
    fromMs,
    toMs,
    window,
    series: [
      { key: "cpu_percent", color: "#38bdf8", gradientId: "grad-cpu", label: "CPU 使用率", unit: "%" },
    ],
    yMin: 0,
    yMax: 100,
    gridSteps: 4,
    formatY: v => `${Math.round(v)}%`,
    formatTooltip: (p) => `${p.time_display}<br>CPU 使用率: <b>${p.cpu_percent.toFixed(1)}%</b> (峰值: ${p.cpu_max.toFixed(1)}%)`,
  });

  // Render Memory Chart
  renderSVGChart("chart-mem-container", {
    points,
    fromMs,
    toMs,
    window,
    series: [
      { key: "memory_percent", color: "#c084fc", gradientId: "grad-mem", label: "内存使用率", unit: "%" },
    ],
    yMin: 0,
    yMax: 100,
    gridSteps: 4,
    formatY: v => `${Math.round(v)}%`,
    formatTooltip: (p) => `${p.time_display}<br>内存占用: <b>${p.memory_percent.toFixed(1)}%</b> (${formatBytes(p.memory_used_bytes)})`,
  });

  // Render Network Chart
  let maxNet = 1024;
  for (const p of points) {
    if (p.net_rx_bytes_per_sec > maxNet) maxNet = p.net_rx_bytes_per_sec;
    if (p.net_tx_bytes_per_sec > maxNet) maxNet = p.net_tx_bytes_per_sec;
  }

  function niceNetCeiling(val) {
    const steps = [
      1024, // 1 KB/s
      4 * 1024, // 4 KB/s
      8 * 1024, // 8 KB/s
      16 * 1024, // 16 KB/s
      32 * 1024, // 32 KB/s
      64 * 1024, // 64 KB/s
      128 * 1024, // 128 KB/s
      256 * 1024, // 256 KB/s
      512 * 1024, // 512 KB/s
      1024 * 1024, // 1 MB/s
      2 * 1024 * 1024, // 2 MB/s
      4 * 1024 * 1024, // 4 MB/s
      8 * 1024 * 1024, // 8 MB/s
      16 * 1024 * 1024, // 16 MB/s
      32 * 1024 * 1024, // 32 MB/s
      64 * 1024 * 1024, // 64 MB/s
      128 * 1024 * 1024, // 128 MB/s
    ];
    for (const s of steps) {
      if (val <= s) return s;
    }
    return Math.ceil(val / (1024 * 1024 * 4)) * (1024 * 1024 * 4);
  }

  const roundedMaxNet = niceNetCeiling(maxNet * 1.05);

  renderSVGChart("chart-net-container", {
    points,
    fromMs,
    toMs,
    window,
    series: [
      { key: "net_rx_bytes_per_sec", color: "#2dd4bf", gradientId: "grad-rx", label: "下行 (RX)", unit: "B/s" },
      { key: "net_tx_bytes_per_sec", color: "#c084fc", gradientId: "grad-tx", label: "上行 (TX)", unit: "B/s" },
    ],
    yMin: 0,
    yMax: roundedMaxNet,
    gridSteps: 4,
    formatY: v => v === 0 ? "0 B/s" : `${formatBytes(v, 0)}/s`,
    formatTooltip: (p) => `${p.time_display}<br>下行速率: <b>${formatBytes(p.net_rx_bytes_per_sec)}/s</b><br>上行速率: <b>${formatBytes(p.net_tx_bytes_per_sec)}/s</b>`,
  });
}

function renderSVGChart(containerId, options) {
  const container = $(`#${containerId}`);
  if (!container) return;

  const {
    points = [],
    series = [],
    fromMs = (Date.now() - 3600000),
    toMs = Date.now(),
    window = "1h",
    yMin = 0,
    yMax = 100,
    gridSteps = 4,
    formatY = (v) => v,
    formatTooltip,
  } = options;

  const containerRect = container.getBoundingClientRect();
  const svgWidth = Math.max(280, Math.floor(containerRect.width || container.clientWidth || 600));
  const svgHeight = 200;
  const padLeft = 68;
  const padRight = 18;
  const padTop = 16;
  const padBottom = 28;

  const plotWidth = Math.max(10, svgWidth - padLeft - padRight);
  const plotHeight = Math.max(10, svgHeight - padTop - padBottom);

  const timeSpan = Math.max(1000, toMs - fromMs);
  const yRange = yMax - yMin || 1;

  const getX = (timestampMs) => {
    const clamped = Math.max(fromMs, Math.min(toMs, timestampMs || fromMs));
    return padLeft + ((clamped - fromMs) / timeSpan) * plotWidth;
  };

  const getY = (val) => {
    const clamped = Math.max(yMin, Math.min(yMax, val || 0));
    return padTop + plotHeight - ((clamped - yMin) / yRange) * plotHeight;
  };

  const isLight = document.documentElement.getAttribute("data-theme") === "light";
  const gridStroke = isLight ? "rgba(0, 0, 0, 0.07)" : "rgba(255, 255, 255, 0.06)";
  const baselineStroke = isLight ? "rgba(0, 0, 0, 0.16)" : "rgba(255, 255, 255, 0.18)";
  const tickStroke = isLight ? "rgba(0, 0, 0, 0.25)" : "rgba(255, 255, 255, 0.3)";
  const textFill = isLight ? "#64748b" : "#8493a8";
  const crosshairStroke = isLight ? "rgba(0, 0, 0, 0.35)" : "rgba(255, 255, 255, 0.4)";

  // Build Horizontal Grid Lines & Y Labels
  let gridSVG = "";
  for (let i = 0; i <= gridSteps; i++) {
    const val = yMin + (i / gridSteps) * yRange;
    const yPos = getY(val);
    if (i > 0 && i < gridSteps) {
      gridSVG += `
        <line x1="${padLeft}" y1="${yPos}" x2="${svgWidth - padRight}" y2="${yPos}" stroke="${gridStroke}" stroke-dasharray="4 4" />
      `;
    }
    gridSVG += `
      <text x="${padLeft - 8}" y="${yPos + 3.5}" fill="${textFill}" font-size="11" text-anchor="end" font-family="'JetBrains Mono', monospace">${formatY(val)}</text>
    `;
  }

  // Coordinate Frame Baselines (Left Y-axis line and Bottom X-axis line)
  const baselineFrameSVG = `
    <line x1="${padLeft}" y1="${padTop}" x2="${padLeft}" y2="${padTop + plotHeight}" stroke="${baselineStroke}" stroke-width="1.2" />
    <line x1="${padLeft}" y1="${padTop + plotHeight}" x2="${svgWidth - padRight}" y2="${padTop + plotHeight}" stroke="${baselineStroke}" stroke-width="1.2" />
  `;

  // Fixed X Time Ticks & Time Labels based on the actual window [fromMs, toMs]
  let xLabelsSVG = "";
  const xTickCount = svgWidth < 420 ? 2 : (svgWidth < 680 ? 3 : 4);
  for (let i = 0; i <= xTickCount; i++) {
    const tickMs = fromMs + (i / xTickCount) * timeSpan;
    const xPos = padLeft + (i / xTickCount) * plotWidth;
    const anchor = i === 0 ? "start" : i === xTickCount ? "end" : "middle";

    const tickDate = new Date(tickMs);
    const pParts = new Intl.DateTimeFormat("en-CA", {
      timeZone: "Asia/Shanghai",
      month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit",
      hourCycle: "h23",
    }).formatToParts(tickDate);
    const parts = Object.fromEntries(pParts.map(({ type, value }) => [type, value]));

    let timeText = `${parts.hour}:${parts.minute}`;
    if (window === "24h" || window === "7d") {
      timeText = `${parts.month}-${parts.day} ${parts.hour}:${parts.minute}`;
    } else if (window === "30m" || window === "1h") {
      timeText = `${parts.hour}:${parts.minute}:${parts.second}`;
    }

    xLabelsSVG += `
      <line x1="${xPos}" y1="${padTop + plotHeight}" x2="${xPos}" y2="${padTop + plotHeight + 5}" stroke="${tickStroke}" stroke-width="1.2" />
      <text x="${xPos}" y="${padTop + plotHeight + 19}" fill="${textFill}" font-size="11" text-anchor="${anchor}" font-family="'JetBrains Mono', monospace">${timeText}</text>
    `;
  }

  // Build Gradients & Series Paths
  let defsSVG = "";
  let pathsSVG = "";

  if (points.length === 0) {
    pathsSVG = `<text x="${padLeft + plotWidth / 2}" y="${padTop + plotHeight / 2}" fill="${textFill}" font-size="12" text-anchor="middle" font-family="'JetBrains Mono', monospace">暂无时序数据</text>`;
  } else {
    series.forEach((s) => {
      const gradId = `${containerId}-${s.gradientId}`;
      defsSVG += `
        <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="${s.color}" stop-opacity="${isLight ? '0.22' : '0.35'}" />
          <stop offset="100%" stop-color="${s.color}" stop-opacity="0.0" />
        </linearGradient>
      `;

      const pts = points.map((p) => ({ x: getX(p.timestamp_ms), y: getY(p[s.key]) }));

      if (pts.length === 1) {
        pathsSVG += `<circle cx="${pts[0].x.toFixed(1)}" cy="${pts[0].y.toFixed(1)}" r="3.5" fill="${s.color}" />`;
      } else {
        // Area Path (anchored from first point's X to last point's X)
        let areaD = `M ${pts[0].x.toFixed(1)} ${(padTop + plotHeight).toFixed(1)}`;
        pts.forEach((pt) => {
          areaD += ` L ${pt.x.toFixed(1)} ${pt.y.toFixed(1)}`;
        });
        areaD += ` L ${pts[pts.length - 1].x.toFixed(1)} ${(padTop + plotHeight).toFixed(1)} Z`;

        // Line Path
        let lineD = `M ${pts[0].x.toFixed(1)} ${pts[0].y.toFixed(1)}`;
        for (let i = 1; i < pts.length; i++) {
          lineD += ` L ${pts[i].x.toFixed(1)} ${pts[i].y.toFixed(1)}`;
        }

        pathsSVG += `
          <path d="${areaD}" fill="url(#${gradId})" />
          <path d="${lineD}" fill="none" stroke="${s.color}" stroke-width="2.2" stroke-linejoin="round" stroke-linecap="round" />
        `;
      }
    });
  }

  const svgHTML = `
    <svg width="${svgWidth}" height="${svgHeight}" viewBox="0 0 ${svgWidth} ${svgHeight}" style="display: block; width: 100%; height: 100%;">
      <defs>${defsSVG}</defs>
      ${gridSVG}
      ${baselineFrameSVG}
      ${pathsSVG}
      ${xLabelsSVG}
      <line id="${containerId}-crosshair" x1="0" y1="${padTop}" x2="0" y2="${padTop + plotHeight}" stroke="${crosshairStroke}" stroke-dasharray="3 3" style="display: none;" />
      ${series.map(s => `<circle id="${containerId}-dot-${s.key}" r="4.5" fill="${s.color}" stroke="#ffffff" stroke-width="1.8" style="display: none;" />`).join("")}
      <rect id="${containerId}-overlay" x="${padLeft}" y="${padTop}" width="${plotWidth}" height="${plotHeight}" fill="transparent" style="cursor: crosshair;" />
    </svg>
    <div id="${containerId}-tooltip" class="chart-tooltip-floating font-mono" style="display: none; opacity: 0;"></div>
  `;

  container.innerHTML = svgHTML;

  // Interactive crosshair & hover tooltip by nearest timestamp
  const overlay = $(`#${containerId}-overlay`, container);
  const crosshair = $(`#${containerId}-crosshair`, container);
  const tooltip = $(`#${containerId}-tooltip`, container);

  if (overlay && tooltip && points.length > 0) {
    const handleMove = (clientX) => {
      const rect = container.getBoundingClientRect();
      const relativeX = clientX - rect.left - padLeft;
      const hoverPct = Math.max(0, Math.min(1, relativeX / plotWidth));
      const hoverMs = fromMs + hoverPct * timeSpan;

      // Find nearest point
      let closestPoint = points[0];
      let minDiff = Math.abs(points[0].timestamp_ms - hoverMs);
      for (let i = 1; i < points.length; i++) {
        const diff = Math.abs(points[i].timestamp_ms - hoverMs);
        if (diff < minDiff) {
          minDiff = diff;
          closestPoint = points[i];
        }
      }

      if (closestPoint) {
        const ptX = getX(closestPoint.timestamp_ms);

        crosshair.setAttribute("x1", ptX);
        crosshair.setAttribute("x2", ptX);
        crosshair.style.display = "block";

        series.forEach((s) => {
          const dot = $(`#${containerId}-dot-${s.key}`, container);
          if (dot) {
            dot.setAttribute("cx", ptX);
            dot.setAttribute("cy", getY(closestPoint[s.key]));
            dot.style.display = "block";
          }
        });

        tooltip.innerHTML = formatTooltip ? formatTooltip(closestPoint) : `${closestPoint.time_display}`;
        tooltip.style.left = `${Math.max(60, Math.min(svgWidth - 60, ptX))}px`;
        tooltip.style.top = `${getY(closestPoint[series[0].key]) - 10}px`;
        tooltip.style.display = "block";
        tooltip.style.opacity = "1";
      }
    };

    const handleLeave = () => {
      crosshair.style.display = "none";
      series.forEach((s) => {
        const dot = $(`#${containerId}-dot-${s.key}`, container);
        if (dot) dot.style.display = "none";
      });
      tooltip.style.display = "none";
      tooltip.style.opacity = "0";
    };

    overlay.addEventListener("mousemove", (e) => handleMove(e.clientX));
    overlay.addEventListener("mouseleave", handleLeave);
    overlay.addEventListener("touchmove", (e) => {
      if (e.touches && e.touches[0]) handleMove(e.touches[0].clientX);
    }, { passive: true });
    overlay.addEventListener("touchend", handleLeave);
  }
}

function calendarMonthsAgo(months) {
  const p = gmt8Parts();
  const year = Number(p.year);
  const monthIndex = Number(p.month) - 1 - months;
  const targetYear = year + Math.floor(monthIndex / 12);
  const targetMonth = ((monthIndex % 12) + 12) % 12;
  const lastDay = new Date(Date.UTC(targetYear, targetMonth + 1, 0)).getUTCDate();
  return partsToInput({
    year: targetYear,
    month: targetMonth + 1,
    day: Math.min(Number(p.day), lastDay),
    hour: p.hour,
    minute: p.minute,
    second: p.second,
  });
}

function updateCleanupCutoff() {
  const value = $("#cleanup-period").value;
  if (value !== "custom") $("#cleanup-cutoff").value = calendarMonthsAgo(Number(value));
  state.confirmationToken = "";
  $("#execute-cleanup").disabled = true;
  const preview = $("#cleanup-preview");
  const span = preview.querySelector("span") || preview;
  span.textContent = "截止时间已变化，请重新预览。";
}

async function mutation(path, body) {
  const csrf = await (await api("/api/csrf")).json();
  return api(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrf.csrf_token,
    },
    body: JSON.stringify(body),
  });
}

async function previewCleanup() {
  const cutoff = inputToAPI($("#cleanup-cutoff").value);
  if (!cutoff) throw new Error("请选择完整的 GMT+8 截止时间");
  const data = await (await mutation("/api/storage/cleanup/preview", { cutoff })).json();
  state.confirmationToken = data.confirmation_token;
  state.previewCutoff = cutoff;
  const preview = $("#cleanup-preview");
  const span = preview.querySelector("span") || preview;
  span.textContent = `将删除 ${data.preview.response_count} 条响应和 ${data.preview.diagnostic_count} 条诊断；截止点：${data.preview.cutoff_display} GMT+8。`;
  const execute = $("#execute-cleanup");
  execute.textContent = `清理 ${data.preview.cutoff_display} GMT+8 之前的日志`;
  execute.disabled = false;
}

async function executeCleanup() {
  if (!state.confirmationToken) throw new Error("请先预览清理范围");
  const exact = `将永久删除 ${state.previewCutoff} GMT+8 之前的日志。此操作无法撤销，是否继续？`;
  if (!window.confirm(exact)) return;
  const button = $("#execute-cleanup");
  button.disabled = true;
  const data = await (await mutation("/api/storage/cleanup", {
    cutoff: state.previewCutoff,
    confirmation_token: state.confirmationToken,
  })).json();
  state.confirmationToken = "";
  const preview = $("#cleanup-preview");
  const span = preview.querySelector("span") || preview;
  span.textContent = `完成：删除 ${data.responses_deleted} 条响应和 ${data.diagnostics_deleted} 条诊断。`;
  toast("日志清理和增量空间回收已完成");
  await Promise.all([loadStorage(), loadEvents(), loadDiagnostics()]);
}

async function refreshVisibleView() {
  if (document.hidden || state.refreshInFlight) return;
  state.refreshInFlight = true;
  const activeView = $(".tab.active")?.dataset.view || "events";
  const requests = [loadBotStatus()];
  if (activeView === "events") requests.push(loadEvents());
  if (activeView === "diagnostics") requests.push(loadDiagnostics());
  if (activeView === "storage") requests.push(loadStorage());
  if (activeView === "system") {
    requests.push(loadSystemStatus());
    requests.push(loadSystemHistory(state.systemWindow));
  }
  try {
    await Promise.all(requests);
    state.refreshErrorShown = false;
  } catch (error) {
    if (!state.refreshErrorShown) {
      toast(`自动刷新失败：${error.message}`);
      state.refreshErrorShown = true;
    }
  } finally {
    state.refreshInFlight = false;
  }
}

function startPeriodicRefresh() {
  clearInterval(state.refreshTimer);
  state.refreshTimer = setInterval(refreshVisibleView, 3000);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refreshVisibleView();
  });
}

function bindEvents() {
  $$(".tab").forEach(button => button.addEventListener("click", () => {
    $$(".tab").forEach(item => item.classList.toggle("active", item === button));
    $$(".view").forEach(view => view.classList.toggle("active", view.id === `view-${button.dataset.view}`));
    if (button.dataset.view === "events") loadEvents().catch(error => toast(error.message));
    if (button.dataset.view === "diagnostics") loadDiagnostics().catch(error => toast(error.message));
    if (button.dataset.view === "storage") loadStorage().catch(error => toast(error.message));
    if (button.dataset.view === "system") {
      Promise.all([loadSystemStatus(), loadSystemHistory(state.systemWindow)]).catch(error => toast(error.message));
    }
  }));
  $("#event-filters").addEventListener("submit", event => {
    event.preventDefault();
    state.eventCursor = "";
    loadEvents().catch(error => toast(error.message));
  });
  $('#event-filters select[name="quick_time"]').addEventListener("change", event => setQuickRange(event.target.value));
  for (const name of ["from", "to"]) {
    $(`#event-filters input[name="${name}"]`).addEventListener("input", () => {
      $('#event-filters select[name="quick_time"]').value = "custom";
    });
  }
  $("#clear-filters").addEventListener("click", () => {
    $("#event-filters").reset();
    setQuickRange("24h");
    loadEvents().catch(error => toast(error.message));
  });
  $("#load-more").addEventListener("click", () => loadEvents({ append: true }).catch(error => toast(error.message)));
  $("#diagnostic-filters").addEventListener("submit", event => {
    event.preventDefault();
    state.diagnosticCursor = "";
    loadDiagnostics().catch(error => toast(error.message));
  });
  $("#diagnostic-more").addEventListener("click", () => loadDiagnostics({ append: true }).catch(error => toast(error.message)));
  $("#cleanup-period").addEventListener("change", updateCleanupCutoff);
  $("#cleanup-cutoff").addEventListener("input", () => {
    $("#cleanup-period").value = "custom";
    updateCleanupCutoff();
  });
  $("#preview-cleanup").addEventListener("click", () => previewCleanup().catch(error => toast(error.message)));
  $("#execute-cleanup").addEventListener("click", () => executeCleanup().catch(error => toast(error.message)));
  $("#reclaim-storage").addEventListener("click", async () => {
    try {
      await mutation("/api/storage/reclaim", {});
      toast("增量空间回收完成");
      await loadStorage();
    } catch (error) { toast(error.message); }
  });

  // Chart range selector buttons
  $$("#chart-range-selector .chart-range-btn").forEach(button => {
    button.addEventListener("click", () => {
      $$("#chart-range-selector .chart-range-btn").forEach(b => b.classList.toggle("active", b === button));
      state.systemWindow = button.dataset.window || "1h";
      loadSystemHistory(state.systemWindow).catch(error => toast(error.message));
    });
  });

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if ($(".tab.active")?.dataset.view === "system" && state.systemHistoryData) {
        loadSystemHistory(state.systemWindow).catch(() => {});
      }
    }, 150);
  });

  if ($("#copy-raw-btn")) {
    $("#copy-raw-btn").addEventListener("click", () => {
      const text = $("#raw-content").textContent;
      if (text) {
        navigator.clipboard.writeText(text).then(() => toast("已复制到剪贴板")).catch(() => toast("复制失败"));
      }
    });
  }
  if ($("#toggle-event-filters")) {
    $("#toggle-event-filters").addEventListener("click", () => {
      const grid = $("#event-filters .filter-grid");
      const isHidden = getComputedStyle(grid).display === "none";
      grid.style.display = isHidden ? "grid" : "none";
      $("#toggle-event-filters").textContent = isHidden ? "收起筛选" : "展开筛选";
    });
  }
  if ($("#theme-toggle")) {
    $("#theme-toggle").addEventListener("click", toggleTheme);
  }
}

function getTheme() {
  return document.documentElement.getAttribute("data-theme") || "dark";
}

function setTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  localStorage.setItem("webconsole_theme", theme);
  const metaColorScheme = $('meta[name="color-scheme"]');
  if (metaColorScheme) {
    metaColorScheme.setAttribute("content", theme === "light" ? "light dark" : "dark light");
  }
  if ($(".tab.active")?.dataset.view === "system" && state.systemHistoryData) {
    loadSystemHistory(state.systemWindow).catch(() => {});
  }
}

function toggleTheme() {
  const current = getTheme();
  const next = current === "light" ? "dark" : "light";
  setTheme(next);
}

async function initialize() {
  bindEvents();
  startClock();
  setQuickRange("24h");
  updateCleanupCutoff();
  try {
    await Promise.all([loadFilters(), loadBotStatus(), loadEvents(), loadDiagnostics(), loadSystemStatus()]);
  } catch (error) {
    toast(`初始化失败：${error.message}`);
  }
  startPeriodicRefresh();
}

document.addEventListener("DOMContentLoaded", initialize);
