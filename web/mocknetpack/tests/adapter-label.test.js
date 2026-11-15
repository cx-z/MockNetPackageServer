#!/usr/bin/env node
"use strict";

// MockNetPack web: 流量「采集方式」（adapter）展示回归测试
// （node，无浏览器依赖）。
// 运行：node tests/adapter-label.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 契约：TrafficEntry 新增可选 adapter（okhttp / httpurlconnection / 业务自填）。
// 期望：列表行在有 adapter 时显示来源徽标（t-adapter），老记录（无 adapter）
// 不显示徽标、不占位；详情页统一显示「采集方式」，缺失时显示 "—"。
// ============================================================================

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

// ---- 极简 DOM stub：renderTraffic 只读 innerHTML / textContent / createElement ----
function makeEl(id) {
  const el = {
    id,
    innerHTML: "",
    textContent: "",
    classList: { add() {}, remove() {}, toggle() {} },
    children: [],
    appendChild(child) { el.children.push(child); },
    querySelectorAll() { return { forEach() {} }; },
    querySelector() { return { onclick: null, disabled: false }; },
  };
  return el;
}

const els = {};
const ctx = {
  document: {
    getElementById(id) {
      if (!els[id]) els[id] = makeEl(id);
      return els[id];
    },
    createElement(tag) {
      return {
        tag,
        className: "",
        innerHTML: "",
        onclick: null,
        querySelector() { return { onclick: null, disabled: false }; },
      };
    },
    querySelectorAll() { return { forEach() {} }; },
  },
};
ctx.detail = { sessionId: "s-1", _activeTraffic: null };
ctx.trafficFilter = "";
ctx.pageLog = [];
vm.createContext(ctx);
for (const f of ["utils.js", "traffic.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}

let n = 0;
function check(name, actual, expected) {
  assert.strictEqual(actual, expected, name);
  n++;
}

// ---- 1. 标签映射：已知来源转可读名，未知来源原样展示，缺失显示 "—" ----
check("okhttp 标签", ctx.adapterLabel({ adapter: "okhttp" }), "OkHttp");
check("httpurlconnection 标签", ctx.adapterLabel({ adapter: "httpurlconnection" }), "HttpURLConnection");
check("业务自填来源原样展示", ctx.adapterLabel({ adapter: "cronet" }), "cronet");
check("老记录缺省显示 —", ctx.adapterLabel({}), "—");
check("空对象显示 —", ctx.adapterLabel(null), "—");

// ---- 2. 列表行：有 adapter 显示 t-adapter 徽标；无 adapter 不显示 ----
ctx.renderTraffic([
  { id: "t1", method: "GET", path: "/api/a", url: "http://x/api/a", statusCode: 200, durationMs: 3, timestamp: "2026-11-09T00:00:00Z", adapter: "okhttp" },
  { id: "t2", method: "GET", path: "/api/b", url: "http://x/api/b", statusCode: 200, durationMs: 4, timestamp: "2026-11-09T00:00:01Z" },
]);
const rows = els.trafficList.children;
check("两行日志都已渲染", rows.length, 2);
assert.ok(rows.some((r) => r.innerHTML.includes('class="t-adapter"') && r.innerHTML.includes("OkHttp")),
  "有 adapter 的行带来源徽标");
assert.ok(rows.some((r) => !r.innerHTML.includes("t-adapter")),
  "老记录行不带来源徽标");
n += 2;

console.log("adapter-label.test.js: " + n + " checks passed");
