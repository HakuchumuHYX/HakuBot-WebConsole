"use strict";

const state = {
  events: [],
  eventCursor: "",
  eventSignature: "",
  knownEventIds: new Set(),
  diagnosticSignature: "",
  pulseSignature: "",
  openIssues: new Set(),
  previewCutoff: "",
  refreshInFlight: false,
  refreshErrorShown: false,
  systemWindow: "1h",
  systemHistory: null,
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

/* GMT+8 time helpers. The API speaks "YYYY-M-D HH:MM:SS" in GMT+8. */

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

// Browsers drop ":00" seconds from datetime-local values, so seconds are optional.
function inputToAPI(value) {
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/);
  if (!match) return "";
  return `${match[1]}-${Number(match[2])}-${Number(match[3])} ${match[4]}:${match[5]}:${match[6] || "00"}`;
}

function partsToInput(parts) {
  const pad = value => String(value).padStart(2, "0");
  return `${parts.year}-${pad(parts.month)}-${pad(parts.day)}T${pad(parts.hour)}:${pad(parts.minute)}:${pad(parts.second)}`;
}

function hoursAgoAPI(hours) {
  return inputToAPI(partsToInput(gmt8Parts(new Date(Date.now() - hours * 3600000))));
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
    const from = inputToAPI(form.elements.from.value);
    const to = inputToAPI(form.elements.to.value);
    if (from) params.set("from", from);
    if (to) params.set("to", to);
  } else {
    params.set("from", hoursAgoAPI(HOURS[range]));
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
  const [, time] = item.time_display.split(" ");
  const flag = eventFlag(item);
  const row = el("button", `row event-grid ${flag}${fresh ? " fresh" : ""}`);
  row.type = "button";

  const message = el("span", "cell-msg");
  if (flag) message.append(el("span", `tag ${flag}`, flag === "failure" ? "失败" : "警告"));
  message.append(...exchange(item.request_summary, item.response_summary));

  const duration = el("span", "cell-dur num", formatDuration(item.duration_ms));
  if (item.duration_ms >= SLOW_MS) duration.classList.add("slow");

  row.append(
    el("span", "cell-time", time),
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
    const [date] = item.time_display.split(" ");
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

  const finished = detail.finished_display?.split(" ")[1];
  setMeta([
    ["时间", `${detail.time_display}${finished ? ` 至 ${finished}` : ""}（GMT+8）`],
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
  $("#detail-title").textContent = group.plugin_name || "系统";
  const tag = $("#detail-tag");
  tag.className = `tag ${group.level.toLowerCase()}`;
  tag.textContent = LEVEL_LABELS[group.level];
  $("#detail-exchange").replaceChildren();
  setMeta([
    ["时间", `${occurrence.time_display}（GMT+8）`],
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
  if (range !== "all") params.set("from", hoursAgoAPI(HOURS[range]));
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
    ? `首次 ${group.first_display}，最近 ${group.last_display}。`
    : `发生于 ${group.last_display}。`));
  const occurrences = el("ol", "occurrences");
  for (const occurrence of group.occurrences) {
    // The year is noise here; the dialog shows the full time.
    const button = el("button", "", occurrence.time_display.replace(/^\d{4}-/, ""));
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
    ? `${count} 条\n${earliest} 至 ${latest}`
    : "没有记录";
  $("#size-db").textContent = data.database;
  $("#size-wal").textContent = `${data.wal} / ${data.shm}`;
  $("#size-spool").textContent = `${data.spool}，${data.spool_files} 个文件`;
  $("#response-records").textContent = records(data.response_count, data.response_earliest, data.response_latest);
  $("#diagnostic-records").textContent = records(data.diagnostic_count, data.diagnostic_earliest, data.diagnostic_latest);
  $("#retention-policy").textContent = data.retention_days > 0
    ? `保留最近 ${data.retention_days} 天，每天 04:15（GMT+8）清理更早的记录`
    : "已关闭，只能手动清理";
}

function updateCleanupCutoff() {
  const value = $("#cleanup-period").value;
  if (value !== "custom") $("#cleanup-cutoff").value = calendarMonthsAgo(Number(value));
  state.previewCutoff = "";
  $("#execute-cleanup").disabled = true;
  $("#execute-cleanup").textContent = "清理";
  $("#cleanup-preview").textContent = "先预览会删除多少条记录。";
}

async function previewCleanup() {
  const cutoff = inputToAPI($("#cleanup-cutoff").value);
  if (!cutoff) throw new Error("请填写完整的截止时间");
  const { preview } = await (await mutation("/api/storage/cleanup/preview", { cutoff })).json();
  state.previewCutoff = cutoff;
  $("#cleanup-preview").textContent =
    `会删除 ${preview.cutoff_display}（GMT+8）之前的 ${preview.response_count} 条响应和 ${preview.diagnostic_count} 条诊断。`;
  $("#execute-cleanup").textContent = `清理 ${preview.response_count + preview.diagnostic_count} 条记录`;
  $("#execute-cleanup").disabled = false;
}

async function executeCleanup() {
  if (!window.confirm(`永久删除 ${state.previewCutoff}（GMT+8）之前的日志？删除后无法恢复。`)) return;
  $("#execute-cleanup").disabled = true;
  const data = await (await mutation("/api/storage/cleanup", { cutoff: state.previewCutoff })).json();
  state.previewCutoff = "";
  $("#execute-cleanup").textContent = "清理";
  $("#cleanup-preview").textContent = `已清理 ${data.responses_deleted} 条响应和 ${data.diagnostics_deleted} 条诊断，空间已回收。`;
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
    tooltip: p => `${p.time_display}\nCPU ${p.cpu_percent.toFixed(1)}%，峰值 ${p.cpu_max.toFixed(1)}%`,
  });
  renderChart("#chart-mem", {
    ...common,
    series: [{ key: "memory_percent", className: "series-2" }],
    yMax: 100,
    thresholds: true,
    formatY: percent,
    tooltip: p => `${p.time_display}\n内存 ${p.memory_percent.toFixed(1)}%（${formatBytes(p.memory_used_bytes)}）`,
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
    tooltip: p => `${p.time_display}\n↓ ${formatBytes(p.net_rx_bytes_per_sec)}/s，↑ ${formatBytes(p.net_tx_bytes_per_sec)}/s`,
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
}

function loadView(view) {
  if (view === "events") return loadEvents();
  if (view === "diagnostics") return loadDiagnostics();
  if (view === "storage") return loadStorage();
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
    await Promise.all([loadBotStatus(), loadOverview(), loadSystemStatus(), loadView(activeView())]);
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
  $("#reclaim-storage").addEventListener("click", async () => {
    try {
      await mutation("/api/storage/reclaim", {});
      toast("空闲页已回收");
      await loadStorage();
    } catch (error) { fail(error); }
  });

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
    await Promise.all([loadFilters(), loadBotStatus(), loadOverview(), loadSystemStatus(), loadEvents()]);
  } catch (error) {
    toast(`加载失败：${error.message}`);
  }
  setInterval(refreshVisibleView, 3000);
}

document.addEventListener("DOMContentLoaded", initialize);
