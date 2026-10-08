#!/usr/bin/env node
"use strict";

// MockNetPack web:  Step2 从零创建规则表单测试（node，无浏览器依赖）。
// 运行：node tests/rules-create.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 覆盖 W1–W4：入口按钮存在；空表单六控件渲染；前端必填/path/状态码/JSON
// 校验分支；提交 payload 不含 source、默认停用、method 大写化、headers 按行
// 解析。W5/W6（201/409 真实服务端行为）由 Go 单测与端到端冒烟兜底。
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
const values = {
  createMethod: "get",
  createPath: "/api/hello",
  createStatus: "200",
  createHeaders: "Content-Type: application/json\nX-Trace: abc\n\nBadLineNoColon",
  createBody: '{"hello":"world"}',
  createNote: "mock greeting endpoint",
};
let detailBox;
const makeEl = () => ({ innerHTML: "", textContent: "", onclick: null, focus() {} });
ctx.document.getElementById = (id) => {
  if (id === "ruleDetail") {
    if (!detailBox) detailBox = makeEl();
    return detailBox;
  }
  const el = makeEl();
  el.value = values[id] !== undefined ? values[id] : "";
  return el;
};
ctx.CodeMirror = { fromTextArea: () => ({ save() {}, refresh() {} }) };
ctx.showDetailPane = () => {};
ctx.loadRules = async () => {};
ctx.detail = { app: "com.example.app", did: "dev-1" };

let lastError = null;
ctx.showError = (m) => { lastError = m; };

let fetchCalls = [];
ctx.apiFetch = async (url, opts) => {
  fetchCalls.push({ url, opts });
  return { ok: true, status: 201, json: async () => ({ id: "new-rule" }) };
};

function reset() {
  lastError = null;
  fetchCalls = [];
}

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log("  PASS  " + name);
  } else {
    failures++;
    console.error("  FAIL  " + name + (detail ? " — " + detail : ""));
  }
}

async function expectBlocked(name, mutate, expectHint) {
  reset();
  mutate();
  await ctx.submitCreateRule();
  check(name + " → 阻止提交（未发请求）", fetchCalls.length === 0, JSON.stringify(fetchCalls));
  check(name + " → 给出中文错误", !!lastError && (!expectHint || lastError.includes(expectHint)), String(lastError));
}

