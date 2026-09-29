"use strict";

// MockNetPack web: device list, connect/disconnect, back-to-list and
// device ops (add/rename/delete). Pure move from app.js.
// ============================================================================
// 设备列表（M1.6）
// ============================================================================

async function loadDevices() {
  try {
    const res = await apiFetch("/devices");
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    render(data.devices || []);
    $("apiInfo").textContent = "API " + location.host + API;
  } catch (e) {
    $("apiInfo").textContent = "API 不可达";
    $("list").innerHTML = '<div class="empty">无法连接服务器：' + e.message + "</div>";
  }
}

function render(devices) {
  const order = { capturing: 0, idle: 1, offline: 2 };
  devices.sort((a, b) => (order[a.status] ?? 9) - (order[b.status] ?? 9)
      || new Date(b.lastSeenAt || 0) - new Date(a.lastSeenAt || 0));

  // M4：管理员视图在设备 cell 上展示注册账号（便于管理多用户设备）；
  // 普通开发者只看得到自己的设备，保持现状不展示。
  const isAdmin = authUser && authUser.role === "admin";

  const online = devices.filter((d) => d.status !== "offline").length;
  $("stats").textContent = `共 ${devices.length} 台设备 · ${online} 台在线 · 点击设备查看请求流`;

  if (devices.length === 0) {
    $("list").innerHTML = '<div class="empty">暂无设备。点击右上角「扫码连接」让手机 App 扫码接入。</div>';
    return;
  }

  $("list").innerHTML = "";
  for (const d of devices) {
    const st = STATUS[d.status] || STATUS.idle;
    const card = document.createElement("div");
    card.className = "card";
    card.dataset.app = d.app;
    card.dataset.did = d.did;
    card.title = "点击查看请求流";
    card.onclick = () => openDetail(d.app, d.did);

    const dot = document.createElement("span");
    dot.className = "dot " + st.cls;

    const meta = document.createElement("div");
    meta.className = "meta";
    meta.innerHTML =
      '<div class="name">' + esc(d.name || d.app) +
        ' <button class="link-btn rename-btn" type="button" title="重命名">✏️</button>' +
      '</div>' +
      '<div class="did"><span class="k">DID</span>' + esc(d.did) + "</div>" +
      '<div class="row2">' +
        '<span class="app-badge" title="App 标识（' + esc(d.app) + '）">' + esc(d.appName || d.app) + "</span>" +
        (isAdmin && d.owner ? '<span class="owner-badge" title="设备注册账号（管理视图）">账号 ' + esc(d.owner) + "</span>" : "") +
        '<span class="badge ' + st.cls + '">' + st.label + "</span>" +
        '<span>最后活跃：' + relTime(d.lastSeenAt) + "</span>" +
        (d.currentSession ? '<span>会话：' + esc(shortId(d.currentSession.id)) + "</span>" : "") +
      "</div>";

    // M7.2.2 rename button
    const renameBtn = meta.querySelector(".rename-btn");
    if (renameBtn) {
      renameBtn.onclick = (e) => { e.stopPropagation(); renameDevice(d); };
    }


    card.appendChild(dot);
    card.appendChild(meta);
    const delBtn = document.createElement("button");
    delBtn.className = "card-delete-btn";
    delBtn.textContent = "✕";
    delBtn.title = "删除设备";
    delBtn.style.cssText = "position:absolute;top:10px;right:10px;width:24px;height:24px;border:none;border-radius:4px;background:transparent;color:#e53e3e;padding:0;font-size:14px;font-weight:bold;cursor:pointer;display:flex;align-items:center;justify-content:center;opacity:0.7;transition:opacity 0.15s;";
    delBtn.onmouseenter = () => delBtn.style.opacity = "1";
    delBtn.onmouseleave = () => delBtn.style.opacity = "0.7";
    delBtn.onclick = (e) => { e.stopPropagation(); deleteDevice(d); };
    card.appendChild(delBtn);
    $("list").appendChild(card);
  }
  $("updated").textContent = "更新于 " + new Date().toLocaleTimeString("zh-CN");
}

async function connect(d) {
  try {
    const res = await apiFetch("/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app: d.app, did: d.did }),
    });
    const session = await res.json().catch(() => null);
    if (!res.ok) { showError((session && session.message) || "连接失败（HTTP " + res.status + "）"); return; }
    if (session && session.id) {
      await registerViewer(session, "设备列表页");
    }
    if (detail && detail.app === d.app && detail.did === d.did) {
      // M9：loadDetail 直接绑定当前会话并启动轮询，无需显式选中。
      await loadDetail();
    } else {
      await loadDevices();
    }
  } catch (e) {
    showError("连接失败：" + e.message);
  }
}

async function disconnect(d) {
  const s = d.currentSession;
  if (!s) return;
  try {
    const res = await apiFetch("/sessions/" + encodeURIComponent(s.id), { method: "DELETE" });
    if (!res.ok && res.status !== 404) { showError("断开失败（HTTP " + res.status + "）"); return; }
    stopViewer(s.id);
    if (detail && detail.sessionId === s.id) {
      // M9.4：会话已删，loadDetail 绑定空会话 → 停轮询；页面日志保留展示（离开页面才清空）。
      await loadDetail();
    } else {
      await loadDevices();
    }
  } catch (e) {
    showError("断开失败：" + e.message);
  }
}

function backToList() {
  stopTrafficPoll();
  if (detailPollTimer) { clearInterval(detailPollTimer); detailPollTimer = null; }
  if (detail && detail.sessionId) stopViewer(detail.sessionId);
  detail = null;
  pageLog = [];        // M9.4 诉求 4：返回设备列表清空页面日志
  trafficFilter = "";
  location.hash = "";
  $("detailView").classList.add("hidden");
  $("listView").classList.remove("hidden");
  // header 恢复
  $("backBtn").classList.add("hidden");
  $("detailApp").classList.add("hidden");
  $("mainTitle").classList.remove("hidden");
  loadDevices();
}

// M8.5: 删除设备（含其 mock rules）
async function deleteDevice(d) {
  if (!confirm("确定删除设备 " + (d.name || d.app) + "？\n\n将同时删除该设备的所有 Mock 规则。此操作不可撤销。")) return;
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(d.app) + "/" + encodeURIComponent(d.did), {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 204) throw new Error("HTTP " + res.status);
    await loadDevices();
  } catch (err) {
    alert("删除失败：" + err.message);
  }
}

async function renameDevice(d) {
  const cur = d.name || "";
  const next = window.prompt("设备新名称：", cur);
  if (next === null) return;
  const name = next.trim();
  if (!name) { window.alert("名称不能为空"); return; }
  try {
    const res = await apiFetch("/devices/" + encodeURIComponent(d.app) + "/" + encodeURIComponent(d.did), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) {
      const e = await res.json().catch(() => ({}));
      throw new Error(e.error || ("HTTP " + res.status));
    }
    await loadDevices();
  } catch (err) {
    window.alert("重命名失败：" + err.message);
  }
}


