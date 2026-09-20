"use strict";
const API = "/api/v1";
const POLL_MS = 5000;        // 设备列表轮询
const TRAFFIC_POLL_MS = 2000; // 请求流轮询（决策 D-M2-1：轮询 2s，秒级可见）
const TRAFFIC_PAGE = 100;    // 每页条数（契约 limit 上限 500，取 100 最新）
const RENEW_MS = 60000;      // viewer 续租（TTL 120s 的一半）

// sessionId -> { viewerId, timer }
const viewers = new Map();
let pollTimer = null;        // 列表轮询
let trafficTimer = null;     // 请求流轮询
let detail = null;           // { app, did, sessionId }
let ruleStore = [];          // 当前规则列表（供详情/删除使用）
let trafficFilter = "";      // 展示规则：页面级字符串过滤（刷新即清空，F3.6/决策16）
let lastTraffic = [];       // 最近一次拉到的请求流（供过滤后重渲染）

const $ = (id) => document.getElementById(id);
const STATUS = {
  idle:      { label: "待命",   cls: "idle" },
  capturing: { label: "抓包中", cls: "capturing" },
  offline:   { label: "离线",   cls: "offline" },
  ended:     { label: "已结束", cls: "offline" },
};

function showError(msg) {
  const bar = $("errorBar");
  bar.textContent = msg;
  bar.style.display = "block";
  clearTimeout(showError._t);
  showError._t = setTimeout(() => { bar.style.display = "none"; }, 6000);
}

function relTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  const diff = (Date.now() - t) / 1000;
  if (diff < 5) return "刚刚";
  if (diff < 60) return Math.floor(diff) + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return new Date(iso).toLocaleString("zh-CN");
}
function clockTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso);
  return t.toLocaleTimeString("zh-CN", { hour12: false }) + "." +
    String(t.getMilliseconds()).padStart(3, "0");
}
function esc(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
function shortId(id) { return id ? id.slice(0, 8) + "…" : ""; }
function methodCls(m) {
  const u = (m || "").toUpperCase();
  if (u === "GET") return "get";
  if (u === "POST") return "post";
  if (u === "PUT" || u === "PATCH") return "put";
  if (u === "DELETE") return "delete";
  return "other";
}

// ============================================================================
// 设备列表（M1.6）
// ============================================================================

async function loadDevices() {
  try {
    const res = await fetch(API + "/devices");
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    render(data.devices || []);
    $("apiInfo").textContent = "API " + location.host + API;
  } catch (e) {
    $("apiInfo").textContent = "API 不可达";
    $("list").innerHTML = '<div class="empty">无法连接服务器：' + e.message + "</div>";
  }
}

function render(devices) {
  const order = { capturing: 0, idle: 1, offline: 2 };
  devices.sort((a, b) => (order[a.status] ?? 9) - (order[b.status] ?? 9)
      || new Date(b.lastSeenAt || 0) - new Date(a.lastSeenAt || 0));

  const online = devices.filter((d) => d.status !== "offline").length;
  $("stats").textContent = `共 ${devices.length} 台设备 · ${online} 台在线 · 点击设备查看请求流`;

  if (devices.length === 0) {
    $("list").innerHTML = '<div class="empty">暂无设备。启动接入 SDK 的 Debug App 后，设备会出现在这里。</div>';
    return;
  }

  $("list").innerHTML = "";
  for (const d of devices) {
    const st = STATUS[d.status] || STATUS.idle;
    const card = document.createElement("div");
    card.className = "card";
    card.dataset.app = d.app;
    card.dataset.did = d.did;
    card.title = "点击查看请求流";
    card.onclick = () => openDetail(d.app, d.did);

    const dot = document.createElement("span");
    dot.className = "dot " + st.cls;

    const meta = document.createElement("div");
    meta.className = "meta";
    meta.innerHTML =
      '<div class="app">' + esc(d.app) + "</div>" +
      '<div class="did">' + esc(d.did) + "</div>" +
      '<div class="row2">' +
        '<span class="badge ' + st.cls + '">' + st.label + "</span>" +
        '<span>最后活跃：' + relTime(d.lastSeenAt) + "</span>" +
        (d.currentSession ? '<span>会话：' + esc(shortId(d.currentSession.id)) + "</span>" : "") +
      "</div>";

    const actions = document.createElement("div");
    actions.className = "actions";
    const btn = document.createElement("button");
    if (d.currentSession && d.currentSession.status === "capturing") {
      btn.textContent = "断开";
      btn.className = "danger";
      btn.onclick = (e) => { e.stopPropagation(); disconnect(d); };
    } else {
      btn.textContent = "连接";
      btn.disabled = d.status === "offline";
      btn.title = d.status === "offline" ? "设备离线（心跳超时），无法连接" : "";
      btn.onclick = (e) => { e.stopPropagation(); connect(d); };
    }
    actions.appendChild(btn);

    card.appendChild(dot);
    card.appendChild(meta);
    card.appendChild(actions);
    $("list").appendChild(card);
  }
  $("updated").textContent = "更新于 " + new Date().toLocaleTimeString("zh-CN");
}

async function connect(d) {
  try {
    const res = await fetch(API + "/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app: d.app, did: d.did }),
    });
    const session = await res.json().catch(() => null);
    if (!res.ok) { showError((session && session.message) || "连接失败（HTTP " + res.status + "）"); return; }
    if (session && session.id) {
      await registerViewer(session, "设备列表页");
    }
    if (detail && detail.app === d.app && detail.did === d.did) {
      await loadDetail();   // 详情页打开时刷新
    } else {
      await loadDevices();
    }
  } catch (e) {
    showError("连接失败：" + e.message);
  }
}

