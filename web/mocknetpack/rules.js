"use strict";

// MockNetPack web: mock rule list, detail/editor (CodeMirror JSON), save/
// delete/toggle, and "Mock this request". Pure move from app.js.
// ============================================================================
// Mock 规则
// ============================================================================

async function loadRules() {
  if (!detail) return;
  const { app, did } = detail;
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(app) + "/" + encodeURIComponent(did) + "/mock-rules");
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    renderRules(data);
  } catch (e) {
    $("rulesInfo").textContent = "规则拉取失败";
    $("rulesList").innerHTML = '<div class="empty">规则拉取失败：' + esc(e.message) + "</div>";
  }
}

//  前端可管理性判定（UX 隐藏，非安全边界——服务端仍强制 403）。
// authUser 为 null（未登录；--no-auth 且未带 token）下服务端不校验权限，
// 全量渲染；已登录（含 --no-auth 但带有效 token）且非 admin：仅规则
// owner 可管理。owner 空的存量规则仅 admin 可管理。
function canManageRule(r) {
  if (!authUser) return true;
  if (authUser.role === "admin") return true;
  return !!(r.owner && r.owner === authUser.username);
}

// ruleOwnersText 渲染"创建人 / 最后编辑人"：空 owner 显示 "—"。
function ruleOwnersText(r) {
  const owner = r.owner || "—";
  const updater = r.updatedBy || "—";
  if (r.owner) {
    return "创建人 " + esc(r.owner) + " · 最后编辑 " + esc(r.updatedBy || "—");
  }
  return "创建人 — · 最后编辑 " + esc(updater);
}

//  Step4：命中可见性徽章文案。创建时 LastUsedAt 与 CreatedAt 同刻
// （7 天滑动清理基线需要），若二者时刻差 < 5s 说明这条规则创建后从未被命中/
// 编辑/启停触动 → 灰字「从未命中」，提示开发者检查路径是否写错；否则显示
// 最近命中的相对时间。
function hitBadgeText(r) {
  const lu = r.lastUsedAt;
  if (!lu) return "从未命中 · 检查路径是否写对";
  if (r.createdAt) {
    const gap = Math.abs(new Date(lu).getTime() - new Date(r.createdAt).getTime());
    if (Number.isFinite(gap) && gap < 5000) return "从未命中 · 检查路径是否写对";
  }
  return "最近命中：" + relTime(lu);
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
    box.innerHTML = '<div class="empty">暂无规则。点上方「新建 Mock 规则」手填接口，或在下方请求流选中一条请求点「Mock 此请求」一键创建。</div>';
    return;
  }
  box.innerHTML = "";
  ruleStore = rules;
  for (const r of rules) {
    const el = document.createElement("div");
    el.className = "rule-row" + (r.enabled ? "" : " disabled") +
      (detail._activeRule === r.id ? " active" : "");
    el.dataset.rid = r.id;
    const effBadge = r.enabled
      ? (r.effective ? '<span class="badge eff">生效中</span>' : '<span class="badge stopped">冲突未生效</span>')
      : '<span class="badge stopped">已停用</span>';
    // 回包体摘要：文本规则显示正文前 80 字符；二进制规则（bodyBase64）优先显示
    // 抓包解码文本片段（ 解码，仅展示），无解码时才显示 "[二进制 N 字节]"。
    const respL = r.response || {};
    let bodySnippet = "";
    if (respL.bodyBase64) {
      const decText = (r.source && r.source.responseBodyDecoded) || "";
      bodySnippet = decText
        ? " · " + esc(String(decText).slice(0, 80))
        : " · [二进制 " + atob(respL.bodyBase64).length + " 字节]";
    } else if (respL.body) {
      bodySnippet = " · " + esc(String(respL.body).slice(0, 80));
    }
    el.innerHTML =
      '<span class="method ' + methodCls(r.method) + '">' + esc(r.method) + "</span>" +
      '<div class="r-body">' +
        '<div class="r-line1">' + esc(r.path) + " " + effBadge + "</div>" +
        '<div class="r-line2">回包 ' + (r.response && r.response.statusCode) + bodySnippet +
          (r.source ? " · 来自抓包" : "") +
          ' <span class="sub"> · ' + esc(hitBadgeText(r)) + "</span></div>" +
        '<div class="r-owner">' + ruleOwnersText(r) + "</div>" +
      "</div>";

    // 点击规则体 → 右列展示详情/编辑（ 两列布局）。
    el.querySelector(".r-body").onclick = () => {
      detail._activeRule = r.id;
      document.querySelectorAll(".rule-row").forEach((el2) =>
        el2.classList.toggle("active", el2.dataset.rid === r.id));
      renderRuleDetail(r);
    };

    // ：权限不符（非 owner 非 admin）时不渲染开关/删除入口（服务端仍强制 403）。
    const manageable = canManageRule(r);
    if (manageable) {
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
    }

    box.appendChild(el);
  }
}

