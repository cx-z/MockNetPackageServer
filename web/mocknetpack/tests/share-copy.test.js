#!/usr/bin/env node
"use strict";

// MockNetPack web: 分享页「复制」按钮回归测试（node，无浏览器依赖）。
// 运行：node tests/share-copy.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 验收口径：分享页右上角「复制」与日志详情页行为一致——
//  1) 请求分享：复制完整 HTTP 报文交换（buildRawTrafficText，与详情页同源，
//     逐例覆盖见 copy-request.test.js）；
//  2) 长连接日志分享：复制逐字段 "k: v" 文本行 + payload 原文，空字段跳过；
//     payload 优先级与页面展示一致：payloadText > payloadBase64 > payloadPreview；
//  3) bindShareCopy 按钮接线（fake DOM）：两条分支渲染出按钮，点击 →
//     copyTextToClipboard → 「已复制」+ 禁用 → 1.5s 恢复 → 失败 alert，
//     与日志详情页 #copyReqBtn 的交互循环一致。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

// ---- 加载前端脚本到同一沙箱上下文（与 copy-request.test.js 同模式：
//     utils.js 顶层引用 document 仅定义不调用，补 stub；atob 供 base64
//     字节数计算；vm 沙箱不带 URL 全局，传入宿主实现）。share.js 顶层
//     仅函数声明，函数体内的 $/fetch/alert 等在加载时不执行。 ----
const ctx = {};
ctx.document = { getElementById: () => null };
ctx.atob = (s) => Buffer.from(s, "base64").toString("binary");
ctx.URL = URL;
vm.createContext(ctx);
for (const f of ["json-lossless.js", "utils.js", "json-tree.js", "share.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}
const buildLongConnShareText = ctx.buildLongConnShareText;

let n = 0;
function check(name, actual, expected) {
  assert.strictEqual(actual, expected, name);
  n++;
}

// ---- 1. 长连接日志分享：全字段 + payloadText 原文 ----
check("长连接全字段复制",
  buildLongConnShareText({
    eventType: "recv",
    channelId: "ch-1",
    ts: "2026-10-27T07:00:00Z",
    msgId: "m-1",
    bizType: "im",
    receiver: "user-9",
    status: "ok",
    payloadText: '{"hello":"world"}',
  }),
  'eventType: recv\nchannelId: ch-1\nts: 2026-10-27T07:00:00Z\nmsgId: m-1\nbizType: im\nreceiver: user-9\nstatus: ok\npayload: {"hello":"world"}');

// ---- 2. 空字段跳过（undefined/null/"" 不产生文本行） ----
check("长连接空字段跳过",
  buildLongConnShareText({ eventType: "send", msgId: "m-2", receiver: "", ts: null, status: undefined }),
  "eventType: send\nmsgId: m-2");

// ---- 3. payload 优先级：payloadText > payloadBase64 > payloadPreview ----
const B64 = Buffer.from("bin").toString("base64");
check("长连接 payload 优先级",
  buildLongConnShareText({
    eventType: "recv",
    payloadBase64: B64,
    payloadPreview: "prev",
  }),
  "eventType: recv\npayload: " + B64);

// ---- 4. 请求分享复制文本与日志详情页同源：同一 buildRawTrafficText 产物 ----
check("请求分享复制文本与详情页同源",
  ctx.buildRawTrafficText({
    method: "GET",
    url: "https://example.com/a",
    statusCode: 200,
  }),
  "GET /a HTTP/1.1\r\nHost: example.com\r\n\r\nHTTP/1.1 200\r\n\r\n");

// ============================================================================
// 5. bindShareCopy 按钮接线回归（fake DOM，node 无浏览器依赖）：两条分享
//    分支渲染出复制按钮，点击 → 复制正确内容 → 「已复制」+ 禁用 → 1.5s
//    恢复 → 复制失败 alert 且按钮状态不变。与日志详情页 #copyReqBtn 的
//    交互循环逐项对齐。
// ============================================================================

// ---- 测试替身：fake 元素 + 全局桩。script 已加载，此处按调用时解析
//     覆盖 document/fetch/alert/navigator/setTimeout/API。 ----
function makeFakeEl() {
  const el = {
    innerHTML: "",
    textContent: "",
    disabled: false,
    onclick: null,
    dataset: {},
    classList: { add() {}, remove() {}, toggle() {} },
    querySelectorAll: () => [],
    querySelector: (sel) => {
      if (sel === "#copyShareBtn") {
        const btn = { textContent: "", disabled: false, onclick: null };
        el._copyBtn = btn;
        return btn;
      }
      return null;
    },
  };
  return el;
}
const els = {};
["authView", "appWrap", "shareView", "shareContent"].forEach((id) => { els[id] = makeFakeEl(); });
ctx.document.getElementById = (id) => els[id] || null;
ctx.API = "/api/v1";
ctx.alert = (m) => { ctx._alertMsg = m; };
ctx.navigator = { clipboard: { writeText: async (t) => { ctx._clipboard = t; } } };
ctx.setTimeout = (fn) => { ctx._restoreTimer = fn; return 1; };

const SNAP_TRAFFIC = {
  shareId: "share-traffic",
  expiresAt: "2026-11-03T00:00:00Z",
  entry: {
    method: "POST",
    url: "https://api-test.dokimoi.com/stat/action?sign=v2-abc",
    requestHeaders: { "Content-Type": ["application/json"] },
    requestBody: '{"a":1}',
    statusCode: 200,
    responseHeaders: { "Content-Type": ["application/json"] },
    responseBody: '{"ok":true}',
  },
};
const SNAP_LONG = {
  shareId: "share-longconn",
  expiresAt: "2026-11-03T00:00:00Z",
  longConnEntry: {
    eventType: "recv",
    channelId: "ch-1",
    ts: "2026-10-27T07:00:00Z",
    msgId: "m-1",
    bizType: "im",
    receiver: "user-9",
    status: "ok",
    payloadText: '{"hello":"world"}',
  },
};
ctx.fetch = async (url) => {
  const id = decodeURIComponent(String(url).split("/").pop());
  const snap = id === "share-traffic" ? SNAP_TRAFFIC : id === "share-longconn" ? SNAP_LONG : null;
  if (!snap) throw new Error("unexpected fetch " + url);
  return { status: 200, ok: true, json: async () => snap };
};

(async () => {
  // ---- 请求分享分支：按钮渲染 + 点击复制完整报文 + 反馈循环 ----
  await ctx.renderShareView("share-traffic");
  const boxT = els.shareContent;
  const btnT = boxT._copyBtn;
  check("请求分享分支渲染复制按钮", boxT.innerHTML.includes('id="copyShareBtn"'), true);
  check("复制按钮标签为「复制」（HTML 渲染）", boxT.innerHTML.includes('>复制</button>'), true);
  ctx._clipboard = null; ctx._restoreTimer = null;
  await btnT.onclick();
  check("请求分享复制含请求行", ctx._clipboard.includes("POST /stat/action?sign=v2-abc HTTP/1.1"), true);
  check("请求分享复制含状态行", ctx._clipboard.includes("HTTP/1.1 200"), true);
  check("复制成功按钮显示「已复制」", btnT.textContent, "已复制");
  check("复制成功按钮被禁用", btnT.disabled, true);
  ctx._restoreTimer();
  check("1.5s 后按钮恢复「复制」", btnT.textContent, "复制");
  check("1.5s 后按钮恢复可用", btnT.disabled, false);

  // ---- 长连接分享分支：按钮渲染 + 点击复制字段/payload + 反馈 ----
  await ctx.renderShareView("share-longconn");
  const boxL = els.shareContent;
  const btnL = boxL._copyBtn;
  check("长连接分享分支渲染复制按钮", boxL.innerHTML.includes('id="copyShareBtn"'), true);
  ctx._clipboard = null; ctx._restoreTimer = null;
  await btnL.onclick();
  check("长连接分享复制字段行", ctx._clipboard.includes("eventType: recv"), true);
  check("长连接分享复制 payload 原文", ctx._clipboard.includes('payload: {"hello":"world"}'), true);
  check("长连接复制成功按钮显示「已复制」", btnL.textContent, "已复制");
  check("长连接复制成功按钮被禁用", btnL.disabled, true);

  // ---- 复制失败分支：alert 提示，按钮状态不变（先复位到恢复后的初始态） ----
  btnL.textContent = "复制";
  btnL.disabled = false;
  ctx.copyTextToClipboard = async () => false;
  ctx._alertMsg = null;
  await btnL.onclick();
  check("复制失败弹出提示", ctx._alertMsg, "复制失败：请手动选择复制");
  check("复制失败按钮不显示「已复制」", btnL.textContent, "复制");
  check("复制失败按钮不被禁用", btnL.disabled, false);
})().then(() => {
  console.log("share-copy.test.js: " + n + " assertions passed");
}).catch((e) => { console.error(e); process.exit(1); });