async function disconnect(d) {
  const s = d.currentSession;
  if (!s) return;
  try {
    const res = await fetch(API + "/sessions/" + encodeURIComponent(s.id), { method: "DELETE" });
    if (!res.ok && res.status !== 404) { showError("断开失败（HTTP " + res.status + "）"); return; }
    stopViewer(s.id);
    if (detail && detail.sessionId === s.id) {
      stopTrafficPoll();
      detail.sessionId = null;
      await loadDetail();
    } else {
      await loadDevices();
    }
  } catch (e) {
    showError("断开失败：" + e.message);
  }
}

// ============================================================================
// 设备详情 + 请求流（M2.5）
// ============================================================================

async function openDetail(app, did) {
  location.hash = "#/device/" + encodeURIComponent(app) + "/" + encodeURIComponent(did);
}

async function enterDetail(app, did) {
  detail = { app, did, sessionId: null };
  $("listView").classList.add("hidden");
  $("detailView").classList.remove("hidden");
  $("dApp").textContent = app;
  $("dDid").textContent = did;
  await loadDetail();
}

/** 加载设备信息 + 会话列表；默认选中当前抓包会话（无则最近一个会话）。 */
async function loadDetail() {
  if (!detail) return;
  const { app, did } = detail;
  try {
    const [devRes, sesRes] = await Promise.all([
      fetch(API + "/devices/" + encodeURIComponent(app) + "/" + encodeURIComponent(did)),
      fetch(API + "/sessions?app=" + encodeURIComponent(app) + "&did=" + encodeURIComponent(did)),
    ]);
    const dev = devRes.ok ? await devRes.json() : null;
    const ses = sesRes.ok ? await sesRes.json() : { sessions: [], total: 0 };

    const st = STATUS[dev && dev.status] || STATUS.idle;
    const stBadge = $("dStatus");
    stBadge.textContent = st.label;
    stBadge.className = "badge " + st.cls;
    $("dLastSeen").textContent = "最后活跃：" + relTime(dev && dev.lastSeenAt);

    const cur = dev && dev.currentSession;
    const toggle = $("dToggleSession");
    if (cur && cur.status === "capturing") {
      toggle.textContent = "断开";
      toggle.className = "small danger";
      toggle.onclick = () => disconnect(dev);
    } else {
      toggle.textContent = "连接";
      toggle.className = "small";
      toggle.disabled = !dev || dev.status === "offline";
      toggle.onclick = () => connect(dev);
    }

    renderSessions(ses.sessions || [], cur);
    loadRules();

    // 默认选中：当前抓包会话优先；否则最近一个历史会话。
    const pick = cur && cur.status === "capturing"
      ? cur
      : (ses.sessions || []).find((s) => !cur || s.id !== cur.id) || (ses.sessions || [])[0];
    if (!detail.sessionId || !(ses.sessions || []).some((s) => s.id === detail.sessionId)) {
      selectSession(pick ? pick.id : null);
    }
  } catch (e) {
    showError("加载设备详情失败：" + e.message);
  }
}

