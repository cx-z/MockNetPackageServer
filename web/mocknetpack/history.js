"use strict";

// MockNetPack web: 历史日志视图（M2 O3.3 + O2.3）。
// 最近 48h 已结束会话的请求：服务端过滤（keyword/statusCode/from/to，
// M2-3 下推参数）+ limit/offset「加载更多」分页。只读——不提供删除/清空
// （O3.1 保留语义：服务端流量在 48h 窗口内由历史日志查询，Web 不再调
// DELETE）。打开时暂停当前会话实时轮询，关闭时恢复。

const HISTORY_PAGE = 100; // 与 TRAFFIC_PAGE 一致，契约 limit 上限 500
let history = null;       // { sid, offset, total, entries }

/** 打开历史日志：暂停实时轮询，拉设备最近已结束会话列表，默认选中最新。 */
async function openHistory() {
  if (!detail) return;
  stopTrafficPoll();            // 历史视图打开期间暂停实时轮询（互不干扰）
  $("historyView").classList.remove("hidden");
  history = null;
  $("historySessions").innerHTML = '<div class="history-empty">加载中…</div>';
  $("historyList").innerHTML = '<div class="history-empty">请选择左侧会话查看历史请求。</div>';
  $("historyInfo").textContent = "";
  $("historyMoreBtn").classList.add("hidden");
  resetHistoryFilters();
  await loadHistorySessions();
}

/** 关闭历史日志；若当前设备仍有抓包会话，恢复实时轮询。 */
function closeHistory() {
  $("historyView").classList.add("hidden");
  history = null;
  if (detail && detail.sessionId && !trafficTimer) {
    trafficTimer = setInterval(pollTraffic, TRAFFIC_POLL_MS);
    pollTraffic();
  }
}

function resetHistoryFilters() {
  $("hKeyword").value = "";
  $("hStatusCode").value = "";
  $("hFrom").value = "";
  $("hTo").value = "";
}

/** 会话列表：GET /sessions?app=&did=，前端筛出 ended 且 48h 保留期内。 */
async function loadHistorySessions() {
  const { app, did } = detail;
  try {
    const res = await apiFetch("/sessions?app=" + encodeURIComponent(app) + "&did=" + encodeURIComponent(did));
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    const now = Date.now();
    const ended = (data.sessions || []).filter((s) =>
      s.status === "ended" &&
      (!s.retainUntil || new Date(s.retainUntil).getTime() > now));
    ended.sort((a, b) => (b.endedAt || b.startedAt || "").localeCompare(a.endedAt || a.startedAt || ""));

    const box = $("historySessions");
    if (!ended.length) {
      box.innerHTML = '<div class="history-empty">暂无已结束会话（48h 保留期内）。</div>';
      $("historyList").innerHTML = '<div class="history-empty">—</div>';
      return;
    }
    box.innerHTML = "";
    for (const s of ended) {
      const el = document.createElement("div");
      el.className = "history-session";
      el.innerHTML =
        '<div class="hs-time">' + clockTime(s.endedAt || s.startedAt) +
        " · " + esc(s.id ? shortId(s.id) : "") + "</div>" +
        '<div class="hs-meta">请求 ' + s.requestCount + " · 结束于 " +
        clockTime(s.endedAt || "—") + "</div>";
      el.onclick = () => {
        box.querySelectorAll(".history-session").forEach((x) => x.classList.remove("active"));
        el.classList.add("active");
        selectHistorySession(s.id);
      };
      box.appendChild(el);
    }
    // 默认选中最新（列表第一个）。
    ended[0] && selectHistorySession(ended[0].id);
  } catch (e) {
    $("historySessions").innerHTML = '<div class="history-empty">加载会话失败：' + esc(e.message) + "</div>";
  }
}

/** 选中会话：重置过滤与分页，拉第一页。 */
async function selectHistorySession(sid) {
  history = { sid, offset: 0, total: 0, entries: [] };
  resetHistoryFilters();
  await applyHistoryFilter();
}

/** datetime-local 值（本地时区）→ RFC3339（UTC，无毫秒，Go time.RFC3339 可解析）。 */
function toRfc3339(v) {
  if (!v) return "";
  const iso = new Date(v).toISOString();
  return iso.replace(/\.\d{3}Z$/, "Z");
}

/** 从表单构建服务端过滤 query（M2-3 参数，下推，不在前端过滤）。 */
function historyQuery(offset) {
  const p = new URLSearchParams();
  const kw = $("hKeyword").value.trim();
  const sc = $("hStatusCode").value.trim();
  const from = toRfc3339($("hFrom").value);
  const to = toRfc3339($("hTo").value);
  if (kw) p.set("keyword", kw);
  if (sc) p.set("statusCode", sc);
  if (from) p.set("from", from);
  if (to) p.set("to", to);
  p.set("limit", String(HISTORY_PAGE));
  p.set("offset", String(offset));
  return p.toString();
}

/** 应用过滤：offset 归零重拉（O2.2：过滤在服务端执行）。 */
async function applyHistoryFilter() {
  if (!history) return;
  history.offset = 0;
  history.entries = [];
  $("historyMoreBtn").classList.add("hidden");
  await loadHistoryPage(true);
}

/** 加载更多：offset += PAGE 追加（O2.3 分页）。 */
async function loadHistoryMore() {
  if (!history) return;
  await loadHistoryPage(false);
}

async function loadHistoryPage(replace) {
  const h = history;
  try {
    const res = await apiFetch("/sessions/" + encodeURIComponent(h.sid) + "/traffic?" + historyQuery(h.offset));
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    h.total = data.total;
    if (replace) h.entries = data.entries || [];
    else h.entries = h.entries.concat(data.entries || []);
    h.offset += (data.entries || []).length;
    $("historyInfo").textContent = "共 " + h.total + " 条（已加载 " + h.entries.length + "）";
    renderHistoryList(replace);
    const hasMore = h.entries.length < h.total;
    $("historyMoreBtn").classList.toggle("hidden", !hasMore);
    if (!hasMore) $("historyMoreBtn").textContent = "加载更多";
  } catch (e) {
    $("historyInfo").textContent = "加载失败：" + e.message;
  }
}

/** 渲染历史请求行（只读：无 ✕ 删除按钮，符合 O3.1 保留语义）。 */
function renderHistoryList(replace) {
  const box = $("historyList");
  if (replace) box.innerHTML = "";
  if (!history.entries.length) {
    box.innerHTML = '<div class="history-empty">该会话暂无匹配请求（调整过滤条件重试）。</div>';
    return;
  }
  for (const e of history.entries) {
    const el = document.createElement("div");
    el.className = "traffic-row";
    const statusCls = e.statusCode == null ? "st-err"
      : e.statusCode < 300 ? "st-2xx" : e.statusCode < 400 ? "st-3xx"
      : e.statusCode < 500 ? "st-4xx" : "st-5xx";
    el.innerHTML =
      '<span class="method ' + methodCls(e.method) + '">' + esc(e.method || "?") + "</span>" +
      '<span class="t-url">' + esc(e.path || e.url) + (e.query ? "?" + esc(e.query) : "") + "</span>" +
      '<span class="t-status ' + statusCls + '">' + (e.statusCode ?? "ERR") + "</span>" +
      '<span class="t-dur">' + e.durationMs + "ms</span>" +
      '<span class="t-time">' + clockTime(e.timestamp) + "</span>";
    box.appendChild(el);
  }
}
