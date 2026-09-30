"use strict";

// MockNetPack web: collapsible JSON tree viewer for request/response
// bodies (read-only display). Pure move from app.js.
// ============================================================================
// JSON 树查看器：请求体/响应体按花括号展开/折叠（默认展开，折叠显示 { … }）。
// 仅用于只读展示；编辑区 textarea 仍为原始文本。节点数超限回退纯文本 <pre>。
// ============================================================================
const JSON_TREE_MAX_NODES = 5000; // 超过该节点数回退 pre，防大 JSON 渲染卡死

/** 生成可折叠 JSON 树 HTML；非 JSON / 空 / 超节点数 → 返回 null（调用方回退 <pre>）。
 *  精度修复：解析用无损 jsonParseTree（数字保留原文 token，json-lossless.js），
 *  不再经 JSON.parse→Number（19 位整数 ID 会舍入成 …5000）。 */
function jsonTreeHtml(text) {
  if (text == null || text === "") return null;
  const s = String(text).trim();
  if (s === "" || (s[0] !== "{" && s[0] !== "[")) return null;
  const parsed = jsonParseTree(s);
  if (parsed === null) return null;
  const stat = { nodes: 0 };
  jvCount(parsed, stat);
  if (stat.nodes > JSON_TREE_MAX_NODES) return null;
  return '<div class="jv-tree">' + jvNode(parsed) + "</div>";
}

/** 节点计数（纯遍历不生成字符串），超限即停。适配无损节点树：
 *  {t:'obj',kvs} / {t:'arr',items} / {t:'str'|'num'|'lit',text}。 */
function jvCount(v, stat) {
  if (++stat.nodes > JSON_TREE_MAX_NODES) return;
  if (v.t === "arr") {
    for (let i = 0; i < v.items.length; i++) jvCount(v.items[i], stat);
  } else if (v.t === "obj") {
    for (let i = 0; i < v.kvs.length; i++) jvCount(v.kvs[i][1], stat);
  }
  // str/num/lit 为叶子
}

/** 渲染一个 JSON 值：叶子直接输出，对象/数组输出可折叠节点。
 *  数字/字符串/字面量节点使用无损解析保留的原文 text（数字含 19 位 ID 逐位不变）。 */
function jvNode(v) {
  if (v.t === "lit") return '<span class="jv-' + (v.text === "null" ? "null" : "bool") + '">' + esc(v.text) + "</span>";
  if (v.t === "str") return '<span class="jv-str">' + esc(v.text) + "</span>";
  if (v.t === "num") return '<span class="jv-num">' + esc(v.text) + "</span>";
  const isArr = v.t === "arr";
  const open = isArr ? "[" : "{";
  const close = isArr ? "]" : "}";
  const items = isArr ? v.items : v.kvs;
  if (items.length === 0) {
    return '<span class="jv-brace">' + open + close + "</span>";
  }
  const rows = isArr
    ? v.items.map((x, i) => '<div class="jv-row">' + jvNode(x) +
        (i < v.items.length - 1 ? '<span class="jv-comma">,</span>' : "") + "</div>").join("")
    : v.kvs.map((kv, i) =>
        '<div class="jv-row"><span class="jv-key">' + esc(kv[0]) +
        '</span><span class="jv-colon">: </span>' + jvNode(kv[1]) +
        (i < v.kvs.length - 1 ? '<span class="jv-comma">,</span>' : "") + "</div>").join("");
  return '<div class="jv-node">' +
    '<span class="jv-arrow" data-jv-toggle title="展开/折叠"></span>' +
    '<span class="jv-brace" data-jv-toggle>' + open + "</span>" +
    '<div class="jv-children">' + rows +
      '<span class="jv-ellipsis">…</span>' +
      '<span class="jv-brace" data-jv-toggle>' + close + "</span>" +
    "</div>" +
  "</div>";
}

// 位图预览白名单（仅 raster，排除 image/svg+xml 等可执行/易混淆类型，防 XSS）。
const IMAGE_PREVIEW_TYPES = new Set([
  "image/jpeg", "image/png", "image/gif", "image/webp",
  "image/bmp", "image/avif", "image/x-icon", "image/heic", "image/heif",
]);

/** 请求/响应体展示入口：图片响应（Content-Type 白名单 + base64）→ 内联预览；
 *  能生成 JSON 树用树，否则美化文本 <pre>（空态显示「（空）」）。
 *  opts = { contentType, base64 }：二进制图片渲染；其余情况回退原逻辑。 */
function bodyHtml(text, opts) {
  const img = imagePreviewHtml(opts);
  if (img) return img;
  const tree = jsonTreeHtml(text);
  if (tree) return tree;
  return "<pre>" + (esc(formatBody(text)) || "（空）") + "</pre>";
}

/** 图片内联预览：优先按 base64 字节嗅探真实格式（上游 Content-Type 可能与实际
 *  内容不符，如声明 image/jpeg 实为 PNG，此时按声明 MIME 渲染会破图）；
 *  嗅探失败再回退 Content-Type 白名单判定。非白名单/无 base64 → null。 */
function imagePreviewHtml(opts) {
  if (!opts) return null;
  const b64 = opts.base64;
  if (!b64) return null;
  const sniffed = sniffImageType(b64);
  const ct = sniffed ||
    String(opts.contentType || "").split(";")[0].trim().toLowerCase();
  if (!IMAGE_PREVIEW_TYPES.has(ct)) return null;
  return '<div class="img-preview"><img src="data:' + ct + ";base64," + b64 +
    '" alt="图片响应预览" loading="lazy"></div>';
}

/** 常见位图魔数嗅探（base64 前缀 → 真实类型）。返回 null 表示无法识别。 */
function sniffImageType(b64) {
  const s = String(b64);
  if (s.startsWith("/9j/")) return "image/jpeg";            // FF D8 FF
  if (s.startsWith("iVBORw0KGgo")) return "image/png";      // 89 50 4E 47 ...
  if (s.startsWith("R0lGOD")) return "image/gif";           // 47 49 46 38
  if (s.startsWith("UklGR")) return "image/webp";           // RIFF....WEBP
  if (s.startsWith("Qk0")) return "image/bmp";              // 42 4D
  if (s.startsWith("AAAB")) return "image/x-icon";          // 00 00 01 00
  if (s.startsWith("AAAAGmZ0eXBhdmlm")) return "image/avif"; // ftypavif
  return null;
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
