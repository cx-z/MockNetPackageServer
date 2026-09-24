"use strict";

// MockNetPack web: mock rule list, detail/editor (CodeMirror JSON), save/
// delete/toggle, and "Mock this request". Pure move from app.js.
// ============================================================================
// Mock 规则（M3.5）
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
    el.className = "rule-row" + (r.enabled ? "" : " disabled") +
      (detail._activeRule === r.id ? " active" : "");
    el.dataset.rid = r.id;
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

    // 点击规则体 → 右列展示详情/编辑（M8.2 两列布局）。
    el.querySelector(".r-body").onclick = () => {
      detail._activeRule = r.id;
      document.querySelectorAll(".rule-row").forEach((el2) =>
        el2.classList.toggle("active", el2.dataset.rid === r.id));
      renderRuleDetail(r);
    };

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
  ruleBodyEditor = null;   // M8.6 离开编辑表单即释放 CM 引用（DOM 由 innerHTML 整体替换）
  showDetailPane("rule");
  detail._activeTraffic = null;
  document.querySelectorAll(".traffic-row").forEach((el) => el.classList.remove("active"));
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

  // 请求页签：来源快照的方法/路径 + 原始请求头 + 原始请求体
  const reqHeadersRows = (src && src.requestHeaders) ? Object.entries(src.requestHeaders)
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("") : "";
  const reqTab =
      '<div class="d-kv"><span class="d-k">接口</span><span class="d-v">' + esc(r.method) + " " + esc(r.path) + "</span></div>" +
      (src
        ? '<div class="d-block"><div class="d-title">原始请求头</div>' + (reqHeadersRows || '<div class="d-v">—</div>') + "</div>" +
          '<div class="d-block"><div class="d-title">原始请求体</div>' +
            bodyHtml(src.requestBodyDecoded || (src.requestBodyBase64 ? "[二进制 " + atob(src.requestBodyBase64).length + " 字节]" : (src.requestBody || ""))) + "</div>"
        : '<div class="d-v" style="color:var(--muted)">（无来源快照）</div>');

  // 响应页签：状态/备注/回包状态码 + 响应头 + 回包体（不展示原始响应体）
  const respTab =
      '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
        (r.enabled ? (r.effective ? "生效中" : "冲突未生效") : "已停用") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">备注</span><span class="d-v">' +
        (r.note ? esc(r.note) : '<span style="color:var(--muted)">（未填写）</span>') + "</span></div>" +
      '<div class="d-kv"><span class="d-k">回包状态码</span><span class="d-v">' + (resp.statusCode ?? "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (headRows(resp.headers) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">回包体</div>' + bodyHtml(bodyDisp) + "</div>";

  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-actions"><button id="ruleEditBtn" class="small">编辑</button></div>' +
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

/** 编辑规则表单（M5）：回包体/备注可改；method/path 与回包状态码/响应头只读不可改。 */
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
            bodyHtml(src.requestBodyDecoded || (src.requestBodyBase64 ? "[二进制 " + atob(src.requestBodyBase64).length + " 字节]" : (src.requestBody || ""))) + "</div>"
        : '<div class="d-v" style="color:var(--muted)">（无来源快照）</div>');

  // 响应页签：回包状态码/响应头只读展示（与请求日志一致，不可修改），
  // 备注/回退按钮在上，回包体 JSON 折叠编辑器在下并撑满剩余高度。
  const respHeadersRows = (resp.headers) ? Object.entries(resp.headers)
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("") : "";
  const respTab =
      '<div class="edit-pane">' +
      '<div class="d-kv"><span class="d-k">回包状态码</span><span class="d-v">' + (resp.statusCode ?? "—") + "</span></div>" +
      '<div class="d-block"><div class="d-title">响应头</div>' + (respHeadersRows || '<div class="d-v">—</div>') + "</div>" +
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
      // M8.6：从隐藏页签切回响应页时 CM 需重算尺寸，否则空白/错位
      if (btn.dataset.tab === "resp" && ruleBodyEditor) {
        setTimeout(() => ruleBodyEditor.refresh(), 0);
      }
    };
  });

  // M8.6：回包体 JSON 折叠编辑器（vendored CodeMirror，同源加载见 lib/codemirror/README.md）。
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
        // M8.6 修正：按当前规则 id 从刷新后的列表取最新数据重渲染详情
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
  // M8.8：回包状态码/响应头为只读（与抓包一致），保存时原样回传，不再从表单输入读取。
  const storedResp = (rule && rule.response) || {};
  const statusCode = Number(storedResp.statusCode) || 200;
  if (!Number.isFinite(statusCode) || statusCode <= 0) {
    showError("回包状态码必须是正整数"); return;
  }
  const headers = storedResp.headers || {};
  const note = $("editNote").value.trim();
  if (!note) { showError("备注必填，请填写后再保存"); return; }

  // M8.6：CodeMirror 内容先回写隐藏 textarea，再统一按 textarea 取值/校验。
  if (ruleBodyEditor) ruleBodyEditor.save();
  // M8.2：保存前 JSON 合法性校验——回包体形如 JSON（{…}/[…]）时必须可解析，
  // 拦截全角符号/多余逗号等低级错误（M6.4 真机教训），避免坏 JSON 以"无网络"误导。
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
      // M5: 不传 method/path/source；不传 enabled（保持当前开关）。
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
