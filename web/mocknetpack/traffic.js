"use strict";

// MockNetPack web: device detail view, live request-stream polling,
// page log merge/filter and traffic detail rendering. Pure move from app.js.
// ============================================================================
// 设备详情 + 请求流
// ============================================================================

async function openDetail(app, did) {
  location.hash = "#/device/" + encodeURIComponent(app) + "/" + encodeURIComponent(did);
}

async function enterDetail(app, did) {
  // 4.5：以 detail 为单一事实源——进入新详情前先释放上一会话的 viewer，
  // 避免 A→B 直跳后 A 会话的续租定时器继续运行、服务端永不超时结束会话。
  if (detail && detail.sessionId) stopViewer(detail.sessionId);
  detail = { app, did, sessionId: null };
  pageLog = [];   // ：进入设备详情（含跨设备跳转）即重新开始页面日志
  $("listView").classList.add("hidden");
  $("detailView").classList.remove("hidden");
  $("dDid").textContent = did;
  // header 动态：显示返回按钮 + app 名，隐藏主标题
  $("backBtn").classList.remove("hidden");
  $("detailApp").textContent = app;
  $("detailApp").classList.remove("hidden");
  $("mainTitle").classList.add("hidden");
  await loadDetail();
}

/** 加载设备信息；绑定当前抓包会话（：仅当前会话，无历史）。 */
async function loadDetail() {
  if (!detail) return;
  const { app, did } = detail;
  try {
    const devRes = await apiFetch("/devices/" + encodeURIComponent(app) + "/" + encodeURIComponent(did));
    const dev = devRes.ok ? await devRes.json() : null;

    const st = STATUS[dev && dev.status] || STATUS.idle;
    const stBadge = $("dStatus");
    stBadge.textContent = st.label;
    stBadge.className = "badge " + st.cls;
    $("dLastSeen").textContent = "最后活跃：" + relTime(dev && dev.lastSeenAt);

    const cur = dev && dev.currentSession;
    const toggle = $("dToggleSession");
    if (cur && cur.status === "capturing") {
      toggle.textContent = "断开";
      toggle.className = "small danger";
      toggle.disabled = false;
      toggle.onclick = () => disconnect(dev);
    } else {
      toggle.textContent = "连接";
      toggle.className = "small";
      toggle.disabled = !dev || dev.status === "offline";
      toggle.onclick = () => connect(dev);
    }

    loadRules();
    // ：只绑定当前抓包会话（无则空态），不展示历史会话。
    // ：历史日志视图打开期间跳过绑定——bindSession 会重启 trafficTimer 轮询，
    // 与 openHistory 的暂停意图冲突；关闭历史后由 closeHistory 恢复。
    if (historyOpen) return;
    bindSession(cur && cur.status === "capturing" ? cur : null);
    // : 详情页定时刷新设备状态（熄屏重启后 offline→online 同步）
    // ：历史打开期间不重建（openHistory 已 clear，在途 loadDetail 完成后
    // 不得把它重新拉起来）。
    if (!detailPollTimer && !historyOpen) {
      detailPollTimer = setInterval(loadDetail, POLL_MS);
    }
  } catch (e) {
    showError("加载设备详情失败：" + e.message);
  }
}

/** ：绑定当前会话。capturing → 注册 viewer + 2s 轮询，日志合并进 pageLog 继续累积；
 *  无会话（断开）→ 停止轮询，已展示日志保留不清空（离开页面才清空）。 */
