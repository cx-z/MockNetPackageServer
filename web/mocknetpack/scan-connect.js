"use strict";

// MockNetPack web: 扫码连接（M9.3，契约 v0.8.1）。
// 登录态签发配对令牌 → 组装 mocknetpack:// 二维码 → 渲染 + 10 分钟倒计时。
// 二维码格式（D4）：mocknetpack://connect?v=1&u=<base64url(server)>&a=<appID>&t=<token>
// u 优先取服务端局域网可达 origin（M9.1-fix，v0.8.1）：浏览器用 localhost 打开时
// location.origin 是 localhost，手机连自己的 localhost 必然失败，故先请求
// GET /api/v1/local-address 拿 `{origin}` 替换；非 localhost（域名/局域网 IP）直接同源。
// 令牌可复用（D5）：同一二维码可扫多台设备，10 分钟有效，过期提示刷新
// （D1 扫码即注册由服务端 RegisterDeviceWithPairing 保证）。
// M9.3-fix（v0.8.2）：二维码弹窗打开期间轮询 GET /pairing-tokens/{token}，
// 检测到有新设备用该令牌完成注册 → 自动关闭二维码弹窗 → 弹出设备命名框 →
// 提交改名后跳转该设备的请求列表页（#/device/{app}/{did}）开始看抓包。

// ============================================================================
// 二维码组装
// ============================================================================

