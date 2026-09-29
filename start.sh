#!/usr/bin/env bash
# ============================================================================
# MockNetPack 服务器 启动/重启脚本（幂等：已在运行则先停止，再启动）
#
# 用法：
#   ./start.sh          启动服务器；若已在运行则自动重启（数据保留）
#   ./start.sh rebuild  强制重新编译二进制后再启动
#
# 端口约定（见 .cursorrules）：mock=4280 / admin=4290（永不 8080）。
# 数据目录：mockd-data/（git 忽略，本机运行时数据）。注意：指定 --data-dir 时
#   必须显式传 --port/--admin-port，否则 mockd 默认把端口偏移到 14280/14290。
# 二进制：bin/mockd-bin（git 忽略；源码有更新会自动重新编译，或显式 rebuild）
# 鉴权：默认 auth 模式（不带 --no-auth）——/mocknetpack 与 /api/v1 走账号登录，
#   上游管理面（/mocks /workspaces 等）由 api-key 保护，首次启动自动生成并
#   打印到 stderr、存至 ~/.local/share/mockd/admin-api-key。
# 跨平台：macOS / Linux 均可运行。端口 PID 查询优先用 ss（Linux iproute2 自带），
#   命令缺失时回退 lsof（macOS 自带）；git pull 后直接 ./start.sh 即可，
#   会检测 cmd/internal/pkg 下源码与 go.mod/go.sum 是否有更新而自动重编译。
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

BIN="bin/mockd-bin"
DATA_DIR="mockd-data"
PORT=4280
ADMIN_PORT=4290
PID_FILE="/tmp/mockd-web.pid"

# daemon 日志：--detach 模式下所有输出写入 ~/.mockd/daemon.log，且每次启动都会被
# O_TRUNC 清空重写。重启前将上一次运行的日志归档为带时间戳的副本，便于回溯
# 上一个版本的行为。设置 ARCHIVE_DAEMON_LOG=0 可关闭归档。
DAEMON_LOG="${DAEMON_LOG:-$HOME/.mockd/daemon.log}"

# Runtime app catalog (git-ignored, hot-reloaded by loadAllowedApps): absolute
# path so the daemon finds it regardless of its working directory. Add real
# bundle ids there (one per line) — no restart needed.
export MOCKD_ALLOWED_APPS_FILE="${MOCKD_ALLOWED_APPS_FILE:-$(pwd)/.allowed-apps.local}"

# ---------------------------------------------------------------------------
# 跨平台端口 PID 查询（macOS / Linux）
#   port_pids      端口上任意连接的 PID（清理残留用，与原 lsof -ti 语义一致）
#   listener_pids  仅 LISTEN 状态的 PID（确认服务就绪/优雅关停用）
# 优先用 ss（Linux 默认带），命令缺失时回退 lsof（macOS 默认带）。
# ---------------------------------------------------------------------------
port_pids() {
  local pids
  if command -v ss >/dev/null 2>&1; then
    pids="$(ss -tnpH "sport = :$1" 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | sort -un)"
    if [ -n "$pids" ]; then
      printf '%s\n' "$pids"
      return 0
    fi
  fi
  # 无匹配时 lsof 返回 1，这里 || true 保证函数总是成功退出（配合 set -e）
  lsof -ti tcp:"$1" 2>/dev/null || true
}

listener_pids() {
  local pids
  if command -v ss >/dev/null 2>&1; then
    pids="$(ss -ltnpH "sport = :$1" 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | sort -un)"
    if [ -n "$pids" ]; then
      printf '%s\n' "$pids"
      return 0
    fi
  fi
  # 无匹配时 lsof 返回 1，这里 || true 保证函数总是成功退出（配合 set -e）
  lsof -ti tcp:"$1" -sTCP:LISTEN 2>/dev/null || true
}

# 是否需要重新编译：二进制缺失、任一 Go 源码（不含测试）或依赖清单比二进制新，
# 或显式 rebuild。
needs_build() {
  [ ! -x "$BIN" ] && return 0
  if find cmd internal pkg -type f -name '*.go' ! -name '*_test.go' -newer "$BIN" -print -quit 2>/dev/null | grep -q .; then
    return 0
  fi
  [ go.mod -nt "$BIN" ] && return 0
  [ go.sum -nt "$BIN" ] && return 0
  return 1
}

# 归档上一次运行的守护日志（重启前调用；ARCHIVE_DAEMON_LOG=0 关闭）。
# 只在旧进程停止后调用，保证归档的是完整快照；无日志/空文件时跳过。
archive_daemon_log() {
  [ "${ARCHIVE_DAEMON_LOG:-1}" = "0" ] && return 0
  [ -s "$DAEMON_LOG" ] || return 0
  local ts
  ts="$(date +%Y%m%d-%H%M%S)"
  cp "$DAEMON_LOG" "${DAEMON_LOG}.${ts}"
  echo "==> 归档守护日志: ${DAEMON_LOG}.${ts}"
}

# 1) 确保二进制存在或按需重建（git pull 后源码更新会自动触发）
if [ "${1:-}" = "rebuild" ] || needs_build; then
  echo "==> 检测到源码更新/强制 rebuild，编译 $BIN ..."
  go build -o "$BIN" ./cmd/mockd
fi

# 2) 停止旧实例：先停本脚本管理的进程，再清理仍占用 4280/4290 的残留
echo "==> 停止旧实例（如有）..."
pkill -f "$BIN start" 2>/dev/null || true
OLD_PIDS="$(port_pids "$PORT"; port_pids "$ADMIN_PORT")"
if [ -n "$OLD_PIDS" ]; then
  echo "==> 停止占用 ${PORT}/${ADMIN_PORT} 的旧进程: $(echo "$OLD_PIDS" | tr '\n' ' ')"
  for p in $OLD_PIDS; do kill "$p" 2>/dev/null || true; done
  sleep 2
  STILL="$(port_pids "$PORT"; port_pids "$ADMIN_PORT")"
  if [ -n "$STILL" ]; then
    echo "==> 旧进程未退出，强制结束: $(echo "$STILL" | tr '\n' ' ')"
    for p in $STILL; do kill -9 "$p" 2>/dev/null || true; done
    sleep 1
  fi
fi

# 2.5) 旧实例已停止：归档上一次运行的守护日志（新进程启动时会截断重建）
archive_daemon_log

# 3) 后台启动
echo "==> 启动 mock server（data-dir=${DATA_DIR}, mock=${PORT}, admin=${ADMIN_PORT}）..."
"$BIN" start --detach --pid-file "$PID_FILE" --data-dir "$DATA_DIR" \
  --admin-port "$ADMIN_PORT" --port "$PORT" \
  --capture-heartbeat-timeout 60

# 4) 等待端口就绪（最多 20s）
for i in $(seq 1 20); do
  PID="$(listener_pids "$PORT" | head -1)"
  if [ -n "$PID" ]; then
    echo "OK 服务器已就绪:"
    echo "    mock    http://localhost:$PORT"
    echo "    admin   http://localhost:$ADMIN_PORT"
    echo "    pid     $PID"
    exit 0
  fi
  sleep 1
done
echo "ERR 启动失败：${PORT} 端口 20 秒内未就绪。" >&2
exit 1