function renderSessions(sessions, current) {
  const box = $("sessionList");
  if (!sessions.length) {
    box.innerHTML = '<div class="empty">暂无会话。点击「连接」开始抓包。</div>';
    return;
  }
  box.innerHTML = "";
  for (const s of sessions) {
    const st = STATUS[s.status] || STATUS.ended;
    const el = document.createElement("div");
    el.className = "session-card" + (detail.sessionId === s.id ? " active" : "");
    el.dataset.sid = s.id;
    el._session = s;
    el.innerHTML =
      '<span class="badge ' + st.cls + '">' + st.label + "</span>" +
      '<div class="s-body">' +
        '<div class="s-row1">' + esc(s.id) +
          (s.id === (current && current.id) ? '<span class="badge capturing">当前</span>' : "") +
        "</div>" +
        '<div class="s-row2">开始 ' + relTime(s.startedAt) +
          (s.endedAt ? " · 结束 " + relTime(s.endedAt) : "") +
          " · 请求数 " + s.requestCount +
          (s.viewerCount ? " · 查看者 " + s.viewerCount : "") +
        "</div>" +
      "</div>";
    el.onclick = () => selectSession(s.id);
    box.appendChild(el);
  }
}

/** 选中会话：capturing → 注册 viewer + 2s 轮询；ended → 停止轮询并提示清空。 */
function selectSession(sessionId) {
  if (!detail) return;
  stopTrafficPoll();
  const old = detail.sessionId;
  detail.sessionId = sessionId;
  if (old && old !== sessionId) stopViewer(old);

  const session = sessionFor(sessionId);
  $("trafficList").innerHTML = "";
  $("trafficDetail").innerHTML = '<div class="empty">点击上方请求查看完整详情。</div>';

  if (!sessionId) {
    $("trafficInfo").textContent = "";
    $("trafficList").innerHTML = '<div class="empty">请选择会话查看实时请求。</div>';
    return;
  }
  if (session && session.status === "capturing") {
    $("trafficInfo").textContent = "实时 · 2s 轮询 · 会话 " + shortId(sessionId);
    registerViewer(session, "设备详情页");
    trafficTimer = setInterval(pollTraffic, TRAFFIC_POLL_MS);
    pollTraffic();
  } else {
    $("trafficInfo").textContent = "会话已结束";
    $("trafficList").innerHTML = '<div class="empty">会话已结束，临时记录已清空（历史流量不保留）。</div>';
  }
  document.querySelectorAll(".session-card").forEach((el) => {
    el.classList.toggle("active", el.dataset.sid === sessionId);
  });
}

function sessionFor(sessionId) {
  const el = document.querySelector('.session-card[data-sid="' + sessionId + '"]');
  return el ? el._session : null;
}

function stopTrafficPoll() {
  if (trafficTimer) { clearInterval(trafficTimer); trafficTimer = null; }
}

