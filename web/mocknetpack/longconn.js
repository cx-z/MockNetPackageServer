"use strict";

// MockNetPack web: 长连接日志页签（业务级长连接日志）。
// 独立页签承载：与 HTTP 请求流分页签、互不混入；独立轮询 since 游标。
// 数据通道：GET /api/v1/sessions/{sid}/longconn（服务端独立额度/独立存档）。
// ============================================================================

const LONGCONN_PAGE = 100;   // 单次拉取页数（同 traffic 页大小）
const LONGCONN_CAP = 300;    // 页面累积上限（防长时间抓包 DOM 膨胀）

let longConnTimer = null;
let longConnLog = [];        // 页面累积的长连接记录（按 id 去重，跨会话保留同 pageLog）
let longConnFilter = "";

/** 页签切换：HTTP 请求流 / 长连接。左列面板 + 右列详情同步显隐。 */
function showLogPane(kind) {
  const isLC = kind === "longconn";
  $("tabTraffic").classList.toggle("active", !isLC);
  $("tabLongConn").classList.toggle("active", isLC);
  // 左列：长连接页签只显示长连接面板（Mock 规则/请求流均为 HTTP 域，长连接不支持）
  $("rulesPane").classList.toggle("hidden", isLC);
  $("trafficPane").classList.toggle("hidden", isLC);
  $("longconnPane").classList.toggle("hidden", !isLC);
  // 右列：切页签即复位详情归属——本域日志详情默认显示，ruleDetail 先隐藏，
  // 由各自 render 函数（renderRuleDetail / renderPushRuleDetail）按需点亮，
  // 避免推送模板详情残留到请求流页签造成 ruleDetail+trafficDetail 双面板堆叠（N5）。
  $("longconnDetail").classList.toggle("hidden", !isLC);
  $("trafficDetail").classList.toggle("hidden", isLC);
  $("ruleDetail").classList.add("hidden");
  if (isLC) {
    detail._activePushRule = null;
    detail._activeLongConn = null;   // N6：行高亮下轮轮询重渲染自然清除，避免高亮残留而右栏是占位
    $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
  } else {
    detail._activeRule = null;
    detail._activeTraffic = null;    // N6：同上，请求流侧行为一致
    $("trafficDetail").innerHTML = '<div class="empty">点击左侧请求或规则查看详情。</div>';
  }
}

/** 绑定会话到长连接轮询（在 traffic.bindSession 后调用）。capturing → 2s 轮询；
 *  无会话 → 停止轮询，已展示记录保留不清空（同请求流语义）。 */
function bindLongConnSession(session) {
  stopLongConnPoll();
  if (!session || session.status !== "capturing") return;
  longConnTimer = setInterval(pollLongConn, TRAFFIC_POLL_MS);
  pollLongConn();
}

function stopLongConnPoll() {
  if (longConnTimer) { clearInterval(longConnTimer); longConnTimer = null; }
}

/** 轮询：先拉 total，再拉最后一页，按 id 合并进 longConnLog（同请求流模式）。 */
async function pollLongConn() {
  if (!detail || !detail.sessionId) return;
  const sid = detail.sessionId;
  try {
    const meta = await apiFetch("/sessions/" + encodeURIComponent(sid) +
      "/longconn?limit=1").then(r => r.json());
    if (!meta || typeof meta.total !== "number") return;
    const offset = Math.max(0, meta.total - LONGCONN_PAGE);
    const data = await apiFetch("/sessions/" + encodeURIComponent(sid) +
      "/longconn?limit=" + LONGCONN_PAGE + "&offset=" + offset).then(r => r.json());
    longConnLog = mergeLongConn(longConnLog, data.entries || []);
    renderLongConn();
  } catch (e) {
    $("longconnInfo").textContent = "长连接拉取失败：" + e.message;
  }
}

/** 按 id 去重合并，保留最近 LONGCONN_CAP 条（服务端 seq 升序到达序）。 */
function mergeLongConn(base, fresh) {
  const seen = new Map();
  for (const e of base) seen.set(e.id, e);
  for (const e of fresh) {
    if (!seen.has(e.id)) seen.set(e.id, e);
  }
  // 复活抑制：清空时刻之前的旧条目一律丢弃（同 traffic._logClearedAt 语义）。
  const cutMs = detail && detail._longConnClearedAt ? new Date(detail._longConnClearedAt).getTime() : 0;
  // 单条删除抑制：用户点 ✕ 删除的条目 id 永久不复活（直到切设备/刷新页面）。
  const deletedIds = (detail && detail._longConnDeletedIds) || new Set();
  let arr = Array.from(seen.values());
  if (cutMs > 0) {
    arr = arr.filter((e) => new Date(e.ts || "").getTime() > cutMs);
  }
  arr = arr.filter((e) => !deletedIds.has(e.id));
  return arr.length > LONGCONN_CAP ? arr.slice(arr.length - LONGCONN_CAP) : arr;
}

/** 真假 push 徽标：mocked=true 为模拟推送（fake），否则真实推送（real）。 */
function longConnFakeBadge(e) {
  return e.mocked
    ? '<span class="lc-type lc-fake">fake</span>'
    : '<span class="lc-type lc-real">real</span>';
}

/** 日志行 meta（channelId · biz= · msg= · dup/empty）：记录顶层字段优先，
 *  SDK 摘要头（type=… msgId=… bizType=… payload=…）兜底——历史/分享快照
 *  顶层无 bizType/msgId，只有摘要头里有。
 *  当前日志与历史日志列表共用，保证两处展示一致。 */
