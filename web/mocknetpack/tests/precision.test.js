#!/usr/bin/env node
"use strict";

// MockNetPack web: 19 位整数 ID 精度回归测试（node，无浏览器依赖）。
// 运行：node tests/precision.test.js（在 web/mocknetpack 目录下）。
// ============================================================================
// 复现：JSON.parse → JavaScript Number → JSON.stringify 会把
// 1473156146373935104 / 1576844185645694976 舍入为 1473156146373935000 /
// 1576844185645695000（超过 Number.MAX_SAFE_INTEGER）。旧版 formatBody 在
// 编辑表单预填时走了该有损往返，用户保存后 ID 被写坏，客户端按完整 Int64
// 匹配导致列表缺项。
//
// 本测试按验收口径：以含真实 ID 的响应为输入，只把某会话 unread 改为 3，
// 校验最终保存/展示文本中 unread=3 且其余大整数 ID 与输入逐位一致；
// 覆盖编辑预填（formatBody）、保存文本校验、保存后重新加载（再次美化）、
// JSON 树渲染（详情/分享页展示）各环节。
// ============================================================================

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

// ---- 加载前端脚本到同一沙箱上下文（utils.js 顶层引用了 document，仅定义
//     箭头函数不调用，补一个 stub 以防未来变化）。 ----
const ctx = {};
ctx.document = { getElementById: () => null };
vm.createContext(ctx);
for (const f of ["json-lossless.js", "utils.js", "json-tree.js"]) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });
}
const formatBody = ctx.formatBody;
const jsonTreeHtml = ctx.jsonTreeHtml;

// ---- 验收输入：/chat/sessions_v2 含真实 19 位 ID 的响应 ----
const STORY_ID = "1473156146373935104";
const CHAT_ID = "1576844185645694976";
const ROUNDED_STORY = "1473156146373935000";
const ROUNDED_CHAT = "1576844185645695000";

const INPUT = `{"ret":1,"data":{"sessions":[{"session_id":1473156146373935104,"bind_session_id":1576844185645694976,"unread":0}],"active_session_list":[1576844185645694976],"wangliao_active_list":[1576844185645694976]}}`;

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log("  PASS  " + name);
  } else {
    failures++;
    console.error("  FAIL  " + name + (detail ? " — " + detail : ""));
  }
}

// ---- 1) 旧行为自证（证明本测试确实覆盖根因；新代码不再有这条路径） ----
(() => {
  const oldPretty = JSON.stringify(JSON.parse(INPUT), null, 2);
  check("旧实现确实会把 ID 舍入（测试有效性自证）",
    oldPretty.includes(ROUNDED_STORY) && oldPretty.includes(ROUNDED_CHAT) &&
    !oldPretty.includes(STORY_ID) && !oldPretty.includes(CHAT_ID),
    "旧 formatBody 产生 " + ROUNDED_STORY);
})();

// ---- 2) 编辑预填：formatBody 美化后大整数 ID 逐位保留 ----
const prefill = formatBody(INPUT);
check("编辑预填美化保留故事会话 ID 原文", prefill.includes(STORY_ID), prefill);
check("编辑预填美化保留网聊会话 ID 原文", prefill.includes(CHAT_ID));
check("编辑预填不出现舍入形态", !prefill.includes(ROUNDED_STORY) && !prefill.includes(ROUNDED_CHAT));
check("编辑预填是合法 JSON", (() => { try { JSON.parse(prefill); return true; } catch { return false; } })());

// ---- 3) 用户只改 unread 0→3（textarea 里把 "unread": 0 改成 "unread": 3）----
const savedText = prefill.replace('"unread": 0', '"unread": 3');
check("用户修改 unread=3 已写入保存文本", savedText.includes('"unread": 3'));
check("保存文本通过 JSON 合法性校验（saveRuleEdit 同款）",
  (() => { try { JSON.parse(savedText); return true; } catch { return false; } })());

// ---- 4) 保存后重新加载：再次美化（等价于刷新后重新打开编辑/详情）----
const reloaded = formatBody(savedText);
check("重新加载后 unread 仍为 3", reloaded.includes('"unread": 3'));
check("重新加载后故事会话 ID 逐位一致", reloaded.includes(STORY_ID));
check("重新加载后网聊会话 ID 逐位一致", reloaded.includes(CHAT_ID));
check("重新加载后不出现舍入形态", !reloaded.includes(ROUNDED_STORY) && !reloaded.includes(ROUNDED_CHAT));

// ---- 5) JSON 树渲染（详情/分享页）：数字字面量逐位展示 ----
const tree = jsonTreeHtml(savedText);
check("JSON 树渲染保留故事会话 ID 原文", tree && tree.includes(STORY_ID));
check("JSON 树渲染保留网聊会话 ID 原文", tree && tree.includes(CHAT_ID));
check("JSON 树不出现舍入形态", tree && !tree.includes(ROUNDED_STORY) && !tree.includes(ROUNDED_CHAT));
check("JSON 树以 jv-num 渲染数字", tree && tree.includes('class="jv-num"'));

// ---- 6) 边界：非法/非 JSON 文本原样回退，行为与旧版一致 ----
check("非法 JSON 原样回退", formatBody('{"a":1,}') === '{"a":1,}');
check("非 JSON 文本原样回退", formatBody("plain text") === "plain text");
check("空文本回退", formatBody("") === "");

console.log(failures === 0 ? "\nALL JS PRECISION TESTS PASSED" : "\n" + failures + " JS TEST(S) FAILED");
process.exit(failures === 0 ? 0 : 1);