/** 请求流轮询：先拿 total，再拉最新一页（契约升序 + offset 分页），按 id 去重合并。 */
async function pollTraffic() {
  if (!detail || !detail.sessionId) return;
  const sid = detail.sessionId;
  try {
    const meta = await fetch(API + "/sessions/" + encodeURIComponent(sid) + "/traffic?limit=1").then(r => r.json());
    if (!meta || typeof meta.total !== "number") return;
    const offset = Math.max(0, meta.total - TRAFFIC_PAGE);
    const data = await fetch(API + "/sessions/" + encodeURIComponent(sid) +
      "/traffic?limit=" + TRAFFIC_PAGE + "&offset=" + offset).then(r => r.json());
    renderTraffic(data.entries || []);
  } catch (e) {
    $("trafficInfo").textContent = "请求流拉取失败：" + e.message;
  }
}

function renderTraffic(entries) {
  if (!detail || !detail.sessionId) return;
  lastTraffic = entries;
  // 倒序：最新在上（服务端按 timestamp 升序）。
  entries = entries.slice().sort((a, b) => (b.timestamp || "").localeCompare(a.timestamp || ""));
  const q = trafficFilter.trim().toLowerCase();
  const total = entries.length;
  if (q) {
    entries = entries.filter((e) =>
      (e.path || e.url || "").toLowerCase().includes(q) ||
      (e.method || "").toLowerCase().includes(q));
  }
  $("trafficInfo").textContent = "实时 · 2s 轮询 · 共 " + total + " 条" + (q ? "（过滤出 " + entries.length + " 条）" : "（显示最近 " + TRAFFIC_PAGE + " 条）");

  const box = $("trafficList");
  if (!entries.length) {
    box.innerHTML = '<div class="empty">暂无请求。设备产生流量后会实时出现在这里。</div>';
    return;
  }
  box.innerHTML = "";
  for (const e of entries) {
    const el = document.createElement("div");
    el.className = "traffic-row" + (detail._activeTraffic === e.id ? " active" : "");
    const statusCls = e.statusCode == null ? "st-err"
      : e.statusCode < 300 ? "st-2xx" : e.statusCode < 400 ? "st-3xx"
      : e.statusCode < 500 ? "st-4xx" : "st-5xx";
    el.innerHTML =
      '<span class="method ' + methodCls(e.method) + '">' + esc(e.method || "?") + "</span>" +
      '<span class="t-url">' + esc(e.path || e.url) + (e.query ? "?" + esc(e.query) : "") + "</span>" +
      '<span class="t-status ' + statusCls + '">' + (e.statusCode ?? "ERR") + "</span>" +
      '<span class="t-dur">' + e.durationMs + "ms</span>" +
      '<span class="t-time">' + clockTime(e.timestamp) + "</span>";
    el.onclick = () => {
      detail._activeTraffic = e.id;
      renderTrafficDetail(e);
      box.querySelectorAll(".traffic-row").forEach((r) =>
        r.classList.toggle("active", r === el));
    };
    box.appendChild(el);
  }
}