function longConnCellMeta(e) {
  const meta = [];
  if (e.channelId) meta.push(e.channelId);
  const headParams = parsePayloadHead(payloadSummaryInfo(e.payloadText || "").head);
  if (e.bizType || headParams.bizType) meta.push("biz=" + (e.bizType || headParams.bizType));
  if (e.msgId) meta.push("msg=" + e.msgId);
  else if (headParams.msgId) meta.push("msg=" + headParams.msgId);
  if (e.duplicate) meta.push("dup");
  if (e.empty) meta.push("empty");
  return meta;
}

/** 渲染：最新 snapshot 卡片 + 倒序列表。 */
function renderLongConn() {
  if (!detail) return;
  // 最新 snapshot（seq 最大的 snapshot 事件）→ 状态卡片
  const snapshots = longConnLog.filter((r) => r.eventType === "snapshot" && r.snapshot);
  renderSnapshotCard(snapshots[snapshots.length - 1]);
  const q = longConnFilter.trim().toLowerCase();
  // 列表只显示业务包（packet）；snapshot 进状态卡，connect/disconnect/heartbeat/
  // timeout 等生命周期事件仅驱动状态卡、不占列表。
  let entries = longConnLog.filter((e) => e.eventType === "packet");
  const total = entries.length;
  if (q) {
    entries = entries.filter((e) => {
      // 真假过滤：仅完整词 fake/real 触发；f/r/fa 等短串走普通文本匹配，
      // 避免劫持 bizType/msgId 搜索。
      if (q === "fake") return !!e.mocked;
      if (q === "real") return !e.mocked;
      return (e.eventType || "").toLowerCase().includes(q) ||
        (e.bizType || "").toLowerCase().includes(q) ||
        (e.msgId || "").toLowerCase().includes(q) ||
        (e.channelId || "").toLowerCase().includes(q);
    });
  }
  $("longconnInfo").textContent = "共 " + total + " 条" + (q ? "（过滤出 " + entries.length + " 条）" : "");

  const box = $("longconnList");
  if (!entries.length) {
    box.innerHTML = '<div class="empty">暂无长连接日志。设备长连接事件会实时出现在这里。</div>';
    return;
  }
  // 倒序：最新在上。按 ts（RFC3339 字符串）排序，不依赖 seq（跨轮询累积后 seq 可能乱）。
  entries = entries.slice().sort((a, b) => (b.ts || "").localeCompare(a.ts || ""));
  box.innerHTML = "";
  for (const e of entries) {
    const el = document.createElement("div");
    el.className = "lc-row" + (detail._activeLongConn === e.id ? " active" : "");
    const meta = longConnCellMeta(e);
    el.innerHTML =
      longConnFakeBadge(e) +
      '<span class="lc-meta">' + esc(meta.join(" · ")) + "</span>" +
      '<span class="t-time">' + clockTime(e.ts) + "</span>" +
      '<button class="t-del" data-lc-del title="删除该条日志（仅本页，不影响服务端 48h 保留）">✕</button>';
    el.querySelector("[data-lc-del]").onclick = (ev) => {
      ev.stopPropagation();
      // 记入删除抑制名单：后续轮询合并时不再复活该条目。
      (detail._longConnDeletedIds = detail._longConnDeletedIds || new Set()).add(e.id);
      const idx = longConnLog.indexOf(e);
      if (idx >= 0) longConnLog.splice(idx, 1);
      if (detail._activeLongConn === e.id) {
        detail._activeLongConn = null;
        $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
      }
      renderLongConn();
    };
    el.onclick = () => {
      detail._activeLongConn = e.id;
      renderLongConnDetail(e);
      box.querySelectorAll(".lc-row").forEach((r) => r.classList.toggle("active", r === el));
    };
    box.appendChild(el);
  }
}

/** 最新连接状态卡片。 */
function renderSnapshotCard(s) {
  const box = $("longconnSnapshot");
  if (!s) { box.classList.add("hidden"); box.innerHTML = ""; return; }
  const snap = s.snapshot || {};
  const row = (k, v) => '<div class="d-kv"><span class="d-k">' + esc(k) + '</span><span class="d-v">' + esc(v) + "</span></div>";
  box.innerHTML =
    '<div class="d-title">连接状态（最新 snapshot · ' + clockTime(s.ts) + "）</div>" +
    row("state", snap.state || "—") +
    row("socket", snap.isSocketConnected ? "已连接" : "未连接") +
    (snap.routerHost ? row("router", snap.routerHost + (snap.routerPort ? ":" + snap.routerPort : "")) : "") +
    (snap.clientId ? row("clientId", snap.clientId) : "") +
    (snap.accountMid ? row("mid", snap.accountMid) : "") +
    (snap.heartbeatInterval ? row("心跳", snap.heartbeatInterval + "s") : "") +
    (snap.receivedMessageCount ? row("累计收包", snap.receivedMessageCount) : "") +
    (snap.receiveServiceCount ? row("累计分发", snap.receiveServiceCount) : "") +
    (snap.lastError ? row("最近错误", snap.lastError) : "");
  box.classList.remove("hidden");
}

/** 解析 payload 文本中的 SDK 摘要头。
 *  SDK 长连接采集上报的是「摘要头 + payload= + 业务体」拼接的展示文本
 *  （LongConnHookEngine.decodePayload：summary + " payload=" + body，如
 *  "type=Packet_PacketType(rawValue: 7) msgId=123 bizType=1 payload={…}"）。
 *  返回 { has, head, body }：has=true 时 head 为摘要头（不含 payload=），body
 *  为 payload= 之后的业务体；非摘要形态 has=false、body=原文。判定供
 *  formatPayloadText / pushReplaceDesc / 一键建模板共用，保证一致不误剥。 */
