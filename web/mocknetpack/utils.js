"use strict";

// MockNetPack web: shared utilities ($, STATUS, time/escape helpers,
// body formatting, method styling, UUID fallback). Pure move from app.js.
const $ = (id) => document.getElementById(id);
const STATUS = {
  idle:      { label: "待命",   cls: "idle" },
  capturing: { label: "抓包中", cls: "capturing" },
  offline:   { label: "离线",   cls: "offline" },
  ended:     { label: "已结束", cls: "offline" },
};

function showError(msg) {
  const bar = $("errorBar");
  bar.textContent = msg;
  bar.style.display = "block";
  clearTimeout(showError._t);
  showError._t = setTimeout(() => { bar.style.display = "none"; }, 6000);
}

function relTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  const diff = (Date.now() - t) / 1000;
  if (diff < 5) return "刚刚";
  if (diff < 60) return Math.floor(diff) + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return new Date(iso).toLocaleString("zh-CN");
}
function clockTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso);
  return t.toLocaleTimeString("zh-CN", { hour12: false }) + "." +
    String(t.getMilliseconds()).padStart(3, "0");
}
function esc(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
// ：请求体/响应体 JSON 美化。能 parse 成 JSON 就分行缩进；纯文本/二进制占位原样返回。
// 仅用于详情页只读展示与编辑表单预填（不影响保存——保存始终提交 textarea 原始文本）。
// 精度修复：不用 JSON.parse→JSON.stringify（会把 19 位整数 ID 舍入成 …5000），
// 改用无损分词美化 jsonPrettyPrint，数字字面量逐位保留（json-lossless.js）。
function formatBody(text) {
  if (text == null || text === "") return "";
  const s = String(text).trim();
  if (s === "") return "";
  const first = s[0];
  if (first !== "{" && first !== "[") return s;
  const pretty = jsonPrettyPrint(s);
  return pretty !== null ? pretty : s;
}

function shortId(id) { return id ? id.slice(0, 8) + "…" : ""; }
// 从请求/响应头中取单值（map 键大小写不敏感，兼容多值取首个）。
function headerValue(headers, name) {
  if (!headers) return "";
  const lower = String(name).toLowerCase();
  for (const k in headers) {
    if (!Object.prototype.hasOwnProperty.call(headers, k)) continue;
    if (k.toLowerCase() !== lower) continue;
    const v = headers[k];
    if (v == null) return "";
    return Array.isArray(v) ? String(v[0] || "") : String(v);
  }
  return "";
}

function methodCls(m) {
  const u = (m || "").toUpperCase();
  if (u === "GET") return "get";
  if (u === "POST") return "post";
  if (u === "PUT" || u === "PATCH") return "put";
  if (u === "DELETE") return "delete";
  return "other";
}

/** H5 请求的 API 或页面加载来源。 */
function h5TrafficKind(entry) {
  if (entry?.adapter === "wkwebview-fetch") return "fetch";
  if (entry?.adapter === "wkwebview-xhr") return "XHR";
  if (entry?.adapter === "wkwebview-beacon") return "Beacon";
  if (entry?.adapter === "wkwebview-eventsource") return "EventSource";
  if (entry?.adapter === "wkwebview-navigation") return "主文档";
  if (entry?.adapter === "wkwebview-resource") return "页面资源";
  return "";
}

/** 业务 API 条目可作为 H5 规则的匹配来源。 */
function h5MockableTraffic(entry) {
  return entry?.adapter === "wkwebview-fetch" || entry?.adapter === "wkwebview-xhr";
}

/** H5 图片统一用原 URL 预览，旧的页面资源记录可按路径后缀识别。 */
function trafficResponseStaticKind(entry) {
  if (!h5TrafficKind(entry) || entry.method !== "GET") return entry.responseStaticKind;
  if (entry.error || entry.statusCode >= 400 || entry.terminalStage === "h5ResourceError") return entry.responseStaticKind;

  let url;
  try { url = new URL(entry.url); } catch (_) { return entry.responseStaticKind; }
  if (url.protocol !== "http:" && url.protocol !== "https:") return entry.responseStaticKind;
  const contentType = headerValue(entry.responseHeaders, "Content-Type");
  if (/^image\//i.test(contentType || "")) return "image";
  if (entry.responseStaticKind || contentType || entry.adapter !== "wkwebview-resource") return entry.responseStaticKind;
  return /\.(avif|bmp|gif|heic|heif|ico|jpe?g|png|svg|webp)$/i.test(url.pathname) ? "image" : undefined;
}

/** 浏览器诊断阶段独立于 HTTP 状态码，排队和观测均有各自含义。 */
function h5DiagnosticStage(entry) {
  switch (entry?.terminalStage) {
  case "h5BeaconQueued": return ["已排队", "浏览器已将 Beacon 排队，未提供 HTTP 回包"];
  case "h5BeaconRejected": return ["未排队", "浏览器未接受 Beacon 排队", true];
  case "h5SSEOpen": return ["已连接", "EventSource 首次连接已打开"];
  case "h5SSEError": return ["连接错误", "EventSource 首次连接触发 error，浏览器未提供错误细节", true];
  case "h5NavigationFinished": return ["加载完成", "WebKit 主文档导航完成"];
  case "h5NavigationFailed": return ["导航失败", "WebKit 主文档导航失败", true];
  case "h5ResourceLoaded": return ["已加载", "资源元素触发 load"];
  case "h5ResourceObserved":
    return ["已观测", entry.statusCode == null ? "已观测到资源，HTTP 状态码不可用" : "已观测到资源"];
  case "h5ResourceError": return ["加载失败", "浏览器报告资源加载失败", true];
  default: return null;
  }
}

/** 诊断详情同时展示可见 HTTP 状态与浏览器加载结果。 */
function h5ResponseStatus(entry) {
  const diagnostic = h5DiagnosticStage(entry);
  if (!diagnostic) {
    return entry.statusCode == null ? entry.error || "浏览器未暴露响应" : String(entry.statusCode);
  }
  const status = entry.statusCode == null ? "" : entry.statusCode + " · ";
  return status + diagnostic[1] + (entry.error ? "：" + entry.error : "");
}

/** 页面资源仅在存在 Resource Timing 耗时时展示毫秒数。 */
function trafficDuration(entry, suffix) {
  if (entry.adapter === "wkwebview-resource" && !entry.durationMs) return "—";
  return (entry.durationMs || 0) + suffix;
}

// 把一条流量记录重建为完整 HTTP 报文交换（请求报文 + 响应报文，各自为
// HTTP/1.1 原始格式：请求行/状态行 + Host 与全部头 + 空行 + 体），供
// 「复制」按钮带走请求和回包的全部内容（粘贴到文档 / 工单 / 交给 AI 分析）。
// 服务端只存解析后的字段、不存原始报文，报文在此重建；JSON 体经 formatBody
// 无损美化排版，避免长串 JSON 一行堆到底。
function buildRawTrafficText(e) {
  // ---- 请求报文 ----
  let u = null;
  try { u = new URL(e.url); } catch (_) {}
  const target = u ? u.pathname + u.search : (e.url || "/");
  const reqLines = [(e.method || "GET") + " " + (target || "/") + " HTTP/1.1"];
  const reqHeaders = e.requestHeaders || {};
  const hasHost = Object.keys(reqHeaders).some((k) => k.toLowerCase() === "host");
  if (u && !hasHost) reqLines.push("Host: " + u.host);   // 捕获数据常缺 Host，原始报文必需
  reqLines.push(...headerLines(reqHeaders));
  const reqBody = formatBody(bodyText(e.requestBodyDecoded, e.requestBodyBase64, e.requestBody));
  const req = reqLines.join("\r\n") + "\r\n\r\n" + reqBody;
  // ---- 响应报文（请求失败时无回包，只复制请求） ----
  if (e.statusCode == null) return req;
  const respLines = ["HTTP/1.1 " + e.statusCode];
  respLines.push(...headerLines(e.responseHeaders));
  const resp = respLines.join("\r\n") + "\r\n\r\n" +
    formatBody(bodyText(e.responseBodyDecoded, e.responseBodyBase64, e.responseBody));
  // 两条报文之间用空行分隔；请求体为空时请求文本已以空行结尾，无需再加
  return req + (reqBody ? "\r\n\r\n" : "") + resp;
}

// 头对象序列化为 "Key: value" 行（多值头 ", " 合并；空值跳过；保留原始大小写）。
function headerLines(headers) {
  const out = [];
  for (const k in (headers || {})) {
    if (!Object.prototype.hasOwnProperty.call(headers, k)) continue;
    const v = headers[k];
    const val = Array.isArray(v) ? v.join(", ") : v;
    if (val == null || val === "") continue;
    out.push(k + ": " + val);
  }
  return out;
}

// 报文体选取：协议解码文本优先；二进制（base64 无解码文本）标注字节数并附
// base64 保证可完整还原；纯文本原样。与详情页展示一致。
function bodyText(decoded, base64, text) {
  if (decoded) return decoded;
  if (base64) {
    let n = 0;
    try { n = atob(base64).length; } catch (_) {}
    return "[binary " + n + " bytes — base64: " + base64 + "]";
  }
  return text || "";
}

// 复制文本到剪贴板。clipboard API 在非 HTTPS（如 http://localhost）下不可用，
// 回退 execCommand；返回是否成功。与分享链接复制的行为一致（share.js）。
async function copyTextToClipboard(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch (_) {}
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed"; ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch (_) { return false; }
}

function newUuid() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}


/** 展示抓包时记录的规则 ID，并区分服务端已核验与设备上报。 */
function trafficMockHtml(e, currentConfigNote = "") {
  if (!e.mocked) return "";
  const rule = e.matchedRuleId ? ' · 规则 ID <span class="d-mono">' + esc(e.matchedRuleId) + "</span>" : "";
  const result = e.mockHitVerified ? "是" : "设备上报，未核验";
  return '<div class="d-kv"><span class="d-k">Mock</span><span class="d-v">' +
    result + rule + esc(currentConfigNote) + "</span></div>";
}
