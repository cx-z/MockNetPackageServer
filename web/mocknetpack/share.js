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

    // 长连接日志分享——渲染只读快照。
    if (snap.longConnEntry) {
      const expDate = new Date(snap.expiresAt);
      const e = snap.longConnEntry;
      const row = (k, v) => (v === undefined || v === null || v === "")
        ? ""
        : '<div class="d-kv"><span class="d-k">' + esc(k) + '</span><span class="d-v">' + esc(String(v)) + "</span></div>";
      let payloadBlock = "";
      if (e.payloadText) {
        payloadBlock = '<div class="d-block"><div class="d-title">payload（解析内容）</div><pre class="raw">' + esc(e.payloadText) + "</pre></div>";
      } else if (e.payloadBase64) {
        payloadBlock = '<div class="d-block"><div class="d-title">payload（base64 二进制）</div><pre class="raw">' + esc(e.payloadBase64) + "</pre></div>";
      } else if (e.payloadPreview) {
        payloadBlock = '<div class="d-block"><div class="d-title">payload（超限预览）</div><pre class="raw">' + esc(e.payloadPreview) + "</pre></div>";
      }
      box.innerHTML =
        '<div class="detail-panel">' +
          '<div class="d-actions">' +
            '<button id="copyShareBtn" class="small ghost">复制</button>' +
          "</div>" +
          '<div style="margin-bottom:8px;color:var(--muted);font-size:12px">MockNetPack 长连接日志分享 · 有效期至 ' + esc(expDate.toLocaleString()) + "</div>" +
          row("eventType", e.eventType) +
          row("channelId", e.channelId) +
          row("ts", e.ts) +
          row("msgId", e.msgId) +
          row("bizType", e.bizType) +
          row("receiver", e.receiver) +
          row("status", e.status) +
          payloadBlock +
        "</div>";
      bindShareCopy(box, buildLongConnShareText(e));
      return;
    }

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
        '<div class="d-actions">' +
          '<button id="copyShareBtn" class="small ghost">复制</button>' +
        "</div>" +
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
    // 与日志详情页「复制」一致：重建为完整 HTTP 报文交换后复制。
    bindShareCopy(box, buildRawTrafficText(e));
  } catch (err) {
    box.innerHTML = '<div class="empty">加载失败：' + esc(err.message) + "</div>";
  }
}

// : 创建分享链接（traffic entry 或 longconn record）
async function shareRequest(e) {
  try {
    const body = e.longConnId ? { longConnId: e.longConnId } : { trafficId: e.id };
    const res = await apiFetch("/shares", {
      method: "POST",
      body: JSON.stringify(body),
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

// 长连接日志分享的复制文本：逐字段 "k: v" 文本行 + payload 原文，
// 与页面渲染内容一致（空字段跳过）。
function buildLongConnShareText(e) {
  const lines = [];
  const push = (k, v) => {
    if (v === undefined || v === null || v === "") return;
    lines.push(k + ": " + String(v));
  };
  push("eventType", e.eventType);
  push("channelId", e.channelId);
  push("ts", e.ts);
  push("msgId", e.msgId);
  push("bizType", e.bizType);
  push("receiver", e.receiver);
  push("status", e.status);
  const payload = e.payloadText || e.payloadBase64 || e.payloadPreview;
  if (payload) lines.push("payload: " + payload);
  return lines.join("\n");
}

// 分享页复制按钮：与日志详情页「复制」一致——整条内容重建为纯文本复制，
// 成功后按钮短暂显示「已复制」，避免高频操作被 alert 打断。
function bindShareCopy(box, text) {
  const copyBtn = box.querySelector("#copyShareBtn");
  if (!copyBtn) return;
  copyBtn.onclick = async () => {
    const ok = await copyTextToClipboard(text);
    if (ok) {
      copyBtn.textContent = "已复制";
      copyBtn.disabled = true;
      setTimeout(() => { copyBtn.textContent = "复制"; copyBtn.disabled = false; }, 1500);
    } else {
      alert("复制失败：请手动选择复制");
    }
  };
}
