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
  cell.textContent = value || "—";
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

async function showEventDetail(id) {
  const detail = await (await api(`/api/events/${id}`)).json();
  $("#detail-title").textContent = `${detail.plugin_name || detail.module_name || "未知插件"} · ${detail.status === "success" ? "成功" : "失败"}`;
  const fields = {
    "开始时间（GMT+8）": detail.time_display,
    "结束时间（GMT+8）": detail.finished_display,
    "模块": detail.module_name,
    "Matcher": `${detail.matcher_type || "—"}${detail.matcher_lineno ? `:${detail.matcher_lineno}` : ""}`,
    "群 / 用户": `${detail.group_id || "私聊"} / ${detail.user_id || "—"}`,
    "消息 ID": detail.source_message_id,
    "发送": `${detail.send_success_count}/${detail.send_count} 成功，${detail.send_failure_count} 失败`,
    "错误": [detail.error_type, detail.error_message].filter(Boolean).join(": ") || "—",
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
  const actions = $("#raw-actions");
  actions.replaceChildren();
  $("#raw-content").textContent = detail.has_full_diagnostics
    ? "选择上方内容查看完整原始数据。"
    : "该普通成功响应仅保存摘要，没有完整诊断数据。";
  if ($("#copy-raw-btn")) $("#copy-raw-btn").classList.add("hidden");
  
  if (detail.has_full_diagnostics) {
    for (const [part, label] of [["input", "完整输入"], ["output", "完整输出"], ["logs", "完整日志"]]) {
      const view = document.createElement("button");
      view.textContent = `查看${label}`;
      view.addEventListener("click", () => loadRaw(`/api/events/${id}/raw/${part}`));
      const download = document.createElement("a");
      download.href = `/api/events/${id}/raw/${part}`;
      download.textContent = `下载${label}`;
      download.className = "button-link";
      actions.append(view, download);
    }
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
      $("#detail-title").textContent = `${item.level} · ${item.plugin_name || item.module_name || "系统"}`;
      $("#detail-meta").replaceChildren();
      $("#raw-actions").replaceChildren();
      if ($("#copy-raw-btn")) $("#copy-raw-btn").classList.add("hidden");
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
}

async function initialize() {
  bindEvents();
  startClock();
  setQuickRange("24h");
  updateCleanupCutoff();
  try {
    await Promise.all([loadFilters(), loadBotStatus(), loadEvents(), loadDiagnostics()]);
  } catch (error) {
    toast(`初始化失败：${error.message}`);
  }
  startPeriodicRefresh();
}

document.addEventListener("DOMContentLoaded", initialize);
