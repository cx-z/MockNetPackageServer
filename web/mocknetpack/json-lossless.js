"use strict";

// MockNetPack web: lossless JSON tokenizer, pretty-printer and tree parser.
// ============================================================================
// 问题：JSON.parse → JavaScript Number → JSON.stringify 会把超过
// Number.MAX_SAFE_INTEGER（2^53-1）的整数字面量舍入。真实案例：会话 ID
// 1473156146373935104 / 1576844185645694976 经 JSON.parse 后变成
// 1473156146373935000 / 1576844185645695000（JSON.stringify 按 double 最短
// 表示输出），客户端按完整 Int64 ID 精确匹配时列表缺项。
//
// 方案：本模块不把 JSON 数值解析成 JS Number——用纯分词 + 递归下降解析，
// 数字（含 19 位 ID）一律保留原始 token 文本。美化（编辑区预填/只读回退
// 展示）与 JSON 树渲染（详情/分享页）都基于该无损结构输出，保证大整数
// 逐位不变；Mock 下发的 JSON 中 ID 仍为数字字面量，绝不转成字符串。
//
// 仅用于展示与格式化；不改变"保存 = 提交 textarea 原始文本"的契约。
// ============================================================================

/** 严格 JSON 分词。失败（非法 JSON）返回 null。token: {t, text}，
 *  t ∈ {'{','}','[',']',',',':','str','num','lit'}；
 *  str/num/lit 的 text 为原文片段（含引号/原始转义）。 */
function jsonTokens(s) {
  const toks = [];
  let i = 0, n = s.length;
  const isWs = (c) => c === " " || c === "\t" || c === "\n" || c === "\r";
  while (i < n) {
    const c = s[i];
    if (isWs(c)) { i++; continue; }
    if (c === "{" || c === "}" || c === "[" || c === "]" || c === "," || c === ":") {
      toks.push({ t: c, text: c });
      i++;
      continue;
    }
    if (c === '"') {
      let j = i + 1;
      while (j < n) {
        const ch = s[j];
        if (ch === "\\") {
          if (j + 1 >= n) return null;
          const e = s[j + 1];
          if (e === '"' || e === "\\" || e === "/" || e === "b" || e === "f" ||
              e === "n" || e === "r" || e === "t") {
            j += 2;
            continue;
          }
          if (e === "u") {
            if (j + 6 > n || !/^[0-9a-fA-F]{4}$/.test(s.slice(j + 2, j + 6))) return null;
            j += 6;
            continue;
          }
          return null; // 非法转义
        }
        if (ch === '"') { j++; break; }
        if (ch.charCodeAt(0) < 0x20) return null; // JSON 字符串内不允许裸控制字符
        j++;
      }
      if (j > n || s[j - 1] !== '"') return null; // 未闭合
      toks.push({ t: "str", text: s.slice(i, j) });
      i = j;
      continue;
    }
    if (c === "-" || (c >= "0" && c <= "9")) {
      const m = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?/.exec(s.slice(i));
      if (!m || m[0] === "-") return null; // 孤立负号
      // 数字必须紧跟分隔符，防止 "-" 前缀匹配吞掉后续非法字符。
      if (!m[0] || m[0].length === 0) return null;
      // 确认匹配后下一个字符必须是分隔符/结束，否则（如 12abc）整体非法。
      const end = i + m[0].length;
      if (end < n) {
        const nc = s[end];
        if (!(isWs(nc) || nc === "{" || nc === "}" || nc === "[" || nc === "]" ||
              nc === "," || nc === ":")) return null;
      }
      toks.push({ t: "num", text: m[0] });
      i = end;
      continue;
    }
    if (s.startsWith("true", i)) { toks.push({ t: "lit", text: "true" }); i += 4; continue; }
    if (s.startsWith("false", i)) { toks.push({ t: "lit", text: "false" }); i += 5; continue; }
    if (s.startsWith("null", i)) { toks.push({ t: "lit", text: "null" }); i += 4; continue; }
    return null; // 非法字符
  }
  return toks;
}

/** 递归下降解析 token 流 → 节点树。数字节点保留原文 text，不做数值解析。
 *  节点形态：{t:'obj', kvs:[[keyRawText, node],...]} / {t:'arr', items:[node]} /
 *  {t:'str'|'num'|'lit', text}。失败返回 null。 */
function jsonParseTree(s) {
  const toks = jsonTokens(s);
  if (!toks || toks.length === 0) return null;
  let p = 0;
  const peek = () => toks[p];
  const next = () => toks[p++];
  const expectPunct = (c) => {
    const t = peek();
    if (!t || t.t !== c) return false;
    p++;
    return true;
  };

  function value() {
    const t = peek();
    if (!t) return null;
    if (t.t === "{") return objectNode();
    if (t.t === "[") return arrayNode();
    if (t.t === "str" || t.t === "num" || t.t === "lit") { p++; return { t: t.t, text: t.text }; }
    return null;
  }

  function objectNode() {
    if (!expectPunct("{")) return null;
    const kvs = [];
    if (expectPunct("}")) return { t: "obj", kvs };
    for (;;) {
      const key = peek();
      if (!key || key.t !== "str") return null;
      p++;
      if (!expectPunct(":")) return null;
      const v = value();
      if (v === null) return null;
      kvs.push([key.text, v]);
      if (expectPunct("}")) return { t: "obj", kvs };
      if (!expectPunct(",")) return null;
    }
  }

  function arrayNode() {
    if (!expectPunct("[")) return null;
    const items = [];
    if (expectPunct("]")) return { t: "arr", items };
    for (;;) {
      const v = value();
      if (v === null) return null;
      items.push(v);
      if (expectPunct("]")) return { t: "arr", items };
      if (!expectPunct(",")) return null;
    }
  }

  const root = value();
  if (root === null || p !== toks.length) return null; // 必须消费全部 token
  return root;
}

/** 无损美化：node 树 → 带 2 空格缩进的 JSON 文本（同 JSON.stringify(x, null, 2)
 *  的版式），数字/字符串字面量保留原文。返回 null 表示无法无损解析。 */
function jsonPrettyPrint(s) {
  const root = jsonParseTree(s);
  if (root === null) return null;
  const INDENT = "  ";
  const out = [];
  function render(v, depth) {
    if (v.t === "str" || v.t === "num" || v.t === "lit") { out.push(v.text); return; }
    const pad = (d) => INDENT.repeat(d);
    if (v.t === "arr") {
      if (v.items.length === 0) { out.push("[]"); return; }
      out.push("[");
      for (let i = 0; i < v.items.length; i++) {
        out.push("\n", pad(depth + 1));
        render(v.items[i], depth + 1);
        if (i < v.items.length - 1) out.push(",");
      }
      out.push("\n", pad(depth), "]");
      return;
    }
    // obj
    if (v.kvs.length === 0) { out.push("{}"); return; }
    out.push("{");
    for (let i = 0; i < v.kvs.length; i++) {
      out.push("\n", pad(depth + 1), v.kvs[i][0], ": ");
      render(v.kvs[i][1], depth + 1);
      if (i < v.kvs.length - 1) out.push(",");
    }
    out.push("\n", pad(depth), "}");
  }
  render(root, 0);
  return out.join("");
}

// 浏览器以全局函数使用；node 测试环境导出。
if (typeof module !== "undefined" && module.exports) {
  module.exports = { jsonTokens, jsonParseTree, jsonPrettyPrint };
}
