"use strict";

// MockNetPack web: 长连接日志页签（能力线 L1, 业务级长连接日志）。
// 独立页签承载：与 HTTP 请求流分页签、互不混入；独立轮询 since 游标。
// 数据通道：GET /api/v1/sessions/{sid}/longconn（服务端独立额度/独立存档）。
// ============================================================================

const LONGCONN_PAGE = 100;   // 单次拉取页数（同 traffic 页大小）
const LONGCONN_CAP = 300;    // 页面累积上限（防长时间抓包 DOM 膨胀）

let longConnTimer = null;
let longConnLog = [];        // 页面累积的长连接记录（按 id 去重，跨会话保留同 pageLog）
let longConnFilter = "";

/** 页签切换：HTTP 请求流 / 长连接。左列面板 + 右列详情同步显隐。 */
function showLogPane(kind) {
  const isLC = kind === "longconn";
  $("tabTraffic").classList.toggle("active", !isLC);
  $("tabLongConn").classList.toggle("active", isLC);
  // 左列：长连接页签只显示长连接面板（Mock 规则/请求流均为 HTTP 域，长连接不支持）
  $("rulesPane").classList.toggle("hidden", isLC);
  $("trafficPane").classList.toggle("hidden", isLC);
  $("longconnPane").classList.toggle("hidden", !isLC);
  // 右列：详情面板同步
  $("longconnDetail").classList.toggle("hidden", !isLC);
  $("trafficDetail").classList.toggle("hidden", isLC);
  $("ruleDetail").classList.toggle("hidden", isLC);
}

/** 绑定会话到长连接轮询（在 traffic.bindSession 后调用）。capturing → 2s 轮询；
 *  无会话 → 停止轮询，已展示记录保留不清空（同请求流语义）。 */
function bindLongConnSession(session) {
  stopLongConnPoll();
  if (!session || session.status !== "capturing") return;
  longConnTimer = setInterval(pollLongConn, TRAFFIC_POLL_MS);
  pollLongConn();
}

function stopLongConnPoll() {
  if (longConnTimer) { clearInterval(longConnTimer); longConnTimer = null; }
}

/** 轮询：先拉 total，再拉最后一页，按 id 合并进 longConnLog（同请求流模式）。 */
async function pollLongConn() {
  if (!detail || !detail.sessionId) return;
  const sid = detail.sessionId;
  try {
    const meta = await apiFetch("/sessions/" + encodeURIComponent(sid) +
      "/longconn?limit=1").then(r => r.json());
    if (!meta || typeof meta.total !== "number") return;
    const offset = Math.max(0, meta.total - LONGCONN_PAGE);
    const data = await apiFetch("/sessions/" + encodeURIComponent(sid) +
      "/longconn?limit=" + LONGCONN_PAGE + "&offset=" + offset).then(r => r.json());
    longConnLog = mergeLongConn(longConnLog, data.entries || []);
    renderLongConn();
  } catch (e) {
    $("longconnInfo").textContent = "长连接拉取失败：" + e.message;
  }
}

/** 按 id 去重合并，保留最近 LONGCONN_CAP 条（服务端 seq 升序到达序）。 */
function mergeLongConn(base, fresh) {
  const seen = new Map();
  for (const e of base) seen.set(e.id, e);
  for (const e of fresh) {
    if (!seen.has(e.id)) seen.set(e.id, e);
  }
  // 复活抑制：清空时刻之前的旧条目一律丢弃（同 traffic._logClearedAt 语义）。
  const cutMs = detail && detail._longConnClearedAt ? new Date(detail._longConnClearedAt).getTime() : 0;
  let arr = Array.from(seen.values());
  if (cutMs > 0) {
    arr = arr.filter((e) => new Date(e.ts || "").getTime() > cutMs);
  }
  return arr.length > LONGCONN_CAP ? arr.slice(arr.length - LONGCONN_CAP) : arr;
}

/** eventType 徽标样式。 */
function longConnTypeCls(et) {
  switch (et) {
    case "packet": return "lc-packet";
    case "ack": return "lc-ack";
    case "connect": return "lc-connect";
    case "disconnect": return "lc-disconnect";
    case "heartbeat": return "lc-heartbeat";
    case "snapshot": return "lc-snapshot";
    default: return "lc-other";
  }
}