function bindSession(session) {
  // ：历史日志视图打开期间不绑定/重启轮询（openHistory 已暂停；在途调用
  // 不得把 trafficTimer 重新拉起），关闭历史后由 closeHistory 恢复。
  if (historyOpen) return;
  stopTrafficPoll();
  detail.sessionId = session ? session.id : null;

  if (!session || session.status !== "capturing") {
    // 断开/无会话：有页面日志则保留展示（诉求 2），否则空态。
    if (pageLog.length) {
      renderTraffic(pageLog);
    } else {
      $("trafficInfo").textContent = "";
      $("trafficList").innerHTML = '<div class="empty">暂无进行中的会话。点击「连接」开始抓包。</div>';
    }
    return;
  }
  // 连接中：若已有页面日志，提示跨会话保留（诉求 3）。
  $("trafficInfo").textContent = pageLog.length
    ? "实时 · 2s 轮询 · 共 " + pageLog.length + " 条（含上次会话日志）"
    : "实时 · 2s 轮询 · 会话 " + shortId(session.id);
  registerViewer(session, "设备详情页");
  trafficTimer = setInterval(pollTraffic, TRAFFIC_POLL_MS);
  pollTraffic();
}

function stopTrafficPoll() {
  if (trafficTimer) { clearInterval(trafficTimer); trafficTimer = null; }
}

/** ：右列详情面板切换（请求详情 / 规则详情）。 */
function showDetailPane(kind) {
  $("ruleDetail").classList.toggle("hidden", kind !== "rule");
  $("trafficDetail").classList.toggle("hidden", kind !== "traffic");
}

/** 请求流轮询：拉当前会话最新一页，合并进页面日志 pageLog（ 跨会话保留、时间线混排）。 */
async function pollTraffic() {
  if (!detail || !detail.sessionId) return;
  const sid = detail.sessionId;
  try {
    const meta = await apiFetch("/sessions/" + encodeURIComponent(sid) + "/traffic?limit=1").then(r => r.json());
    if (!meta || typeof meta.total !== "number") return;
    const offset = Math.max(0, meta.total - TRAFFIC_PAGE);
    const data = await apiFetch("/sessions/" + encodeURIComponent(sid) +
      "/traffic?limit=" + TRAFFIC_PAGE + "&offset=" + offset).then(r => r.json());
    pageLog = mergeLog(pageLog, data.entries || []);
    renderTraffic(pageLog);
  } catch (e) {
    $("trafficInfo").textContent = "请求流拉取失败：" + e.message;
  }
}

/** 合并日志：按 id 去重、timestamp 升序，保留最近 LOG_CAP 条（跨会话时间线混排）。
 *   复活抑制：被前端删除（✕/清空）的条目不再被轮询拉回——
 *  _deletedIds 精确丢弃单条；_logClearedAt 之前的旧条目在清空后一律丢弃。 */
function mergeLog(base, fresh) {
  const seen = new Map();
  const cutMs = detail && detail._logClearedAt ? new Date(detail._logClearedAt).getTime() : 0;
  for (const e of base) seen.set(e.id, e);
  for (const e of fresh) {
    if (seen.has(e.id)) continue;
    if (detail && detail._deletedIds && detail._deletedIds.has(e.id)) continue;
    // 清空前的旧条目：毫秒比较（服务端 timestamp 带时区偏移，字符串比较会错位）
    if (cutMs && new Date(e.timestamp || 0).getTime() <= cutMs) continue;
    seen.set(e.id, e);
  }
  const arr = Array.from(seen.values())
    .sort((a, b) => (a.timestamp || "").localeCompare(b.timestamp || ""));
  return arr.length > LOG_CAP ? arr.slice(arr.length - LOG_CAP) : arr;
}

