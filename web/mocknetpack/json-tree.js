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

/** 请求/响应体展示入口：静态资源（opts.staticKind === "video"/"image"）→ 原 URL
 *  卡片；图片响应（Content-Type 白名单 + base64）→ 内联预览；
 *  能生成 JSON 树用树，否则美化文本 <pre>（空态显示「（空）」）。
 *  opts = { staticKind, url, contentType, contentLength, contentRange, base64,
 *           isRequest, text }。 */
function bodyHtml(text, opts) {
  const media = staticResourceHtml(opts);
  if (media) return media;
  const img = imagePreviewHtml(text, opts);
  if (img) return img;
  const tree = jsonTreeHtml(text);
  if (tree) return tree;
  return "<pre>" + (esc(formatBody(text)) || "（空）") + "</pre>";
}

// 方案 D：静态资源预览 —— 命中白名单的响应不存字节，浏览器直接用原
// URL 回放（不引入新 UI 框架）。bodyHtml 按 opts.staticKind 分发，不依赖
// 具体 MIME 串；新增分类只需在此加一个分支。

/** 静态资源卡片分发：video → <video> 封面卡；image → 大图占位卡（M15）。
 *  非静态 / 缺 url → null（回退原有文本/JSON 路径）。 */
function staticResourceHtml(opts) {
  if (!opts || !opts.url) return null;
  if (opts.staticKind === "video") return videoResourceHtml(opts);
  if (opts.staticKind === "image") return staticImageHtml(opts);
  return null;
}

/** 视频响应卡片 HTML：<video preload="metadata"> 只拉前几百 KB 自动出首帧当
 *  封面，不下载整段；加载失败由 bindJsonTree 挂 onerror 降级 ▶ 占位。 */
function videoResourceHtml(opts) {
  const url = opts.url;
  const name = fileNameFromUrl(url);
  const sub = [opts.contentType || "video/*"]
    .concat(opts.contentLength ? [humanSize(Number(opts.contentLength))] : [])
    .concat(opts.contentRange ? ["Range " + opts.contentRange] : [])
    .join(" · ");
  return '<div class="media-card" data-media-url="' + esc(url) + '">' +
      '<video class="media-cover" preload="metadata" src="' + esc(url) + '" muted playsinline></video>' +
      '<div class="media-meta">' +
        '<div class="media-name">' + esc(name) + "</div>" +
        '<div class="media-sub">' + esc(sub) + "</div>" +
        '<div class="media-actions">' +
          '<a class="media-open" href="' + esc(url) + '" target="_blank" rel="noopener">新页签打开</a>' +
          '<button type="button" class="ghost small" data-copy-url>复制链接</button>' +
        "</div>" +
      "</div>" +
    "</div>";
}

/** M15：大图响应卡片。字节未入库，默认只显示占位（不自动发起原图请求，
 *  避免一条抓包记录就向任意内网/CDN 地址拉取大图）；用户点「加载原图」后
 *  才把原 URL 塞进 <img>，失败降级为文案。 */
function staticImageHtml(opts) {
  const url = opts.url;
  const name = fileNameFromUrl(url);
  const sub = [opts.contentType || "image/*"]
    .concat(opts.contentLength ? [humanSize(Number(opts.contentLength))] : [])
    .concat(opts.contentRange ? ["Range " + opts.contentRange] : [])
    .join(" · ");
  return '<div class="media-card img-media-card" data-media-url="' + esc(url) + '">' +
      '<div class="media-cover img-media-cover" data-load-image>点击加载</div>' +
      '<div class="media-meta">' +
        '<div class="media-name">' + esc(name) + "</div>" +
        '<div class="media-sub">' + esc(sub) + "</div>" +
        '<div class="media-note">原图未入库，需联网从原 URL 加载（内网 / 需鉴权资源可能不可达）</div>' +
        '<div class="media-actions">' +
          '<button type="button" class="ghost small" data-load-image>加载原图</button>' +
          '<a class="media-open" href="' + esc(url) + '" target="_blank" rel="noopener">新页签打开</a>' +
          '<button type="button" class="ghost small" data-copy-url>复制链接</button>' +
        "</div>" +
      "</div>" +
    "</div>";
}

