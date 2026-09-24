"use strict";

// MockNetPack web: token storage + unified Authorization header, and the
// auth API calls (login/register/logout). Pure move from app.js.
// ===== M7.1.2 账号登录态：token 存储 + 统一鉴权头 =====
const TOKEN_KEY = "mocknetpack_token";
let authUser = null; // { username, role, createdAt }

function getToken() { try { return localStorage.getItem(TOKEN_KEY); } catch { return null; } }
function setToken(t) {
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t);
    else localStorage.removeItem(TOKEN_KEY);
  } catch { /* 隐私模式等场景降级为内存态 */ }
}
// 所有 MockNetPack API 请求统一带 Authorization 头（M7.1.3 服务端强制鉴权后无缝）。
function apiFetch(path, options = {}) {
  const headers = Object.assign({}, options.headers);
  const token = getToken();
  if (token) headers["Authorization"] = "Bearer " + token;
  return fetch(API + path, Object.assign({}, options, { headers }));
}

async function doLogin(ev) {
  ev.preventDefault();
  const u = $("loginUser").value.trim();
  const p = $("loginPass").value;
  hideAuthError();
  if (!u || !p) { showAuthError("请输入用户名和密码"); return; }
  try {
    const res = await fetch(API + "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { showAuthError(data.message || "登录失败（HTTP " + res.status + "）"); return; }
    setToken(data.token);
    authUser = data.user;
    $("loginForm").reset();
    enterApp();
  } catch (e) {
    showAuthError("无法连接服务器：" + e.message);
  }
}

async function doRegister(ev) {
  ev.preventDefault();
  const u = $("regUser").value.trim();
  const p = $("regPass").value;
  const p2 = $("regPass2").value;
  hideAuthError();
  if (!u || !p) { showAuthError("请输入用户名和密码"); return; }
  if (p !== p2) { showAuthError("两次输入的密码不一致"); return; }
  try {
    const res = await fetch(API + "/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { showAuthError(data.message || "注册失败（HTTP " + res.status + "）"); return; }
    // 注册成功自动登录（拿 token 进主界面）。
    const lres = await fetch(API + "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p }),
    });
    const ldata = await lres.json().catch(() => ({}));
    if (!lres.ok) {
      showAuthError("注册成功，自动登录失败：" + (ldata.message || "HTTP " + lres.status));
      $("registerForm").classList.add("hidden");
      $("loginForm").classList.remove("hidden");
      $("switchToLogin").classList.add("hidden");
      $("switchToRegister").classList.remove("hidden");
      return;
    }
    setToken(ldata.token);
    authUser = ldata.user;
    $("registerForm").reset();
    enterApp();
  } catch (e) {
    showAuthError("无法连接服务器：" + e.message);
  }
}

// 登出：服务端吊销 token + 清除本地 + 回登录页（吊销后 token 立即失效）。
async function doLogout() {
  const token = getToken();
  if (token) {
    try { await apiFetch("/auth/logout", { method: "POST" }); } catch { /* 服务不可达也继续清本地 */ }
  }
  setToken(null);
  authUser = null;
  // M7.2.4: 清详情页状态/轮询/会话，回到列表页，避免换账号后残留旧设备
  stopTrafficPoll();
  if (detail && detail.sessionId) stopViewer(detail.sessionId);
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  detail = null;
  pageLog = [];
  location.hash = "";
  $("detailView").classList.add("hidden");
  $("listView").classList.remove("hidden");
  showAuth();
}