/** 渲染单条规则详情（状态码/响应头/回包体/备注/来源快照）。 */
function renderRuleDetail(r) {
  ruleBodyEditor = null;   //  离开编辑表单即释放 CM 引用（DOM 由 innerHTML 整体替换）
  showDetailPane("rule");
  detail._activeTraffic = null;
  document.querySelectorAll(".traffic-row").forEach((el) => el.classList.remove("active"));
  const box = $("ruleDetail");
  const headRows = (h) => Object.entries(h || {})
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("");
  const resp = r.response || {};
  const src = r.source;
  // 二进制规则（bodyBase64 存在）：SDK 回放仍按 bodyBase64 原始字节。详情里
  // 优先展示来源快照的抓包解码文本（ 解码，仅展示），让"Mock 此请求"创建
  // 的规则无需进入编辑即可看到 JSON 详情；无解码文本时才回退二进制占位。
  let bodyDisp = resp.body || "";
  let bodyReplayNote = "";
  if (resp.bodyBase64) {
    if (src && src.responseBodyDecoded) {
      bodyDisp = src.responseBodyDecoded;
      bodyReplayNote = '<div class="sub" style="margin-top:6px">（原始回包为二进制，以上为抓包解码文本，仅用于展示；Mock 回放仍为原始字节）</div>';
    } else {
      bodyDisp = "[二进制 " + atob(resp.bodyBase64).length + " 字节，base64 已用于回放]";
    }
  }

  // 请求页签：来源快照的方法/路径 + 原始请求头 + 原始请求体
  const reqHeadersRows = (src && src.requestHeaders) ? Object.entries(src.requestHeaders)
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("") : "";
  const reqTab =
      '<div class="d-kv"><span class="d-k">接口</span><span class="d-v">' + esc(r.method) + " " + esc(r.path) + "</span></div>" +
      (src
        ? '<div class="d-block"><div class="d-title">原始请求头</div>' + (reqHeadersRows || '<div class="d-v">—</div>') + "</div>" +
          '<div class="d-block"><div class="d-title">原始请求体</div>' +
            bodyHtml(src.requestBodyDecoded || (src.requestBodyBase64 ? "[二进制 " + atob(src.requestBodyBase64).length + " 字节]" : (src.requestBody || "")), { contentType: headerValue(src.requestHeaders, "Content-Type"), base64: src.requestBodyBase64 }) + "</div>"
        : '<div class="d-v" style="color:var(--muted)">（无来源快照）</div>');

  // 响应页签：状态/备注/回包状态码 + 响应头 + 回包体（不展示原始响应体）
  const respTab =
      '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
        (r.enabled ? (r.effective ? "生效中" : "冲突未生效") : "已停用") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">备注</span><span class="d-v">' +
        (r.note ? esc(r.note) : '<span style="color:var(--muted)">（未填写）</span>') + "</span></div>" +
      '<div class="d-kv"><span class="d-k">回包状态码</span><span class="d-v">' + (resp.statusCode ?? "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (headRows(resp.headers) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">回包体</div>' + bodyHtml(bodyDisp, { contentType: headerValue(resp.headers, "Content-Type"), base64: resp.bodyBase64 }) + bodyReplayNote + "</div>";

  box.innerHTML =
    '<div class="detail-panel">' +
      // ：权限不符时不渲染编辑入口（服务端仍强制 403）。
      (canManageRule(r)
        ? '<div class="d-actions"><button id="ruleEditBtn" class="small">编辑</button></div>'
        : '<div class="d-actions"><span class="sub">只读（仅创建人/admin 可编辑）</span></div>') +
      '<div class="tabs">' +
        '<button class="tab active" data-tab="req">请求</button>' +
        '<button class="tab" data-tab="resp">响应</button>' +
      "</div>" +
      '<div class="tab-pane" data-pane="req">' + reqTab + "</div>" +
      '<div class="tab-pane hidden" data-pane="resp">' + respTab + "</div>" +
    "</div>";

  // 页签切换
  box.querySelectorAll(".tab").forEach((btn) => {
    btn.onclick = () => {
      box.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      box.querySelectorAll(".tab-pane").forEach((p) => {
        p.classList.toggle("hidden", p.dataset.pane !== btn.dataset.tab);
      });
    };
  });

  const editBtn = box.querySelector("#ruleEditBtn");
  if (editBtn) editBtn.onclick = () => openEditRuleForm(r);
  bindJsonTree(box);
}

/** parseHeadersText 把「每行 Key: Value」的多行文本解析为对象；空行/无冒号行跳过。 */
function parseHeadersText(text) {
  const out = {};
  for (const raw of String(text || "").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const idx = line.indexOf(":");
    if (idx <= 0) continue;
    const k = line.slice(0, idx).trim();
    if (k) out[k] = line.slice(idx + 1).trim();
  }
  return out;
}

/** headersToText 把响应头对象拍平为「每行 Key: Value」多行文本（编辑表单预填用）。 */
function headersToText(h) {
  return Object.entries(h || {})
    .map(([k, v]) => k + ": " + (Array.isArray(v) ? v.join(", ") : v))
    .join("\n");
}

/** 编辑规则表单：回包体/备注可改；method/path 与回包状态码/响应头只读不可改。 */
function openEditRuleForm(r) {
  const box = $("ruleDetail");
  const resp = r.response || {};
  let bodyDefault = resp.body || "";
  if ((!bodyDefault || bodyDefault.startsWith("[binary")) && r.source && r.source.responseBodyDecoded) {
    bodyDefault = r.source.responseBodyDecoded;
  }
  const src = r.source;
  const hasOriginal = !!(src && src.responseBodyDecoded);

  // 请求页签：只读展示原始请求头 + 请求体
  const editReqHeadersRows = (src && src.requestHeaders) ? Object.entries(src.requestHeaders)
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("") : "";
  const reqTab =
      '<div class="d-kv"><span class="d-k">接口</span><span class="d-v">' + esc(r.method) + " " + esc(r.path) + "</span></div>" +
      (src
        ? '<div class="d-block"><div class="d-title">原始请求头（只读）</div>' + (editReqHeadersRows || '<div class="d-v">—</div>') + "</div>" +
          '<div class="d-block"><div class="d-title">原始请求体（只读）</div>' +
            bodyHtml(src.requestBodyDecoded || (src.requestBodyBase64 ? "[二进制 " + atob(src.requestBodyBase64).length + " 字节]" : (src.requestBody || "")), { contentType: headerValue(src.requestHeaders, "Content-Type"), base64: src.requestBodyBase64 }) + "</div>"
        : '<div class="d-v" style="color:var(--muted)">（无来源快照）</div>');

  // 响应页签：备注/回退按钮在上，回包体 JSON 折叠编辑器在下并撑满剩余高度。
  //  Step3：手填规则（无 source）的回包状态码/响应头可编辑（输入框/多行文本）；
  // 日志规则（有 source）维持  只读（与抓包快照绑定，只读 KV 展示）。
  const handAuthored = !src;
  const respHeadersRows = (resp.headers) ? Object.entries(resp.headers)
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("") : "";
  const metaFields = handAuthored
    ? '<div class="edit-row"><label>回包状态码（100–599）</label>' +
        '<input id="editStatusCode" type="number" min="100" max="599" class="filter-input" value="' + esc(String(resp.statusCode ?? 200)) + '" /></div>' +
      '<div class="edit-row"><label>响应头（每行 Key: Value，可留空）</label>' +
        '<textarea id="editHeaders" class="filter-input" rows="3">' + esc(headersToText(resp.headers)) + '</textarea></div>'
    : '<div class="d-kv"><span class="d-k">回包状态码</span><span class="d-v">' + (resp.statusCode ?? "—") + '</span></div>' +
      '<div class="d-block"><div class="d-title">响应头（只读）</div>' + (respHeadersRows || '<div class="d-v">—</div>') + '</div>';
  const respTab =
      '<div class="edit-pane">' +
      metaFields +
      '<div class="edit-row"><label>备注（必填）</label>' +
        '<input id="editNote" type="text" class="filter-input" placeholder="说明这条规则的用途/场景" value="' + esc(r.note || "") + '" /></div>' +
      (hasOriginal
        ? '<div class="edit-row" style="margin-top:12px">' +
            '<button id="revertOriginalBtn" class="ghost small" type="button">一键回退为原始响应体</button>' +
            '<span class="sub" style="margin-left:8px">（回退后所有修改丢弃，直接生效）</span>' +
          '</div>'
        : '') +
      '<div class="edit-row edit-body-row"><label>回包体（UTF-8 文本）' +
        (resp.bodyBase64 ? ' <span class="sub">（原回包为二进制；已载入抓包解码文本作为缺省值，保存后将以文本回包为准）</span>' : '') +
        ' <span class="sub">（点击行号左侧箭头按花括号折叠/展开）</span>' +
        '</label>' +
        '<div class="edit-body-wrap"><textarea id="editBody" class="filter-input">' + esc(formatBody(bodyDefault)) + '</textarea></div>' +
      '</div>' +
      '</div>';

  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-title">编辑规则 · ' + esc(r.method) + " " + esc(r.path) +
      ' <span class="sub">（接口与匹配键不可改）</span></div>' +
      '<div class="tabs">' +
        '<button class="tab" data-tab="req">请求</button>' +
        '<button class="tab active" data-tab="resp">响应</button>' +
      '</div>' +
      '<div class="tab-pane hidden" data-pane="req">' + reqTab + '</div>' +
      '<div class="tab-pane" data-pane="resp">' + respTab + '</div>' +
      '<div class="edit-actions">' +
        '<button id="editCancelBtn" class="ghost small">取消</button>' +
        '<button id="editSaveBtn" class="small">保存</button>' +
      '</div>' +
    "</div>";

  // 页签切换
  box.querySelectorAll(".tab").forEach((btn) => {
    btn.onclick = () => {
      box.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      box.querySelectorAll(".tab-pane").forEach((p) => {
        p.classList.toggle("hidden", p.dataset.pane !== btn.dataset.tab);
      });
      // ：从隐藏页签切回响应页时 CM 需重算尺寸，否则空白/错位
      if (btn.dataset.tab === "resp" && ruleBodyEditor) {
        setTimeout(() => ruleBodyEditor.refresh(), 0);
      }
    };
  });

  // ：回包体 JSON 折叠编辑器（vendored CodeMirror，同源加载见 lib/codemirror/README.md）。
  // fromTextArea 会把 textarea 隐藏并替换为编辑器；保存前 cm.save() 回写 textarea 统一取值。
  ruleBodyEditor = CodeMirror.fromTextArea($("editBody"), {
    mode: { name: "javascript", json: true },
    lineNumbers: true,
    lineWrapping: true,
    indentUnit: 2,
    tabSize: 2,
    foldGutter: true,
    gutters: ["CodeMirror-linenumbers", "CodeMirror-foldgutter"],
    matchBrackets: true,
    autoCloseBrackets: true,
    extraKeys: {
      "Ctrl-Q": (cm) => cm.foldCode(cm.getCursor()),
    },
  });
  ruleBodyEditor.refresh();

  // 一键回退：把回包体重置为原始响应体，直接保存生效
  const revertBtn = box.querySelector("#revertOriginalBtn");
  if (revertBtn) {
    revertBtn.onclick = async () => {
      if (!src || !src.responseBodyDecoded) return;
      try {
        const newResp = {
          statusCode: src.statusCode ?? 200,
          headers: src.responseHeaders || {},
          body: src.responseBodyDecoded,
        };
        await apiFetch("/mock-rules/" + encodeURIComponent(r.id), {
          method: "PUT",
          body: JSON.stringify({ response: newResp }),
        });
        await loadRules();
        //  修正：按当前规则 id 从刷新后的列表取最新数据重渲染详情
        //（此前误用不存在的 detail._activeRuleId/rulesList，回退后表单不刷新）。
        const updated = (ruleStore || []).find((x) => x.id === r.id);
        if (updated) renderRuleDetail(updated);
      } catch (e) {
        alert("回退失败：" + e.message);
      }
    };
  }

  $("editCancelBtn").onclick = () => renderRuleDetail(r);
  $("editSaveBtn").onclick = () => saveRuleEdit(r);
  $("editNote").focus();
  bindJsonTree(box);
}

/** 收集编辑表单 → PUT /mock-rules/{id} → 刷新。前端先做 note 非空拦截。 */
async function saveRuleEdit(rule) {
  if (!detail) return;
  const storedResp = (rule && rule.response) || {};
  //  Step3：手填规则（无 source 快照）的回包状态码/响应头在编辑表单
  // 里可改，保存时从表单输入读取；日志规则（有 source）维持  只读语义——
  // 与抓包快照绑定的状态码/响应头原样回传，不从表单读取。
  const handAuthored = !rule.source;
  let statusCode = Number(storedResp.statusCode) || 200;
  let headers = storedResp.headers || {};
  if (handAuthored) {
    const scEl = $("editStatusCode");
    if (scEl) statusCode = Number(scEl.value);
    headers = parseHeadersText($("editHeaders") && $("editHeaders").value);
  }
  if (!Number.isFinite(statusCode) || statusCode < 100 || statusCode > 599) {
    showError("回包状态码必须在 100–599 之间"); return;
  }
  const note = $("editNote").value.trim();
  if (!note) { showError("备注必填，请填写后再保存"); return; }

  // ：CodeMirror 内容先回写隐藏 textarea，再统一按 textarea 取值/校验。
  if (ruleBodyEditor) ruleBodyEditor.save();
  // ：保存前 JSON 合法性校验——回包体形如 JSON（{…}/[…]）时必须可解析，
  // 拦截全角符号/多余逗号等低级错误（ 真机教训），避免坏 JSON 以"无网络"误导。
  const bodyText = $("editBody").value;
  const trimmedBody = bodyText.trim();
  if (trimmedBody && (trimmedBody.startsWith("{") || trimmedBody.startsWith("["))) {
    try {
      JSON.parse(trimmedBody);
    } catch (e) {
      showError("回包体不是合法 JSON，已阻止保存：" + String(e && e.message || "").slice(0, 80) +
        "（常见原因：全角逗号/冒号、多余逗号）");
      return;
    }
  }

  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // : 不传 method/path/source；不传 enabled（保持当前开关）。
      body: JSON.stringify({
        response: { statusCode: statusCode, headers: headers, body: bodyText },
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
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 204) { showError("删除失败（HTTP " + res.status + "）"); return; }
    detail._activeRule = null;
    document.querySelectorAll(".rule-row").forEach((el) => el.classList.remove("active"));
    $("ruleDetail").innerHTML = '<div class="empty">已删除。</div>';
    showDetailPane("rule");
    await loadRules();
  } catch (e) {
    showError("删除失败：" + e.message);
  }
}

async function toggleRule(rule, enabled) {
  if (!detail) return;
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules/" + encodeURIComponent(rule.id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // : edit body carries only response + note + enabled; method/path/source
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
      ...(e.requestBodyDecoded ? { requestBodyDecoded: e.requestBodyDecoded } : {}),
      statusCode: e.statusCode,
      responseHeaders: e.responseHeaders,
      responseBody: e.responseBody,
      ...(e.responseBodyDecoded ? { responseBodyDecoded: e.responseBodyDecoded } : {}),
      ...(e.responseBodyBase64 ? { responseBodyBase64: e.responseBodyBase64 } : {}),
      capturedAt: e.timestamp,
    },
  };
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
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
//  Step2：从零创建 Mock 规则（空表单手填，无抓包背书）
// ============================================================================