/** 请求详情面板（列表数据已含完整字段，无需再查详情接口）。 */
function renderTrafficDetail(e) {
  const box = $("trafficDetail");
  const headRows = (h) => Object.entries(h || {})
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("");

  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-actions">' +
        '<button id="mockThisBtn" class="small">Mock 此请求</button>' +
      "</div>" +
      '<div class="d-kv"><span class="d-k">请求</span><span class="d-v">' + esc(e.method) + " " + esc(e.url) + "</span></div>" +
      '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
        (e.statusCode != null ? e.statusCode + (e.error ? "（" + esc(e.error) + "）" : "") : "请求失败： " + esc(e.error || "")) +
      "</span></div>" +
      '<div class="d-kv"><span class="d-k">耗时</span><span class="d-v">' + e.durationMs + " ms</span></div>" +
      '<div class="d-kv"><span class="d-k">时间</span><span class="d-v">' + esc(e.timestamp || "—") + "</span></div>" +
      (e.mocked ? '<div class="d-kv"><span class="d-k">Mock</span><span class="d-v">是（M3 起标记）</span></div>' : "") +
      '<div class="d-block"><div class="d-title">请求头</div>' +
        (headRows(e.requestHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">请求体</div><pre>' + (esc(e.requestBody) || "（空）") + "</pre></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' +
        (headRows(e.responseHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">响应体</div><pre>' + (esc(e.responseBodyDecoded || e.responseBody) || "（空）") + "</pre></div>" +
    "</div>";

  const btn = box.querySelector("#mockThisBtn");
  if (btn) {
    btn.disabled = e.statusCode == null;  // 失败请求无回包可固化
    btn.onclick = () => mockThisRequest(e);
  }
}

function backToList() {
  stopTrafficPoll();
  if (detail && detail.sessionId) stopViewer(detail.sessionId);
  detail = null;
  location.hash = "";
  $("detailView").classList.add("hidden");
  $("listView").classList.remove("hidden");
  loadDevices();
}

// hash 路由：#/device/{app}/{did}
window.addEventListener("hashchange", () => {
  const m = location.hash.match(/^#\/device\/([^/]+)\/(.+)$/);
  if (m) {
    enterDetail(decodeURIComponent(m[1]), decodeURIComponent(m[2]));
  } else if (detail) {
    backToList();
  }
});

// ============================================================================
// Mock 规则（M3.5）
// ============================================================================

async function loadRules() {
  if (!detail) return;
  const { app, did } = detail;
  try {
    const res = await fetch(API + "/devices/" + encodeURIComponent(app) + "/" + encodeURIComponent(did) + "/mock-rules");
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    renderRules(data);
  } catch (e) {
    $("rulesInfo").textContent = "规则拉取失败";
    $("rulesList").innerHTML = '<div class="empty">规则拉取失败：' + esc(e.message) + "</div>";
  }
}

function renderRules(data) {
  const rules = data.rules || [];
  $("rulesInfo").textContent = "· 共 " + rules.length + " 条 · 版本 " + (data.version ?? 0);

  // 异常态冲突报告（服务端固定文案）。
  const cb = $("rulesConflict");
  const conflicts = data.conflicts || [];
  if (conflicts.length) {
    cb.innerHTML = '<div class="c-title">⚠ 检测到接口冲突，以下接口暂不 Mock：</div>' +
      conflicts.map((c) => '<div class="c-item">' + esc(c.method) + " " + esc(c.path) +
        " — " + esc(c.message) + "</div>").join("");
    cb.classList.remove("hidden");
  } else {
    cb.classList.add("hidden");
  }

  const box = $("rulesList");
  if (!rules.length) {
    box.innerHTML = '<div class="empty">暂无规则。在下方请求流选中一条请求，点「Mock 此请求」一键创建。</div>';
    return;
  }
  box.innerHTML = "";
  ruleStore = rules;
  for (const r of rules) {
    const el = document.createElement("div");
    el.className = "rule-row" + (r.enabled ? "" : " disabled");
    const effBadge = r.enabled
      ? (r.effective ? '<span class="badge eff">生效中</span>' : '<span class="badge stopped">冲突未生效</span>')
      : '<span class="badge stopped">已停用</span>';
    el.innerHTML =
      '<span class="method ' + methodCls(r.method) + '">' + esc(r.method) + "</span>" +
      '<div class="r-body">' +
        '<div class="r-line1">' + esc(r.path) + " " + effBadge + "</div>" +
        '<div class="r-line2">回包 ' + (r.response && r.response.statusCode) +
          (r.response && r.response.body ? " · " + esc(String(r.response.body).slice(0, 80)) : "") +
          (r.response && r.response.bodyBase64 ? " · [二进制 " + atob(r.response.bodyBase64).length + " 字节]" : "") +
          (r.source ? " · 来自抓包" : "") + "</div>" +
      "</div>";

    // 点击规则体 → 展示详情。
    el.querySelector(".r-body").onclick = () => renderRuleDetail(r);

    const sw = document.createElement("label");
    sw.className = "switch";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = !!r.enabled;
    const slider = document.createElement("span");
    slider.className = "slider";
    sw.appendChild(input);
    sw.appendChild(slider);
    input.addEventListener("change", () => toggleRule(r, input.checked));
    el.appendChild(sw);

    const del = document.createElement("button");
    del.className = "small danger";
    del.textContent = "删除";
    del.onclick = (ev) => { ev.stopPropagation(); deleteRule(r); };
    el.appendChild(del);

    box.appendChild(el);
  }
}

/** 渲染单条规则详情（状态码/响应头/回包体/备注/来源快照）。 */
function renderRuleDetail(r) {
  const box = $("ruleDetail");
  const headRows = (h) => Object.entries(h || {})
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("");
  const resp = r.response || {};
  let bodyDisp = resp.body || "";
  if (resp.bodyBase64) {
    bodyDisp = "[二进制 " + atob(resp.bodyBase64).length + " 字节，base64 已用于回放]";
  }
  const src = r.source;
  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-actions"><button id="ruleEditBtn" class="small">编辑</button></div>' +
      '<div class="d-kv"><span class="d-k">接口</span><span class="d-v">' + esc(r.method) + " " + esc(r.path) + "</span></div>" +
      '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
        (r.enabled ? (r.effective ? "生效中" : "冲突未生效") : "已停用") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">备注</span><span class="d-v">' +
        (r.note ? esc(r.note) : '<span style="color:var(--muted)">（未填写）</span>') + "</span></div>" +
      '<div class="d-kv"><span class="d-k">回包状态码</span><span class="d-v">' + (resp.statusCode ?? "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (headRows(resp.headers) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">回包体</div><pre>' + esc(bodyDisp) + "</pre></div>" +
      (src
        ? '<div class="d-block"><div class="d-title">来源快照（原始真实请求）</div>' +
            '<div class="d-kv"><span class="d-k">方法/路径</span><span class="d-v">' + esc(src.method) + " " + esc(src.path) + "</span></div>" +
            '<div class="d-kv"><span class="d-k">原始状态码</span><span class="d-v">' + (src.statusCode ?? "—") + "</span></div>" +
            '<div class="d-block"><div class="d-title">原始请求体</div><pre>' +
              esc(src.requestBodyBase64 ? "[二进制 " + atob(src.requestBodyBase64).length + " 字节]" : (src.requestBody || "（空）")) + "</pre></div>" +
            '<div class="d-block"><div class="d-title">原始响应体</div><pre>' +
              esc(src.responseBodyDecoded || (src.responseBodyBase64 ? "[二进制 " + atob(src.responseBodyBase64).length + " 字节]" : (src.responseBody || "（空）"))) + "</pre></div>" +
          "</div>"
        : "") +
    "</div>";
  const editBtn = box.querySelector("#ruleEditBtn");
  if (editBtn) editBtn.onclick = () => openEditRuleForm(r);
}

/** 编辑规则表单（M5）：回包状态码/响应头/回包体/备注可改；method/path 只读不可改。 */
function openEditRuleForm(r) {
  const box = $("ruleDetail");
  const resp = r.response || {};
  const headersText = Object.entries(resp.headers || {})
    .map(([k, v]) => k + ": " + v).join("\n");
  // 回包体缺省值：文本规则直接用 response.body；二进制规则的 response.body 是
  // "[binary N bytes]" 占位符，改用抓包快照里解码器解出的可读文本（M4）作为缺省值。
  let bodyDefault = resp.body || "";
  if ((!bodyDefault || bodyDefault.startsWith("[binary")) && r.source && r.source.responseBodyDecoded) {
    bodyDefault = r.source.responseBodyDecoded;
  }
  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-title">编辑规则 · ' + esc(r.method) + " " + esc(r.path) +
      ' <span class="sub">（接口与匹配键不可改）</span></div>' +
      '<div class="edit-row"><label>回包状态码</label>' +
        '<input id="editStatusCode" type="number" class="filter-input" value="' + esc(resp.statusCode ?? 200) + '" /></div>' +
      '<div class="edit-row"><label>响应头（每行一个「Key: Value」）</label>' +
        '<textarea id="editHeaders" class="filter-input" rows="4">' + esc(headersText) + '</textarea></div>' +
      '<div class="edit-row"><label>回包体（UTF-8 文本）' +
        (resp.bodyBase64 ? ' <span class="sub">（原回包为二进制；已载入抓包解码文本作为缺省值，保存后将以文本回包为准）</span>' : '') +
        '</label>' +
        '<textarea id="editBody" class="filter-input" rows="8">' + esc(bodyDefault) + '</textarea></div>' +
      '<div class="edit-row"><label>备注（必填）</label>' +
        '<input id="editNote" type="text" class="filter-input" placeholder="说明这条规则的用途/场景" value="' + esc(r.note || "") + '" /></div>' +
      '<div class="edit-actions">' +
        '<button id="editCancelBtn" class="ghost small">取消</button>' +
        '<button id="editSaveBtn" class="small">保存</button>' +
      '</div>' +
    "</div>";
  $("editCancelBtn").onclick = () => renderRuleDetail(r);
  $("editSaveBtn").onclick = () => saveRuleEdit(r);
  $("editNote").focus();
}

/** 收集编辑表单 → PUT /mock-rules/{id} → 刷新。前端先做 note 非空拦截。 */
async function saveRuleEdit(rule) {
  if (!detail) return;
  const statusCode = parseInt($("editStatusCode").value, 10);
  if (!Number.isFinite(statusCode) || statusCode <= 0) {
    showError("回包状态码必须是正整数"); return;
  }
  const note = $("editNote").value.trim();
  if (!note) { showError("备注必填，请填写后再保存"); return; }

  // 解析响应头文本：每行 "Key: Value"。
  const headers = {};
  for (const line of $("editHeaders").value.split("\n")) {
    const idx = line.indexOf(":");
    if (idx <= 0) continue;
    const k = line.slice(0, idx).trim();
    const v = line.slice(idx + 1).trim();
    if (k) headers[k] = v;
  }

  try {
    const res = await fetch(API + "/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // M5: 不传 method/path/source；不传 enabled（保持当前开关）。
      body: JSON.stringify({
        response: { statusCode: statusCode, headers: headers, body: $("editBody").value },
        note: note,
      }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => null);
      showError((err && err.message) || "保存失败（HTTP " + res.status + "）");
      return;
    }
    showError("已保存");
    await loadRules();
    // 找到刷新后的同 id 规则，重新渲染详情。
    const fresh = (ruleStore || []).find((x) => x.id === rule.id);
    if (fresh) renderRuleDetail(fresh);
  } catch (e) {
    showError("保存失败：" + e.message);
  }
}

async function deleteRule(rule) {
  if (!detail) return;
  if (!confirm("删除规则 " + rule.method + " " + rule.path + " ？")) return;
  try {
    const res = await fetch(API + "/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 204) { showError("删除失败（HTTP " + res.status + "）"); return; }
    $("ruleDetail").innerHTML = '<div class="empty">已删除。</div>';
    await loadRules();
  } catch (e) {
    showError("删除失败：" + e.message);
  }
}

async function toggleRule(rule, enabled) {
  if (!detail) return;
  try {
    const res = await fetch(API + "/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // M5: edit body carries only response + note + enabled; method/path/source
      // are immutable. note is required server-side — toggle sends the current
      // note (a blank legacy rule is rejected and the error prompt steers to edit).
      body: JSON.stringify({
        response: rule.response,
        note: rule.note || "",
        enabled: enabled,
      }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => null);
      // 409：同接口已有生效规则，弹窗提示固定文案。400 + note 相关：老规则没备注。
      showError((err && err.message) || "切换失败（HTTP " + res.status + "）");
      await loadRules();   // 回滚开关显示
      return;
    }
    await loadRules();
  } catch (e) {
    showError("切换失败：" + e.message);
    await loadRules();
  }
}

/** 请求流详情里的「Mock 此请求」：把这条真实请求/回包固化为一条规则（默认停用）。 */
async function mockThisRequest(e) {
  if (!detail) return;
  const flatHeaders = (h) => {
    const out = {};
    for (const [k, arr] of Object.entries(h || {})) out[k] = Array.isArray(arr) ? arr.join(", ") : String(arr);
    return out;
  };
  const input = {
    method: e.method,
    path: e.path,
    response: {
      statusCode: e.statusCode || 200,
      headers: flatHeaders(e.responseHeaders),
      body: e.responseBody || "",
      ...(e.responseBodyBase64 ? { bodyBase64: e.responseBodyBase64 } : {}),
    },
    enabled: false,
    source: {
      method: e.method,
      path: e.path,
      url: e.url,
      query: e.query,
      requestHeaders: e.requestHeaders,
      requestBody: e.requestBody,
      ...(e.requestBodyBase64 ? { requestBodyBase64: e.requestBodyBase64 } : {}),
      statusCode: e.statusCode,
      responseHeaders: e.responseHeaders,
      responseBody: e.responseBody,
      ...(e.responseBodyDecoded ? { responseBodyDecoded: e.responseBodyDecoded } : {}),
      ...(e.responseBodyBase64 ? { responseBodyBase64: e.responseBodyBase64 } : {}),
      capturedAt: e.timestamp,
    },
  };
  try {
    const res = await fetch(API + "/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => null);
      showError((err && err.message) || "创建规则失败（HTTP " + res.status + "）");
      return;
    }
    showError("已创建规则（默认停用，到上方打开开关即可 Mock）");
    await loadRules();
  } catch (err) {
    showError("创建规则失败：" + err.message);
  }
}

// ============================================================================
// viewer 租约（M1.6）
// ============================================================================

async function registerViewer(session, label) {
  const viewerId = crypto.randomUUID();
  try {
    const res = await fetch(API + "/sessions/" + encodeURIComponent(session.id) + "/viewers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ viewerId, label }),
    });
    if (!res.ok) return;
  } catch { /* 租约失败不影响展示；服务端 TTL 兜底 */ }
  stopViewer(session.id);
  const timer = setInterval(() => renewViewer(session.id, viewerId, label), RENEW_MS);
  viewers.set(session.id, { viewerId, timer });
}

async function renewViewer(sessionId, viewerId, label) {
  try {
    await fetch(API + "/sessions/" + encodeURIComponent(sessionId) + "/viewers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ viewerId, label }),
    });
  } catch { /* 网络抖动；TTL 120s 兜底 */ }
}

function stopViewer(sessionId) {
  const v = viewers.get(sessionId);
  if (v) { clearInterval(v.timer); viewers.delete(sessionId); }
}

// 页面关闭/隐藏：尽力释放全部 viewer 租约（keepalive），服务端 TTL 兜底。
function releaseAllViewers() {
  for (const [sessionId, v] of viewers) {
    fetch(API + "/sessions/" + encodeURIComponent(sessionId) + "/viewers/" + encodeURIComponent(v.viewerId),
      { method: "DELETE", keepalive: true });
  }
}
window.addEventListener("pagehide", releaseAllViewers);

// ============================================================================
// 启动
// ============================================================================

function start() {
  $("backBtn").onclick = backToList;
  const m = location.hash.match(/^#\/device\/([^/]+)\/(.+)$/);
  if (m) {
    enterDetail(decodeURIComponent(m[1]), decodeURIComponent(m[2]));
  } else {
    loadDevices();
    pollTimer = setInterval(loadDevices, POLL_MS);
  }
}
start();

// M4.6 展示规则：页面级字符串过滤，仅当前页面、刷新即清空（F3.6/决策16）。
(function () {
  const box = $("trafficFilter");
  if (box) box.addEventListener("input", (ev) => {
    trafficFilter = ev.target.value || "";
    renderTraffic(lastTraffic);
  });
})();
