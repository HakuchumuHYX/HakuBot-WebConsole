"use strict";

const state = {
  events: [],
  eventCursor: "",
  eventSignature: "",
  knownEventIds: new Set(),
  diagnosticSignature: "",
  pulseSignature: "",
  openIssues: new Set(),
  previewCutoff: 0,
  refreshInFlight: false,
  refreshErrorShown: false,
  systemWindow: "1h",
  systemHistory: null,
  autochat: { status: null, activity: null, activityMs: 0, history: null, runs: [] },
  acSignatures: {},
  acTab: "life",
};

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

function el(tag, className = "", text = "") {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text) element.textContent = text;
  return element;
}

function toast(message) {
  const element = $("#toast");
  element.textContent = message;
  element.hidden = false;
  clearTimeout(element._timer);
  element._timer = setTimeout(() => { element.hidden = true; }, 4500);
}

function formatBytes(bytes, decimals = 1) {
  if (!bytes) return "0 B";
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(sizes.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  return `${parseFloat((bytes / 1024 ** i).toFixed(decimals))} ${sizes[i]}`;
}

function formatDuration(ms) {
  if (ms == null) return "";
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

async function api(path, options = {}) {
  const response = await fetch(path, { credentials: "same-origin", cache: "no-store", ...options });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      message = (await response.json()).error || message;
    } catch (_) {}
    throw new Error(message);
  }
  return response;
}