/** 打开「新建 Mock 规则」空表单（右列 ruleDetail 面板）。与 openEditRuleForm
 *  复用同一套 .edit-row/.edit-pane 样式与 CodeMirror 配置；提交不带 source。 */
function openCreateRuleForm() {
  if (!detail) return;
  ruleBodyEditor = null;   //  离开编辑表单即释放 CM 引用
  showDetailPane("rule");
  detail._activeRule = null;
  detail._activeTraffic = null;
  document.querySelectorAll(".rule-row").forEach((el) => el.classList.remove("active"));

  const methodOptions = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]
    .map((m) => '<option value="' + m + '"></option>').join("");

  const box = $("ruleDetail");
  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-title">新建 Mock 规则 <span class="sub">（手填接口与回包；创建后默认停用，需手动打开开关）</span></div>' +
      '<div class="edit-pane">' +
        '<div class="d-kv"><span class="d-k">生效设备</span><span class="d-v">' +
          esc(detail.app) + " / " + esc(detail.did) +
          ' <span class="sub">（自动绑定当前设备，不可改）</span></span></div>' +
        '<div class="edit-row"><label>Method</label>' +
          '<input id="createMethod" list="createMethodList" class="filter-input" placeholder="GET" autocomplete="off" />' +
          '<datalist id="createMethodList">' + methodOptions + '</datalist></div>' +
        '<div class="edit-row"><label>Path（必须以 / 开头，不含查询串 ?）</label>' +
          '<input id="createPath" class="filter-input" placeholder="/api/hello" autocomplete="off" />' +
          '<div class="sub">二进制接口（如 xcp 加密回包）建议从抓包一键创建</div></div>' +
        '<div class="edit-row"><label>状态码</label>' +
          '<input id="createStatus" type="number" min="100" max="599" class="filter-input" value="200" /></div>' +
        '<div class="edit-row"><label>响应头（每行 Key: Value，可留空）</label>' +
          '<textarea id="createHeaders" class="filter-input" rows="3" placeholder="Content-Type: application/json"></textarea></div>' +
        '<div class="edit-row"><label>备注（必填：这条规则测什么）</label>' +
          '<input id="createNote" class="filter-input" placeholder="例如：联调期模拟 /api/hello 的成功回包" /></div>' +
        '<div class="edit-row edit-body-row"><label>回包体（UTF-8 文本；以 { 或 [ 开头时按 JSON 校验）</label>' +
          '<div class="edit-body-wrap"><textarea id="createBody" class="filter-input"></textarea></div></div>' +
      "</div>" +
      '<div class="edit-actions">' +
        '<button id="createCancelBtn" class="ghost small">取消</button>' +
        '<button id="createSaveBtn" class="small">创建</button>' +
      "</div>" +
    "</div>";

  // CodeMirror 接管回包体（与编辑表单同款配置）。
  ruleBodyEditor = CodeMirror.fromTextArea($("createBody"), {
    mode: { name: "javascript", json: true },
    lineNumbers: true,
    lineWrapping: true,
    indentUnit: 2,
    tabSize: 2,
    foldGutter: true,
    gutters: ["CodeMirror-linenumbers", "CodeMirror-foldgutter"],
    matchBrackets: true,
    autoCloseBrackets: true,
    extraKeys: {
      "Ctrl-Q": (cm) => cm.foldCode(cm.getCursor()),
    },
  });
  ruleBodyEditor.refresh();

  $("createCancelBtn").onclick = () => {
    $("ruleDetail").innerHTML = '<div class="empty">点击左侧一条规则查看详情。</div>';
  };
  $("createSaveBtn").onclick = () => submitCreateRule();
  $("createMethod").focus();
}

