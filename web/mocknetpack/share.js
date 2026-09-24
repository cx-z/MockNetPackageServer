"use strict";

// MockNetPack web: share-link creation and public read-only share view.
// Pure move from app.js.
// M8.5: 渲染分享只读视图（免登录）
async function renderShareView(shareId) {
  $("authView").classList.add("hidden");
  $("appWrap").classList.add("hidden");
  $("shareView").classList.remove("hidden");
  const box = $("shareContent");

  try {
    const res = await fetch(API + "/shares/" + encodeURIComponent(shareId));
    if (res.status === 404) {
      box.innerHTML = '<div class="empty">分享链接已过期或不存在（有效期 7 天）</div>';
      return;
    }
    if (!res.ok) throw new Error("HTTP " + res.status);
    const snap = await res.json();
    const e = snap.entry;
    const headRows = (h) => Object.entries(h || {})
      .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
        '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("");

    const reqTab =
        '<div class="d-block"><div class="d-title">请求头</div>' + (headRows(e.requestHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">请求体</div>' + bodyHtml(e.requestBodyDecoded || e.requestBody) + "</div>";
    const respTab =
        '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
          (e.statusCode != null ? e.statusCode + (e.error ? "（" + esc(e.error) + "）" : "") : "请求失败： " + esc(e.error || "")) +
        "</span></div>" +
        '<div class="d-kv"><span class="d-k">耗时</span><span class="d-v">' + (e.durationMs || 0) + " ms</span></div>" +
        '<div class="d-kv"><span class="d-k">时间</span><span class="d-v">' + esc(e.timestamp || "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (headRows(e.responseHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">响应体</div>' + bodyHtml(e.responseBodyDecoded || e.responseBody) + "</div>";

    const expDate = new Date(snap.expiresAt);
    box.innerHTML =
      '<div class="detail-panel">' +
        '<div style="margin-bottom:8px;color:var(--muted);font-size:12px">MockNetPack 请求分享 · 有效期至 ' + esc(expDate.toLocaleString()) + "</div>" +
        '<div class="d-kv"><span class="d-k">请求</span><span class="d-v">' + esc(e.method) + " " + esc(e.url) + "</span></div>" +
        '<div class="tabs">' +
          '<button class="tab active" data-tab="req">请求</button>' +
          '<button class="tab" data-tab="resp">响应</button>' +
        "</div>" +
        '<div class="tab-pane" data-pane="req">' + reqTab + "</div>" +
        '<div class="tab-pane hidden" data-pane="resp">' + respTab + "</div>" +
      "</div>";

    box.querySelectorAll(".tab").forEach((btn) => {
      btn.onclick = () => {
        box.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
        btn.classList.add("active");
        box.querySelectorAll(".tab-pane").forEach((p) => {
          p.classList.toggle("hidden", p.dataset.pane !== btn.dataset.tab);
        });
      };
    });
    bindJsonTree(box);
  } catch (err) {
    box.innerHTML = '<div class="empty">加载失败：' + esc(err.message) + "</div>";
  }
}

// M8.5: 创建分享链接
async function shareRequest(e) {
  try {
    const res = await apiFetch("/shares", {
      method: "POST",
      body: JSON.stringify({ trafficId: e.id }),
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    const shareUrl = location.origin + location.pathname + "#/share/" + data.shareId;
    // 复制到剪贴板（clipboard API 在非 HTTPS 下不可用，用 execCommand fallback）
    let copied = false;
    try { await navigator.clipboard.writeText(shareUrl); copied = true; } catch (_) {}
    if (!copied) {
      try {
        const ta = document.createElement("textarea");
        ta.value = shareUrl;
        ta.style.position = "fixed"; ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        copied = document.execCommand("copy");
        document.body.removeChild(ta);
      } catch (_) {}
    }
    alert((copied ? "分享链接已复制到剪贴板：\n" : "分享链接（请手动复制）：\n") + shareUrl + "\n\n（7 天有效，任何人打开即可查看只读快照）");
  } catch (err) {
    alert("分享失败：" + err.message);
  }
}