async function mutation(path, body) {
  return api(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

/* GMT+8 time helpers. The API speaks Unix milliseconds; everything shown is GMT+8. */

function gmt8Parts(date = new Date()) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai",
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  return Object.fromEntries(parts.map(({ type, value }) => [type, value]));
}

function gmt8Date(date = new Date()) {
  const p = gmt8Parts(date);
  return `${Number(p.year)}-${Number(p.month)}-${Number(p.day)}`;
}

// "2026-1-2 15:04:05"; withYear false drops the year.
function formatTime(ms, withYear = true) {
  const p = gmt8Parts(new Date(ms));
  const date = `${Number(p.month)}-${Number(p.day)}`;
  return `${withYear ? `${Number(p.year)}-` : ""}${date} ${p.hour}:${p.minute}:${p.second}`;
}

function timeOfDay(ms) {
  const p = gmt8Parts(new Date(ms));
  return `${p.hour}:${p.minute}:${p.second}`;
}

// datetime-local values carry no zone; read them as GMT+8. Browsers drop
// ":00" seconds, which Date.parse accepts. Returns NaN when incomplete.
function inputToMs(value) {
  return Date.parse(`${value}+08:00`);
}

function partsToInput(parts) {
  const pad = value => String(value).padStart(2, "0");
  return `${parts.year}-${pad(parts.month)}-${pad(parts.day)}T${pad(parts.hour)}:${pad(parts.minute)}:${pad(parts.second)}`;
}

function calendarMonthsAgo(months) {
  const p = gmt8Parts();
  const monthIndex = Number(p.month) - 1 - months;
  const year = Number(p.year) + Math.floor(monthIndex / 12);
  const month = ((monthIndex % 12) + 12) % 12;
  const lastDay = new Date(Date.UTC(year, month + 1, 0)).getUTCDate();
  return partsToInput({ ...p, year, month: month + 1, day: Math.min(Number(p.day), lastDay) });
}

function dayLabel(date) {
  if (date === gmt8Date()) return "今天";
  if (date === gmt8Date(new Date(Date.now() - 86400000))) return "昨天";
  const [year, month, day] = date.split("-");
  const label = `${month} 月 ${day} 日`;
  return year === gmt8Date().split("-")[0] ? label : `${year} 年 ${label}`;
}

function relativeTime(ms) {
  const minutes = Math.floor((Date.now() - ms) / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时前`;
  return `${Math.floor(minutes / 1440)} 天前`;
}

const HOURS = { "1h": 1, "24h": 24, "7d": 168, "30d": 720 };
const LEVEL_LABELS = { WARNING: "警告", ERROR: "错误", CRITICAL: "严重" };

// Message segments arrive as "[image]" etc.; show them as small tokens.
const SEGMENT_LABELS = {
  image: "图片", at: "@", face: "表情", mface: "表情", record: "语音", video: "视频",
  file: "文件", reply: "回复", forward: "合并转发", json: "卡片", xml: "卡片", poke: "戳一戳",
};

function messageContent(text) {
  const fragment = document.createDocumentFragment();
  let last = 0;
  for (const match of text.matchAll(/\[([a-z]+)\]/g)) {
    const label = SEGMENT_LABELS[match[1]];
    if (!label) continue;
    fragment.append(text.slice(last, match.index), el("span", "token", label));
    last = match.index + match[0].length;
  }
  fragment.append(text.slice(last));
  return fragment;
}

/* Response log */

function eventParams() {
  const form = $("#event-filters");
  const params = new URLSearchParams();
  for (const name of ["status", "group_id", "plugin"]) {
    if (form.elements[name].value) params.set(name, form.elements[name].value);
  }
  const range = form.elements.quick_time.value;
  if (range === "custom") {
    const from = inputToMs(form.elements.from.value);
    const to = inputToMs(form.elements.to.value);
    if (from) params.set("from", from);
    if (to) params.set("to", to);
  } else {
    params.set("from", Date.now() - HOURS[range] * 3600000);
  }
  return params;
}

function exchange(input, reply) {
  const nodes = [];
  if (input) {
    const chip = el("span", "msg-input");
    chip.append(messageContent(input));
    nodes.push(chip);
  }
  if (!reply) {
    nodes.push(el("span", "msg-reply quiet", "没有发送回复"));
  } else if (reply === input) {
    nodes.push(el("span", "msg-reply quiet", "复读"));
  } else {
    const text = el("span", "msg-reply");
    text.append(messageContent(reply));
    nodes.push(text);
  }
  return nodes;
}

// A success may still carry WARNING logs; flag it without changing the status.
function eventFlag(item) {
  if (item.status === "failure") return "failure";
  return item.max_log_level === "WARNING" ? "warning" : "";
}

// Anything this slow is worth noticing even when it succeeded.
const SLOW_MS = 10000;

function eventRow(item, fresh) {
  const flag = eventFlag(item);
  const row = el("button", `row event-grid ${flag}${fresh ? " fresh" : ""}`);
  row.type = "button";

  const message = el("span", "cell-msg");
  if (flag) message.append(el("span", `tag ${flag}`, flag === "failure" ? "失败" : "警告"));
  message.append(...exchange(item.request_summary, item.response_summary));

  const duration = el("span", "cell-dur num", formatDuration(item.duration_ms));
  if (item.duration_ms >= SLOW_MS) duration.classList.add("slow");

  row.append(
    el("span", "cell-time", timeOfDay(item.started_at_ms)),
    el("span", "cell-plugin", item.plugin_name || item.module_name || "未知插件"),
    el("span", "cell-id", item.group_id || "私聊"),
    el("span", "cell-id", item.user_id || "—"),
    message,
    duration,
  );
  row.addEventListener("click", () => showEventDetail(item.id).catch(error => toast(error.message)));
  return row;
}

// Rows the previous render did not have flash once, so arrivals during the
// 3-second refresh are visible; a fresh filter starts with nothing known.
function renderEvents(flashNew) {
  const list = $("#event-list");
  list.replaceChildren();
  let currentDay = "";
  for (const item of state.events) {
    const date = gmt8Date(new Date(item.started_at_ms));
    if (date !== currentDay) {
      currentDay = date;
      list.append(el("li", "day-row", dayLabel(date)));
    }
    const row = el("li");
    row.append(eventRow(item, flashNew && !state.knownEventIds.has(item.id)));
    list.append(row);
  }
  state.knownEventIds = new Set(state.events.map(item => item.id));
  $("#event-empty").hidden = state.events.length > 0;
  $("#load-more").hidden = !state.eventCursor;
}

function renderEventSummary(stats) {
  const summary = $("#event-summary");
  if (!stats.total) {
    summary.textContent = "没有记录";
    return;
  }
  summary.replaceChildren("共 ", el("b", "", String(stats.total)), " 条");
  if (stats.failure) summary.append("，", el("b", "bad", `失败 ${stats.failure}`));
  if (stats.warning) summary.append("，", el("b", "warn", `带警告 ${stats.warning}`));
}

// A refresh reloads every page already shown, so "load more" survives the
// 3-second refresh; unchanged results skip the DOM rebuild.
async function loadEvents({ append = false } = {}) {
  const params = eventParams();
  if (append) {
    params.set("limit", "50");
    params.set("cursor", state.eventCursor);
  } else {
    params.set("limit", String(Math.min(200, Math.max(50, state.events.length))));
  }
  const [page, stats] = await Promise.all([
    api(`/api/events?${params}`).then(response => response.json()),
    append ? null : api(`/api/stats?${params}`).then(response => response.json()),
  ]);
  state.events = append ? state.events.concat(page.items || []) : (page.items || []);
  state.eventCursor = page.next_cursor || "";
  if (stats) renderEventSummary(stats);
  const signature = JSON.stringify([state.events, state.eventCursor, gmt8Date()]);
  if (signature === state.eventSignature) return;
  state.eventSignature = signature;
  renderEvents(!append && state.knownEventIds.size > 0);
}

function resetEvents() {
  state.events = [];
  state.eventCursor = "";
  state.knownEventIds = new Set();
  const form = $("#event-filters");
  $("#reset-event-filters").hidden = form.elements.quick_time.value === "24h"
    && !form.elements.status.value && !form.elements.group_id.value && !form.elements.plugin.value;
  return loadEvents();
}

/* Detail dialog */

function setMeta(fields) {
  const meta = $("#detail-meta");
  meta.replaceChildren();
  for (const [label, value, mono] of fields) {
    if (!value) continue;
    meta.append(el("dt", "", label), el("dd", mono ? "mono" : "", value));
  }
}

function setRaw(parts) {
  const section = $("#raw-section");
  const tabs = $("#raw-tabs");
  tabs.replaceChildren();
  section.hidden = parts.length === 0;
  if (!parts.length) return;
  for (const part of parts) {
    const button = el("button", "", part.label);
    button.type = "button";
    button.addEventListener("click", () => {
      $$("button", tabs).forEach(item => item.classList.toggle("active", item === button));
      loadRaw(part.path);
    });
    tabs.append(button);
  }
  tabs.hidden = parts.length === 1;
  tabs.firstChild.click();
}

async function loadRaw(path) {
  const target = $("#raw-content");
  const download = $("#download-raw");
  download.href = path;
  download.hidden = false;
  $("#copy-raw").hidden = true;
  target.textContent = "正在加载…";
  try {
    const text = await (await api(path)).text();
    try {
      target.textContent = JSON.stringify(JSON.parse(text), null, 2);
    } catch (_) {
      target.textContent = text;
    }
    $("#copy-raw").hidden = false;
  } catch (error) {
    target.textContent = `加载失败：${error.message}`;
  }
}

async function showEventDetail(id) {
  const detail = await (await api(`/api/events/${id}`)).json();
  $("#detail-custom").replaceChildren();
  $("#detail-title").textContent = detail.plugin_name || detail.module_name || "未知插件";
  const tag = $("#detail-tag");
  if (detail.status === "failure") {
    tag.className = "tag failure";
    tag.textContent = "失败";
  } else {
    tag.className = "tag success";
    tag.textContent = "成功";
  }

  const conversation = $("#detail-exchange");
  conversation.replaceChildren(...exchange(detail.request_summary, detail.response_summary));
  if (detail.status === "failure" || detail.error_type || detail.error_message) {
    const error = [detail.error_type, detail.error_message].filter(Boolean).join(": ") || "未知错误";
    conversation.append(el("pre", "error-text", error));
  }

  const finished = detail.finished_at_ms ? ` 至 ${timeOfDay(detail.finished_at_ms)}` : "";
  setMeta([
    ["时间", `${formatTime(detail.started_at_ms)}${finished}（GMT+8）`],
    ["耗时", formatDuration(detail.duration_ms)],
    ["群", detail.group_id || "私聊"],
    ["用户", detail.user_id],
    ["事件类型", detail.event_name],
    ["模块", detail.module_name, true],
    ["Matcher", [detail.matcher_type, detail.matcher_lineno && `第 ${detail.matcher_lineno} 行`].filter(Boolean).join("，")],
    ["发送", `${detail.send_success_count}/${detail.send_count} 条成功${detail.send_failure_count ? `，${detail.send_failure_count} 条失败` : ""}`],
    ["消息 ID", detail.source_message_id, true],
    ["run_id", detail.run_id, true],
  ]);

  setRaw(detail.has_full_diagnostics
    ? [["input", "完整输入"], ["output", "完整输出"], ["logs", "完整日志"]]
      .map(([part, label]) => ({ label, path: `/api/events/${id}/raw/${part}` }))
    : []);
  $("#detail-dialog").showModal();
}

function showDiagnosticDetail(group, occurrence) {
  $("#detail-custom").replaceChildren();
  $("#detail-title").textContent = group.plugin_name || "系统";
  const tag = $("#detail-tag");
  tag.className = `tag ${group.level.toLowerCase()}`;
  tag.textContent = LEVEL_LABELS[group.level];
  $("#detail-exchange").replaceChildren();
  setMeta([
    ["时间", `${formatTime(occurrence.created_at_ms)}（GMT+8）`],
    ["Logger", group.logger_name, true],
  ]);
  setRaw([{ label: "完整日志", path: `/api/diagnostics/${occurrence.id}` }]);
  $("#detail-dialog").showModal();
}

/* Diagnostics, grouped by message */

function diagnosticParams() {
  const form = $("#diagnostic-filters");
  const params = new URLSearchParams();
  if (form.elements.level.value) params.set("level", form.elements.level.value);
  if (form.elements.plugin.value) params.set("plugin", form.elements.plugin.value);
  const range = form.elements.quick_time.value;
  if (range !== "all") params.set("from", Date.now() - HOURS[range] * 3600000);
  return params;
}

function issueItem(group, maxCount) {
  const level = group.level.toLowerCase();
  const details = el("details", `issue level-${level}`);
  details.open = state.openIssues.has(group.key);
  details.addEventListener("toggle", () => {
    if (details.open) state.openIssues.add(group.key);
    else state.openIssues.delete(group.key);
  });

  const summary = el("summary", "issue-grid");
  const when = el("span", "issue-when num", relativeTime(group.last_ms));
  when.dataset.ms = group.last_ms;
  // Bars compare against the busiest group, so the loudest problem stands out.
  const fill = el("span");
  fill.style.width = `${(group.count / maxCount) * 100}%`;
  const bar = el("span", "count-bar");
  bar.append(fill);
  const count = el("span", "count-cell");
  count.append(el("span", "count-number", String(group.count)), bar);
  summary.append(
    el("span", `tag ${level}`, LEVEL_LABELS[group.level]),
    el("span", "cell-plugin", group.plugin_name || "系统"),
    el("span", "issue-msg", group.message),
    count,
    when,
  );

  const body = el("div", "issue-body issue-grid");
  body.append(el("p", "", group.count > 1
    ? `首次 ${formatTime(group.first_ms)}，最近 ${formatTime(group.last_ms)}。`
    : `发生于 ${formatTime(group.last_ms)}。`));
  const occurrences = el("ol", "occurrences");
  for (const occurrence of group.occurrences) {
    // The year is noise here; the dialog shows the full time.
    const button = el("button", "", formatTime(occurrence.created_at_ms, false));
    button.type = "button";
    button.addEventListener("click", () => showDiagnosticDetail(group, occurrence));
    const item = el("li");
    item.append(button);
    occurrences.append(item);
  }
  body.append(occurrences);
  if (group.count > group.occurrences.length) {
    body.append(el("p", "", `只列出最近 ${group.occurrences.length} 次，点开查看完整日志。`));
  }
  details.append(summary, body);
  const row = el("li");
  row.append(details);
  return row;
}

async function loadDiagnostics() {
  const data = await (await api(`/api/diagnostics/groups?${diagnosticParams()}`)).json();
  const summary = $("#diagnostic-summary");
  if (data.total) {
    summary.replaceChildren(
      "共 ", el("b", "", String(data.total)), " 条，归为 ", el("b", "", String(data.groups.length)), " 类",
      data.truncated ? "，只显示最近出现的 100 类" : "",
    );
  } else {
    summary.textContent = "没有记录";
  }

  const signature = JSON.stringify(data.groups.map(group => [group.key, group.count, group.last_ms]));
  if (signature !== state.diagnosticSignature) {
    state.diagnosticSignature = signature;
    const maxCount = Math.max(1, ...data.groups.map(group => group.count));
    $("#diagnostic-list").replaceChildren(...data.groups.map(group => issueItem(group, maxCount)));
  }
  $$("#diagnostic-list .issue-when").forEach(item => { item.textContent = relativeTime(Number(item.dataset.ms)); });
  $("#diagnostic-empty").hidden = data.groups.length > 0;
}

/* Header, filters, storage */

async function loadFilters() {
  const data = await (await api("/api/filters")).json();
  const groups = $('#event-filters select[name="group_id"]');
  for (const value of data.groups || []) groups.add(new Option(value, value));
  for (const select of $$('select[name="plugin"]')) {
    for (const value of data.plugins || []) select.add(new Option(value, value));
  }
}

async function loadBotStatus() {
  const data = await (await api("/api/bot-status")).json();
  const element = $("#bot-status");
  element.className = `bot ${data.online ? "online" : "offline"}`;
  $(".bot-state", element).textContent = data.online ? "在线" : "离线";
  $(".bot-id", element).textContent = data.bots.map(bot => bot.bot_id).join("、");
}

/* 24-hour pulse */

const OUTCOMES = ["success", "warning", "failure"];

function hourLabel(hour) {
  const start = new Date(hour.start_ms);
  const p = gmt8Parts(start);
  const next = gmt8Parts(new Date(hour.start_ms + 3600000));
  const total = hour.success + hour.warning + hour.failure;
  return `${dayLabel(gmt8Date(start))} ${p.hour}:00–${next.hour}:00\n`
    + `响应 ${total}，带警告 ${hour.warning}，失败 ${hour.failure}`;
}

function showPulseTip(bar, hour) {
  const tip = $("#pulse-tip");
  tip.textContent = hourLabel(hour);
  tip.hidden = false;
  const half = tip.offsetWidth / 2;
  const width = bar.parentElement.clientWidth;
  const center = bar.offsetLeft + bar.offsetWidth / 2;
  tip.style.left = `${Math.max(half, Math.min(width - half, center))}px`;
}

function hidePulseTip() {
  $("#pulse-tip").hidden = true;
}

// Clicking an hour narrows the response log to exactly that hour.
function showHour(startMs) {
  const form = $("#event-filters");
  form.elements.quick_time.value = "custom";
  $(".custom-range", form).hidden = false;
  form.elements.from.value = partsToInput(gmt8Parts(new Date(startMs)));
  form.elements.to.value = partsToInput(gmt8Parts(new Date(startMs + 3599000)));
  showView("events");
  resetEvents().catch(error => toast(error.message));
}

function renderPulse(hours) {
  const signature = JSON.stringify(hours);
  if (signature === state.pulseSignature) return;
  state.pulseSignature = signature;

  const max = Math.max(1, ...hours.map(hour => hour.success + hour.warning + hour.failure));
  const bars = hours.map(hour => {
    const bar = el("button", "pulse-bar");
    bar.type = "button";
    bar.setAttribute("aria-label", hourLabel(hour));
    for (const outcome of OUTCOMES) {
      if (!hour[outcome]) continue;
      const segment = el("span", `seg-${outcome}`);
      segment.style.height = `${(hour[outcome] / max) * 100}%`;
      bar.append(segment);
    }
    bar.addEventListener("mouseenter", () => showPulseTip(bar, hour));
    bar.addEventListener("focus", () => showPulseTip(bar, hour));
    bar.addEventListener("mouseleave", hidePulseTip);
    bar.addEventListener("blur", hidePulseTip);
    bar.addEventListener("click", () => {
      hidePulseTip();
      showHour(hour.start_ms);
    });
    return bar;
  });
  $("#pulse-bars").replaceChildren(...bars);

  // Label every sixth hour, leaving room before the "现在" mark.
  const last = hours.length - 1;
  $("#pulse-axis").replaceChildren(...hours.map((hour, index) => {
    if (index === last) return el("span", "now", "现在");
    const p = gmt8Parts(new Date(hour.start_ms));
    return el("span", "", Number(p.hour) % 6 === 0 && index < last - 3 ? `${p.hour}:00` : "");
  }));
}

async function loadOverview() {
  const { stats, hours, diagnostics } = await (await api("/api/overview")).json();
  $("#pulse-total").textContent = stats.total;
  $("#pulse-warning").textContent = stats.warning;
  $("#pulse-failure").textContent = stats.failure;
  $(".count-warning").classList.toggle("hot", stats.warning > 0);
  $(".count-failure").classList.toggle("hot", stats.failure > 0);
  $("#nav-events").textContent = stats.total;
  const navDiagnostics = $("#nav-diagnostics");
  navDiagnostics.textContent = diagnostics;
  navDiagnostics.classList.toggle("alert", diagnostics > 0);
  renderPulse(hours);
}

function updateClock() {
  const p = gmt8Parts();
  $("#current-time").textContent = `${p.hour}:${p.minute}:${p.second}`;
  $("#current-time").title = "GMT+8";
}

async function loadStorage() {
  const data = await (await api("/api/storage")).json();
  const records = (count, earliest, latest) => count
    ? `${count} 条\n${formatTime(earliest)} 至 ${formatTime(latest)}`
    : "没有记录";
  $("#size-db").textContent = formatBytes(data.database_bytes);
  $("#size-wal").textContent = `${formatBytes(data.wal_bytes)} / ${formatBytes(data.shm_bytes)}`;
  $("#response-records").textContent = records(data.response_count, data.response_earliest_ms, data.response_latest_ms);
  $("#diagnostic-records").textContent = records(data.diagnostic_count, data.diagnostic_earliest_ms, data.diagnostic_latest_ms);
  $("#llm-call-records").textContent = records(data.llm_call_count, data.llm_call_earliest_ms, data.llm_call_latest_ms);
  $("#look-records").textContent = records(data.look_count, data.look_earliest_ms, data.look_latest_ms);
  $("#retention-policy").textContent = data.retention_days > 0
    ? `保留最近 ${data.retention_days} 天，每天 04:15（GMT+8）清理更早的记录`
    : "已关闭，只能手动清理";
}

function updateCleanupCutoff() {
  const value = $("#cleanup-period").value;
  if (value !== "custom") $("#cleanup-cutoff").value = calendarMonthsAgo(Number(value));
  state.previewCutoff = 0;
  $("#execute-cleanup").disabled = true;
  $("#execute-cleanup").textContent = "清理";
  $("#cleanup-preview").textContent = "先预览会删除多少条记录。";
}

async function previewCleanup() {
  const cutoff = inputToMs($("#cleanup-cutoff").value);
  if (!cutoff) throw new Error("请填写完整的截止时间");
  const { preview } = await (await mutation("/api/storage/cleanup/preview", { cutoff_ms: cutoff })).json();
  state.previewCutoff = cutoff;
  $("#cleanup-preview").textContent =
    `会删除 ${formatTime(cutoff)}（GMT+8）之前的 ${preview.response_count} 条响应、${preview.diagnostic_count} 条诊断和 ${preview.autochat_count} 条 Autochat 记录。`;
  $("#execute-cleanup").textContent = `清理 ${preview.response_count + preview.diagnostic_count + preview.autochat_count} 条记录`;
  $("#execute-cleanup").disabled = false;
}

async function executeCleanup() {
  if (!window.confirm(`永久删除 ${formatTime(state.previewCutoff)}（GMT+8）之前的日志？删除后无法恢复。`)) return;
  $("#execute-cleanup").disabled = true;
  const data = await (await mutation("/api/storage/cleanup", { cutoff_ms: state.previewCutoff })).json();
  state.previewCutoff = 0;
  $("#execute-cleanup").textContent = "清理";
  $("#cleanup-preview").textContent = `已清理 ${data.responses_deleted} 条响应、${data.diagnostics_deleted} 条诊断和 ${data.autochat_deleted} 条 Autochat 记录，空间已回收。`;
  toast("已清理");
  await loadStorage();
}

/* Server */

function setMeter(id, percent) {
  const bar = $(id);
  bar.style.width = `${Math.min(100, percent)}%`;
  bar.className = percent >= 90 ? "critical" : percent >= 80 ? "high" : "";
}

function setFacts(list, pairs) {
  list.replaceChildren(...pairs.map(([label, value]) => {
    const row = el("div");
    row.append(el("dt", "", label), el("dd", "", value));
    return row;
  }));
}

// Runs on every refresh: the sidebar vitals are visible from every view.
async function loadSystemStatus() {
  const status = await (await api("/api/system/status")).json();
  const current = status.current;

  $("#vital-cpu").textContent = `${Math.round(current.cpu_percent)}%`;
  setMeter("#vital-cpu-bar", current.cpu_percent);
  $("#vital-mem").textContent = `${Math.round(current.memory_percent)}%`;
  setMeter("#vital-mem-bar", current.memory_percent);

  setFacts($("#host-facts"), [
    ["主机名", status.hostname],
    ["内核", `${status.kernel}（${status.arch}）`],
    ["CPU", `${status.cpu_model || "未知型号"}，${status.cpu_cores} 核`],
    ["系统运行", status.system_uptime_desc],
    ["控制台运行", status.process_uptime_desc],
    ["控制台内存", `${formatBytes(status.go_heap_alloc_bytes)} 堆，${status.goroutines} 个 goroutine`],
  ]);

  $("#sys-cpu-value").textContent = `${current.cpu_percent.toFixed(1)}%`;
  setMeter("#sys-cpu-bar", current.cpu_percent);
  $("#sys-cpu-detail").textContent = `${status.cpu_cores} 核，${current.process_count} 个进程`;

  $("#sys-mem-value").textContent = `${current.memory_percent.toFixed(1)}%`;
  setMeter("#sys-mem-bar", current.memory_percent);
  $("#sys-mem-detail").textContent =
    `${formatBytes(current.memory_used_bytes)} / ${formatBytes(current.memory_total_bytes)}，Swap ${formatBytes(current.swap_used_bytes)} / ${formatBytes(current.swap_total_bytes)}`;

  $("#sys-disk-value").textContent = `${current.disk_percent.toFixed(1)}%`;
  setMeter("#sys-disk-bar", current.disk_percent);
  $("#sys-disk-detail").textContent = `${formatBytes(current.disk_used_bytes)} / ${formatBytes(current.disk_total_bytes)}`;

  $("#sys-load-value").textContent = current.load1.toFixed(2);
  setMeter("#sys-load-bar", (current.load1 / status.cpu_cores) * 100);
  $("#sys-load-detail").textContent = `5 分钟 ${current.load5.toFixed(2)}，15 分钟 ${current.load15.toFixed(2)}`;

  $("#sys-net-value").textContent = `↓ ${formatBytes(current.net_rx_bytes_per_sec)}/s`;
  $("#sys-net-detail").textContent = `↑ ${formatBytes(current.net_tx_bytes_per_sec)}/s`;
}

function niceNetCeiling(value) {
  let ceiling = 1024;
  while (ceiling < value) ceiling *= 2;
  return ceiling;
}

async function loadSystemHistory() {
  const data = await (await api(`/api/system/history?window=${state.systemWindow}`)).json();
  state.systemHistory = data;
  renderCharts();
}

function renderCharts() {
  const data = state.systemHistory;
  const points = data.points || [];
  const last = points[points.length - 1];
  const peak = points.reduce((max, p) => Math.max(max, p.cpu_max, p.cpu_percent), 0);
  $("#chart-cpu-stat").textContent = last ? `当前 ${last.cpu_percent.toFixed(1)}%，峰值 ${peak.toFixed(1)}%` : "";
  $("#chart-mem-stat").textContent = last ? `当前 ${last.memory_percent.toFixed(1)}%（${formatBytes(last.memory_used_bytes)}）` : "";
  $("#chart-net-stat").textContent = last
    ? `↓ ${formatBytes(last.net_rx_bytes_per_sec)}/s，↑ ${formatBytes(last.net_tx_bytes_per_sec)}/s`
    : "";

  const common = { points, fromMs: data.from_ms, toMs: data.to_ms };
  const percent = value => `${Math.round(value)}%`;
  renderChart("#chart-cpu", {
    ...common,
    series: [{ key: "cpu_percent", className: "series-1" }],
    yMax: 100,
    thresholds: true,
    formatY: percent,
    tooltip: p => `${formatTime(p.timestamp_ms)}\nCPU ${p.cpu_percent.toFixed(1)}%，峰值 ${p.cpu_max.toFixed(1)}%`,
  });
  renderChart("#chart-mem", {
    ...common,
    series: [{ key: "memory_percent", className: "series-2" }],
    yMax: 100,
    thresholds: true,
    formatY: percent,
    tooltip: p => `${formatTime(p.timestamp_ms)}\n内存 ${p.memory_percent.toFixed(1)}%（${formatBytes(p.memory_used_bytes)}）`,
  });
  const netMax = points.reduce((max, p) => Math.max(max, p.net_rx_bytes_per_sec, p.net_tx_bytes_per_sec), 0);
  renderChart("#chart-net", {
    ...common,
    series: [
      { key: "net_rx_bytes_per_sec", className: "series-2" },
      { key: "net_tx_bytes_per_sec", className: "series-3", noArea: true },
    ],
    yMax: niceNetCeiling(netMax * 1.05),
    formatY: value => (value ? `${formatBytes(value, 0)}/s` : "0"),
    tooltip: p => `${formatTime(p.timestamp_ms)}\n↓ ${formatBytes(p.net_rx_bytes_per_sec)}/s，↑ ${formatBytes(p.net_tx_bytes_per_sec)}/s`,
  });
}

// Colors come from CSS classes, so a theme switch needs no re-render.
function renderChart(selector, { points, fromMs, toMs, series, yMax, thresholds, formatY, tooltip }) {
  const container = $(selector);
  const width = Math.max(280, Math.floor(container.clientWidth));
  const height = container.clientHeight;
  const pad = { left: 52, right: 8, top: 8, bottom: 24 };
  const plotWidth = width - pad.left - pad.right;
  const plotHeight = height - pad.top - pad.bottom;
  const span = Math.max(1000, toMs - fromMs);
  const x = ms => pad.left + ((Math.min(toMs, Math.max(fromMs, ms)) - fromMs) / span) * plotWidth;
  const y = value => pad.top + plotHeight - (Math.min(yMax, Math.max(0, value || 0)) / yMax) * plotHeight;
  const bottom = pad.top + plotHeight;

  let svg = "";
  for (let i = 0; i <= 4; i++) {
    const value = (yMax * i) / 4;
    const yPos = y(value).toFixed(1);
    svg += `<line class="${i ? "chart-grid" : "chart-axis"}" x1="${pad.left}" x2="${width - pad.right}" y1="${yPos}" y2="${yPos}"/>`;
    svg += `<text class="chart-label" x="${pad.left - 8}" y="${Number(yPos) + 4}" text-anchor="end">${formatY(value)}</text>`;
  }
  // Same 80% / 90% lines the meters turn amber and red at.
  if (thresholds) {
    for (const [value, className] of [[80, "threshold-high"], [90, "threshold-critical"]]) {
      svg += `<line class="chart-threshold ${className}" x1="${pad.left}" x2="${width - pad.right}" y1="${y(value)}" y2="${y(value)}"/>`;
    }
  }
  const ticks = width < 420 ? 2 : 3;
  const longSpan = span > 12 * 3600000;
  for (let i = 0; i <= ticks; i++) {
    const p = gmt8Parts(new Date(fromMs + (i / ticks) * span));
    const label = longSpan ? `${Number(p.month)}-${Number(p.day)} ${p.hour}:${p.minute}` : `${p.hour}:${p.minute}`;
    const anchor = i === 0 ? "start" : i === ticks ? "end" : "middle";
    svg += `<text class="chart-label" x="${pad.left + (i / ticks) * plotWidth}" y="${height - 6}" text-anchor="${anchor}">${label}</text>`;
  }

  if (!points.length) {
    svg += `<text class="chart-label" x="${pad.left + plotWidth / 2}" y="${pad.top + plotHeight / 2}" text-anchor="middle">还没有采样数据</text>`;
  }
  for (const s of series) {
    if (!points.length) break;
    const coords = points.map(p => `${x(p.timestamp_ms).toFixed(1)} ${y(p[s.key]).toFixed(1)}`);
    if (!s.noArea) {
      svg += `<path class="chart-area ${s.className}" d="M ${x(points[0].timestamp_ms).toFixed(1)} ${bottom} L ${coords.join(" L ")} L ${x(points[points.length - 1].timestamp_ms).toFixed(1)} ${bottom} Z"/>`;
    }
    svg += `<path class="chart-line ${s.className}" d="M ${coords.join(" L ")}"/>`;
  }
  svg += `<line class="chart-cross" y1="${pad.top}" y2="${bottom}" visibility="hidden"/>`;
  svg += series.map(s => `<circle class="chart-dot ${s.className}" r="4" visibility="hidden"/>`).join("");
  svg += `<rect class="chart-hit" x="${pad.left}" y="${pad.top}" width="${plotWidth}" height="${plotHeight}" fill="transparent"/>`;

  container.innerHTML = `<svg viewBox="0 0 ${width} ${height}" aria-hidden="true">${svg}</svg>`;
  if (!points.length) return;

  const tip = el("div", "chart-tip");
  tip.hidden = true;
  container.append(tip);
  const cross = $(".chart-cross", container);
  const dots = $$(".chart-dot", container);
  const show = clientX => {
    const ms = fromMs + ((clientX - container.getBoundingClientRect().left - pad.left) / plotWidth) * span;
    const point = points.reduce((best, p) => (Math.abs(p.timestamp_ms - ms) < Math.abs(best.timestamp_ms - ms) ? p : best));
    const px = x(point.timestamp_ms);
    cross.setAttribute("x1", px);
    cross.setAttribute("x2", px);
    cross.setAttribute("visibility", "visible");
    series.forEach((s, i) => {
      dots[i].setAttribute("cx", px);
      dots[i].setAttribute("cy", y(point[s.key]));
      dots[i].setAttribute("visibility", "visible");
    });
    tip.textContent = tooltip(point);
    tip.style.left = `${Math.max(80, Math.min(width - 80, px))}px`;
    tip.style.top = `${y(point[series[0].key])}px`;
    tip.hidden = false;
  };
  const hide = () => {
    cross.setAttribute("visibility", "hidden");
    dots.forEach(dot => dot.setAttribute("visibility", "hidden"));
    tip.hidden = true;
  };
  const hit = $(".chart-hit", container);
  hit.addEventListener("mousemove", event => show(event.clientX));
  hit.addEventListener("mouseleave", hide);
  hit.addEventListener("touchmove", event => show(event.touches[0].clientX), { passive: true });
  hit.addEventListener("touchend", hide);
}

/* Autochat. Current state is the last poll of the process and goes stale when
   it is down; the LLM curve, decisions and maintenance come from the shared
   database and stay live. Everything from the process is chat content, so it
   only ever reaches the page through textContent. */

const DAY_MS = 86400000;
const MODE_LABELS = { free: "有空", busy: "在忙", away: "放下手机", sleep: "睡觉" };
const SOURCE_LABELS = { due: "到点定的", said: "她在群里说的", called: "被叫到", start: "启动时定的", calm: "/calm" };
const LLM_LABELS = {
  chat: "Chat", feedback: "Feedback", background: "Background",
  embedding: "Embedding", rerank: "Rerank", web_search: "联网搜索",
};
const LLM_ORDER = Object.keys(LLM_LABELS);
const TRIGGER_LABELS = { relevant: "被点名", conversation: "接话", poke: "被戳", rhythm: "按节奏" };
const QUIET_AFTER_MS = 15 * 60000;
const MAINTENANCE_LATE_HOUR = 5;
// Below this share of the day a segment's text would only be stray glyphs.
const SEGMENT_LABEL_MIN_SHARE = 0.06;

function hhmm(ms) {
  const p = gmt8Parts(new Date(ms));
  return `${p.hour}:${p.minute}`;
}

function monthDay(ms) {
  const p = gmt8Parts(new Date(ms));
  return `${p.month}-${p.day}`;
}

function isoDay(ms) {
  const p = gmt8Parts(new Date(ms));
  return `${p.year}-${p.month}-${p.day}`;
}

function formatSpan(ms) {
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return `${Math.max(0, Math.round(ms / 1000))} 秒`;
  if (minutes < 60) return `${minutes} 分 ${Math.floor((ms % 60000) / 1000)} 秒`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分`;
  return `${Math.floor(minutes / 1440)} 天 ${Math.floor((minutes % 1440) / 60)} 小时`;
}

function formatTokens(value) {
  if (value < 10000) return String(value);
  return `${(value / 10000).toFixed(value < 1000000 ? 1 : 0)} 万`;
}

// The page refreshes every 3 seconds; rebuilding only what changed keeps
// keyboard focus and open tips in place.
function acChanged(key, value) {
  const signature = JSON.stringify(value);
  if (state.acSignatures[key] === signature) return false;
  state.acSignatures[key] = signature;
  return true;
}

function withTip(element, text) {
  const tip = $("#ac-tip");
  const show = () => {
    tip.textContent = text;
    tip.hidden = false;
    const box = element.getBoundingClientRect();
    const half = tip.offsetWidth / 2;
    tip.style.left = `${Math.max(half + 4, Math.min(innerWidth - half - 4, box.left + box.width / 2))}px`;
    tip.style.top = `${box.top - 6}px`;
  };
  const hide = () => { tip.hidden = true; };
  element.addEventListener("mouseenter", show);
  element.addEventListener("mouseleave", hide);
  element.addEventListener("focus", show);
  element.addEventListener("blur", hide);
  element.setAttribute("aria-label", text);
  return element;
}

function groupTitle(id) {
  const group = state.autochat.activity?.groups.find(item => item.id === id);
  return group?.name || id;
}

function renderAcProcess() {
  const { status, history } = state.autochat;
  const overview = status.overview;
  if (!acChanged("process", [
    status.reachable, overview?.bots, overview?.started_at_ms,
    overview?.groups.map(group => group.worker_alive), history?.warnings_24h, Math.floor(Date.now() / 60000),
  ])) return;
  const line = $("#ac-process-state");
  line.className = `ac-state ${status.reachable ? "ok" : "bad"}`;
  line.replaceChildren(el("span", "status-dot"), status.reachable ? "运行中" : "离线");

  const pairs = [];
  if (overview) {
    pairs.push(["已运行", formatSpan(overview.now_ms - overview.started_at_ms)]);
    pairs.push(["QQ", overview.bots.length ? `已连接 ${overview.bots.join("、")}` : "没有连接"]);
    const alive = overview.groups.filter(group => group.worker_alive).length;
    pairs.push(["群 worker", `${alive}/${overview.groups.length}`]);
  }
  setFacts($("#ac-process"), pairs);
  if (history) {
    const row = el("div");
    const link = el("button", "text-button", `${history.warnings_24h} 条`);
    link.type = "button";
    link.addEventListener("click", showAutochatDiagnostics);
    const value = el("dd");
    value.append(link);
    row.append(el("dt", "", "24h 警告"), value);
    $("#ac-process").append(row);
  }
}

function showAutochatDiagnostics() {
  const form = $("#diagnostic-filters");
  if (![...form.elements.plugin.options].some(option => option.value === "nyaturingtest")) {
    form.elements.plugin.add(new Option("nyaturingtest", "nyaturingtest"));
  }
  form.elements.plugin.value = "nyaturingtest";
  form.elements.quick_time.value = "24h";
  form.elements.level.value = "";
  showView("diagnostics");
  loadDiagnostics().catch(error => toast(error.message));
}

function llmSpark(endpoint, fromMs) {
  const spark = el("span", "ac-spark");
  const byIndex = new Map(endpoint.hours.map(hour => [hour.index, hour]));
  const max = Math.max(1, ...endpoint.hours.map(hour => hour.ok + hour.failed));
  for (let index = 0; index < 24; index++) {
    const hour = byIndex.get(index);
    const start = fromMs + index * 3600000;
    const label = `${hhmm(start)}–${hhmm(start + 3600000)}\n`
      + (hour ? `成功 ${hour.ok}，失败 ${hour.failed}，平均 ${formatDuration(Math.round(hour.avg_latency_ms))}` : "没有请求");
    const column = withTip(el("span", "ac-spark-col"), label);
    if (hour) {
      for (const [count, className] of [[hour.ok, "ok"], [hour.failed, "failed"]]) {
        if (!count) continue;
        const part = el("span", className);
        part.style.height = `${(count / max) * 100}%`;
        column.append(part);
      }
    }
    spark.append(column);
  }
  return spark;
}

function renderAcLlm() {
  const { status, history, activity } = state.autochat;
  if (!history) return;
  const live = new Map((status.overview?.llm || []).map(item => [item.name, item]));
  if (acChanged("llm", [history.endpoints, history.from_ms, [...live.values()].map(item => item.circuit_remaining_seconds)])) {
    const endpoints = [...history.endpoints].sort((a, b) => LLM_ORDER.indexOf(a.endpoint) - LLM_ORDER.indexOf(b.endpoint));
    $("#ac-llm").replaceChildren(...endpoints.map(endpoint => {
      const row = el("li");
      const name = el("span", "ac-llm-name", LLM_LABELS[endpoint.endpoint] || endpoint.endpoint);
      name.title = endpoint.model;
      const counts = el("span", "num mono", `${endpoint.recent_ok}/${endpoint.recent_ok + endpoint.recent_failed}`);
      if (endpoint.recent_failed) counts.classList.add("ac-warn");
      const latency = el("span", "num mono", endpoint.recent_ok + endpoint.recent_failed ? formatDuration(Math.round(endpoint.recent_avg_ms)) : "—");
      row.append(name, counts, latency, llmSpark(endpoint, history.from_ms));
      const circuit = live.get(endpoint.endpoint)?.circuit_remaining_seconds;
      if (circuit > 0) row.append(el("span", "tag failure", `熔断 ${circuit} 秒`));
      return row;
    }));
    $("#ac-llm-empty").hidden = endpoints.length > 0;
  }

  const errors = $("#ac-errors");
  errors.hidden = history.recent_errors.length === 0;
  errors.textContent = `最近的错误（${history.recent_errors.length}）`;

  const groupIds = new Set((activity?.groups || []).map(group => group.id));
  const outside = (activity?.tokens || []).filter(item => !groupIds.has(item.session_id));
  const total = outside.reduce((sum, item) => sum + item.total, 0);
  $("#ac-outside").textContent = activity
    ? `各群之外的 Token（状态、人设、表情包等）近 24 小时 ${formatTokens(total)}`
    : "";
}

function showLlmErrors() {
  const list = el("ul", "ac-list");
  for (const item of state.autochat.history.recent_errors) {
    const row = el("li");
    row.append(
      el("span", "mono", formatTime(item.created_at_ms, false)),
      el("span", "", LLM_LABELS[item.endpoint] || item.endpoint),
      el("span", "tag failure", item.error_type || "未知"),
      el("span", "ac-dim", [item.session_id, item.model, formatDuration(item.latency_ms)].filter(Boolean).join("，")),
    );
    list.append(row);
  }
  const note = el("p", "ac-note", "原始错误信息在「警告与错误」里。");
  openAcDialog("最近的 LLM 错误", "", "", [list, note]);
}

function segmentMode(segment) {
  return segment.sleep ? "sleep" : segment.mode;
}

function statusBand(persona, dayStart, now) {
  const band = el("div", "tl-band");
  const percent = ms => `${((ms - dayStart) / DAY_MS) * 100}%`;
  for (const segment of persona.timeline) {
    const start = Math.max(segment.start_ms, dayStart);
    const realEnd = segment.end_ms || now;
    const end = Math.min(realEnd, dayStart + DAY_MS);
    if (end <= start) continue;
    const pastEnd = Math.min(end, Math.max(start, now));
    const mode = segmentMode(segment);
    const label = `${hhmm(segment.start_ms)}–${hhmm(realEnd)} ${MODE_LABELS[mode]}\n${segment.doing}\n${SOURCE_LABELS[segment.source] || segment.source}`;
    if (pastEnd > start) {
      const text = (pastEnd - start) / DAY_MS >= SEGMENT_LABEL_MIN_SHARE ? segment.doing : "";
      const part = withTip(el("div", `tl-seg mode-${mode}`, text), label);
      part.tabIndex = 0;
      part.style.left = percent(start);
      part.style.width = `${((pastEnd - start) / DAY_MS) * 100}%`;
      band.append(part);
    }
    if (end > pastEnd) {
      // The current segment past "now" is only her plan.
      const planned = withTip(el("div", `tl-seg tl-planned mode-${mode}`), `${label}\n后面这段是预计`);
      planned.style.left = percent(pastEnd);
      planned.style.width = `${((end - pastEnd) / DAY_MS) * 100}%`;
      band.append(planned);
    }
  }
  return band;
}

function groupLane(group, dayStart, max) {
  const lane = el("div", "tl-lane");
  const speaks = new Map();
  for (const ms of group.speaks) {
    const index = Math.floor((ms - dayStart) / (DAY_MS / 48));
    speaks.set(index, (speaks.get(index) || 0) + 1);
    const tick = el("span", "tl-tick");
    tick.style.left = `${((ms - dayStart) / DAY_MS) * 100}%`;
    lane.append(tick);
  }
  group.buckets.forEach((count, index) => {
    const start = dayStart + index * (DAY_MS / 48);
    const column = withTip(
      el("span", "tl-col"),
      `${hhmm(start)}–${hhmm(start + DAY_MS / 48)}\n消息 ${count} 条，她开口 ${speaks.get(index) || 0} 次`,
    );
    if (count) {
      const bar = el("span", "tl-bar");
      bar.style.height = `${Math.max(6, (count / max) * 100)}%`;
      column.append(bar);
    }
    lane.append(column);
  });
  return lane;
}

function renderAcToday() {
  const { activity, status } = state.autochat;
  const container = $("#ac-today");
  if (!activity) {
    if (acChanged("today", null)) container.replaceChildren(el("p", "empty", "还没有拿到过 autochat 的状态。"));
    return;
  }
  const now = Date.now();
  const dayStart = activity.day_start_ms;
  if (!acChanged("today", [activity.personas.map(p => p.timeline), activity.groups, Math.floor(now / 60000)])) return;

  const overviewGroups = new Map((status.overview?.groups || []).map(group => [group.id, group]));
  const max = Math.max(1, ...activity.groups.flatMap(group => group.buckets));
  const rows = [];
  const laneRow = group => {
    const label = el("button", "tl-label");
    label.type = "button";
    label.append(el("span", "tl-name", group.name || group.id), el("span", "tl-sub", `${group.messages} 条，她 ${group.speaks.length} 次`));
    label.addEventListener("click", () => showAcGroup(group.id));
    const row = el("div", "tl-row");
    row.append(label, groupLane(group, dayStart, max));
    return row;
  };
  const shown = new Set();
  for (const persona of activity.personas) {
    const bandRow = el("div", "tl-row tl-row-band");
    const label = el("div", "tl-label tl-label-static");
    label.append(el("span", "tl-name", persona.name), el("span", "tl-sub", "她的一天"));
    bandRow.append(label, statusBand(persona, dayStart, now));
    rows.push(bandRow);
    for (const group of activity.groups) {
      if (overviewGroups.get(group.id)?.preset !== persona.preset) continue;
      shown.add(group.id);
      rows.push(laneRow(group));
    }
  }
  for (const group of activity.groups) {
    if (!shown.has(group.id)) rows.push(laneRow(group));
  }

  const axisRow = el("div", "tl-row tl-row-axis");
  const axis = el("div", "tl-axis");
  for (const hour of [0, 6, 12, 18, 24]) {
    const tick = el("span", "", String(hour).padStart(2, "0"));
    tick.style.left = `${(hour / 24) * 100}%`;
    axis.append(tick);
  }
  axisRow.append(el("span"), axis);
  rows.push(axisRow);

  // Every lane shares one track width, so one line marks now across all of them.
  if (now < dayStart + DAY_MS) {
    const nowLine = el("div", "tl-now");
    nowLine.style.setProperty("--pos", String((now - dayStart) / DAY_MS));
    nowLine.append(el("span", "", "现在"));
    rows.push(nowLine);
  }
  container.replaceChildren(...rows);
}

function moodMeter(label, value, signed) {
  const item = el("span", "ac-vad");
  item.append(el("span", "ac-vad-label", label));
  const track = el("span", `ac-vad-track${signed ? " signed" : ""}`);
  const fill = el("span");
  if (signed) {
    fill.style.left = value >= 0 ? "50%" : `${50 + value * 50}%`;
    fill.style.width = `${Math.abs(value) * 50}%`;
  } else {
    fill.style.left = "0";
    fill.style.width = `${value * 100}%`;
  }
  track.append(fill);
  item.append(track, el("span", "mono", value.toFixed(2)));
  return item;
}

function groupCard(group, today, tokens, now) {
  const quiet = !group.last_message_ms || now - group.last_message_ms > QUIET_AFTER_MS;
  const card = el("button", `panel ac-card${quiet ? " quiet" : ""}`);
  card.type = "button";
  card.addEventListener("click", () => showAcGroup(group.id));

  const head = el("div", "ac-card-head");
  head.append(el("span", "ac-card-name", today?.name || group.id));
  if (today?.name) head.append(el("span", "mono ac-dim", group.id));
  if (!group.worker_alive) head.append(el("span", "tag failure", "worker 停了"));
  if (group.muted_until_ms) head.append(el("span", "tag warning", `禁言到 ${hhmm(group.muted_until_ms)}`));
  if (!group.name) head.append(el("span", "tag warning", "没设人设"));

  const vad = el("div", "ac-vads");
  vad.append(
    moodMeter("愉悦", group.emotion.valence, true),
    moodMeter("兴奋", group.emotion.arousal, false),
    moodMeter("强势", group.emotion.dominance, true),
  );
  const facts = el("ul", "ac-card-facts");
  const add = text => facts.append(el("li", "", text));
  add(`未读 ${group.unread}`);
  add(group.last_look_ms ? `看群 ${relativeTime(group.last_look_ms)}` : "这次启动还没看过群");
  if (today) add(`今天 ${today.messages} 条，她 ${today.speaks.length} 次`);
  add(tokens ? `24h Token ${formatTokens(tokens.total)}，${tokens.requests} 次` : "24h Token 0");
  if (group.last_message_ms) add(`最后一条 ${relativeTime(group.last_message_ms)}`);

  card.append(head, el("p", "ac-mood", group.mood), vad, facts);
  if (group.summary) card.append(el("p", "ac-summary", group.summary));
  return card;
}

function renderAcGroups() {
  const { status, activity } = state.autochat;
  const container = $("#ac-groups");
  const groups = status.overview?.groups || [];
  const now = Date.now();
  if (!acChanged("groups", [groups.map(({ queue, background_tasks, ...rest }) => rest), activity?.groups.map(g => [g.id, g.name, g.messages, g.speaks.length]), activity?.tokens, Math.floor(now / 60000)])) return;
  const today = new Map((activity?.groups || []).map(group => [group.id, group]));
  const tokens = new Map((activity?.tokens || []).map(item => [item.session_id, item]));
  const sorted = [...groups].sort((a, b) => (b.last_message_ms || 0) - (a.last_message_ms || 0));
  container.replaceChildren(...sorted.map(group => groupCard(group, today.get(group.id), tokens.get(group.id), now)));
}

function runFailures(detail) {
  const failures = [...detail.errors];
  for (const [id, group] of Object.entries(detail.groups)) {
    if (group.profiles_failed) failures.push(`群 ${groupTitle(id)} 有 ${group.profiles_failed} 份档案整理失败`);
    if (group.notes_failed) failures.push(`群 ${groupTitle(id)} 群志整理失败`);
  }
  for (const [preset, persona] of Object.entries(detail.personas)) {
    if (persona.life_failed) failures.push(`${preset} 人生线整理失败`);
    if (persona.diary_failed) failures.push(`${preset} 日记没写成`);
    for (const id of persona.groups_blocked) failures.push(`群 ${groupTitle(id)} 群层整理失败，停在半路`);
  }
  if (detail.memories?.pending) failures.push(`还有 ${detail.memories.pending} 条记忆没算出向量`);
  return failures;
}

function runSummary(detail) {
  const groups = Object.values(detail.groups);
  const personas = Object.values(detail.personas);
  return [
    `档案 ${groups.reduce((sum, group) => sum + group.profiles, 0)} 人`,
    `群志 ${groups.filter(group => group.notes).length} 个`,
    `群层 ${personas.reduce((sum, persona) => sum + persona.groups_updated.length, 0)} 个`,
    detail.memories ? `删过期 ${detail.memories.deleted} 条` : "",
  ].filter(Boolean).join("，");
}

function renderAcMaintenance() {
  const { runs } = state.autochat;
  const now = gmt8Parts();
  const ranToday = runs.some(run => isoDay(run.started_at_ms) === `${now.year}-${now.month}-${now.day}`);
  const note = $("#ac-maintenance-note");
  note.hidden = ranToday || Number(now.hour) < MAINTENANCE_LATE_HOUR;
  note.textContent = "今天还没有整理记录，03:30 那次可能没跑。";
  $("#ac-maintenance-empty").hidden = runs.length > 0;
  if (!acChanged("runs", runs)) return;
  $("#ac-maintenance").replaceChildren(...runs.map(run => {
    const button = el("button", `row ac-run${run.status === "partial" ? " warning" : ""}`);
    button.type = "button";
    button.addEventListener("click", () => showAcRun(run));
    const failures = runFailures(run.detail);
    button.append(
      el("span", "mono", `${monthDay(run.started_at_ms)} ${hhmm(run.started_at_ms)}`),
      el("span", `tag ${run.status === "ok" ? "success" : "warning"}`, run.status === "ok" ? "正常" : "部分失败"),
      el("span", "ac-dim", formatSpan(run.finished_at_ms - run.started_at_ms)),
      el("span", "ac-run-text", failures[0] || runSummary(run.detail)),
    );
    const item = el("li");
    item.append(button);
    return item;
  }));
}

function selfLife(persona) {
  const block = el("div", "ac-self-block");
  block.append(el("p", "ac-prose", persona.life.text || "还没整理过。"));
  const pending = persona.life.pending_facts;
  block.append(el("p", "ac-note", [
    persona.life.last_digest ? `整理到 ${persona.life.last_digest}` : "",
    `待整理的自我陈述 ${pending.length} 条`,
  ].filter(Boolean).join("，")));
  if (pending.length) {
    const list = el("ul", "ac-list");
    for (const fact of pending) list.append(el("li", "", fact));
    block.append(list);
  }
  return block;
}

function selfDiary(persona) {
  const block = el("div", "ac-self-block");
  if (!persona.diary.length) block.append(el("p", "ac-note", "还没有日记。"));
  for (const entry of [...persona.diary].reverse()) {
    const item = el("div", "ac-diary");
    item.append(el("span", "mono ac-dim", entry.day.slice(5)), el("p", "ac-prose", entry.text));
    block.append(item);
  }
  return block;
}

function selfSleep(persona) {
  const block = el("div", "ac-self-block");
  if (!persona.nights.length) block.append(el("p", "ac-note", "还没有记录。"));
  const list = el("ul", "ac-list");
  for (const night of [...persona.nights].reverse()) {
    const row = el("li");
    row.append(
      el("span", "mono", monthDay(night.sleep_ms)),
      el("span", "mono", `${hhmm(night.sleep_ms)}–${night.wake_ms ? hhmm(night.wake_ms) : "还没醒"}`),
    );
    if (night.wake_ms) row.append(el("span", "ac-dim", formatSpan(night.wake_ms - night.sleep_ms)));
    list.append(row);
  }
  block.append(list);
  return block;
}

function selfSegments(persona, dayStart) {
  const block = el("div", "ac-self-block");
  const list = el("ul", "ac-list");
  for (const segment of persona.timeline) {
    if (segment.end_ms && segment.end_ms < dayStart) continue;
    const mode = segmentMode(segment);
    const row = el("li");
    row.append(
      el("span", "mono", `${hhmm(segment.start_ms)}–${segment.end_ms ? hhmm(segment.end_ms) : ""}`),
      el("span", `ac-mode mode-${mode}`, MODE_LABELS[mode]),
      el("span", "", segment.doing),
      el("span", "ac-dim", SOURCE_LABELS[segment.source] || segment.source),
    );
    list.append(row);
  }
  block.append(list);
  return block;
}

function renderAcSelf() {
  const { activity } = state.autochat;
  if (!acChanged("self", [activity?.personas, state.acTab])) return;
  const container = $("#ac-self");
  if (!activity) {
    container.replaceChildren(el("p", "ac-note", "还没有拿到过 autochat 的状态。"));
    return;
  }
  const many = activity.personas.length > 1;
  container.replaceChildren(...activity.personas.flatMap(persona => {
    const render = {
      life: () => selfLife(persona),
      diary: () => selfDiary(persona),
      sleep: () => selfSleep(persona),
      segments: () => selfSegments(persona, activity.day_start_ms),
    }[state.acTab];
    return many ? [el("h3", "ac-section-title", persona.name), render()] : [render()];
  }));
}

function renderAcNow() {
  const personas = (state.autochat.status.overview?.personas || []).filter(persona => persona.mode);
  if (!acChanged("now", personas)) return;
  $("#ac-now").replaceChildren(...personas.map(persona => {
    const mode = persona.sleep ? "sleep" : persona.mode;
    const line = el("p");
    line.append(
      el("span", "ac-now-name", persona.name),
      el("span", `ac-mode mode-${mode}`, MODE_LABELS[mode]),
      el("span", "", `${persona.doing}，到 ${hhmm(persona.until_ms)}`),
    );
    if (persona.calls) line.append(el("span", "ac-dim", `放下手机后被叫了 ${persona.calls} 次`));
    const night = persona.last_night;
    if (night) {
      line.append(el("span", "ac-dim", night.wake_ms ? `昨晚 ${hhmm(night.sleep_ms)}–${hhmm(night.wake_ms)}` : `${hhmm(night.sleep_ms)} 睡的`));
    }
    return line;
  }));
}

function renderAutochat() {
  const { status } = state.autochat;
  const banner = $("#ac-offline");
  banner.hidden = status.reachable;
  if (!status.reachable) {
    banner.textContent = `连不上 autochat：${status.reason}。`
      + (status.last_ok_ms ? `最后成功在 ${formatTime(status.last_ok_ms, false)}，当前状态是那时的快照；` : "")
      + "LLM 曲线、决策和整理记录来自共享库，照常可看。";
  }
  $("#view-autochat").classList.toggle("stale", !status.reachable);
  renderAcProcess();
  renderAcNow();
  renderAcLlm();
  renderAcToday();
  renderAcGroups();
  renderAcMaintenance();
  renderAcSelf();
}

/* Dialogs share the existing detail dialog; loading never blocks opening. */

function acSection(title, ...children) {
  const section = el("section", "ac-section");
  section.append(el("h3", "ac-section-title", title), ...children);
  return section;
}

function openAcDialog(title, tagClass, tagText, nodes) {
  $("#detail-title").textContent = title;
  const tag = $("#detail-tag");
  tag.className = `tag ${tagClass}`;
  tag.textContent = tagText;
  $("#detail-exchange").replaceChildren();
  $("#detail-meta").replaceChildren();
  $("#raw-section").hidden = true;
  $("#detail-custom").replaceChildren(...nodes);
  if (!$("#detail-dialog").open) $("#detail-dialog").showModal();
}

function lookOutcome(look) {
  if (look.speak) return ["开口", "success"];
  if (look.muted) return ["禁言中", "warning"];
  if (look.willing === null) return ["Feedback 失败", "failure"];
  if (look.willing < look.threshold) return ["不想说", ""];
  return ["自己说得多了", ""];
}

function lookRow(look) {
  const row = el("li", "ac-look");
  const [outcome, tagClass] = lookOutcome(look);
  row.append(
    el("span", "mono", formatTime(look.created_at_ms, false)),
    el("span", "", TRIGGER_LABELS[look.trigger] || look.trigger),
    el("span", "mono ac-dim", `未读 ${look.unread}`),
    el("span", "mono ac-dim", `${look.willing === null ? "—" : look.willing.toFixed(2)}/${look.threshold.toFixed(2)}`),
    el("span", `tag ${tagClass || "plain"}`, outcome),
  );
  const extras = [
    look.focus,
    look.react && `贴了 ${look.react}`,
    look.poke && `戳了 ${look.poke}`,
    `自己占比 ${Math.round(look.self_share * 100)}%`,
  ].filter(Boolean).join("；");
  row.append(el("p", "ac-look-extra", extras));
  return row;
}

async function appendLooks(groupId, list, more, before) {
  const params = before ? `?before=${before}` : "";
  const page = await (await api(`/api/autochat/groups/${groupId}/looks${params}`)).json();
  list.append(...page.items.map(lookRow));
  if (!page.items.length && !before) list.append(el("li", "ac-note", "还没有记录。"));
  more.hidden = !page.next_before;
  more.onclick = () => appendLooks(groupId, list, more, page.next_before).catch(error => toast(error.message));
}

function tokensTable(data) {
  if (!data.models.length) return el("p", "ac-note", "这段时间没有用量。");
  const table = el("table", "ac-table");
  const head = el("tr");
  for (const label of ["模型", "输入", "输出", "思考", "次数", "合计"]) head.append(el("th", label === "模型" ? "" : "num", label));
  table.append(head);
  for (const model of data.models) {
    const row = el("tr");
    row.append(
      el("td", "", model.model),
      el("td", "num mono", formatTokens(model.prompt)),
      el("td", "num mono", formatTokens(model.completion)),
      el("td", "num mono", formatTokens(model.reasoning)),
      el("td", "num mono", String(model.requests)),
      el("td", "num mono", formatTokens(model.prompt + model.completion)),
    );
    table.append(row);
  }
  return table;
}

function tokensSection(groupId) {
  const range = el("div", "range");
  const body = el("div", "", "正在加载…");
  const load = async hours => {
    $$("button", range).forEach(button => button.classList.toggle("active", Number(button.dataset.hours) === hours));
    try {
      const data = await (await api(`/api/autochat/groups/${groupId}/tokens?hours=${hours}`)).json();
      body.replaceChildren(tokensTable(data));
    } catch (error) {
      body.replaceChildren(el("p", "ac-note", `进程离线：${error.message}`));
    }
  };
  for (const [hours, label] of [[24, "24 小时"], [168, "7 天"], [720, "30 天"]]) {
    const button = el("button", "", label);
    button.type = "button";
    button.dataset.hours = hours;
    button.addEventListener("click", () => load(hours));
    range.append(button);
  }
  load(24);
  return acSection("Token 用量", range, body);
}

function textBlock(text, empty) {
  return el("p", text ? "ac-prose" : "ac-note", text || empty);
}

async function showAcGroup(groupId) {
  const list = el("ul", "ac-looks");
  const more = el("button", "more-button", "加载更早的决策");
  more.type = "button";
  more.hidden = true;
  const summary = acSection("话题摘要", el("p", "ac-note", "正在加载…"));
  const live = el("div", "ac-detail");
  openAcDialog(groupTitle(groupId), "", "", [
    summary,
    tokensSection(groupId),
    acSection("最近的决策（意愿/门槛）", list, more),
    live,
  ]);
  appendLooks(groupId, list, more, 0).catch(error => toast(error.message));
  try {
    const detail = await (await api(`/api/autochat/groups/${groupId}`)).json();
    const messages = el("ul", "ac-messages");
    for (const message of detail.messages) {
      const row = el("li", message.self ? "self" : "");
      row.append(el("span", "mono ac-dim", hhmm(message.time_ms)), el("span", "ac-speaker", message.user_name), el("span", "", message.content));
      messages.append(row);
    }
    const threads = el("ul", "ac-list");
    for (const thread of detail.threads) {
      const row = el("li");
      row.append(
        el("span", "tag plain", thread.kind),
        el("span", "", `${thread.who ? `${thread.who}：` : ""}${thread.content}`),
        el("span", "ac-dim", `${thread.ask_from_ms > Date.now() ? `${monthDay(thread.ask_from_ms)} 起提，` : ""}${monthDay(thread.forget_at_ms)} 忘`),
      );
      threads.append(row);
    }
    const affinity = el("ul", "ac-list");
    for (const person of detail.affinity) {
      const row = el("li");
      row.append(
        el("span", "mono", String(person.affinity)),
        el("span", "", person.name),
        el("span", "ac-dim", `互动 ${person.interactions} 次${person.last_ms ? `，最近 ${relativeTime(person.last_ms)}` : ""}`),
      );
      affinity.append(row);
    }
    summary.replaceWith(acSection("话题摘要", textBlock(detail.summary, "还没有摘要。")));
    live.replaceChildren(
      acSection("最近的消息", detail.messages.length ? messages : textBlock("", "还没有消息。")),
      acSection("悬着的事", detail.threads.length ? threads : textBlock("", "没有。")),
      acSection("好感前 5", detail.affinity.length ? affinity : textBlock("", "还没有。")),
      acSection(`群里的她${detail.digested_until ? `（整理到 ${detail.digested_until}）` : ""}`, textBlock(detail.persona_group, "还没整理过。")),
      acSection("观察日记", textBlock(detail.journal.slice().reverse().join("\n"), "还没有。")),
      acSection(`群志${detail.notes_until_ms ? `（整理到 ${monthDay(detail.notes_until_ms)}）` : ""}`, textBlock(detail.notes, "还没有。")),
      el("p", "ac-note", detail.episodes_until_ms ? `分段整理读到 ${formatTime(detail.episodes_until_ms, false)}` : "分段整理还没开始"),
    );
  } catch (error) {
    summary.replaceWith(el("p", "ac-note", `进程离线，摘要、消息和群里的她要等它恢复才能看：${error.message}`));
  }
}

async function showAcRun(run) {
  const detail = run.detail;
  const day = isoDay(run.started_at_ms - DAY_MS);
  const failures = runFailures(detail);
  const meta = el("dl", "meta");
  for (const [label, value] of [
    ["时间", `${formatTime(run.started_at_ms)} 至 ${timeOfDay(run.finished_at_ms)}（GMT+8）`],
    ["耗时", formatSpan(run.finished_at_ms - run.started_at_ms)],
    ["过期清理", detail.memories ? `删 ${detail.memories.deleted} 条，补算向量 ${detail.memories.filled} 条，还差 ${detail.memories.pending} 条` : "没跑完"],
    ["悬着的事", detail.threads_forgotten === null ? "没跑完" : `忘掉 ${detail.threads_forgotten} 件`],
  ]) meta.append(el("dt", "", label), el("dd", "", value));

  const groups = el("table", "ac-table");
  const head = el("tr");
  for (const label of ["群", "档案", "群志", "群层"]) head.append(el("th", "", label));
  groups.append(head);
  const updated = new Set(Object.values(detail.personas).flatMap(persona => persona.groups_updated));
  const blocked = new Set(Object.values(detail.personas).flatMap(persona => persona.groups_blocked));
  for (const [id, group] of Object.entries(detail.groups)) {
    const row = el("tr");
    row.append(
      el("td", "", groupTitle(id)),
      el("td", group.profiles_failed ? "ac-warn" : "", `${group.profiles} 人${group.profiles_failed ? `，失败 ${group.profiles_failed}` : ""}`),
      el("td", group.notes_failed ? "ac-warn" : "", group.notes_failed ? "失败" : group.notes ? "已更新" : "不到期"),
      el("td", blocked.has(id) ? "ac-warn" : "", blocked.has(id) ? "失败" : updated.has(id) ? "已更新" : "没变"),
    );
    groups.append(row);
  }
  const nodes = [meta];
  if (failures.length) {
    nodes.push(el("pre", "error-text", failures.join("\n")));
  }
  nodes.push(acSection("各群", groups));
  for (const [preset, persona] of Object.entries(detail.personas)) {
    nodes.push(el("p", "ac-note", `${preset}：人生线整理到 ${persona.life_until || "还没整理过"}${persona.life_failed ? "，这次失败" : ""}${persona.diary_failed ? "，日记没写成" : ""}`));
  }
  const content = el("div", "ac-detail", "正在加载改动和日记…");
  nodes.push(content);
  openAcDialog(`${monthDay(run.started_at_ms)} 的整理`, run.status === "ok" ? "success" : "warning", run.status === "ok" ? "正常" : "部分失败", nodes);

  try {
    const digest = await (await api(`/api/autochat/digest?day=${day}`)).json();
    const changes = el("ul", "ac-list ac-list-stack");
    // The model writes "无" when a rewrite changed nothing worth noting.
    for (const item of digest.life) {
      if (item.changes !== "无") changes.append(el("li", "", `人生线：${item.changes}`));
    }
    for (const item of digest.groups) {
      if (item.changes !== "无") changes.append(el("li", "", `${groupTitle(item.session_id)}：${item.changes}`));
    }
    const journals = el("ul", "ac-list ac-list-stack");
    for (const item of digest.journals) journals.append(el("li", "", `${groupTitle(item.session_id)}：${item.text}`));
    content.replaceChildren(
      acSection(`${day} 的改动`, changes.children.length ? changes : textBlock("", "没有改动。")),
      acSection(`${day} 的日记`, textBlock(digest.diary.map(item => item.text).join("\n"), "这天没有日记，或者已经过了保留期。")),
      acSection(`${day} 的观察日记`, journals.children.length ? journals : textBlock("", "这天没有，或者已经过了保留期。")),
    );
  } catch (error) {
    content.replaceChildren(el("p", "ac-note", `进程离线，改动说明和日记要等它恢复才能看：${error.message}`));
  }
}

async function loadAutochatStatus() {
  const status = await (await api("/api/autochat")).json();
  state.autochat.status = status;
  $("#nav-autochat").className = `status-dot ${status.reachable ? "ok" : "bad"}`;
}

async function loadAutochat() {
  await loadAutochatStatus();
  const { status } = state.autochat;
  const [activity, history, runs] = await Promise.all([
    status.activity_ms && status.activity_ms !== state.autochat.activityMs
      ? api("/api/autochat/activity").then(response => response.json())
      : state.autochat.activity,
    api("/api/autochat/history").then(response => response.json()),
    api("/api/autochat/maintenance").then(response => response.json()),
  ]);
  state.autochat.activity = activity;
  state.autochat.activityMs = status.activity_ms || 0;
  state.autochat.history = history;
  state.autochat.runs = runs;
  renderAutochat();
}

/* Views, refresh, theme */

function activeView() {
  return $(".nav-item.active").dataset.view;
}

function showView(view) {
  $$(".nav-item").forEach(item => {
    const active = item.dataset.view === view;
    item.classList.toggle("active", active);
    if (active) item.setAttribute("aria-current", "page");
    else item.removeAttribute("aria-current");
  });
  $$(".view").forEach(section => section.classList.toggle("active", section.id === `view-${view}`));
  // The 24-hour pulse is HakuBot only; autochat has its own timeline.
  $(".pulse").hidden = view === "autochat";
}

function loadView(view) {
  if (view === "events") return loadEvents();
  if (view === "diagnostics") return loadDiagnostics();
  if (view === "storage") return loadStorage();
  if (view === "autochat") return loadAutochat();
  return loadSystemHistory();
}

function setLive(ok) {
  const live = $("#live");
  live.classList.remove("tick");
  live.classList.toggle("error", !ok);
  $(".live-text", live).textContent = ok ? "每 3 秒刷新" : "刷新失败，正在重试";
  if (!ok) return;
  void live.offsetWidth; // restart the pulse animation
  live.classList.add("tick");
}

async function refreshVisibleView() {
  if (document.hidden || state.refreshInFlight) return;
  state.refreshInFlight = true;
  try {
    const view = activeView();
    await Promise.all([
      loadBotStatus(),
      loadOverview(),
      loadSystemStatus(),
      // The sidebar dot needs this on every view; the autochat view loads it itself.
      view === "autochat" ? null : loadAutochatStatus(),
      loadView(view),
    ]);
    state.refreshErrorShown = false;
    setLive(true);
  } catch (error) {
    setLive(false);
    if (!state.refreshErrorShown) {
      toast(`自动刷新失败：${error.message}`);
      state.refreshErrorShown = true;
    }
  } finally {
    state.refreshInFlight = false;
  }
}

const systemTheme = () => (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");

function applyTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  $("#theme-toggle").setAttribute("aria-label", theme === "light" ? "切换到深色主题" : "切换到浅色主题");
}

// Choosing the system's own theme drops the saved choice, so the page
// follows the system again from then on.
function toggleTheme() {
  const next = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
  if (next === systemTheme()) localStorage.removeItem("webconsole_theme");
  else localStorage.setItem("webconsole_theme", next);
  applyTheme(next);
}

function bindEvents() {
  const fail = error => toast(error.message);

  $$(".nav-item").forEach(item => item.addEventListener("click", () => {
    showView(item.dataset.view);
    loadView(item.dataset.view).catch(fail);
  }));

  const eventForm = $("#event-filters");
  eventForm.addEventListener("submit", event => event.preventDefault());
  eventForm.addEventListener("change", event => {
    if (event.target.name === "quick_time") {
      const custom = event.target.value === "custom";
      $(".custom-range", eventForm).hidden = !custom;
      if (custom) {
        // Start the custom range from the window that was just showing.
        eventForm.elements.from.value = partsToInput(gmt8Parts(new Date(Date.now() - 24 * 3600000)));
        eventForm.elements.to.value = partsToInput(gmt8Parts());
      }
    }
    resetEvents().catch(fail);
  });
  $("#reset-event-filters").addEventListener("click", () => {
    eventForm.reset();
    $(".custom-range", eventForm).hidden = true;
    resetEvents().catch(fail);
  });
  $("#load-more").addEventListener("click", () => loadEvents({ append: true }).catch(fail));

  const diagnosticForm = $("#diagnostic-filters");
  diagnosticForm.addEventListener("submit", event => event.preventDefault());
  diagnosticForm.addEventListener("change", () => loadDiagnostics().catch(fail));

  $("#cleanup-period").addEventListener("change", updateCleanupCutoff);
  $("#cleanup-cutoff").addEventListener("input", () => {
    $("#cleanup-period").value = "custom";
    updateCleanupCutoff();
  });
  $("#preview-cleanup").addEventListener("click", () => previewCleanup().catch(fail));
  $("#execute-cleanup").addEventListener("click", () => executeCleanup().catch(fail));

  $$("#chart-range button").forEach(button => button.addEventListener("click", () => {
    $$("#chart-range button").forEach(item => item.classList.toggle("active", item === button));
    state.systemWindow = button.dataset.window;
    loadSystemHistory().catch(fail);
  }));

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if (activeView() === "system" && state.systemHistory) renderCharts();
    }, 150);
  });

  $("#copy-raw").addEventListener("click", () => {
    navigator.clipboard.writeText($("#raw-content").textContent)
      .then(() => toast("已复制"))
      .catch(() => toast("复制失败，浏览器没有授予剪贴板权限"));
  });

  $$("#ac-tabs button").forEach(button => button.addEventListener("click", () => {
    $$("#ac-tabs button").forEach(item => item.classList.toggle("active", item === button));
    state.acTab = button.dataset.tab;
    renderAcSelf();
  }));
  $("#ac-errors").addEventListener("click", showLlmErrors);

  $("#theme-toggle").addEventListener("click", toggleTheme);
  matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    if (!localStorage.getItem("webconsole_theme")) applyTheme(systemTheme());
  });
  document.addEventListener("visibilitychange", refreshVisibleView);
}

async function initialize() {
  applyTheme(document.documentElement.getAttribute("data-theme"));
  bindEvents();
  updateClock();
  setInterval(updateClock, 1000);
  updateCleanupCutoff();
  try {
    await Promise.all([loadFilters(), loadBotStatus(), loadOverview(), loadSystemStatus(), loadAutochatStatus(), loadEvents()]);
  } catch (error) {
    toast(`加载失败：${error.message}`);
  }
  setInterval(refreshVisibleView, 3000);
}

document.addEventListener("DOMContentLoaded", initialize);