/** 收集新建表单 → POST /mock-rules（不带 source，默认停用）。前端先做必填/
 *  path/JSON 校验，与服务端 Step1 规则对齐；服务端 400/409 时保留表单可修正。 */
async function submitCreateRule() {
  if (!detail) return;
  const method = ($("createMethod").value || "").trim().toUpperCase();
  if (!method) { showError("Method 不能为空"); return; }
  const path = ($("createPath").value || "").trim();
  if (!path) { showError("Path 不能为空"); return; }
  if (!path.startsWith("/")) { showError("Path 必须以 / 开头"); return; }
  if (path.indexOf("?") !== -1) { showError("Path 不得包含查询串 ?（查询参数不参与匹配）"); return; }
  const statusCode = Number($("createStatus").value);
  if (!Number.isFinite(statusCode) || statusCode < 100 || statusCode > 599) {
    showError("状态码必须在 100–599 之间"); return;
  }
  const note = ($("createNote").value || "").trim();
  if (!note) { showError("备注必填，请填写这条规则测什么"); return; }

  // CodeMirror 内容先回写隐藏 textarea，再统一取值。
  if (ruleBodyEditor) ruleBodyEditor.save();
  const bodyText = $("createBody").value || "";
  const trimmedBody = bodyText.trim();
  if (trimmedBody && (trimmedBody.startsWith("{") || trimmedBody.startsWith("["))) {
    try {
      JSON.parse(trimmedBody);
    } catch (e) {
      showError("回包体不是合法 JSON，已阻止创建：" + String(e && e.message || "").slice(0, 80) +
        "（常见原因：全角逗号/冒号、多余逗号）");
      return;
    }
  }

  // 响应头：每行 "Key: Value"，空行/无冒号行跳过。
  const headers = parseHeadersText($("createHeaders") && $("createHeaders").value);

  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/mock-rules", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // D4 默认停用；从零创建不带 source（D1 统一模型，Source=nil）。
      body: JSON.stringify({
        method: method,
        path: path,
        response: { statusCode: statusCode, headers: headers, body: bodyText },
        note: note,
        enabled: false,
      }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => null);
      showError((err && err.message) || "创建规则失败（HTTP " + res.status + "）");
      return;   // 表单保留打开，可修正后重提
    }
    showError("已创建规则（默认停用，到左侧打开开关即可 Mock）");
    await loadRules();
    $("ruleDetail").innerHTML = '<div class="empty">已创建。点击左侧规则可查看详情。</div>';
  } catch (e) {
    showError("创建规则失败：" + e.message);
  }
}

//  Step2：规则区头部「新建 Mock 规则」入口（脚本在 body 末尾加载，DOM 已就绪）。
(function () {
  const btn = document.getElementById("ruleCreateBtn");
  if (btn) btn.onclick = openCreateRuleForm;
})();