/** 渲染：最新 snapshot 卡片 + 倒序列表。 */
function renderLongConn() {
  if (!detail) return;
  // 最新 snapshot（seq 最大的 snapshot 事件）→ 状态卡片
  const snapshots = longConnLog.filter((r) => r.eventType === "snapshot" && r.snapshot);
  renderSnapshotCard(snapshots[snapshots.length - 1]);

  const q = longConnFilter.trim().toLowerCase();
  // snapshot 是连接状态（顶部状态卡片已展示），不进日志列表、不占计数。
  let entries = longConnLog.filter((e) => e.eventType !== "snapshot");
  const total = entries.length;
  if (q) {
    entries = entries.filter((e) =>
      (e.eventType || "").toLowerCase().includes(q) ||
      (e.bizType || "").toLowerCase().includes(q) ||
      (e.msgId || "").toLowerCase().includes(q) ||
      (e.channelId || "").toLowerCase().includes(q));
  }
  $("longconnInfo").textContent = "共 " + total + " 条" + (q ? "（过滤出 " + entries.length + " 条）" : "");

  const box = $("longconnList");
  if (!entries.length) {
    box.innerHTML = '<div class="empty">暂无长连接日志。设备长连接事件会实时出现在这里。</div>';
    return;
  }
  // 倒序：最新在上。按 ts（RFC3339 字符串）排序，不依赖 seq（跨轮询累积后 seq 可能乱）。
  entries = entries.slice().sort((a, b) => (b.ts || "").localeCompare(a.ts || ""));
  box.innerHTML = "";
  for (const e of entries) {
    const el = document.createElement("div");
    el.className = "lc-row" + (detail._activeLongConn === e.id ? " active" : "");
    const meta = [];
    if (e.bizType) meta.push("biz=" + e.bizType);
    if (e.msgId) meta.push("msg=" + e.msgId);
    if (e.duplicate) meta.push("dup");
    if (e.empty) meta.push("empty");
    el.innerHTML =
      '<span class="lc-type ' + longConnTypeCls(e.eventType) + '">' + esc(e.eventType || "?") + "</span>" +
      '<span class="lc-meta">' + esc(meta.join(" · ")) + "</span>" +
      '<span class="t-time">' + clockTime(e.ts) + "</span>";
    el.onclick = () => {
      detail._activeLongConn = e.id;
      renderLongConnDetail(e);
      box.querySelectorAll(".lc-row").forEach((r) => r.classList.toggle("active", r === el));
    };
    box.appendChild(el);
  }
}

/** 最新连接状态卡片。 */
function renderSnapshotCard(s) {
  const box = $("longconnSnapshot");
  if (!s) { box.classList.add("hidden"); box.innerHTML = ""; return; }
  const snap = s.snapshot || {};
  const row = (k, v) => '<div class="d-kv"><span class="d-k">' + esc(k) + '</span><span class="d-v">' + esc(v) + "</span></div>";
  box.innerHTML =
    '<div class="d-title">连接状态（最新 snapshot · ' + clockTime(s.ts) + "）</div>" +
    row("state", snap.state || "—") +
    row("socket", snap.isSocketConnected ? "已连接" : "未连接") +
    (snap.routerHost ? row("router", snap.routerHost + (snap.routerPort ? ":" + snap.routerPort : "")) : "") +
    (snap.clientId ? row("clientId", snap.clientId) : "") +
    (snap.accountMid ? row("mid", snap.accountMid) : "") +
    (snap.heartbeatInterval ? row("心跳", snap.heartbeatInterval + "s") : "") +
    (snap.receivedMessageCount ? row("累计收包", snap.receivedMessageCount) : "") +
    (snap.receiveServiceCount ? row("累计分发", snap.receiveServiceCount) : "") +
    (snap.lastError ? row("最近错误", snap.lastError) : "");
  box.classList.remove("hidden");
}

/** 详情面板：Overview 字段 + snapshot + payload（Raw，esc 防 XSS）。
 *  target 默认为主详情区 longconnDetail；history.js 可传 historyDetail 浮层。 */
