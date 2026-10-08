"use strict";
const API = "/api/v1";
const POLL_MS = 5000;        // 设备列表轮询
const TRAFFIC_POLL_MS = 2000; // 请求流轮询（轮询 2s，秒级可见）
const TRAFFIC_PAGE = 100;    // 每页条数（契约 limit 上限 500，取 100 最新）
const RENEW_MS = 60000;      // viewer 续租（TTL 120s 的一半）

// sessionId -> { viewerId, timer }
const viewers = new Map();
let pollTimer = null;
let shareViewActive = false;
let detailPollTimer = null;        // 列表轮询
let trafficTimer = null;     // 请求流轮询
let detail = null;           // { app, did, sessionId }
let ruleStore = [];          // 当前规则列表（供详情/删除使用）
let ruleBodyEditor = null;   //  编辑表单回包体 CodeMirror 实例（保存前 save() 回写 textarea）
let trafficFilter = "";      // 展示规则：页面级字符串过滤（刷新即清空）
let pageLog = [];            //  页面级日志缓冲（详情页停留期间跨会话累积，离开页面清空）
const LOG_CAP = 300;         // 页面日志上限（防止长时间抓包内存/DOM 过大）
// hash 路由：#/device/{app}/{did} 或 #/share/{id}
window.addEventListener("hashchange", () => {
  const m = location.hash.match(/^#\/device\/([^/]+)\/(.+)$/);
  if (m) {
    enterDetail(decodeURIComponent(m[1]), decodeURIComponent(m[2]));
    return;
  }
  const sm = location.hash.match(/^#\/share\/(.+)$/);
  if (sm) {
    // 4.5：切到分享视图同样释放当前详情页的 viewer（detail 为单一事实源）。
    if (detail && detail.sessionId) stopViewer(detail.sessionId);
    renderShareView(sm[1]);
    return;
  }
  if (detail) backToList();
  else if (shareViewActive) showAuth();
  shareViewActive = false;
});
// ============================================================================
// 启动（：登录门禁 → enterApp）
// ============================================================================

function showAuth() {
  $("authView").classList.remove("hidden");
  $("appWrap").classList.add("hidden");
  $("userInfo").textContent = "";
  $("logoutBtn").classList.add("hidden");
  // 清空表单（避免登出后残留他人输入的账号/密码）
  $("loginForm").reset();
  $("registerForm").reset();
  $("loginForm").classList.remove("hidden");
  $("registerForm").classList.add("hidden");
  $("switchToRegister").classList.remove("hidden");
  $("switchToLogin").classList.add("hidden");
  stopTrafficPoll();
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  releaseAllViewers();
  detail = null;
  pageLog = [];
}
function showAuthError(msg) {
  const b = $("authError");
  b.textContent = msg;
  b.classList.remove("hidden");
}
function hideAuthError() { $("authError").classList.add("hidden"); }
function enterApp() {
  $("authView").classList.add("hidden");
  $("appWrap").classList.remove("hidden");
  hideAuthError();
  const u = authUser;
  $("userInfo").textContent = u
    ? (u.username + " · " + (u.role === "admin" ? "管理员" : "开发者"))
    : "";
  $("logoutBtn").classList.remove("hidden");
  // : 已登录用户可见 API Key 管理入口（创建/列表/吊销）
  $("apiKeyBtn").classList.remove("hidden");
  initApiKeyUI();

  $("backBtn").onclick = backToList;
  const clearBtn = $("trafficClearBtn");
  if (clearBtn) clearBtn.onclick = clearTrafficLog;
  //  : 历史日志视图（最近 48h 已结束会话，服务端过滤 + 分页）
  const historyBtn = $("historyBtn");
  if (historyBtn) historyBtn.onclick = openHistory;
  const historyCloseBtn = $("historyCloseBtn");
  if (historyCloseBtn) historyCloseBtn.onclick = closeHistory;
  const hFilterBtn = $("hFilterBtn");
  if (hFilterBtn) hFilterBtn.onclick = applyHistoryFilter;
  const hResetBtn = $("hResetBtn");
  if (hResetBtn) {
    hResetBtn.onclick = () => {
      resetHistoryFilters();
      applyHistoryFilter();
    };
  }
  const hMoreBtn = $("historyMoreBtn");
  if (hMoreBtn) hMoreBtn.onclick = loadHistoryMore;
  loadDevices();
  pollTimer = setInterval(loadDevices, POLL_MS);
  // 刷新时若 hash 是详情页，直接进入（否则 hash 与当前相同，点击同一条目不触发 hashchange）
  const m = location.hash.match(/^#\/device\/([^/]+)\/(.+)$/);
  if (m) enterDetail(decodeURIComponent(m[1]), decodeURIComponent(m[2]));
}

// 启动：有 token 先验 /auth/me（刷新保持登录）；无效/无 token 进登录门禁。
async function boot() {
  $("loginForm").addEventListener("submit", doLogin);
  $("registerForm").addEventListener("submit", doRegister);
  $("logoutBtn").addEventListener("click", doLogout);
  $("toRegister").addEventListener("click", () => {
    hideAuthError();
    $("loginForm").classList.add("hidden");
    $("registerForm").classList.remove("hidden");
    $("switchToRegister").classList.add("hidden");
    $("switchToLogin").classList.remove("hidden");
  });
  $("toLogin").addEventListener("click", () => {
    hideAuthError();
    $("registerForm").classList.add("hidden");
    $("loginForm").classList.remove("hidden");
    $("switchToLogin").classList.add("hidden");
    $("switchToRegister").classList.remove("hidden");
  });

  // : share 链接免登录直接渲染
  const sm = location.hash.match(/^#\/share\/(.+)$/);
  if (sm) {
    shareViewActive = true;
    await renderShareView(sm[1]);
    return;
  }

  const token = getToken();
  if (token) {
    try {
      const res = await apiFetch("/auth/me");
      if (res.ok) {
        authUser = await res.json();
        enterApp();
        return;
      }
      setToken(null);   // token 失效/已吊销 → 回登录页
    } catch (e) {
      showAuthError("无法连接服务器：" + e.message);
    }
  }
  showAuth();
}
boot();
//  展示规则：页面级字符串过滤，仅当前页面、刷新即清空（/）。
(function () {
  const box = $("trafficFilter");
  if (box) box.addEventListener("input", (ev) => {
    trafficFilter = ev.target.value || "";
    renderTraffic(pageLog);
  });
})();

// : wire scan-connect modal buttons（签发/刷新/关闭）
(function () {
  const openBtn = document.getElementById("scanConnectBtn");
  if (openBtn) openBtn.onclick = openScanConnectModal;
  const refresh = document.getElementById("scanConnectRefresh");
  if (refresh) refresh.onclick = () => {
    const app = $("addApp").value || "com.example.integrating";
    issueScanToken(app);
  };
  const cancel = document.getElementById("scanConnectCancel");
  if (cancel) cancel.onclick = closeScanConnectModal;
})();

//  (v0.8.2): wire device-naming modal（保存 + 回车提交）
(function () {
  const submit = document.getElementById("deviceNameSubmit");
  if (submit) submit.onclick = submitDeviceName;
  const input = document.getElementById("deviceNameInput");
  if (input) input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") submitDeviceName();
  });
})();