/** URL 最后一段路径作为展示名（解码；空则回退 host+path，仍空回退原串）。 */
function fileNameFromUrl(url) {
  try {
    const u = new URL(url);
    const last = decodeURIComponent(u.pathname.split("/").filter(Boolean).pop() || "");
    if (last) return last;
    return u.host + u.pathname;
  } catch (_) { return url; }
}

/** 字节数 → 人类可读（与 Go 端 HumanBytes 口径一致，1024 进制）；非正数返回空串。 */
function humanSize(n) {
  if (!(n > 0)) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

/** 字节数 → 十进制单位（1 MB = 1,000,000 B）。仅用于「完整 N MB」这类与
 *  文件大小直觉一致的展示（7,441,522 B → 7.4 MB）。 */
function decimalSize(n) {
  if (!(n > 0)) return "";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + " MB";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + " KB";
  return n + " B";
}

// SDK 抓包上限（= 服务端 ImageInlineLimit）：请求体最多上传 1 MiB，超出即截断。
const IMG_INLINE_LIMIT_BYTES = 1048576;

/** 截断徽标：仍在内联路径的图（含上传失败保留的截断样本）用 base64 实际长度
 *  与声明的完整大小比对，小于即标注「仅前 X / 完整 Y · 原因」。
 *  声明大小来源优先级：[binary N bytes] 占位文本 > Content-Length。 */
function imageTruncBadge(text, opts, b64Len) {
  const actual = Math.floor((b64Len * 3) / 4);
  const declared = declaredBodySize(text, opts);
  if (!declared || actual <= 0 || actual >= declared) return "";
  const shown = actual >= IMG_INLINE_LIMIT_BYTES ? "1 MiB" : humanSize(actual);
  const reason = opts && opts.isRequest
    ? "本次上传未成功，源站无可回读内容"
    : "完整内容未保留（仅存前 1 MiB）";
  return '<div class="img-trunc-badge">仅前 ' + esc(shown) +
    " / 完整 " + esc(decimalSize(declared)) + " · " + esc(reason) + "</div>";
}

/** 从占位文本 / Content-Length 解析声明的完整字节数；无法确定返回 0。 */
function declaredBodySize(text, opts) {
  const m = /\[binary (\d+) bytes\]/.exec(String(text == null ? "" : text));
  if (m) return Number(m[1]);
  const len = opts && opts.contentLength ? Number(opts.contentLength) : 0;
  return Number.isFinite(len) && len > 0 ? len : 0;
}

/** 图片内联预览：优先按 base64 字节嗅探真实格式（上游 Content-Type 可能与实际
 *  内容不符，如声明 image/jpeg 实为 PNG，此时按声明 MIME 渲染会破图）；
 *  嗅探失败再回退 Content-Type 白名单判定。非白名单/无 base64 → null。
 *  被截断的图额外加一条警示徽标（不改变渲染路径）。 */
function imagePreviewHtml(text, opts) {
  if (!opts) return null;
  const b64 = opts.base64;
  if (!b64) return null;
  const sniffed = sniffImageType(b64);
  const ct = sniffed ||
    String(opts.contentType || "").split(";")[0].trim().toLowerCase();
  if (!IMAGE_PREVIEW_TYPES.has(ct)) return null;
  const badge = imageTruncBadge(text, opts, String(b64).length);
  return '<div class="img-preview">' + badge +
    '<img src="data:' + ct + ";base64," + b64 +
    '" alt="图片响应预览" loading="lazy" data-open-lightbox></div>';
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

/** 事件委托：点击箭头/花括号切换节点折叠；视频卡片点击 → 新页签打开原 URL，
 *  复制按钮仅复制不打开；大图卡片点击「加载原图」→ 塞入 <img>（失败降级），
 *  点已加载的图 / 内联预览图 → 灯箱放大（Esc 或点背景关闭）。
 *  绑定在渲染容器（赋值覆盖，不累积监听器）。 */
function bindJsonTree(root) {
  if (!root) return;
  root.onclick = (ev) => {
    const t = ev.target && ev.target.closest ? ev.target.closest("[data-jv-toggle]") : null;
    if (t) {
      const node = t.closest(".jv-node");
      if (node) { ev.preventDefault(); node.classList.toggle("jv-collapsed"); }
      return;
    }
    if (!ev.target || !ev.target.closest) return;

    // M15：内联预览图 / 已加载原图 → 灯箱放大。
    const previewImg = ev.target.closest(".img-preview img");
    if (previewImg) {
      ev.preventDefault();
      openImageLightbox(previewImg.src);
      return;
    }
    const loadedImg = ev.target.closest("img.img-media-full");
    if (loadedImg) {
      ev.preventDefault();
      openImageLightbox(loadedImg.src);
      return;
    }

    // 方案 D：媒体卡片交互（复制按钮 / 加载原图 / 整卡打开）。
    const copyBtn = ev.target.closest("[data-copy-url]");
    if (copyBtn) {
      ev.preventDefault();
      ev.stopPropagation();
      const card = copyBtn.closest(".media-card");
      if (card && card.dataset.mediaUrl) copyTextToClipboard(card.dataset.mediaUrl);
      return;
    }
    const loadBtn = ev.target.closest("[data-load-image]");
    if (loadBtn) {
      ev.preventDefault();
      ev.stopPropagation();
      loadCardImage(loadBtn.closest(".media-card"));
      return;
    }
    const card = ev.target.closest(".media-card");
    if (card && !ev.target.closest("a") && card.dataset.mediaUrl) {
      window.open(card.dataset.mediaUrl, "_blank", "noopener");
    }
  };
  // 视频封面加载失败（原 URL 内网不可达等）→ 降级 ▶ 占位，页面不报错。
  // error 事件不冒泡，故在绑定处逐个挂载。
  root.querySelectorAll("video.media-cover").forEach((v) => {
    v.onerror = () => v.classList.add("media-cover-fallback");
  });
}

/** M15：把原 URL 加载进大图卡片的封面位。<img> 失败时降级为文案并允许重试
 *  （data-media-loaded 回到 0）；成功后置 1，避免重复请求。 */
function loadCardImage(card) {
  if (!card || card.dataset.mediaLoaded === "1" || card.dataset.mediaLoaded === "loading") return;
  const url = card.dataset.mediaUrl || "";
  const slot = card.querySelector(".img-media-cover");
  if (!url || !slot) return;
  card.dataset.mediaLoaded = "loading";
  slot.classList.remove("media-cover-fallback");
  slot.textContent = "加载中…";
  const img = document.createElement("img");
  img.className = "media-cover img-media-full";
  img.alt = "原图预览";
  img.loading = "lazy";
  img.onload = () => { card.dataset.mediaLoaded = "1"; };
  img.onerror = () => {
    card.dataset.mediaLoaded = "0";
    slot.textContent = "原图加载失败（内网 / 需鉴权 / 已过期）";
    slot.classList.add("media-cover-fallback");
  };
  img.src = url;
  slot.textContent = "";
  slot.appendChild(img);
}

/** 图片灯箱：全屏遮罩 + 等比放大的图；点背景 / × / Esc 关闭。
 *  遮罩只创建一次（挂在 body 上），关闭时清空 src 释放大图内存。 */
function openImageLightbox(src) {
  if (!src) return;
  let box = document.getElementById("imgLightbox");
  if (!box) {
    box = document.createElement("div");
    box.id = "imgLightbox";
    box.className = "img-lightbox";
    box.innerHTML = '<span class="img-lightbox-close" role="button" aria-label="关闭">×</span>' +
      '<img alt="原图预览">';
    box.addEventListener("click", (ev) => {
      if (ev.target === box || (ev.target.classList && ev.target.classList.contains("img-lightbox-close"))) {
        closeImageLightbox();
      }
    });
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") closeImageLightbox();
    });
    document.body.appendChild(box);
  }
  box.querySelector("img").src = src;
  box.classList.add("open");
}

/** 关闭灯箱（无遮罩时 no-op）。 */
function closeImageLightbox() {
  const box = document.getElementById("imgLightbox");
  if (!box || !box.classList.contains("open")) return;
  box.classList.remove("open");
  box.querySelector("img").removeAttribute("src");
}
