#!/usr/bin/env node
"use strict";

// MockNetPack web: M15 大图预览（静态大图卡片 / 截断徽标 / 灯箱入口）回归测试
// （node，无浏览器依赖）。
// 运行：node tests/image-preview.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 验收口径：
// 1) responseStaticKind="image" → 原 URL 卡片（占位「点击加载」+ 新页签打开 +
//    复制链接），不写 <img src>（不自动向任意内网地址拉取大图）；
// 2) 仍在内联 base64 路径的截断图（上传失败分支）→ 徽标
//    「仅前 1 MiB / 完整 7.4 MB · 本次上传未成功，源站无可回读内容」；
// 3) 完整图片不出现徽标；
// 4) bodyHtml 按 staticKind 分发：大图 → 卡片，普通图 → 内联预览。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

const ctx = {};
ctx.atob = (s) => Buffer.from(s, "base64").toString("binary");
ctx.document = {
  getElementById: () => null,
  querySelectorAll: () => ({ forEach() {} }),
  createElement: () => null,
  addEventListener: () => {},
  body: { appendChild: () => {} },
};
ctx.URL = URL;
vm.createContext(ctx);
for (const f of ["json-lossless.js", "utils.js", "json-tree.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}

let n = 0;
function check(name, cond, detail) {
  assert.ok(cond, name + (detail ? "\n  " + detail : ""));
  n++;
}

// PNG 魔数前缀的 1 MiB base64 样本（1398104 字符 ≈ 1 MiB 字节）。
const BIG_B64 = "iVBORw0KGgo" + "A".repeat(1398096);
const TINY_B64 = "iVBORw0KGgoAAAA";

// ---- 1. 大图卡片：占位 + 点击加载 + 新页签 + 复制链接，默认不发起图片请求 ----
const card = ctx.staticImageHtml({
  staticKind: "image",
  url: "https://cdn.example.com/covers/big-banner.jpg",
  contentType: "image/jpeg",
  contentLength: "7441522",
});
check("card returned", card != null);
check("card url dataset", card.includes('data-media-url="https://cdn.example.com/covers/big-banner.jpg"'), card);
check("card placeholder", card.includes("点击加载"), card);
check("card load button", card.includes("data-load-image"), card);
check("card new tab link", card.includes('target="_blank"'), card);
check("card copy button", card.includes("data-copy-url"), card);
check("card file name", card.includes("big-banner.jpg"), card);
check("card size", card.includes("7.1 MB"), card);
check("card does not auto-load image", !card.includes("<img "), card);

// 非 image / 缺 url → 不渲染卡片（回退文本路径）。
check("no kind → null", ctx.staticResourceHtml({ staticKind: "", url: "https://x/a.jpg" }) === null);
check("no url → null", ctx.staticResourceHtml({ staticKind: "image" }) === null);

// ---- 2. 截断徽标：上传失败（保留 1 MiB 样本 + 完整大小占位） ----
const truncated = ctx.imagePreviewHtml("[binary 7441522 bytes]", {
  base64: BIG_B64,
  contentType: "image/jpeg",
  contentLength: "7441522",
  isRequest: true,
});
check("truncated preview rendered", truncated != null && truncated.includes("img-preview"), truncated);
check(
  "truncated badge text",
  truncated.includes("仅前 1 MiB / 完整 7.4 MB · 本次上传未成功，源站无可回读内容"),
  truncated);

// 从 Content-Length 兜底判断完整大小（无占位文本时）。
const truncatedCL = ctx.imagePreviewHtml("", {
  base64: BIG_B64,
  contentType: "image/png",
  contentLength: "7441522",
  isRequest: true,
});
check("truncated via content-length", truncatedCL.includes("仅前 1 MiB / 完整 7.4 MB"), truncatedCL);

// ---- 3. 完整图片：无徽标 ----
// TINY_B64 长 15 → 解码 11 字节；声明 11 字节即「无截断」。
const full = ctx.imagePreviewHtml("[binary 11 bytes]", {
  base64: TINY_B64,
  contentType: "image/png",
  contentLength: "11",
});
check("full image rendered", full != null && full.includes("img-preview"), full);
check("full image has no badge", !full.includes("img-trunc-badge"), full);
check("full image is zoomable", full.includes("data-open-lightbox"), full);

// 声明大小缺失时不做臆断（不显示徽标）。
const unknown = ctx.imagePreviewHtml("", { base64: BIG_B64, contentType: "image/png" });
check("unknown size → no badge", unknown != null && !unknown.includes("img-trunc-badge"), unknown);

// ---- 4. bodyHtml 分发 ----
const bigCard = ctx.bodyHtml("[image/jpeg 7.1 MB → 原 URL]", {
  staticKind: "image",
  url: "https://cdn.example.com/a.jpg",
  contentType: "image/jpeg",
  contentLength: "7441522",
});
check("bodyHtml routes big image to card", bigCard.includes("img-media-card"), bigCard);
check("bodyHtml card keeps url", bigCard.includes("https://cdn.example.com/a.jpg"), bigCard);

const inline = ctx.bodyHtml("[binary 7441522 bytes]", {
  base64: BIG_B64,
  contentType: "image/jpeg",
  contentLength: "7441522",
  isRequest: true,
});
check("bodyHtml routes inline image to preview+badge", inline.includes("img-trunc-badge"), inline);

// ---- 5. 灯箱入口存在（DOM 行为在浏览器里验证，node 侧只查函数可选） ----
check("openImageLightbox defined", typeof ctx.openImageLightbox === "function");
check("closeImageLightbox defined", typeof ctx.closeImageLightbox === "function");

console.log("image-preview.test.js: " + n + " checks passed");
