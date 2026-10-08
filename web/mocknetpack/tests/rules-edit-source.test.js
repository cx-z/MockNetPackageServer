#!/usr/bin/env node
"use strict";

// MockNetPack web: 编辑放开回归（node，无浏览器依赖）。
// 运行：node tests/rules-edit-source.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 编辑表单能力与 MCP update_mock_rule 对齐（放开 source 限制）：
//   · 手填规则（source=null）：回包状态码/响应头可编辑（输入框/多行文本），
//     PUT 全量回传表单值；
//   · 日志规则（source 存在）：回包状态码/响应头同样可编辑（放开），
//     PUT 从表单读取并回传；
//   · 二进制规则（bodyBase64 存在）：渲染 clearBodyBase64 勾选（默认勾选）；
//     勾选 → PUT 不回传 bodyBase64（清除二进制、以文本回放）；
//     取消勾选 → PUT 回传原 bodyBase64（保留原始字节）。
//   · 一键回退：状态码/响应头恢复抓包快照；回包体回滚为抓包解码 JSON 原文
//     （source.responseBodyDecoded）并以文本回放——有解码文本时不再恢复
//     bodyBase64 密文；仅无解码文本的二进制接口才保留二进制快照原样回放。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const ctx = {};
ctx.atob = (s) => Buffer.from(s, "base64").toString("binary");
ctx.document = {
  getElementById: () => null,
  querySelectorAll: () => ({ forEach() {} }),
  createElement: () => null,
};
vm.createContext(ctx);
vm.runInContext(
  "var authUser = null; var detail = null; var ruleStore = null; var ruleBodyEditor = null;",
  ctx);
for (const f of ["json-lossless.js", "utils.js", "json-tree.js", "rules.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}

// ---- 测试替身 ----
const values = { editNote: "memo", editBody: '{"hello":"world"}', editStatusCode: "200", editHeaders: "" };
const checkedValues = { editClearBodyBase64: true };   // 默认勾选
let detailBox;
const selCache = {};
const makeEl = () => ({ innerHTML: "", textContent: "", onclick: null, checked: false, focus() {}, querySelectorAll: () => [], querySelector: (sel) => { if (!selCache[sel]) selCache[sel] = { onclick: null }; return selCache[sel]; } });
ctx.document.getElementById = (id) => {
  if (id === "ruleDetail") { if (!detailBox) detailBox = makeEl(); return detailBox; }
  const el = makeEl();
  el.value = values[id] !== undefined ? values[id] : "";
  el.checked = checkedValues[id] !== undefined ? checkedValues[id] : false;
  return el;
};
ctx.CodeMirror = { fromTextArea: () => ({ save() {}, refresh() {} }) };
ctx.showDetailPane = () => {};
ctx.loadRules = async () => {};
ctx.renderRuleDetail = () => {};
ctx.detail = { app: "com.example.app", did: "dev-1" };
let lastError = null;
ctx.showError = (m) => { lastError = m; };

let putCall = null;
ctx.apiFetch = async (url, opts) => {
  putCall = { url, opts };
  return { ok: true, status: 200, json: async () => ({}) };
};

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log("  PASS  " + name);
  } else {
    failures++;
    console.error("  FAIL  " + name + (detail ? " — " + detail : ""));
  }
}

function handRule(over) {
  return Object.assign({
    id: "r-hand", method: "GET", path: "/api/hello", enabled: false, note: "memo",
    response: { statusCode: 200, headers: { "Content-Type": "application/json" }, body: '{"hello":"world"}' },
    source: null,
  }, over || {});
}
function logRule(over) {
  return Object.assign({
    id: "r-log", method: "POST", path: "/api/session", enabled: false, note: "note",
    response: { statusCode: 200, headers: { "Content-Type": "application/json" }, body: '{"data":1}' },
    source: { method: "POST", path: "/api/session", url: "https://x/api/session", responseHeaders: { "Content-Type": ["application/json"] } },
  }, over || {});
}
function binaryRule(over) {
  return Object.assign({
    id: "r-bin", method: "GET", path: "/api/bin", enabled: false, note: "bin",
    response: { statusCode: 200, headers: {}, body: "", bodyBase64: "AQIDBA==" },
    source: { method: "GET", path: "/api/bin", url: "https://x/api/bin", responseHeaders: { "Content-Type": ["application/x-protobuf"] }, responseBodyDecoded: "解码文本占位" },
  }, over || {});
}

