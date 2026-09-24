"use strict";

// MockNetPack web: collapsible JSON tree viewer for request/response
// bodies (read-only display). Pure move from app.js.
// ============================================================================
// JSON 树查看器：请求体/响应体按花括号展开/折叠（默认展开，折叠显示 { … }）。
// 仅用于只读展示；编辑区 textarea 仍为原始文本。节点数超限回退纯文本 <pre>。
// ============================================================================
const JSON_TREE_MAX_NODES = 5000; // 超过该节点数回退 pre，防大 JSON 渲染卡死

/** 生成可折叠 JSON 树 HTML；非 JSON / 空 / 超节点数 → 返回 null（调用方回退 <pre>）。 */
function jsonTreeHtml(text) {
  if (text == null || text === "") return null;
  const s = String(text).trim();
  if (s === "" || (s[0] !== "{" && s[0] !== "[")) return null;
  let parsed;
  try { parsed = JSON.parse(s); } catch (e) { return null; }
  const stat = { nodes: 0 };
  jvCount(parsed, stat);
  if (stat.nodes > JSON_TREE_MAX_NODES) return null;
  return '<div class="jv-tree">' + jvNode(parsed) + "</div>";
}

/** 节点计数（纯遍历不生成字符串），超限即停。 */
function jvCount(v, stat) {
  if (++stat.nodes > JSON_TREE_MAX_NODES) return;
  if (v === null || typeof v !== "object") return;
  if (Array.isArray(v)) {
    for (let i = 0; i < v.length; i++) jvCount(v[i], stat);
  } else {
    for (const k in v) if (Object.prototype.hasOwnProperty.call(v, k)) jvCount(v[k], stat);
  }
}

/** 渲染一个 JSON 值：叶子直接输出，对象/数组输出可折叠节点。 */
function jvNode(v) {
  if (v === null) return '<span class="jv-null">null</span>';
  const t = typeof v;
  if (t === "string") return '<span class="jv-str">' + esc(JSON.stringify(v)) + "</span>";
  if (t === "number") return '<span class="jv-num">' + v + "</span>";
  if (t === "boolean") return '<span class="jv-bool">' + v + "</span>";
  const isArr = Array.isArray(v);
  const open = isArr ? "[" : "{";
  const close = isArr ? "]" : "}";
  const keys = isArr ? null : Object.keys(v);
  if ((isArr && v.length === 0) || (!isArr && keys.length === 0)) {
    return '<span class="jv-brace">' + open + close + "</span>";
  }
  const rows = isArr
    ? v.map((x, i) => '<div class="jv-row">' + jvNode(x) +
        (i < v.length - 1 ? '<span class="jv-comma">,</span>' : "") + "</div>").join("")
    : keys.map((k, i) =>
        '<div class="jv-row"><span class="jv-key">' + esc(JSON.stringify(k)) +
        '</span><span class="jv-colon">: </span>' + jvNode(v[k]) +
        (i < keys.length - 1 ? '<span class="jv-comma">,</span>' : "") + "</div>").join("");
  return '<div class="jv-node">' +
    '<span class="jv-arrow" data-jv-toggle title="展开/折叠"></span>' +
    '<span class="jv-brace" data-jv-toggle>' + open + "</span>" +
    '<div class="jv-children">' + rows +
      '<span class="jv-ellipsis">…</span>' +
      '<span class="jv-brace" data-jv-toggle>' + close + "</span>" +
    "</div>" +
  "</div>";
}

/** 请求/响应体展示入口：能生成 JSON 树用树，否则美化文本 <pre>（空态显示「（空）」）。 */
function bodyHtml(text) {
  const tree = jsonTreeHtml(text);
  if (tree) return tree;
  return "<pre>" + (esc(formatBody(text)) || "（空）") + "</pre>";
}

/** 事件委托：点击箭头/花括号切换节点折叠。绑定在渲染容器（赋值覆盖，不累积监听器）。 */
function bindJsonTree(root) {
  if (!root) return;
  root.onclick = (ev) => {
    const t = ev.target && ev.target.closest ? ev.target.closest("[data-jv-toggle]") : null;
    if (!t) return;
    const node = t.closest(".jv-node");
    if (node) { ev.preventDefault(); node.classList.toggle("jv-collapsed"); }
  };
}
