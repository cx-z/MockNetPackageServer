#!/usr/bin/env node
"use strict";

// MockNetPack web: 设备「能力声明」（capabilities）展示回归测试
// （node，无浏览器依赖）。
// 运行：node tests/device-capabilities.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 契约：SDK 注册时可选上报 capabilities{adapters,instrumented,requestBodyTee}，
// 服务端透传到 DeviceView。
// 期望：设备行展示采集来源徽标 + 插桩状态徽标；老设备（无 capabilities）
// 不渲染任何徽标，也不误报「尚未观测到插桩」。
// ============================================================================

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const els = {};
function makeEl(id) {
  const el = {
    id,
    innerHTML: "",
    textContent: "",
    dataset: {},
    className: "",
    style: { cssText: "" },
    children: [],
    classList: { add() {}, remove() {}, toggle() {} },
    appendChild(child) { el.children.push(child); },
    querySelector() { return { onclick: null, disabled: false }; },
    querySelectorAll() { return { forEach() {} }; },
    set onclick(_) {},
  };
  return el;
}

const ctx = {
  document: {
    getElementById(id) {
      if (!els[id]) els[id] = makeEl(id);
      return els[id];
    },
    createElement(tag) {
      const e = makeEl(tag);
      e.tag = tag;
      return e;
    },
    querySelectorAll() { return { forEach() {} }; },
  },
};
ctx.authUser = { role: "dev" };
vm.createContext(ctx);
for (const f of ["utils.js", "devices.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}

let n = 0;
function check(name, actual, expected) {
  assert.strictEqual(actual, expected, name);
  n++;
}

// ---- 1. 来源标签映射 ----
check("okhttp 标签", ctx.capabilityLabel("okhttp"), "OkHttp");
check("httpurlconnection 标签", ctx.capabilityLabel("httpurlconnection"), "HttpURLConnection");
check("business 标签", ctx.capabilityLabel("business"), "业务上报");
check("未知来源原样展示", ctx.capabilityLabel("cronet"), "cronet");

// ---- 2. 有 capabilities：来源徽标 + 已观测到插桩 ----
const on = ctx.capabilityChips({
  capabilities: { adapters: ["okhttp", "httpurlconnection", "business"], instrumented: true, requestBodyTee: true },
});
assert.ok(on.includes("OkHttp") && on.includes("HttpURLConnection") && on.includes("业务上报"), "三个来源徽标都在");
assert.ok(on.includes("已观测到插桩"), "插桩开启显示「已观测到插桩」");
assert.ok(!on.includes("尚未观测到插桩"), "不出现相反状态");
n += 2;

// ---- 3. 有 capabilities 但尚未观测到插桩：明确显示「尚未观测到插桩」----
const off = ctx.capabilityChips({ capabilities: { adapters: ["okhttp"], instrumented: false } });
assert.ok(off.includes("尚未观测到插桩"), "尚未观测到插桩显示「尚未观测到插桩」");
assert.ok(!off.includes("已观测到插桩"), "不误报已观测到插桩");
n += 2;

// ---- 4. 老设备（无 capabilities）：不渲染徽标 ----
check("无 capabilities 时空串", ctx.capabilityChips({ app: "a", did: "d" }), "");
check("null 输入空串", ctx.capabilityChips(null), "");

// ---- 5. 设备行整体渲染：能力徽标出现在 meta 内 ----
ctx.render([
  { app: "com.a", did: "d1", status: "idle", lastSeenAt: "2026-11-09T00:00:00Z",
    capabilities: { adapters: ["okhttp"], instrumented: true } },
  { app: "com.b", did: "d2", status: "idle", lastSeenAt: "2026-11-09T00:00:01Z" },
]);
const cards = els.list.children;
check("两张设备卡", cards.length, 2);
const html = cards.map((c) => c.children.map((x) => x.innerHTML || "").join("")).join("");
assert.ok(html.includes("cap-badge"), "有能力的设备出现 cap-badge");
assert.ok(html.includes("已观测到插桩"), "有能力的设备显示插桩状态");
n += 3;

console.log("device-capabilities.test.js: " + n + " checks passed");
