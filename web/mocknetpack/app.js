"use strict";
const API = "/api/v1";
const POLL_MS = 5000;        // 设备列表轮询
const RENEW_MS = 60000;      // viewer 续租（TTL 120s 的一半）

// sessionId -> { viewerId, timer }
const viewers = new Map();
let pollTimer = null;

const $ = (id) => document.getElementById(id);
const STATUS = {
  idle:      { label: "待命",   cls: "idle" },
  capturing: { label: "抓包中", cls: "capturing" },
  offline:   { label: "离线",   cls: "offline" },
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
  $("stats").textContent = `共 ${devices.length} 台设备 · ${online} 台在线`;

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
      btn.onclick = () => disconnect(d);
    } else {
      btn.textContent = "连接";
      btn.disabled = d.status === "offline";
      btn.title = d.status === "offline" ? "设备离线（心跳超时），无法连接" : "";
      btn.onclick = () => connect(d);
    }
    actions.appendChild(btn);

    card.appendChild(dot);
    card.appendChild(meta);
    card.appendChild(actions);
    $("list").appendChild(card);
  }
  $("updated").textContent = "更新于 " + new Date().toLocaleTimeString("zh-CN");
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
function shortId(id) { return id ? id.slice(0, 8) + "…" : ""; }

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
      await registerViewer(session);
    }
    await loadDevices();
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
    await loadDevices();
  } catch (e) {
    showError("断开失败：" + e.message);
  }
}

async function registerViewer(session) {
  const viewerId = crypto.randomUUID();
  try {
    const res = await fetch(API + "/sessions/" + encodeURIComponent(session.id) + "/viewers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ viewerId, label: "设备列表页" }),
    });
    if (!res.ok) return;
  } catch { /* 租约失败不影响列表；服务端 TTL 兜底 */ }
  stopViewer(session.id);
  const timer = setInterval(() => renewViewer(session.id, viewerId), RENEW_MS);
  viewers.set(session.id, { viewerId, timer });
}

async function renewViewer(sessionId, viewerId) {
  try {
    await fetch(API + "/sessions/" + encodeURIComponent(sessionId) + "/viewers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ viewerId, label: "设备列表页" }),
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

function start() {
  loadDevices();
  pollTimer = setInterval(loadDevices, POLL_MS);
}
start();
