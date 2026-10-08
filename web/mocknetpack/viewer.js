"use strict";

// MockNetPack web: viewer lease registration/renew/release (session
// keep-alive across tabs). Pure move from app.js.
// ============================================================================
// viewer 租约
// ============================================================================

async function registerViewer(session, label) {
  const viewerId = newUuid();
  try {
    const res = await apiFetch("/sessions/" + encodeURIComponent(session.id) + "/viewers", {
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
    await apiFetch("/sessions/" + encodeURIComponent(sessionId) + "/viewers", {
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
    apiFetch("/sessions/" + encodeURIComponent(sessionId) + "/viewers/" + encodeURIComponent(v.viewerId),
      { method: "DELETE", keepalive: true });
  }
}
window.addEventListener("pagehide", releaseAllViewers);