/** 渲染页面日志（live = 当前绑定会话在抓包；断开后仅展示与过滤，不轮询）。 */
function renderTraffic(entries) {
  if (!detail) return;
  const live = !!detail.sessionId;
  // 倒序：最新在上（服务端按 timestamp 升序）。
  entries = entries.slice().sort((a, b) => (b.timestamp || "").localeCompare(a.timestamp || ""));
  const q = trafficFilter.trim().toLowerCase();
  const total = entries.length;
  if (q) {
    entries = entries.filter((e) =>
      (e.path || e.url || "").toLowerCase().includes(q) ||
      (e.method || "").toLowerCase().includes(q));
  }
  $("trafficInfo").textContent = (live ? "实时 · 2s 轮询" : "已断开 · 日志保留") +
    " · 共 " + total + " 条" + (q ? "（过滤出 " + entries.length + " 条）" : "");

  const box = $("trafficList");
  if (!entries.length) {
    box.innerHTML = '<div class="empty">暂无请求。设备产生流量后会实时出现在这里。</div>';
    return;
  }
  box.innerHTML = "";
  for (const e of entries) {
    const el = document.createElement("div");
    el.className = "traffic-row" + (detail._activeTraffic === e.id ? " active" : "");
    const statusCls = e.statusCode == null ? "st-err"
      : e.statusCode < 300 ? "st-2xx" : e.statusCode < 400 ? "st-3xx"
      : e.statusCode < 500 ? "st-4xx" : "st-5xx";
    el.innerHTML =
      '<span class="method ' + methodCls(e.method) + '">' + esc(e.method || "?") + "</span>" +
      '<span class="t-url">' + esc(e.path || e.url) + (e.query ? "?" + esc(e.query) : "") + "</span>" +
      '<span class="t-status ' + statusCls + '">' + (e.statusCode ?? "ERR") + "</span>" +
      '<span class="t-dur">' + e.durationMs + "ms</span>" +
      '<span class="t-time">' + clockTime(e.timestamp) + "</span>" +
      '<button class="t-del" title="删除该日志（不影响已创建的 Mock 规则）">✕</button>';
    el.onclick = () => {
      detail._activeTraffic = e.id;
      renderTrafficDetail(e);
      box.querySelectorAll(".traffic-row").forEach((r) =>
        r.classList.toggle("active", r === el));
    };
    el.querySelector(".t-del").onclick = (ev) => {
      ev.stopPropagation();   // 只删日志，不触发详情
      deleteTrafficEntry(e);
    };
    box.appendChild(el);
  }
}

/**  ( 保留语义)：删除单条日志 = 只清前端页面日志，不再调服务端
 *  DELETE（服务端流量按 48h 保留供历史日志查询）。只动日志，不影响已创建的
 *  Mock 规则。 */
async function deleteTrafficEntry(e) {
  if (!detail) return;
  if (!detail._deletedIds) detail._deletedIds = new Set();
  detail._deletedIds.add(e.id);   // 复活抑制：轮询合并时丢弃该条
  pageLog = pageLog.filter((x) => x.id !== e.id);
  if (detail._activeTraffic === e.id) {
    detail._activeTraffic = null;
    $("trafficDetail").innerHTML = '<div class="empty">点击左侧请求或规则查看详情。</div>';
    showDetailPane("traffic");
  }
  renderTraffic(pageLog);
}

/**  ( 保留语义)：清空本页日志 = 只清前端页面日志，不再调服务端
 *  DELETE（服务端流量按 48h 保留供历史日志查询）。连接中清空后新流量继续
 *  累积。只动日志，不影响已创建的 Mock 规则。 */
async function clearTrafficLog() {
  if (!detail) return;
  detail._logClearedAt = new Date().toISOString(); // 复活抑制：清空时刻前的旧条目轮询时丢弃
  pageLog = [];
  if (detail.sessionId) {
    renderTraffic(pageLog);   // live：共 0 条，新流量继续累积
  } else {
    $("trafficInfo").textContent = "";
    $("trafficList").innerHTML = '<div class="empty">暂无进行中的会话。点击「连接」开始抓包。</div>';
  }
  if (detail._activeTraffic) {
    detail._activeTraffic = null;
    $("trafficDetail").innerHTML = '<div class="empty">点击左侧请求或规则查看详情。</div>';
    showDetailPane("traffic");
  }
}

/** 请求详情面板（列表数据已含完整字段，无需再查详情接口）。 */
/** 渲染请求详情（headers/body/页签/Mock/分享）。默认输出到主详情区
 *  #trafficDetail；history.js 可传入 historyDetail 目标，把同一渲染复用进
 *  历史浮层（此时不触碰主布局的 showDetailPane）。 */
