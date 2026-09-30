#!/usr/bin/env node
"use strict";

// MockNetPack web: 二进制 mock 规则（bodyBase64）详情/列表展示回归测试
// （node，无浏览器依赖）。
// 运行：node tests/rule-detail.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 复现（用户反馈）：平台"Mock 此请求"创建的规则，响应体为私有二进制协议
// （如 xcp AES+gzip）时，SDK 抓包产出 responseBody="[binary N bytes]" 占位 +
// responseBodyBase64（原始字节）+ responseBodyDecoded（解码 JSON，仅展示）。
// "Mock 此请求"把 body 存为占位文本、bodyBase64 保留用于精确回放，解码文本只
// 进 source 快照。旧 renderRuleDetail 只要 bodyBase64 存在就显示
// "[二进制 N 字节，base64 已用于回放]" 占位，忽略 source.responseBodyDecoded，
// 导致未编辑的规则看不到 JSON 详情；编辑保存后 bodyBase64 被清除才正常。
//
// 修复口径：详情/列表在二进制规则有解码文本时优先展示解码文本（附"回放仍为
// 原始字节"说明），无解码文本才回退占位。回放语义不变（SDK 仍按 bodyBase64
// 原始字节回放）。同时复用了 json-lossless，19 位大整数 ID 逐位保留。
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
// rules.js 顶层 "use strict"：renderRules/renderRuleDetail 会向这些全局变量赋值，
// 先声明绑定避免 strict 模式 ReferenceError（浏览器里由 app.js 声明）。
vm.runInContext(
  "var authUser = null; var detail = null; var ruleStore = null; var ruleBodyEditor = null;",
  ctx);
for (const f of ["json-lossless.js", "utils.js", "json-tree.js", "rules.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}

// ---- 测试替身 ----
const fakeEl = () => ({
  innerHTML: "",
  textContent: "",
  className: "",
  dataset: {},
  onclick: null,
  querySelector: () => ({ onclick: null }),
  querySelectorAll: () => [],
  appendChild() {},
  addEventListener() {},
  classList: { add() {}, remove() {}, contains: () => false },
});

let detailBox;
const created = [];
ctx.document.getElementById = (id) => {
  const el = fakeEl();
  if (id === "ruleDetail") detailBox = el;
  return el;
};
ctx.document.createElement = () => { const el = fakeEl(); created.push(el); return el; };
ctx.showDetailPane = () => {};
ctx.detail = { _activeTraffic: null };

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log("  PASS  " + name);
  } else {
    failures++;
    console.error("  FAIL  " + name + (detail ? " — " + detail : ""));
  }
}

const b64 = (s) => Buffer.from(s, "utf8").toString("base64");
const JSON_BODY = '{"ret":1,"data":{"session_id":1473156146373935104,"unread":3}}';

function makeRule(over) {
  const rule = {
    id: "r-binary-1",
    app: "com.example.app",
    did: "dev-1",
    method: "POST",
    path: "/api/session",
    enabled: false,
    note: "",
    response: {
      statusCode: 200,
      headers: { "Content-Type": "application/xcp", "x-xc-proto-res": "k1" },
      body: "[binary 56 bytes]",
      bodyBase64: b64(JSON_BODY),
    },
    source: {
      method: "POST",
      path: "/api/session",
      url: "https://api.example.com/api/session",
      statusCode: 200,
      responseHeaders: { "Content-Type": ["application/xcp"] },
      responseBody: "[binary 56 bytes]",
      responseBodyBase64: b64(JSON_BODY),
      responseBodyDecoded: JSON_BODY,
      capturedAt: "2026-09-30T07:32:53Z",
    },
    createdAt: "2026-09-30T07:32:53Z",
    updatedAt: "2026-09-30T07:32:53Z",
    lastUsedAt: "2026-09-30T07:32:53Z",
  };
  return Object.assign(rule, over || {});
}

// ---- 1) 二进制规则 + 有解码文本 → 详情展示 JSON 树（含 19 位 ID 原样），不显示占位 ----
(() => {
  ctx.renderRuleDetail(makeRule());
  const html = detailBox.innerHTML;
  check("二进制规则详情展示解码 JSON（JSON 树 + 键名）",
    html.includes("jv-tree") && html.includes("&quot;ret&quot;") && html.includes("&quot;session_id&quot;"),
    html.slice(0, 400));
  check("19 位大整数 ID 逐位保留（json-lossless 未回归）",
    html.includes("1473156146373935104") && !html.includes("1473156146373935000"),
    html.slice(0, 400));
  check("不再显示二进制占位",
    !html.includes("base64 已用于回放") && !html.includes("[二进制 "),
    html.slice(0, 400));
  check("附『回放仍为原始字节』说明",
    html.includes("回放仍为原始字节"),
    html.slice(0, 400));
})();

// ---- 2) 二进制规则 + 无解码文本 → 回退二进制占位 ----
(() => {
  const r = makeRule();
  delete r.source.responseBodyDecoded;
  ctx.renderRuleDetail(r);
  const html = detailBox.innerHTML;
  check("无解码文本时回退二进制占位",
    html.includes("base64 已用于回放") && html.includes("[二进制 "),
    html.slice(0, 400));
})();

// ---- 3) 文本规则（无 bodyBase64）→ 正常 JSON 树，无说明 ----
(() => {
  const r = makeRule({
    response: {
      statusCode: 200,
      headers: { "Content-Type": "application/json" },
      body: JSON_BODY,
    },
    source: null,
  });
  ctx.renderRuleDetail(r);
  const html = detailBox.innerHTML;
  check("文本规则详情正常展示 JSON 树",
    html.includes("jv-tree") && html.includes("&quot;ret&quot;") && html.includes("1473156146373935104"),
    html.slice(0, 400));
  check("文本规则不出现二进制说明/占位",
    !html.includes("回放仍为原始字节") && !html.includes("[二进制 "),
    html.slice(0, 400));
})();

// ---- 4) 规则列表行：二进制规则显示解码片段，不再出现冗余双重占位 ----
(() => {
  created.length = 0;
  ctx.renderRules({ rules: [makeRule()], version: 1, conflicts: [] });
  const row = created.find((e) => e.innerHTML && e.innerHTML.includes("r-line2"));
  check("列表行找到规则项", !!row, "renderRules 未生成规则行");
  if (row) {
    check("列表行二进制规则显示解码文本片段",
      row.innerHTML.includes("&quot;session_id&quot;") && row.innerHTML.includes("1473156146373935104"),
      row.innerHTML.slice(0, 400));
    check("列表行不再显示冗余『[二进制 N 字节]』标签",
      !row.innerHTML.includes("[二进制 "),
      row.innerHTML.slice(0, 400));
  }
})();

// ---- 5) 规则列表行：文本规则仍显示正文摘要（无回归） ----
(() => {
  created.length = 0;
  ctx.renderRules({ rules: [makeRule({
    response: { statusCode: 200, headers: { "Content-Type": "application/json" }, body: JSON_BODY },
    source: null,
  })], version: 1, conflicts: [] });
  const row = created.find((e) => e.innerHTML && e.innerHTML.includes("r-line2"));
  check("列表行文本规则显示正文摘要",
    !!row && row.innerHTML.includes("&quot;session_id&quot;"),
    row ? row.innerHTML.slice(0, 400) : "未生成规则行");
})();

if (failures) {
  console.error("\n共 " + failures + " 项失败");
  process.exit(1);
}
console.log("\n全部通过");
