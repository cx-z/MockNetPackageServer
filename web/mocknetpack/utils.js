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
// M8.4：请求体/响应体 JSON 美化。能 parse 成 JSON 就分行缩进；纯文本/二进制占位原样返回。
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

function newUuid() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}
