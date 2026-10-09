#!/usr/bin/env node
"use strict";

// MockNetPack web: 「复制」按钮的完整报文交换重建回归测试（node，无浏览器
// 依赖）。运行：node tests/copy-request.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 验收口径：点击详情面板顶部「复制」后，剪贴板内容是完整 HTTP 报文交换——
// 请求报文（请求行 + Host 与全部请求头 + 空行 + 请求体）、空行、响应报文
// （状态行 + 响应头 + 空行 + 响应体）；JSON 体经 formatBody 无损美化（2 空格
// 缩进，19 位大整数逐位保留）；二进制体附 [binary N bytes — base64: …] 原样
// 保留；解码文本优先；请求失败无回包时只复制请求；已带 Host 头不重复添加。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

// ---- 加载前端脚本到同一沙箱上下文（utils.js 顶层引用了 document，仅定义
//     函数不调用，补 stub 以防未来变化；atob 供 base64 字节数计算；
//     vm 沙箱不带 URL 全局，传入宿主实现）。 ----
const ctx = {};
ctx.document = { getElementById: () => null };
ctx.atob = (s) => Buffer.from(s, "base64").toString("binary");
ctx.URL = URL;
vm.createContext(ctx);
for (const f of ["json-lossless.js", "utils.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}
const buildRawTrafficText = ctx.buildRawTrafficText;

let n = 0;
function check(name, actual, expected) {
  assert.strictEqual(actual, expected, name);
  n++;
}

// ---- 1. 完整报文交换：请求 + 响应；JSON 体美化排版（含 19 位大整数逐位保留）；
//      解码文本优先；无 Host 头 → 推导；多值头 ", " 合并 ----
const BIG_ID = "1473156146373935104";
check("请求+响应完整交换（JSON 美化）",
  buildRawTrafficText({
    method: "POST",
    url: "https://gateway-gray.character.xunlei.com/chat/route?sign=v2-fe602f43940b85fbc6563cc264224ff0",
    requestHeaders: { "Content-Type": ["application/json"], "X-Custom": ["a", "b"] },
    requestBody: '{"q":"hi","id":' + BIG_ID + '}',
    statusCode: 200,
    responseHeaders: { "Cache-Control": ["no-cache"], "x-xc-proto-res": ["lion-1"] },
    responseBodyBase64: Buffer.from("raw").toString("base64"),
    responseBodyDecoded: '{"connect":"bare"}',
  }),
  "POST /chat/route?sign=v2-fe602f43940b85fbc6563cc264224ff0 HTTP/1.1\r\n" +
  "Host: gateway-gray.character.xunlei.com\r\n" +
  "Content-Type: application/json\r\n" +
  "X-Custom: a, b\r\n" +
  "\r\n" +
  "{\n  \"q\": \"hi\",\n  \"id\": " + BIG_ID + "\n}" + "\r\n\r\n" +
  "HTTP/1.1 200\r\n" +
  "Cache-Control: no-cache\r\n" +
  "x-xc-proto-res: lion-1\r\n" +
  "\r\n" +
  "{\n  \"connect\": \"bare\"\n}");

// ---- 2. 请求体为空 + 有响应：请求以空行结尾，两报文间恰好一个空行 ----
check("空请求体两报文间一个空行",
  buildRawTrafficText({
    method: "GET",
    url: "https://example.com/a?x=1",
    requestHeaders: { host: ["example.com"], "Accept": ["*/*"] },
    statusCode: 204,
  }),
  "GET /a?x=1 HTTP/1.1\r\nhost: example.com\r\nAccept: */*\r\n\r\n" +
  "HTTP/1.1 204\r\n\r\n");

// ---- 3. 响应体为二进制（base64 无解码文本）→ 标注字节数并附 base64，原样 ----
check("响应二进制附 base64",
  buildRawTrafficText({
    method: "POST",
    url: "https://example.com/upload",
    requestHeaders: { "Content-Type": ["application/octet-stream"] },
    statusCode: 200,
    responseHeaders: { "Content-Type": ["application/octet-stream"] },
    responseBodyBase64: Buffer.from([0, 1, 2, 255]).toString("base64"),
  }),
  "POST /upload HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/octet-stream\r\n\r\n" +
  "HTTP/1.1 200\r\nContent-Type: application/octet-stream\r\n\r\n" +
  "[binary 4 bytes — base64: AAEC/w==]");

// ---- 4. 纯文本请求体：非 JSON 原样保留，不加缩进 ----
check("纯文本 body 原样保留",
  buildRawTrafficText({
    method: "POST",
    url: "https://example.com/x",
    requestBody: "plain text",
    statusCode: 200,
  }),
  "POST /x HTTP/1.1\r\nHost: example.com\r\n\r\nplain text\r\n\r\n" +
  "HTTP/1.1 200\r\n\r\n");

// ---- 5. 请求失败（statusCode 为 null）→ 只复制请求，无响应段 ----
check("失败请求只复制请求",
  buildRawTrafficText({
    method: "GET",
    url: "https://example.com/fail",
    error: "connection refused",
  }),
  "GET /fail HTTP/1.1\r\nHost: example.com\r\n\r\n");

// ---- 6. URL 无法解析（相对地址）→ 请求行回退原文，不推导 Host ----
check("相对 URL 回退",
  buildRawTrafficText({ method: "GET", url: "/relative/path" }),
  "GET /relative/path HTTP/1.1\r\n\r\n");

console.log("copy-request.test.js: " + n + " assertions passed");
