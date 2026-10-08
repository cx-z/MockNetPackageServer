"use strict";

// : Web API Key 管理（已登录用户）。
// 已登录用户在头部「API Key」入口创建/查看/吊销长效凭证（MCP/CLI/脚本免登录用）。
// 安全约定：明文仅创建时展示一次；列表只显示 keyPrefix（服务端只存 SHA-256 哈希）。
// 明文只保存在内存变量 lastKey，关闭弹窗或刷新即清除，绝不落 localStorage。
function initApiKeyUI() {
  const modal = $("apiKeyModal");
  const btn = $("apiKeyBtn");
  const fresh = $("apiKeyFresh");
  const freshValue = $("apiKeyFreshValue");
  const copyBtn = $("apiKeyCopyBtn");
  const listEl = $("apiKeyList");
  const errEl = $("apiKeyError");
  const createBtn = $("apiKeyCreateBtn");
  let lastKey = null; // 明文仅存在于内存

  function hideErr() { errEl.classList.add("hidden"); }
  function showErr(msg) { errEl.textContent = msg; errEl.classList.remove("hidden"); }

  btn.onclick = () => {
    modal.classList.remove("hidden");
    hideErr();
    loadList();
  };
  $("apiKeyClose").onclick = closeModal;
  function closeModal() {
    modal.classList.add("hidden");
    fresh.classList.add("hidden");
    freshValue.textContent = "";
    lastKey = null;
    copyBtn.textContent = "复制";
  }
  // 点击遮罩空白处关闭
  modal.addEventListener("click", (e) => { if (e.target === modal) closeModal(); });

  createBtn.onclick = async () => {
    hideErr();
    createBtn.disabled = true;
    try {
      const res = await apiFetch("/auth/keys", { method: "POST" });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) { showErr(data.message || "创建失败，请重试"); return; }
      lastKey = data.key || "";
      freshValue.textContent = lastKey;
      fresh.classList.remove("hidden");
      copyBtn.textContent = "复制";
      await loadList();
    } finally {
      createBtn.disabled = false;
    }
  };

  copyBtn.onclick = async () => {
    if (!lastKey) return;
    let copied = false;
    try { await navigator.clipboard.writeText(lastKey); copied = true; } catch (_) { /* http 非安全上下文 */ }
    if (!copied) {
      const ta = document.createElement("textarea");
      ta.value = lastKey;
      document.body.appendChild(ta);
      ta.select();
      copied = document.execCommand("copy");
      ta.remove();
    }
    copyBtn.textContent = copied ? "已复制" : "复制失败";
    setTimeout(() => { copyBtn.textContent = "复制"; }, 1500);
  };

  async function loadList() {
    hideErr();
    listEl.innerHTML = '<div class="empty">加载中…</div>';
    const res = await apiFetch("/auth/keys");
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      listEl.innerHTML = "";
      showErr(data.message || "加载失败，请重试");
      return;
    }
    const keys = data.keys || [];
    if (!keys.length) {
      listEl.innerHTML = '<div class="empty">还没有 API Key，点击「新建 API Key」创建</div>';
      return;
    }
    listEl.innerHTML = keys.map((k) =>
      `<div class="key-row">
        <code class="api-key-prefix">${esc(k.keyPrefix)}</code>
        <span class="sub">创建于 ${relTime(k.createdAt)}${k.expiresAt ? " · 过期 " + k.expiresAt : " · 长期有效"}</span>
        <button type="button" class="ghost small" data-del="${esc(k.id)}">吊销</button>
      </div>`
    ).join("");
    listEl.querySelectorAll("[data-del]").forEach((b) => {
      b.onclick = async () => {
        const id = b.getAttribute("data-del");
        if (!window.confirm("吊销后该 Key 立即失效且无法恢复，确认吊销？")) return;
        hideErr();
        const res = await apiFetch("/auth/keys/" + encodeURIComponent(id), { method: "DELETE" });
        if (!res.ok) {
          const d = await res.json().catch(() => ({}));
          showErr(d.message || "吊销失败，请重试");
          return;
        }
        // 吊销的若是当前展示的明文，一并清除
        lastKey = null;
        fresh.classList.add("hidden");
        freshValue.textContent = "";
        await loadList();
      };
    });
  }
}