function renderTrafficDetail(e, target) {
  if (!target) showDetailPane("traffic");
  detail._activeRule = null;
  document.querySelectorAll(".rule-row").forEach((el) => el.classList.remove("active"));
  const box = target || $("trafficDetail");
  const headRows = (h) => Object.entries(h || {})
    .map(([k, v]) => '<div class="d-kv"><span class="d-k">' + esc(k) + "</span>" +
      '<span class="d-v">' + esc(Array.isArray(v) ? v.join(", ") : v) + "</span></div>").join("");

  const reqTab =
      '<div class="d-block"><div class="d-title">请求头</div>' +
        (headRows(e.requestHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">请求体</div>' + bodyHtml(e.requestBodyDecoded || e.requestBody, { contentType: headerValue(e.requestHeaders, "Content-Type"), base64: e.requestBodyBase64 }) + "</div>";
  const respTab =
      '<div class="d-kv"><span class="d-k">状态</span><span class="d-v">' +
        (e.statusCode != null ? e.statusCode + (e.error ? "（" + esc(e.error) + "）" : "") : "请求失败： " + esc(e.error || "")) +
      "</span></div>" +
      '<div class="d-kv"><span class="d-k">耗时</span><span class="d-v">' + e.durationMs + " ms</span></div>" +
      '<div class="d-kv"><span class="d-k">时间</span><span class="d-v">' + esc(e.timestamp || "—") + "</span></div>" +
      (e.mocked ? '<div class="d-kv"><span class="d-k">Mock</span><span class="d-v">是</span></div>' : "") +
      '<div class="d-block"><div class="d-title">响应头</div>' +
        (headRows(e.responseHeaders) || '<div class="d-v">—</div>') + "</div>" +
      '<div class="d-block"><div class="d-title">响应体</div>' + bodyHtml(e.responseBodyDecoded || e.responseBody, { contentType: headerValue(e.responseHeaders, "Content-Type"), base64: e.responseBodyBase64 }) + "</div>";

  box.innerHTML =
    '<div class="detail-panel">' +
      '<div class="d-actions">' +
        '<button id="mockThisBtn" class="small">Mock 此请求</button>' +
        '<button id="copyReqBtn" class="small ghost">复制</button>' +
        '<button id="shareBtn" class="small ghost">分享</button>' +
      "</div>" +
      '<div class="d-kv"><span class="d-k">请求</span><span class="d-v">' + esc(e.method) + " " + esc(e.url) + "</span></div>" +
      '<div class="tabs">' +
        '<button class="tab active" data-tab="req">请求</button>' +
        '<button class="tab" data-tab="resp">响应</button>' +
      "</div>" +
      '<div class="tab-pane" data-pane="req">' + reqTab + "</div>" +
      '<div class="tab-pane hidden" data-pane="resp">' + respTab + "</div>" +
    "</div>";

  // 页签切换
  box.querySelectorAll(".tab").forEach((btn) => {
    btn.onclick = () => {
      box.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      box.querySelectorAll(".tab-pane").forEach((p) => {
        p.classList.toggle("hidden", p.dataset.pane !== btn.dataset.tab);
      });
    };
  });

  const btn = box.querySelector("#mockThisBtn");
  if (btn) {
    btn.disabled = e.statusCode == null;  // 失败请求无回包可固化
    btn.onclick = () => mockThisRequest(e);
  }
  const shareBtn = box.querySelector("#shareBtn");
  if (shareBtn) {
    shareBtn.onclick = () => shareRequest(e);
  }
  const copyBtn = box.querySelector("#copyReqBtn");
  if (copyBtn) {
    // 复制整条请求 + 回包为完整 HTTP 报文交换（重建逻辑见 utils.buildRawTrafficText）。
    // 成功后按钮短暂显示「已复制」，避免高频操作被 alert 打断。
    copyBtn.onclick = async () => {
      const ok = await copyTextToClipboard(buildRawTrafficText(e));
      if (ok) {
        copyBtn.textContent = "已复制";
        copyBtn.disabled = true;
        setTimeout(() => { copyBtn.textContent = "复制"; copyBtn.disabled = false; }, 1500);
      } else {
        alert("复制失败：请手动选择复制");
      }
    };
  }
  bindJsonTree(box);
}