(async () => {
  // ---- 1) 手填规则编辑表单：状态码/响应头可编辑 ----
  let r = handRule();
  ctx.openEditRuleForm(r);
  let html = detailBox.innerHTML;
  check("W7 手填规则：状态码渲染为可编辑输入 #editStatusCode",
    html.includes('id="editStatusCode"') && html.includes("回包状态码（100–599）"), html.slice(0, 400));
  check("W7 手填规则：响应头渲染为可编辑多行文本 #editHeaders",
    html.includes('id="editHeaders"'), html.slice(0, 400));
  check("W7 手填规则：不出现只读状态码展示", !html.includes("回包状态码</span>"), html.slice(0, 400));

  // ---- 2) 手填规则保存：PUT 带回表单改过的状态码/响应头 ----
  values.editStatusCode = "201";
  values.editHeaders = "X-Trace: abc-123\nContent-Type: application/json";
  values.editNote = "updated memo";
  values.editBody = '{"changed":1}';
  putCall = null;
  await ctx.saveRuleEdit(r);
  check("W7 手填规则保存发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W7 PUT 状态码来自表单（201）", body.response.statusCode === 201, JSON.stringify(body.response));
    check("W7 PUT 响应头来自表单（X-Trace 解析）",
      body.response.headers["X-Trace"] === "abc-123", JSON.stringify(body.response.headers));
    check("W7 PUT body/备注透传", body.response.body === '{"changed":1}' && body.note === "updated memo");
  }

  // ---- 3) 日志规则编辑表单：状态码/响应头可编辑（放开 source 限制） ----
  r = logRule();
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  html = detailBox.innerHTML;
  check("W8 日志规则：渲染可编辑状态码输入 #editStatusCode", html.includes('id="editStatusCode"'), html.slice(0, 400));
  check("W8 日志规则：渲染可编辑响应头 #editHeaders", html.includes('id="editHeaders"'), html.slice(0, 400));
  check("W8 日志规则：不再出现「响应头（只读）」", !html.includes("响应头（只读）"), html.slice(0, 400));

  // ---- 4) 日志规则保存：PUT 从表单读取状态码/响应头 ----
  values.editStatusCode = "502";
  values.editHeaders = "X-Real: 1\nContent-Type: text/plain";
  values.editNote = "note";
  values.editBody = '{"data":1}';
  putCall = null;
  await ctx.saveRuleEdit(r);
  check("W8 日志规则保存发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W8 PUT 状态码来自表单（502）", body.response.statusCode === 502, JSON.stringify(body.response));
    check("W8 PUT 响应头来自表单（X-Real 解析）",
      body.response.headers["X-Real"] === "1", JSON.stringify(body.response.headers));
    check("W8 PUT 未携带 bodyBase64（非二进制规则）", !("bodyBase64" in body.response), JSON.stringify(body.response));
  }

  // ---- 5) 二进制规则编辑表单：渲染 clearBodyBase64 勾选，默认勾选 ----
  r = binaryRule();
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  html = detailBox.innerHTML;
  check("W9 二进制规则：渲染 #editClearBodyBase64 勾选框", html.includes('id="editClearBodyBase64"'), html.slice(0, 400));
  check("W9 二进制规则：勾选框默认 checked",
    html.includes('id="editClearBodyBase64" type="checkbox" checked'), html.slice(0, 400));

  // ---- 6) 二进制规则保存（默认勾选）：PUT 不回传 bodyBase64（清除二进制） ----
  values.editStatusCode = "200";
  values.editHeaders = "";
  values.editNote = "bin";
  values.editBody = '{"text":1}';
  checkedValues.editClearBodyBase64 = true;
  putCall = null;
  await ctx.saveRuleEdit(r);
  check("W9 勾选清除：保存发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W9 勾选清除：PUT response 不含 bodyBase64", !("bodyBase64" in body.response), JSON.stringify(body.response));
    check("W9 勾选清除：PUT body 为表单文本", body.response.body === '{"text":1}', JSON.stringify(body.response));
  }

  // ---- 7) 二进制规则保存（取消勾选）：PUT 回传原 bodyBase64（保留二进制） ----
  checkedValues.editClearBodyBase64 = false;
  putCall = null;
  await ctx.saveRuleEdit(r);
  check("W9 取消勾选：保存发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W9 取消勾选：PUT 回传原 bodyBase64", body.response.bodyBase64 === "AQIDBA==", JSON.stringify(body.response));
  }

  // ---- 8) 一键回退：恢复快照状态码/响应头；回包体回滚为抓包解码 JSON 原文（文本回放） ----
  // 日志规则（有 source，含多值响应头）：按钮渲染且回退 PUT 全量恢复
  r = logRule({ source: {
    method: "POST", path: "/api/session", url: "https://x/api/session", statusCode: 500,
    responseHeaders: { "Content-Type": ["application/json"], "X-Trace": ["a", "b"] },
    responseBody: '{"orig":1}',
  } });
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  html = detailBox.innerHTML;
  check("W10 有 source 即渲染回退按钮 #revertOriginalBtn", html.includes('id="revertOriginalBtn"'), html.slice(0, 400));
  check("W10 按钮文案为「一键回退为原始响应」", html.includes("一键回退为原始响应"), html.slice(0, 400));

  putCall = null;
  values.editNote = "note";   // 模拟表单当前备注（回退不改备注，取表单值）
  await selCache["#revertOriginalBtn"].onclick();
  check("W10 回退发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    // 必须走完整设备路由（旧写法 /mock-rules/{id} 404 静默失败）
    check("W10 回退 PUT 使用完整设备路由",
      putCall.url === "/devices/com.example.app/dev-1/mock-rules/r-log", putCall.url);
    const body = JSON.parse(putCall.opts.body);
    check("W10 回退恢复状态码（500）", body.response.statusCode === 500, JSON.stringify(body.response));
    check("W10 回退恢复响应头（多值拍平为单值）",
      body.response.headers["Content-Type"] === "application/json" && body.response.headers["X-Trace"] === "a, b",
      JSON.stringify(body.response.headers));
    check("W10 回退恢复回包体", body.response.body === '{"orig":1}', JSON.stringify(body.response));
    check("W10 回退携带备注（表单值兜底）", body.note === "note", JSON.stringify(body.note));
  }

  // 二进制规则 + 有解码文本回退：回包体恢复为解码 JSON 原文、清除二进制密文（文本回放）
  r = binaryRule({ source: {
    method: "GET", path: "/api/bin", url: "https://x/api/bin", statusCode: 200,
    responseHeaders: { "Content-Type": ["application/x-protobuf"] },
    responseBody: "", responseBodyBase64: "AQIDBA==", responseBodyDecoded: "解码文本占位",
  } });
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  putCall = null;
  await selCache["#revertOriginalBtn"].onclick();
  check("W10 二进制回退发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W10 二进制回退：回包体恢复为解码 JSON 原文", body.response.body === "解码文本占位", JSON.stringify(body.response));
    check("W10 二进制回退：不再携带 bodyBase64（转文本回放）", !("bodyBase64" in body.response), JSON.stringify(body.response));
  }

  // 二进制规则 + 无解码文本回退：保留二进制快照原样回放（无文本可回滚）
  r = binaryRule({ source: {
    method: "GET", path: "/api/bin", url: "https://x/api/bin", statusCode: 200,
    responseHeaders: { "Content-Type": ["application/x-protobuf"] },
    responseBody: "", responseBodyBase64: "AQIDBA==",
  } });
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  putCall = null;
  await selCache["#revertOriginalBtn"].onclick();
  check("W10 无解码文本回退发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W10 无解码文本回退：保留 bodyBase64 二进制快照", body.response.bodyBase64 === "AQIDBA==", JSON.stringify(body.response));
  }

  // 文本规则（已转文本、body 被编辑）+ 有解码文本回退：body 回滚为解码 JSON 原文
  r = logRule({ source: {
    method: "POST", path: "/api/session", url: "https://x/api/session", statusCode: 500,
    responseHeaders: { "Content-Type": ["application/json"] },
    responseBody: "[binary 3888 bytes]", responseBodyBase64: "AQIDBA==", responseBodyDecoded: '{"orig":1}',
  } });
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  putCall = null;
  await selCache["#revertOriginalBtn"].onclick();
  check("W10 文本规则回退发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W10 文本规则回退：body 回滚为解码 JSON 原文", body.response.body === '{"orig":1}', JSON.stringify(body.response));
    check("W10 文本规则回退：不携带 bodyBase64", !("bodyBase64" in body.response), JSON.stringify(body.response));
  }

  if (failures) {
    console.error("\n共 " + failures + " 项失败");
    process.exit(1);
  }
  console.log("\n全部通过");
})();