function renderLongConnDetail(e, target) {
  detail._activeTraffic = null;
  const box = target || $("longconnDetail");
  const row = (k, v) => (v === undefined || v === null || v === "")
    ? ""
    : '<div class="d-kv"><span class="d-k">' + esc(k) + '</span><span class="d-v">' + esc(String(v)) + "</span></div>";

  // 工具条：复制（payload 文本）+ 分享（单条快照，7 天有效）。
  const toolbar =
    '<div class="d-toolbar">' +
      '<button class="small ghost" id="lcCopyBtn">复制内容</button>' +
      '<button class="small ghost" id="lcShareBtn">分享</button>' +
    "</div>";

  const snap = e.snapshot ?
    '<div class="d-block"><div class="d-title">snapshot</div>' +
      row("state", e.snapshot.state) +
      row("isSocketConnected", e.snapshot.isSocketConnected) +
      row("routerHost", e.snapshot.routerHost) +
      row("routerPort", e.snapshot.routerPort) +
      row("clientId", e.snapshot.clientId) +
      row("receivedMessageCount", e.snapshot.receivedMessageCount) +
      row("receiveServiceCount", e.snapshot.receiveServiceCount) +
      row("lastError", e.snapshot.lastError) + "</div>"
    : "";

  // payload：三选一（完整 text / base64 二进制 / 截断 preview）。全部 esc。
  let payloadBlock = "";
  if (e.payloadText) {
    payloadBlock = '<div class="d-block"><div class="d-title">payload（解析内容）</div><pre class="raw">' + esc(e.payloadText) + "</pre></div>";
  } else if (e.payloadBase64) {
    payloadBlock = '<div class="d-block"><div class="d-title">payload（base64 二进制）</div><pre class="raw">' + esc(e.payloadBase64) + "</pre></div>";
  } else if (e.payloadPreview) {
    payloadBlock = '<div class="d-block"><div class="d-title">payload（超限预览，≤1KB）</div><pre class="raw">' + esc(e.payloadPreview) + "</pre></div>";
  }

  box.innerHTML =
    '<div class="detail-panel">' + toolbar +
      '<div class="d-kv"><span class="d-k">eventType</span><span class="d-v">' + esc(e.eventType || "?") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">channelId</span><span class="d-v">' + esc(e.channelId || "?") + "</span></div>" +
      row("seq", e.seq) +
      row("ts", e.ts) +
      row("msgId", e.msgId) +
      row("bizType", e.bizType) +
      row("zipType", e.zipType) +
      row("status", e.status) +
      row("receiver", e.receiver) +
      row("receiverResult", e.receiverResult) +
      row("duplicate", e.duplicate ? "true" : "") +
      row("empty", e.empty ? "true" : "") +
      row("ackSent", e.ackSent ? "true" : "") +
      snap + payloadBlock +
    "</div>";

  // 按钮绑定：复制整条记录可读摘要；分享调 shareRequest(longConnId)。
  const copyBtn = box.querySelector("#lcCopyBtn");
  const shareBtn = box.querySelector("#lcShareBtn");
  if (copyBtn) copyBtn.onclick = async () => {
    const kv = (k, v) => (v === undefined || v === null || v === "") ? "" : k + ": " + v + "\n";
    let text =
      "=== MockNetPack 长连接记录 ===\n" +
      kv("eventType", e.eventType) + kv("channelId", e.channelId) +
      kv("seq", e.seq) + kv("ts", e.ts) + kv("msgId", e.msgId) +
      kv("bizType", e.bizType) + kv("zipType", e.zipType) + kv("status", e.status) +
      kv("receiver", e.receiver) + kv("ackSent", e.ackSent ? "true" : "");
    if (e.snapshot) {
      text += "\n[snapshot]\n" +
        kv("state", e.snapshot.state) + kv("routerHost", e.snapshot.routerHost) +
        kv("clientId", e.snapshot.clientId) + kv("receivedMessageCount", e.snapshot.receivedMessageCount);
    }
    text += "\n[payload]\n" + (e.payloadText || e.payloadBase64 || e.payloadPreview || "(空)");
    const copied = await copyTextToClipboard(text);
    alert(copied ? "已复制整条记录到剪贴板" : "复制失败，请手动选择文本复制");
  };
  if (shareBtn) shareBtn.onclick = () => shareRequest({ longConnId: e.id });
}

// 页签按钮 + 过滤输入绑定（DOMContentLoaded 后由 app 初始化调用一次即可——
// 直接在脚本加载后绑定，index.html 在底部引入本文件，DOM 已就绪）。
$("tabLongConn").onclick = () => showLogPane("longconn");
$("tabTraffic").onclick = () => showLogPane("traffic");
$("longconnFilter").addEventListener("input", (ev) => {
  longConnFilter = ev.target.value;
  renderLongConn();
});

/** 清空本页长连接日志（只清前端，服务端 48h 保留供历史日志查询；同请求流语义）。 */
function clearLongConnLog() {
  if (!detail) return;
  detail._longConnClearedAt = new Date().toISOString();
  longConnLog = [];
  renderLongConn();
  $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
  detail._activeLongConn = null;
}
$("longconnClearBtn").onclick = clearLongConnLog;
$("longconnHistoryBtn").onclick = () => openHistory("longconn");