(async () => {
  // ---- 1) index.html 入口按钮存在（W1） ----
  const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");
  check("index.html 含「新建 Mock 规则」按钮 #ruleCreateBtn",
    html.includes('id="ruleCreateBtn"') && html.includes("新建 Mock 规则"),
    html.slice(0, 200));

  // ---- 2) openCreateRuleForm 渲染空表单（W1/W2） ----
  ctx.openCreateRuleForm();
  const formHtml = detailBox.innerHTML;
  for (const id of ["createMethod", "createPath", "createStatus", "createHeaders", "createNote", "createBody"]) {
    check("表单含控件 #" + id, formHtml.includes('id="' + id + '"'), formHtml.slice(0, 300));
  }
  check("Method 带 datalist 常用方法建议", formHtml.includes("createMethodList") && formHtml.includes("OPTIONS"));
  check("Path 旁提示二进制接口建议从抓包创建", formHtml.includes("二进制接口") && formHtml.includes("xcp"));
  check("备注标注必填", formHtml.includes("备注（必填"));
  check("只读展示生效设备", formHtml.includes("com.example.app") && formHtml.includes("dev-1"));
  check("创建/取消按钮就位", formHtml.includes('id="createCancelBtn"') && formHtml.includes('id="createSaveBtn"'));

  // ---- 3) W3 必填/非法 path/状态码校验：均不发请求 ----
  await expectBlocked("Method 为空", () => { values.createMethod = ""; }, "Method");
  await expectBlocked("Path 为空", () => { values.createMethod = "GET"; values.createPath = ""; }, "Path");
  await expectBlocked("Path 不以 / 开头", () => { values.createPath = "api/hello"; }, "/ 开头");
  await expectBlocked("Path 含查询串 ?", () => { values.createPath = "/api/hello?x=1"; }, "查询串");
  await expectBlocked("备注为空白", () => { values.createPath = "/api/hello"; values.createNote = "  "; }, "备注");
  await expectBlocked("状态码越界", () => { values.createNote = "memo"; values.createStatus = "999"; }, "100–599");

  // ---- 4) W4：坏 JSON body 阻止提交 ----
  reset();
  values.createMethod = "GET";
  values.createPath = "/api/hello";
  values.createStatus = "200";
  values.createNote = "memo";
  values.createBody = '{"hello": ';   // 截断的 JSON
  await ctx.submitCreateRule();
  check("坏 JSON body 阻止提交", fetchCalls.length === 0, JSON.stringify(fetchCalls));
  check("坏 JSON 给出中文错误", !!lastError && lastError.includes("JSON"), String(lastError));

  // ---- 5) 合法提交：payload 正确（无 source / 默认停用 / method 大写 / headers 解析） ----
  reset();
  values.createMethod = "post";                 // 小写输入 → 大写化
  values.createPath = "/api/hello";
  values.createStatus = "201";
  values.createHeaders = "Content-Type: application/json\nX-Trace: abc\n\nBadLineNoColon";
  values.createBody = '{"hello":"world"}';
  values.createNote = "memo";
  await ctx.submitCreateRule();
  check("合法输入发出 POST", fetchCalls.length === 1 && fetchCalls[0].opts.method === "POST", JSON.stringify(fetchCalls));
  if (fetchCalls.length === 1) {
    const body = JSON.parse(fetchCalls[0].opts.body);
    check("payload 不含 source 键", !("source" in body), fetchCalls[0].opts.body);
    check("payload enabled=false（D4 默认停用）", body.enabled === false, JSON.stringify(body.enabled));
    check("method 大写化为 POST", body.method === "POST", body.method);
    check("path/statusCode/note/body 透传",
      body.path === "/api/hello" && body.response.statusCode === 201 &&
      body.note === "memo" && body.response.body === '{"hello":"world"}',
      fetchCalls[0].opts.body);
    check("headers 按行解析为对象",
      body.response.headers["Content-Type"] === "application/json" &&
      body.response.headers["X-Trace"] === "abc",
      JSON.stringify(body.response.headers));
    check("headers 空行/无冒号行被跳过", !("BadLineNoColon" in body.response.headers));
    check("请求路径绑定当前设备",
      fetchCalls[0].url === "/devices/com.example.app/dev-1/mock-rules", fetchCalls[0].url);
  }

  // ---- 6)  Step4：命中徽章两分支（W9） ----
  (() => {
    const fakeRow = () => ({
      innerHTML: "", textContent: "", className: "", dataset: {}, onclick: null,
      classList: { add() {}, remove() {}, contains: () => false },
      appendChild() {}, addEventListener() {},
      querySelector: () => ({ onclick: null }), querySelectorAll: () => [],
    });
    const rows = [];
    const prevCreate = ctx.document.createElement;
    ctx.document.createElement = () => { const el = fakeRow(); rows.push(el); return el; };
    const rulesBox = fakeRow();
    const prevGet = ctx.document.getElementById;
    ctx.document.getElementById = (id) => {
      if (id === "rulesList") return rulesBox;
      if (id === "rulesInfo" || id === "rulesConflict") return fakeRow();
      return prevGet(id);
    };
    const iso = (minusMs) => new Date(Date.now() - minusMs).toISOString();

    // 6a: 从未命中（lastUsedAt ≈ createdAt，同刻创建）
    ctx.renderRules({ rules: [{ id: "r-new", method: "GET", path: "/api/new", enabled: false,
      response: { statusCode: 200, body: "{}" }, source: null,
      createdAt: iso(0), lastUsedAt: iso(0) }], version: 1, conflicts: [] });
    const rowNew = rows.find((e) => e.innerHTML.includes("r-line2"));
    check("W9 从未命中规则显示『从未命中』", !!rowNew && rowNew.innerHTML.includes("从未命中"),
      rowNew && rowNew.innerHTML.slice(0, 300));

    // 6b: 命中过（lastUsedAt 远晚于 createdAt）
    rows.length = 0;
    ctx.renderRules({ rules: [{ id: "r-hit", method: "GET", path: "/api/hit", enabled: true,
      response: { statusCode: 200, body: "{}" }, source: null,
      createdAt: iso(3 * 3600 * 1000), lastUsedAt: iso(3 * 60 * 1000) }], version: 1, conflicts: [] });
    const rowHit = rows.find((e) => e.innerHTML.includes("r-line2"));
    check("W9 命中过规则显示最近命中相对时间", !!rowHit && rowHit.innerHTML.includes("最近命中") && rowHit.innerHTML.includes("分钟前"),
      rowHit && rowHit.innerHTML.slice(0, 300));

    ctx.document.createElement = prevCreate;
    ctx.document.getElementById = prevGet;
  })();

  if (failures) {
    console.error("\n共 " + failures + " 项失败");
    process.exit(1);
  }
  console.log("\n全部通过");
})();