function payloadSummaryInfo(text) {
  const info = { has: false, head: "", body: text };
  if (!text) return info;
  const idx = text.indexOf("payload=");
  if (idx <= 0) return info;
  const head = text.slice(0, idx);
  // 摘要头特征：单行、纯 key=value 文本（不含 JSON 结构字符）。
  // 纯 JSON（多行 pretty，或单行字符串值里恰好含 "payload="）不会命中，原样保留。
  if (head.indexOf("\n") >= 0) return info;
  if (/[{\[\\'"]/.test(head)) return info;
  info.has = true;
  info.head = head;
  info.body = text.slice(idx + "payload=".length).replace(/^\s+/, "");
  return info;
}

/** 剥离 SDK 摘要头，返回纯业务体（非摘要形态原样返回）。 */
function stripPayloadSummary(text) {
  return payloadSummaryInfo(text).body;
}

/** 解析摘要头（"type=… msgId=… bizType=…" 单行 key=value，值可含空格/括号）
 *  为 {key: value}。按已知 key= 位置切分（不用空格 split，避免
 *  "Packet_PacketType(rawValue: 7)" 这类值被拆碎）。 */
function parsePayloadHead(head) {
  const out = {};
  if (!head) return out;
  const re = /([A-Za-z_][A-Za-z0-9_]*)=/g;
  const keys = [];
  let m;
  while ((m = re.exec(head)) !== null) keys.push({ key: m[1], idx: m.index });
  for (let i = 0; i < keys.length; i++) {
    const start = keys[i].idx + keys[i].key.length + 1; // 值起点
    const end = i + 1 < keys.length ? keys[i + 1].idx : head.length;
    out[keys[i].key] = head.slice(start, end).trim();
  }
  return out;
}

/** payload 文本美化：SDK 摘要形态（"type=... msgId=... payload={json}"）或纯 JSON
 *  都尝试美化为缩进多行；解析失败原样返回。返回 { display, json }——json=true
 *  时给 pre 加 lc-payload-json 类（语义标记，全展开无折叠）。
 *  摘要形态只展示业务体（body），摘要头参数由
 *  renderLongConnDetail 移入上方参数列表。注意：64 位 msgid/session_id 等
 *  数字字段在 JSON.parse 时已丢精度（仅展示美化，不回写服务端/不做模板
 *  替换，无副作用）。 */
function formatPayloadText(text) {
  if (!text) return { display: text, json: false };
  const body = stripPayloadSummary(text);
  try {
    return { display: JSON.stringify(JSON.parse(body), null, 2), json: true };
  } catch (e) {
    // 原文本 JSON 解析失败：剥离过摘要头则展示剥离后的 body（如非 JSON 业务体），
    // 否则原样（防误改纯文本/非 JSON 形态）。
    return body !== text ? { display: body, json: false } : { display: text, json: false };
  }
}

/** 详情面板：Overview 字段 + snapshot + payload（Raw，esc 防 XSS）。
 *  target 默认为主详情区 longconnDetail；history.js 可传 historyDetail 浮层。 */
function renderLongConnDetail(e, target) {
  detail._activeTraffic = null;
  // 点日志时退出模板详情态（ruleDetail 隐藏、模板行高亮清除）。
  detail._activePushRule = null;
  document.querySelectorAll(".lc-push-rule-row").forEach((el) => el.classList.remove("active"));
  $("ruleDetail").classList.add("hidden");
  $("longconnDetail").classList.remove("hidden");
  const box = target || $("longconnDetail");
  const row = (k, v) => (v === undefined || v === null || v === "")
    ? ""
    : '<div class="d-kv"><span class="d-k">' + esc(k) + '</span><span class="d-v">' + esc(String(v)) + "</span></div>";

  // 工具条：复制（payload 文本）+ 分享（单条快照，7 天有效）+
  // 一键建推送模板（模拟推送形态：仅业务包展示，channelId 必选）。
  const mockBtn = e.eventType === "packet"
    ? '<button class="small ghost" id="lcMockBtn">一键建推送模板</button>'
    : "";
  const toolbar =
    '<div class="d-actions">' +
      '<button class="small ghost" id="lcCopyBtn">复制内容</button>' +
      '<button class="small ghost" id="lcShareBtn">分享</button>' +
      mockBtn +
    "</div>";

  const snap = e.snapshot ?
    '<div class="d-block"><div class="d-title">snapshot</div>' +
      row("state", e.snapshot.state) +
      row("isSocketConnected", e.snapshot.isSocketConnected) +
      row("routerHost", e.snapshot.routerHost) +
      row("routerPort", e.snapshot.routerPort) +
      row("clientId", e.snapshot.clientId) +
      row("receivedMessageCount", e.snapshot.receivedMessageCount) +
      row("receiveServiceCount", e.snapshot.receiveServiceCount) +
      row("lastError", e.snapshot.lastError) + "</div>"
    : "";

  // payload：三选一（完整 text / base64 二进制 / 截断 preview）。全部 esc。
  // text 路径：JSON 美化，默认全展开（不再有展开/收起切换）。
  let payloadBlock = "";
  if (e.payloadText) {
    const fmt = formatPayloadText(e.payloadText);
    payloadBlock =
      '<div class="d-block"><div class="d-title">payload（解析内容）</div>' +
      '<pre class="raw' + (fmt.json ? " lc-payload-json" : "") + '">' +
      esc(fmt.display) + "</pre></div>";
  } else if (e.payloadBase64) {
    payloadBlock = '<div class="d-block"><div class="d-title">payload（base64 二进制）</div><pre class="raw">' + esc(e.payloadBase64) + "</pre></div>";
  } else if (e.payloadPreview) {
    payloadBlock = '<div class="d-block"><div class="d-title">payload（超限预览，≤1KB）</div><pre class="raw">' + esc(e.payloadPreview) + "</pre></div>";
  }

  // 采集方式如实标注：哪条适配器采的（xcpush）/ API 注入（unknown-inject）/
  // 老 SDK 无字段 → 缺省展示 "—"（零副作用）。
  const adapterLabel = e.adapter === "xcpush"
    ? '<span class="lc-type lc-adapter">xcpush 适配器</span>'
    : e.adapter === "unknown-inject"
      ? '<span class="lc-type lc-inject">注入 API</span>'
      : e.adapter
        ? '<span class="lc-type">' + esc(e.adapter) + "</span>"
        : "—";

  // 摘要头参数（type/msgId/bizType 等）移入上方参数列表（解析内容只保留业务体）。记录顶层若无 msgId/bizType 字段（如分享
  // 快照），以摘要头解析值兜底；type 仅存在于摘要头。
  const headParams = parsePayloadHead(payloadSummaryInfo(e.payloadText || "").head);
  const msgId = e.msgId || headParams.msgId;
  const bizType = e.bizType || headParams.bizType;

  box.innerHTML =
    '<div class="detail-panel">' + toolbar +
      '<div class="d-kv"><span class="d-k">eventType</span><span class="d-v">' + esc(e.eventType || "?") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">来源</span><span class="d-v">' +
        (e.mocked ? '<span class="lc-type lc-fake">fake 模拟推送</span>' : '<span class="lc-type lc-real">real 真实推送</span>') + "</span></div>" +
      '<div class="d-kv"><span class="d-k">采集方式</span><span class="d-v">' + adapterLabel + "</span></div>" +
      '<div class="d-kv"><span class="d-k">channelId</span><span class="d-v">' + esc(e.channelId || "?") + "</span></div>" +
      row("seq", e.seq) +
      row("ts", e.ts) +
      row("type", headParams.type) +
      row("msgId", msgId) +
      row("bizType", bizType) +
      row("zipType", e.zipType || headParams.zipType) +
      row("status", e.status) +
      row("receiver", e.receiver) +
      row("receiverResult", e.receiverResult) +
      row("duplicate", e.duplicate ? "true" : "") +
      row("empty", e.empty ? "true" : "") +
      row("ackSent", e.ackSent ? "true" : "") +
      snap + payloadBlock +
    "</div>";

  // 按钮绑定：复制整条记录可读摘要；分享调 shareRequest(longConnId)。
  const copyBtn = box.querySelector("#lcCopyBtn");
  const shareBtn = box.querySelector("#lcShareBtn");
  if (copyBtn) copyBtn.onclick = async () => {
    const kv = (k, v) => (v === undefined || v === null || v === "") ? "" : k + ": " + v + "\n";
    let text =
      "=== MockNetPack 长连接记录 ===\n" +
      kv("eventType", e.eventType) + kv("channelId", e.channelId) +
      kv("采集方式", e.adapter) +
      kv("seq", e.seq) + kv("ts", e.ts) + kv("msgId", e.msgId) +
      kv("bizType", e.bizType) + kv("zipType", e.zipType) + kv("status", e.status) +
      kv("receiver", e.receiver) + kv("ackSent", e.ackSent ? "true" : "");
    if (e.snapshot) {
      text += "\n[snapshot]\n" +
        kv("state", e.snapshot.state) + kv("routerHost", e.snapshot.routerHost) +
        kv("clientId", e.snapshot.clientId) + kv("receivedMessageCount", e.snapshot.receivedMessageCount);
    }
    text += "\n[payload]\n" + (e.payloadText || e.payloadBase64 || e.payloadPreview || "(空)");
    const copied = await copyTextToClipboard(text);
    alert(copied ? "已复制整条记录到剪贴板" : "复制失败，请手动选择文本复制");
  };
  if (shareBtn) shareBtn.onclick = () => shareRequest({ longConnId: e.id });
  const mockRuleBtn = box.querySelector("#lcMockBtn");
  if (mockRuleBtn) mockRuleBtn.onclick = () => createPushMockRuleFromLog(e);
}

// ============================================================================
// 一键建推送模板（模拟推送形态）
// ============================================================================

/** 模板化替换 payload（技术方案 3.3#3 业务兼容）：保留结构字段
 *  （session_id/msgid/from/touser 等原值）。[mock] 标记必须注入顶层
 *  content JSON **内部**的 say_content.content 文本前面（
 *  加在整个 content 字符串外面会破坏 JSON → 业务 toJson 失败 → 显示
 *  "不支持的消息"）。不能用 JSON.parse/stringify：64 位 msgid/session_id
 *  超过 2^53 会丢精度，全程字符串扫描。 */
function pushMockTemplatePayload(rawText) {
  if (!rawText) return "";
  // 1) 定位顶层 "content" 的字符串值（vStart..i）。
  const rel = rawText.match(/("content"\s*:\s*")/);
  if (!rel) return rawText; // 无 content 键：原样返回
  const vStart = rel.index + rel[0].length;
  let i = vStart, esc = false;
  for (; i < rawText.length; i++) {
    const ch = rawText[i];
    if (esc) { esc = false; continue; }
    if (ch === "\\") { esc = true; continue; }
    if (ch === '"') break;   // 值结束（未转义引号）
  }
  if (i >= rawText.length) return rawText;
  const value = rawText.slice(vStart, i);   // content 值（转义 JSON 串内容）
  // 2) 在值内部找转义形式的 \"content\":\"（say_content.content），
  //    在其文本前插入 [mock] 前缀。
  const inner = value.match(/\\"content\\":\s*\\"/);
  if (!inner) return rawText; // 非 say_content 结构：不加标记，原样
  const insertAt = vStart + inner.index + inner[0].length;
  return rawText.slice(0, insertAt) + "[mock] " + rawText.slice(insertAt);
}

/** 一键建推送模板（对齐 HTTP「Mock 此请求」交互：不弹窗，直接生成）。
 *  模拟推送形态：不再需要匹配关键字——模板即 payload；SDK 下轮
 *  心跳拉取指令后构造假 Packet 注入，业务收到"新 push"。
 *  模板以纯业务体为底：SDK 上报的 payloadText 是「摘要头 + payload= + 业务体」
 *  展示文本，直接入模板会让业务注入器按 JSON 解析失败，
 *  先剥离摘要头；二进制（base64）日志无法自动生成文本模板，toast 提示手写。
 *  channelId/bizType 取日志值（bizType 摘要头兜底——分享快照顶层无该字段）。 */
async function createPushMockRuleFromLog(record) {
  if (!detail) return;
  if (!record.payloadText && record.payloadBase64) {
    showError("该日志为 base64 二进制 payload，无法自动生成文本模板；请用下方「新建推送模板」手写完整 push 体 JSON。");
    return;
  }
  const payload = pushMockTemplatePayload(stripPayloadSummary(record.payloadText || ""));
  if (!payload.trim()) {
    showError("日志无业务 payload，无法自动生成模板；请用下方「新建推送模板」手写。");
    return;
  }
  const headParams = parsePayloadHead(payloadSummaryInfo(record.payloadText || "").head);
  const body = {
    channelId: record.channelId || "",
    bizType: record.bizType || headParams.bizType || "",
    enabled: true,
    match: {},
    replacePayload: payload,
    source: {
      channelId: record.channelId || "",
      bizType: record.bizType || headParams.bizType || "",
      msgId: record.msgId || headParams.msgId || "",
    },
  };
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/push-mock-rules", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      showError("创建失败：" + (data.message || "HTTP " + res.status));
      return;
    }
    showError("推送模板已创建（" + data.id + "）。点模板行 → 右栏「模拟推送」下发，SDK 下轮心跳生效。");
    // 若 push 规则列表已渲染则刷新。
    if (typeof refreshPushMockRules === "function") refreshPushMockRules();
  } catch (err) {
    showError("创建失败：" + err.message);
  }
}

// 页签按钮 + 过滤输入绑定（DOMContentLoaded 后由 app 初始化调用一次即可——
// 直接在脚本加载后绑定，index.html 在底部引入本文件，DOM 已就绪）。
$("tabLongConn").onclick = () => showLogPane("longconn");
$("tabTraffic").onclick = () => showLogPane("traffic");
$("longconnFilter").addEventListener("input", (ev) => {
  longConnFilter = ev.target.value;
  renderLongConn();
});

/** 切设备/退出详情时调用：彻底重置长连接模块状态（日志数组、删除抑制名单、
 *  右栏详情、列表 DOM、快照卡），避免 A 设备日志混入 B 设备视图（P1/N3 修复）。 */
function resetLongConnSession() {
  longConnLog = [];
  $("longconnList").innerHTML = '<div class="empty">暂无长连接日志。设备长连接事件会实时出现在这里。</div>';
  $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
  $("ruleDetail").innerHTML = '<div class="empty">点击左侧一条规则查看详情。</div>';
  $("longconnInfo").textContent = "";
  $("pushRuleInfo").textContent = "";
  $("pushRuleList").innerHTML = '<div class="empty">暂无推送模板。点击下方一条长连接日志的「一键建推送模板」创建。</div>';
  // N3：连接状态快照卡一并复位，否则 A 设备的连接状态会残留到 B 设备视图。
  const snap = $("longconnSnapshot");
  if (snap) { snap.classList.add("hidden"); snap.innerHTML = ""; }
  // N4：detail.app/did 已就绪，自动拉一次新设备的推送模板列表，避免停留在空态。
  refreshPushMockRules();
}

/** 清空本页长连接日志（只清前端，服务端 48h 保留供历史日志查询；同请求流语义）。 */
function clearLongConnLog() {
  if (!detail) return;
  detail._longConnClearedAt = new Date().toISOString();
  longConnLog = [];
  renderLongConn();
  $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
  detail._activeLongConn = null;
}
$("longconnClearBtn").onclick = clearLongConnLog;
$("longconnHistoryBtn").onclick = () => openHistory("longconn");
$("pushRuleCreateBtn").onclick = openBlankPushMockRuleForm;

/** 凭空新建推送模板（无日志来源）：右栏 CodeMirror 编辑表单，保存走 POST。
 *  场景：某些 push 线上难触发，测试/AI 直接根据客户端协议手写
 *  channelId+bizType+payload；后端仅校验 channelId 必填（见服务端 validatePushMockRuleInput）。 */
function openBlankPushMockRuleForm() {
  detail._activeLongConn = null;
  document.querySelectorAll(".lc-row").forEach((el) => el.classList.remove("active"));
  $("longconnDetail").classList.add("hidden");
  $("ruleDetail").classList.remove("hidden");
  const box = $("ruleDetail");
  pushBodyEditor = null;

  box.innerHTML =
    '<div class="detail-panel editing">' +
      '<div class="d-actions">' +
        '<button class="small ghost" id="lcBlankCancelBtn">取消</button>' +
        '<button class="small" id="lcBlankSaveBtn">创建模板</button>' +
      "</div>" +
      '<div class="d-block"><div class="d-title">channelId（必填；本设备当前通道为 xcpush_tcp）</div>' +
        '<input class="lc-mock-input" id="lcBlankChannelId" value="xcpush_tcp" /></div>' +
      '<div class="d-block"><div class="d-title">bizType（可选，留空=任意；参照客户端 proto 枚举）</div>' +
        '<input class="lc-mock-input" id="lcBlankBizType" placeholder="如 1 / 2，可留空" /></div>' +
      '<div class="d-block"><div class="d-title">payload（完整 push 体 JSON；[mock] 标记自动注入 say_content.content 文本前）</div>' +
        '<div class="edit-body-wrap"><textarea id="lcBlankPayload">{\n\n}</textarea></div></div>' +
    "</div>";

  pushBodyEditor = CodeMirror.fromTextArea($("lcBlankPayload"), {
    mode: { name: "javascript", json: true },
    lineNumbers: true,
    lineWrapping: true,
    indentUnit: 2,
    tabSize: 2,
    foldGutter: true,
    gutters: ["CodeMirror-linenumbers", "CodeMirror-foldgutter"],
    matchBrackets: true,
    autoCloseBrackets: true,
    extraKeys: { "Ctrl-Q": (cm) => cm.foldCode(cm.getCursor()) },
  });
  pushBodyEditor.refresh();

  const cancel = () => {
    detail._activePushRule = null;
    $("ruleDetail").classList.add("hidden");
    $("longconnDetail").classList.remove("hidden");
  };
  box.querySelector("#lcBlankCancelBtn").onclick = cancel;

  box.querySelector("#lcBlankSaveBtn").onclick = async () => {
    pushBodyEditor.save();
    const channelId = $("lcBlankChannelId").value.trim();
    const bizType = $("lcBlankBizType").value.trim();
    const payload = $("lcBlankPayload").value;
    if (!channelId) { alert("channelId 必填"); return; }
    const btn = box.querySelector("#lcBlankSaveBtn");
    btn.disabled = true;
    btn.textContent = "创建中…";
    try {
      const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
        encodeURIComponent(detail.did) + "/push-mock-rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          channelId,
          bizType,
          enabled: true,
          match: {},
          replacePayload: payload,
          source: { channelId, bizType, msgId: "" },
          note: "凭空创建（无日志来源）",
        }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        btn.disabled = false;
        btn.textContent = "创建模板";
        alert("创建失败：" + (data.message || "HTTP " + res.status));
        return;
      }
      refreshPushMockRules();
      renderPushRuleDetail(data.rule || { id: data.id, channelId, bizType, replacePayload: payload }, false);
    } catch (err) {
      btn.disabled = false;
      btn.textContent = "创建模板";
      alert("创建失败：" + err.message);
    }
  };
}

// ============================================================================
// push mock 规则列表与启停（长连接页签内嵌区块）
// ============================================================================

/** 替换 payload 摘要（base64 标记或 JSON 缩成一行；带 SDK 摘要头的先剥离，
 *  避免列表摘要显示 "type=Packet… payload=" 前缀噪音）。 */
function pushReplaceDesc(r) {
  if (r.replacePayloadBase64) return "base64 二进制替换";
  const t = stripPayloadSummary(r.replacePayload || "");
  if (!t) return "（无替换 payload）";
  try {
    const pretty = JSON.stringify(JSON.parse(t));
    return pretty.length > 48 ? pretty.slice(0, 48) + "…" : pretty;
  } catch (_) { return t.length > 48 ? t.slice(0, 48) + "…" : t; }
}

/** 拉取该设备待下发指令状态（Web 视图：规则行「待下发 N」提示）。 */
async function fetchPendingPushCommands() {
  if (!detail || !detail.app || !detail.did) return [];
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/push-rule-commands");
    if (!res.ok) return [];
    const data = await res.json();
    return data.commands || [];
  } catch (_) { return []; }
}

/** 拉取该设备 push mock 规则全量（Web 视图，含 conflicts/命中统计）并渲染。 */
async function refreshPushMockRules() {
  if (!detail || !detail.app || !detail.did) return;
  const box = $("pushRuleList");
  const info = $("pushRuleInfo");
  box.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/push-mock-rules");
    if (res.status === 401) {
      box.innerHTML = '<div class="empty">登录后可管理 push mock 规则。</div>';
      return;
    }
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    const rules = data.rules || [];
    info.textContent = rules.length ? "共 " + rules.length + " 条" : "";
    if (!rules.length) {
      box.innerHTML = '<div class="empty">暂无推送模板。点击左侧一条长连接日志的「一键建推送模板」创建。</div>';
      return;
    }
    // 待下发指令状态（并行拉取；失败降级为不显示）。
    const pending = await fetchPendingPushCommands();
    const pendingByRule = {};
    for (const c of pending) {
      (pendingByRule[c.ruleId] = pendingByRule[c.ruleId] || []).push(c);
    }

    box.innerHTML = "";
    for (const r of rules) {
      const row = document.createElement("div");
      row.className = "rule-row lc-push-rule-row" +
        (detail._activePushRule === r.id ? " active" : "");
      row.dataset.rid = r.id;
      const pend = pendingByRule[r.id] || [];
      const pendingBadge = pend.length
        ? '<span class="badge eff" title="' + esc(pend.map((c) => c.id).join("\n")) + '">待下发 ' + pend.length + "</span>"
        : "";
      const domain = esc(r.channelId || "?") + (r.bizType ? " · biz=" + esc(r.bizType) : " · 任意 bizType");
      row.innerHTML =
        '<span class="method push">push</span>' +
        '<div class="r-body">' +
          '<div class="r-line1">' + domain + " " + pendingBadge + "</div>" +
          '<div class="r-line2">' + esc(pushReplaceDesc(r)) +
            ' <span class="sub"> · 命中 ' + (r.hitCount || 0) + "</span></div>" +
        "</div>" +
        '<button class="t-del" data-act="del" title="删除该推送模板">✕</button>';
      row.querySelector('[data-act="del"]').onclick = (ev) => {
        ev.stopPropagation();
        deletePushMockRule(r, row);
      };
      row.querySelector(".r-body").onclick = () => {
        detail._activePushRule = r.id;
        box.querySelectorAll(".lc-push-rule-row").forEach((el2) =>
          el2.classList.toggle("active", el2.dataset.rid === r.id));
        renderPushRuleDetail(r);
      };
      box.appendChild(row);
    }
  } catch (err) {
    box.innerHTML = '<div class="empty">拉取失败：' + esc(err.message) + "</div>";
  }
}

/** 推送模板详情（右列 ruleDetail 面板，结构仿 push 日志详情：toolbar 置顶 +
 *  d-kv 字段 + payload d-block）。只读态：元信息 + payload + 操作；
 *  点「编辑」切编辑态（CodeMirror JSON 折叠编辑器，同 HTTP 规则编辑）。 */
let pushBodyEditor = null;   // CodeMirror 引用；离开编辑表单即释放（DOM 由 innerHTML 整体替换）
function renderPushRuleDetail(rule, editing) {
  detail._activeLongConn = null;
  document.querySelectorAll(".lc-row").forEach((el) => el.classList.remove("active"));
  $("longconnDetail").classList.add("hidden");
  $("ruleDetail").classList.remove("hidden");
  const box = $("ruleDetail");
  pushBodyEditor = null;   // 每次渲染重置：上一次 CM 的 DOM 已被 innerHTML 丢弃

  if (editing) {
    const payload = rule.replacePayload || "";
    const pinfo = payloadSummaryInfo(payload);
    const stripBtn = pinfo.has
      ? '<button class="small ghost" id="lcStripBtn">剥离摘要头</button>'
      : "";
    box.innerHTML =
      '<div class="detail-panel editing">' +
        '<div class="d-actions">' +
          '<button class="small ghost" id="lcMockCancelBtn">取消</button>' +
          stripBtn +
          '<button class="small" id="lcMockSaveBtn">保存模板</button>' +
        "</div>" +
        '<div class="d-kv"><span class="d-k">模板 ID</span><span class="d-v">' + esc(rule.id) + "</span></div>" +
        '<label class="lc-mock-label">channelId（必填；修改会迁移匹配键域——模板池语义：同域允许多条模板共存，迁移/重复建模板均合法）</label>' +
        '<input class="lc-mock-input" id="lcEditChannelId" value="' + esc(rule.channelId || "") + '">' +
        '<label class="lc-mock-label">bizType（空 = 任意 bizType）</label>' +
        '<input class="lc-mock-input" id="lcEditBizType" value="' + esc(rule.bizType || "") + '">' +
        '<label class="lc-mock-label">备注（必填）</label>' +
        '<input class="lc-mock-input" id="lcEditNote" type="text" placeholder="说明这条模板的用途/场景" value="' + esc(rule.note || "") + '">' +
        (pinfo.has ? '<div class="lc-warn">⚠ 该 payload 带日志摘要头（' + esc(pinfo.head + "payload=…") + '），客户端按业务 JSON 解析可能失败。点「剥离摘要头」自动去除前缀后保存。</div>' : "") +
        '<div class="d-block"><div class="d-title">payload（注意：顶层 content 必须保持合法 JSON；[mock] 标记加在 say_content.content 文本前）</div>' +
          '<div class="edit-body-wrap"><textarea id="lcMockPayload">' + esc(payload) + "</textarea></div></div>" +
      "</div>";
    // CodeMirror 接管（配置与 HTTP 规则编辑同款：JSON mode + 折叠槽 + 行号 + 自动换行）。
    pushBodyEditor = CodeMirror.fromTextArea($("lcMockPayload"), {
      mode: { name: "javascript", json: true },
      lineNumbers: true,
      lineWrapping: true,
      indentUnit: 2,
      tabSize: 2,
      foldGutter: true,
      gutters: ["CodeMirror-linenumbers", "CodeMirror-foldgutter"],
      matchBrackets: true,
      autoCloseBrackets: true,
      extraKeys: { "Ctrl-Q": (cm) => cm.foldCode(cm.getCursor()) },
    });
    pushBodyEditor.refresh();
    box.querySelector("#lcMockCancelBtn").onclick = () => renderPushRuleDetail(rule, false);
    // 剥离摘要头：历史模板可能带 SDK 日志摘要头（type=… payload=），点击后
    // 就地改写编辑区为纯业务体（仅改编辑器，保存时才会写服务端）。
    const strip = box.querySelector("#lcStripBtn");
    if (strip) strip.onclick = () => {
      pushBodyEditor.save();
      const stripped = stripPayloadSummary($("lcMockPayload").value);
      pushBodyEditor.setValue(stripped);
      const warn = box.querySelector(".lc-warn");
      if (warn) warn.remove();
      strip.disabled = true;
      strip.textContent = "已剥离";
      pushBodyEditor.refresh();
    };
    box.querySelector("#lcMockSaveBtn").onclick = async () => {
      pushBodyEditor.save();   // CM 内容回写 textarea 后统一取值
      const p = $("lcMockPayload").value;
      const ch = $("lcEditChannelId").value.trim();
      const bz = $("lcEditBizType").value.trim();
      if (!ch) { alert("channelId 不能为空"); return; }
      // 备注必填（对齐 HTTP 规则编辑语义：实质编辑保存必须写备注）。
      const note = $("lcEditNote").value.trim();
      if (!note) { showError("备注必填，请填写后再保存"); return; }
      const btn = box.querySelector("#lcMockSaveBtn");
      btn.disabled = true;
      btn.textContent = "保存中…";
      try {
        const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
          encodeURIComponent(detail.did) + "/push-mock-rules/" + encodeURIComponent(rule.id), {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            match: rule.match || {},
            replacePayload: p,
            replacePayloadBase64: rule.replacePayloadBase64 || "",
            note: note,
            channelId: ch,
            bizType: bz,
          }),
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) {
          btn.disabled = false;
          btn.textContent = "保存模板";
          alert("保存失败：" + (data.message || "HTTP " + res.status));
          return;
        }
        const updated = Object.assign({}, rule, data.rule || { replacePayload: p });
        refreshPushMockRules();
        renderPushRuleDetail(updated, false);
      } catch (err) {
        btn.disabled = false;
        btn.textContent = "保存模板";
        alert("保存失败：" + err.message);
      }
    };
    return;
  }

  const payload = rule.replacePayload || "";
  const pinfo = payloadSummaryInfo(payload);
  const payloadBlock =
    (pinfo.has ? '<div class="lc-warn">⚠ 该 payload 带日志摘要头（' + esc(pinfo.head + "payload=…") + '），客户端按业务 JSON 解析可能失败；点「编辑」→「剥离摘要头」修复。</div>' : "") +
    '<div class="d-block"><div class="d-title">payload（模拟推送内容）</div>' +
      '<pre class="raw">' + esc(payload || "(无 payload)") + "</pre></div>";
  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-actions">' +
        '<button class="small ghost" id="lcEditBtn">编辑</button>' +
        '<button class="small" id="lcSendBtn">模拟推送</button>' +
      "</div>" +
      '<div class="d-kv"><span class="d-k">模板 ID</span><span class="d-v">' + esc(rule.id) + "</span></div>" +
      '<div class="d-kv"><span class="d-k">channelId</span><span class="d-v">' + esc(rule.channelId || "—") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">bizType</span><span class="d-v">' + esc(rule.bizType || "（空=任意）") + "</span></div>" +
      '<div class="d-kv"><span class="d-k">命中次数</span><span class="d-v">' + (rule.hitCount || 0) + "</span></div>" +
      payloadBlock +
    "</div>";
  box.querySelector("#lcEditBtn").onclick = () => renderPushRuleDetail(rule, true);
  box.querySelector("#lcSendBtn").onclick = async () => {
    const btn = box.querySelector("#lcSendBtn");
    btn.disabled = true;
    btn.textContent = "下发中…";
    try {
      const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
        encodeURIComponent(detail.did) + "/push-mock-rules/" + encodeURIComponent(rule.id) + "/send", {
        method: "POST",
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        btn.disabled = false;
        btn.textContent = "模拟推送";
        alert("下发失败：" + (data.message || "HTTP " + res.status));
        return;
      }
      alert("模拟推送已下发（指令 " + data.id + "）。SDK 下轮心跳（约 3s）拉取注入，长连接日志会出现 fake 标记。");
      btn.disabled = false;
      btn.textContent = "模拟推送";
      // 延迟 4s 再刷列表：此刻 SDK 尚未心跳拉走指令，pending 计数仍为 1，
      // 立即刷新会误显「待下发」；等 SDK 消费后 pending 归零，徽标自然消失。
      setTimeout(refreshPushMockRules, 4000);
    } catch (err) {
      btn.disabled = false;
      btn.textContent = "模拟推送";
      alert("下发失败：" + err.message);
    }
  };
}

