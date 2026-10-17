#!/usr/bin/env node
"use strict";

// MockNetPack web: push mock 模板生成 / SDK 摘要头剥离回归测试（node，无浏览器
// 依赖）。运行：node tests/push-template-payload.test.js（在 web/mocknetpack 目录）。
// ============================================================================
// 验收口径：
//   1) stripPayloadSummary 剥离 SDK 摘要头（"type=… msgId=… bizType=1 payload={…}"）
//      得到纯业务体；纯 JSON / 无摘要头文本原样返回，绝不误剥。
//   2) pushMockTemplatePayload 在剥离后的纯业务体上工作：say_content 结构注入
//      [mock] 前缀；普通消息（无内层 say_content）原样返回纯业务体——不再像旧版
//      那样把带摘要头的整段展示文本存成模板 payload（导致业务注入器解析失败、
//      指令永远 pending）。
//   3) pushReplaceDesc 对带摘要头的规则列表摘要显示剥离后的 JSON。
//   4) formatPayloadText 摘要形态只展示业务体（摘要头参数由
//      renderLongConnDetail 移入上方参数列表，解析内容不再带 type=… 前缀）。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

// ---- 加载前端脚本到同一沙箱上下文（longconn.js 顶层有 DOM 事件绑定：
//     $(...).onclick / addEventListener，补最小 stub；CodeMirror/apiFetch 等
//     仅在函数体内引用，不执行）。 ----
const ctx = {};
const stubEl = () => ({
  onclick: null,
  addEventListener() {},
  classList: { add() {}, remove() {}, toggle() {} },
  appendChild() {},
});
ctx.document = { getElementById: () => stubEl(), createElement: () => stubEl(), body: stubEl() };
vm.createContext(ctx);
for (const f of ["json-lossless.js", "utils.js", "longconn.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}
const stripPayloadSummary = ctx.stripPayloadSummary;
const payloadSummaryInfo = ctx.payloadSummaryInfo;
const pushMockTemplatePayload = ctx.pushMockTemplatePayload;
const pushReplaceDesc = ctx.pushReplaceDesc;
const formatPayloadText = ctx.formatPayloadText;

let n = 0;
function check(name, actual, expected) {
  assert.strictEqual(actual, expected, name);
  n++;
}

// ---- 1. stripPayloadSummary：摘要头剥离 ----
const SUMMARY = 'type=Packet_PacketType(rawValue: 7) msgId=1582693126455128064 bizType=1 payload={\n  "avatar" : 84,\n  "content" : "你好"\n}';
check("剥离摘要头得纯业务体",
  stripPayloadSummary(SUMMARY),
  '{\n  "avatar" : 84,\n  "content" : "你好"\n}');

// ---- 2. payloadSummaryInfo：识别标记 ----
const info = payloadSummaryInfo(SUMMARY);
check("摘要识别 has=true", info.has, true);
check("摘要识别 head", info.head, "type=Packet_PacketType(rawValue: 7) msgId=1582693126455128064 bizType=1 ");
check("摘要识别 body", info.body, '{\n  "avatar" : 84,\n  "content" : "你好"\n}');

// ---- 3. 纯 JSON（多行 pretty）原样，不误剥 ----
const PRETTY = '{\n  "content" : "value 含 payload= 字样"\n}';
check("多行 pretty JSON 不误剥", stripPayloadSummary(PRETTY), PRETTY);
check("多行 pretty JSON has=false", payloadSummaryInfo(PRETTY).has, false);

// ---- 4. 单行 JSON 字符串值含 payload= 不误剥 ----
const ONE_LINE = '{"a":"b payload=c"}';
check("单行 JSON 含 payload= 不误剥", stripPayloadSummary(ONE_LINE), ONE_LINE);

// ---- 5. 无摘要头普通文本 / 空串原样 ----
check("普通文本原样", stripPayloadSummary("hello world"), "hello world");
check("空串", stripPayloadSummary(""), "");
check("undefined 原样", stripPayloadSummary(undefined), undefined);

// ---- 6. pushMockTemplatePayload：普通消息（无 say_content）原样返回纯业务体 ----
check("普通消息模板=纯业务体",
  pushMockTemplatePayload('{\n  "content" : "这坛酒要沉在最下面"\n}'),
  '{\n  "content" : "这坛酒要沉在最下面"\n}');

// ---- 7. pushMockTemplatePayload：say_content 结构注入 [mock] 前缀 ----
check("say_content 注入 [mock]",
  pushMockTemplatePayload('{"content":"{\\"say_content\\": {\\"content\\": \\"你好\\"}}"}'),
  '{"content":"{\\"say_content\\": {\\"content\\": \\"[mock] 你好\\"}}"}');

// ---- 8. 一键建模板组合行为（剥离后交给模板函数）：带摘要头的 xcpush 日志
//       生成纯业务 JSON 模板（不再带 type=… payload= 前缀） ----
check("一键建模板组合=纯业务体",
  pushMockTemplatePayload(stripPayloadSummary(SUMMARY)),
  '{\n  "avatar" : 84,\n  "content" : "你好"\n}');

// ---- 9. pushReplaceDesc：规则列表摘要剥离摘要头 ----
check("列表摘要剥离摘要头",
  pushReplaceDesc({ replacePayload: SUMMARY }),
  '{"avatar":84,"content":"你好"}');
check("列表摘要普通 JSON",
  pushReplaceDesc({ replacePayload: '{"a":1}' }),
  '{"a":1}');
check("列表摘要 base64 标记",
  pushReplaceDesc({ replacePayloadBase64: "AQI=" }),
  "base64 二进制替换");

// ---- 10. formatPayloadText：摘要形态只展示业务体（摘要头由参数列表承载）----
const fmt = formatPayloadText(SUMMARY);
check("formatPayloadText json=true", fmt.json, true);
check("formatPayloadText 摘要形态只展示业务体",
  fmt.display,
  '{\n  "avatar": 84,\n  "content": "你好"\n}');
check("formatPayloadText 纯 JSON 美化",
  formatPayloadText(PRETTY).display,
  '{\n  "content": "value 含 payload= 字样"\n}');
check("formatPayloadText 非 JSON 原样",
  formatPayloadText("plain").display,
  "plain");

// ---- 11. parsePayloadHead：摘要头参数解析（值可含空格/括号，不能按空格拆）----
const parsePayloadHead = ctx.parsePayloadHead;
const head = payloadSummaryInfo(SUMMARY).head;
const hp = parsePayloadHead(head);
check("parsePayloadHead type（含括号值）", hp.type, "Packet_PacketType(rawValue: 7)");
check("parsePayloadHead msgId", hp.msgId, "1582693126455128064");
check("parsePayloadHead bizType", hp.bizType, "1");
check("parsePayloadHead 空串", Object.keys(parsePayloadHead("")).length, 0);

// ---- 12. longConnCellMeta：日志行 meta（历史/当前共用；摘要头兜底）----
const longConnCellMeta = ctx.longConnCellMeta;
// 历史/分享快照形态：顶层无 bizType/msgId，只摘要头有 → 摘要头兜底显示。
const snapLike = { channelId: "xcpush_tcp", payloadText: SUMMARY };
const snapMeta = longConnCellMeta(snapLike);
check("历史快照形态 meta 含 channelId", snapMeta[0], "xcpush_tcp");
check("历史快照形态 biz 摘要头兜底", snapMeta[1], "biz=1");
check("历史快照形态 msg 摘要头兜底", snapMeta[2], "msg=1582693126455128064");
// 当前日志形态：顶层字段优先。
const liveLike = { channelId: "xcpush_tcp", bizType: "2", msgId: "999", duplicate: true, payloadText: SUMMARY };
const liveMeta = longConnCellMeta(liveLike);
check("当前日志形态 biz 顶层优先", liveMeta[1], "biz=2");
check("当前日志形态 msg 顶层优先", liveMeta[2], "msg=999");
check("当前日志形态 dup 标记", liveMeta[3], "dup");
// 无摘要头/无字段 → 空 meta。
check("无字段 meta 空", longConnCellMeta({}).length, 0);

console.log("push-template-payload.test.js: " + n + " assertions passed");
