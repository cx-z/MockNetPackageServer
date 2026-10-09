"use strict";

// MockNetPack web: share-link creation and public read-only share view.
// Pure move from app.js.
// : 渲染分享只读视图（免登录）
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
      '<div class="d-block"><div class="d-title">请求体</div>' + bodyHtml(e.requestBodyDecoded || e.requestBody, { contentType: headerValue(e.requestHeaders, "Content-Type"), base64: e.requestBodyBase64 }) + "</div>";
    const respTab =
        '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
          (e.statusCode != null ? e.statusCode + (e.error ? "（" + esc(e.error) + "）" : "") : "请求失败： " + esc(e.error || "")) +
        "</span></div>" +
        '<div class="d-kv"><span class="d-k">耗时</span><span class="d-v">' + (e.durationMs || 0) + " ms</span></div>" +
        '<div class="d-kv"><span class="d-k">时间</span><span class="d-v">' + esc(e.timestamp || "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (headRows(e.responseHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">响应体</div>' + bodyHtml(e.responseBodyDecoded || e.responseBody, { contentType: headerValue(e.responseHeaders, "Content-Type"), base64: e.responseBodyBase64 }) + "</div>";

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

// : 创建分享链接
async function shareRequest(e) {
  try {
    const res = await apiFetch("/shares", {
      method: "POST",
      body: JSON.stringify({ trafficId: e.id }),
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    const shareUrl = location.origin + location.pathname + "#/share/" + data.shareId;
    // 复制到剪贴板（非 HTTPS 下 fallback，见 utils.copyTextToClipboard）
    const copied = await copyTextToClipboard(shareUrl);
    alert((copied ? "分享链接已复制到剪贴板：\n" : "分享链接（请手动复制）：\n") + shareUrl + "\n\n（7 天有效，任何人打开即可查看只读快照）");
  } catch (err) {
    alert("分享失败：" + err.message);
  }
}
