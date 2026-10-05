#!/usr/bin/env node
"use strict";

// MockNetPack web: M11 Step3 编辑放开回归（node，无浏览器依赖）。
// 运行：node tests/rules-edit-source.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// D3：编辑表单按 rule.source 有无分叉——
//   · 手填规则（source=null）：回包状态码/响应头可编辑（输入框/多行文本），
//     PUT 全量回传表单值；
//   · 日志规则（source 存在）：状态码/响应头维持 M8.8 只读，PUT 原样回传存储值。
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
let detailBox;
const makeEl = () => ({ innerHTML: "", textContent: "", onclick: null, focus() {}, querySelectorAll: () => [], querySelector: () => ({ onclick: null }) });
ctx.document.getElementById = (id) => {
  if (id === "ruleDetail") { if (!detailBox) detailBox = makeEl(); return detailBox; }
  const el = makeEl();
  el.value = values[id] !== undefined ? values[id] : "";
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

(async () => {
  // ---- 1) 手填规则编辑表单：状态码/响应头可编辑（W7） ----
  let r = handRule();
  ctx.openEditRuleForm(r);
  let html = detailBox.innerHTML;
  check("W7 手填规则：状态码渲染为可编辑输入 #editStatusCode",
    html.includes('id="editStatusCode"') && html.includes("回包状态码（100–599）"), html.slice(0, 400));
  check("W7 手填规则：响应头渲染为可编辑多行文本 #editHeaders",
    html.includes('id="editHeaders"'), html.slice(0, 400));
  check("W7 手填规则：不出现只读状态码展示", !html.includes("回包状态码</span>"), html.slice(0, 400));

  // ---- 2) 手填规则保存：PUT 带回表单改过的状态码/响应头（W7） ----
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

  // ---- 3) 日志规则编辑表单：状态码/响应头只读（W8，M8.8 回归） ----
  r = logRule();
  detailBox.innerHTML = "";
  ctx.openEditRuleForm(r);
  html = detailBox.innerHTML;
  check("W8 日志规则：不渲染可编辑状态码输入 #editStatusCode", !html.includes('id="editStatusCode"'), html.slice(0, 400));
  check("W8 日志规则：不渲染可编辑响应头 #editHeaders", !html.includes('id="editHeaders"'), html.slice(0, 400));
  check("W8 日志规则：响应头为只读展示", html.includes("响应头（只读）"), html.slice(0, 400));

  // ---- 4) 日志规则保存：PUT 原样回传存储状态码/响应头（W8 回归） ----
  values.editStatusCode = "999";   // 表单没有该控件，应被忽略
  values.editHeaders = "Forged: yes";
  values.editNote = "note";
  values.editBody = '{"data":1}';
  putCall = null;
  await ctx.saveRuleEdit(r);
  check("W8 日志规则保存发出 PUT", !!putCall && putCall.opts.method === "PUT", JSON.stringify(putCall));
  if (putCall) {
    const body = JSON.parse(putCall.opts.body);
    check("W8 PUT 状态码保持存储值（200，不受伪造输入影响）", body.response.statusCode === 200, JSON.stringify(body.response));
    check("W8 PUT 响应头保持存储值（无 Forged）",
      JSON.stringify(body.response.headers) === JSON.stringify({ "Content-Type": "application/json" }),
      JSON.stringify(body.response.headers));
  }

  if (failures) {
    console.error("\n共 " + failures + " 项失败");
    process.exit(1);
  }
  console.log("\n全部通过");
})();