/// UTF-8 安全 base64url（RFC 4648 §5：- _ 替代 + /，去 padding）。
function base64url(str) {
  const b64 = btoa(unescape(encodeURIComponent(str)));
  return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

/// 浏览器本机回环主机名集合：此时 location.origin 对手机不可达。
const LOCAL_HOSTNAMES = new Set(["localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0"]);

/// 判断 hostname 是否为本机回环（纯函数，供 node 交叉验证）。
function isLocalHostname(hostname) {
  return LOCAL_HOSTNAMES.has(String(hostname || "").toLowerCase());
}

/// 二维码中的服务器地址（SDK 以它为 base URL 注册/心跳）。
/// localhost 访问时先向服务端要局域网可达 origin（GET /api/v1/local-address），
/// 失败回退同源并提示——二维码此时无法被手机连接，让用户知道原因。
async function scanQRServerURL() {
  if (isLocalHostname(location.hostname)) {
    try {
      const res = await apiFetch("/local-address");
      const data = await res.json().catch(() => ({}));
      if (res.ok && data && data.origin) {
        return data.origin.replace(/\/$/, "") + API;
      }
    } catch (_) { /* fallthrough */ }
    const err = $("scanConnectError");
    if (err) {
      err.textContent = "无法获取服务器局域网地址，请改用局域网 IP 打开本页面后重试";
      err.classList.remove("hidden");
    }
  }
  return location.origin + API;
}

/// 组装协议二维码文本（async：需先解析服务器地址）。
async function scanQRText(token, app) {
  return "mocknetpack://connect?v=1&u=" + base64url(await scanQRServerURL()) +
         "&a=" + encodeURIComponent(app) +
         "&t=" + encodeURIComponent(token);
}

// ============================================================================
// 模态框：签发 → 渲染 → 倒计时
// ============================================================================

const SCAN_TOKEN_TTL_SEC = 600; // 10 分钟（服务端 pairingTokenTTL）
let scanQR = null;              // QRCode 实例
let scanCountdownTimer = null;

/// 从服务端加载 app 目录（GET /api/v1/apps，M9.4）填充扫码弹窗下拉框。
/// 目录由后端热加载（默认占位 + MOCKD_ALLOWED_APPS + git-ignored 本地文件），
/// 所以往 allowed-apps.local 里加 bundle id 后，下拉框刷新即出现、无需重启。
/// 加载失败时保留现有（默认）选项，不影响签发。
async function loadAppOptions() {
  const sel = $("addApp");
  if (!sel || sel.dataset.loaded) return;
  try {
    const res = await apiFetch("/apps");
    const data = await res.json().catch(() => ({}));
    if (res.ok && Array.isArray(data.apps) && data.apps.length > 0) {
      sel.innerHTML = "";
      for (const app of data.apps) {
        const opt = document.createElement("option");
        opt.value = app;
        opt.textContent = app;
        sel.appendChild(opt);
      }
      sel.dataset.loaded = "1";
    }
  } catch (_) { /* 保留默认选项 */ }
}

async function openScanConnectModal() {
  // 扫码连接弹窗内 App 下拉（v1.5：手动注册入口已移除，下拉迁移至扫码弹窗）。
  await loadAppOptions(); // M9.4：先同步服务端 app 目录，再取当前选中值
  const app = $("addApp").value || "com.example.integrating";
  $("scanConnectApp").textContent = "App：" + app;
  $("scanConnectError").classList.add("hidden");
  $("scanConnectModal").classList.remove("hidden");
  await issueScanToken(app);
}

/// 登录态签发配对令牌并渲染二维码；失败显示错误、保留弹窗可重试。
async function issueScanToken(app) {
  clearScanCountdown();
  $("scanConnectQR").innerHTML = '<div class="empty">正在签发二维码…</div>';
  try {
    const res = await apiFetch("/pairing-tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.message || ("HTTP " + res.status));
    renderScanQR(await scanQRText(data.token, data.app), data.expiresAt);
    startScanPoll(data.token); // M9.3-fix：扫码完成自动关弹窗
  } catch (e) {
    $("scanConnectError").textContent = "签发二维码失败：" + e.message;
    $("scanConnectError").classList.remove("hidden");
    $("scanConnectQR").innerHTML = "";
  }
}

function renderScanQR(text, expiresAt) {
  const box = $("scanConnectQR");
  box.innerHTML = "";
  scanQR = new QRCode(box, { width: 220, height: 220, correctLevel: QRCode.CorrectLevel.M });
  scanQR.makeCode(text);
  startScanCountdown(expiresAt);
}

/// 10 分钟倒计时（服务端 expiresAt 为准；缺失则按本地 10 分钟兜底）。
function startScanCountdown(expiresAt) {
  const deadline = expiresAt ? new Date(expiresAt).getTime() : Date.now() + SCAN_TOKEN_TTL_SEC * 1000;
  const tick = () => {
    const remain = Math.max(0, Math.round((deadline - Date.now()) / 1000));
    const mm = String(Math.floor(remain / 60)).padStart(2, "0");
    const ss = String(remain % 60).padStart(2, "0");
    const el = $("scanConnectCountdown");
    if (el) {
      el.textContent = remain > 0
        ? "二维码有效（10 分钟）：" + mm + ":" + ss
        : "二维码已过期，请点击「刷新二维码」";
    }
  };
  tick();
  scanCountdownTimer = setInterval(tick, 1000);
}

function clearScanCountdown() {
  if (scanCountdownTimer) { clearInterval(scanCountdownTimer); scanCountdownTimer = null; }
}

function closeScanConnectModal() {
  stopScanPoll();
  clearScanCountdown();
  $("scanConnectQR").innerHTML = "";
  $("scanConnectCountdown").textContent = "";
  $("scanConnectModal").classList.add("hidden");
  scanQR = null;
}

// ============================================================================
// M9.3-fix (v0.8.2)：扫码完成检测（轮询）+ 设备命名
// ============================================================================

const SCAN_POLL_MS = 3000; // 轮询间隔
let scanPollTimer = null;
let scanPollToken = null;
let scanPollBaseline = 0; // 打开弹窗时的已配对数量（令牌可复用 D5，以「新增」为准）

/// 轮询令牌状态：一旦出现新的配对注册，自动关闭二维码弹窗并进入设备命名。
function startScanPoll(token) {
  stopScanPoll();
  scanPollToken = token;
  scanPollBaseline = 0;
  const tick = async () => {
    if (!scanPollToken) return;
    try {
      const res = await apiFetch("/pairing-tokens/" + encodeURIComponent(scanPollToken));
      const data = await res.json().catch(() => ({}));
      if (!res.ok || !data || !Array.isArray(data.pairedDevices)) return; // 下次重试
      const fresh = data.pairedDevices.slice(scanPollBaseline);
      if (fresh.length > 0) {
        scanPollBaseline = data.pairedDevices.length;
        stopScanPoll();
        clearScanCountdown();
        closeScanConnectModal();
        openDeviceNameModal(data.app, fresh[fresh.length - 1].did);
        return;
      }
      if (data.expiresAt && new Date(data.expiresAt).getTime() <= Date.now()) {
        stopScanPoll(); // 过期后倒计时会提示刷新，轮询即可停止
      }
    } catch (_) { /* 瞬时错误：下个 tick 重试 */ }
  };
  tick();
  scanPollTimer = setInterval(tick, SCAN_POLL_MS);
}

function stopScanPoll() {
  if (scanPollTimer) { clearInterval(scanPollTimer); scanPollTimer = null; }
  scanPollToken = null;
}

/// 当前待命名设备（扫码注册完成、二维码弹窗已关）。
let namingDevice = null;

function openDeviceNameModal(app, did) {
  namingDevice = { app, did };
  $("deviceNameError").classList.add("hidden");
  $("deviceNameDid").textContent = "did：" + did;
  $("deviceNameInput").value = "";
  $("deviceNameModal").classList.remove("hidden");
  $("deviceNameInput").focus();
}

function closeDeviceNameModal() {
  namingDevice = null;
  $("deviceNameModal").classList.add("hidden");
}

/// 保存设备名 → 跳转该设备的请求列表页（#/device/{app}/{did}）开始看抓包。
async function submitDeviceName() {
  if (!namingDevice) return;
  $("deviceNameError").classList.add("hidden");
  const name = $("deviceNameInput").value.trim();
  if (!name) {
    $("deviceNameError").textContent = "设备名称不能为空";
    $("deviceNameError").classList.remove("hidden");
    return;
  }
  const { app, did } = namingDevice;
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(app) + "/" + encodeURIComponent(did), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) {
      const e = await res.json().catch(() => ({}));
      throw new Error(e.error || ("HTTP " + res.status));
    }
    closeDeviceNameModal();
    location.hash = "#/device/" + encodeURIComponent(app) + "/" + encodeURIComponent(did);
  } catch (err) {
    $("deviceNameError").textContent = "命名失败：" + err.message;
    $("deviceNameError").classList.remove("hidden");
  }
}