/** 删除模板（DELETE 204）。删除后若右栏正展示该模板详情，复位右栏到日志占位。 */
async function deletePushMockRule(rule, row) {
  if (!confirm("删除推送模板 " + rule.id + "？\n已下发的指令不受影响，真实 push 也不受影响。")) return;
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(detail.app) + "/" +
      encodeURIComponent(detail.did) + "/push-mock-rules/" + encodeURIComponent(rule.id), {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 204) {
      const data = await res.json().catch(() => ({}));
      alert("删除失败：" + (data.message || "HTTP " + res.status));
      return;
    }
    if (detail._activePushRule === rule.id) {
      // 右栏正展示被删模板（无论入口是行内 ✕ 还是详情面板）：复位右列。
      detail._activePushRule = null;
      $("ruleDetail").classList.add("hidden");
      $("longconnDetail").classList.remove("hidden");
      $("longconnDetail").innerHTML = '<div class="empty">点击左侧一条长连接日志查看详情。</div>';
    }
    refreshPushMockRules();
  } catch (err) { alert("删除失败：" + err.message); }
}

$("pushRuleRefreshBtn").onclick = refreshPushMockRules;
// 切到长连接页签时刷新规则列表（与日志同屏）。
const _lcTabOnclick = $("tabLongConn").onclick;
$("tabLongConn").onclick = (ev) => { _lcTabOnclick(ev); refreshPushMockRules(); };
