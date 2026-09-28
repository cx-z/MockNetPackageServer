#!/usr/bin/env bash
# ============================================================================
# MockNetPack 服务器 启动/重启脚本（幂等：已在运行则先停止，再启动）
#
# 用法：
#   ./start.sh          启动服务器；若已在运行则自动重启（数据保留）
#   ./start.sh rebuild  重新编译二进制后再启动（代码变更后使用）
#
# 端口约定（见 .cursorrules）：mock=4280 / admin=4290（永不 8080）。
# 数据目录：.smoke-data-m711（当前在用）。注意：指定 --data-dir 时必须显式
#   传 --port/--admin-port，否则 mockd 默认把端口偏移到 14280/14290。
# 二进制：/tmp/mockd-web（首次执行自动编译；代码变更后执行 ./start.sh rebuild）
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

BIN="/tmp/mockd-web"
DATA_DIR=".smoke-data-m711"
PORT=4280
ADMIN_PORT=4290
PID_FILE="/tmp/mockd-web.pid"

# Runtime app catalog (git-ignored, hot-reloaded by loadAllowedApps): absolute
# path so the daemon finds it regardless of its working directory. Add real
# bundle ids there (one per line) — no restart needed.
export MOCKD_ALLOWED_APPS_FILE="${MOCKD_ALLOWED_APPS_FILE:-$(pwd)/.allowed-apps.local}"

# 1) 确保二进制存在（首次）或按需重建
if [ ! -x "$BIN" ] || [ "${1:-}" = "rebuild" ]; then
  echo "==> 编译 $BIN ..."
  go build -o "$BIN" ./cmd/mockd
fi

# 2) 停止旧实例：先停本脚本管理的进程，再清理仍占用 4280/4290 的残留
echo "==> 停止旧实例（如有）..."
pkill -f "$BIN start" 2>/dev/null || true
OLD_PIDS="$(lsof -ti tcp:$PORT 2>/dev/null || true; lsof -ti tcp:$ADMIN_PORT 2>/dev/null || true)"
if [ -n "$OLD_PIDS" ]; then
  echo "==> 停止占用 ${PORT}/${ADMIN_PORT} 的旧进程: $(echo "$OLD_PIDS" | tr '\n' ' ')"
  for p in $OLD_PIDS; do kill "$p" 2>/dev/null || true; done
  sleep 2
  STILL="$(lsof -ti tcp:$PORT 2>/dev/null || true; lsof -ti tcp:$ADMIN_PORT 2>/dev/null || true)"
  if [ -n "$STILL" ]; then
    echo "==> 旧进程未退出，强制结束: $(echo "$STILL" | tr '\n' ' ')"
    for p in $STILL; do kill -9 "$p" 2>/dev/null || true; done
    sleep 1
  fi
fi

# 3) 后台启动
echo "==> 启动 mock server（data-dir=${DATA_DIR}, mock=${PORT}, admin=${ADMIN_PORT}）..."
"$BIN" start --detach --pid-file "$PID_FILE" --data-dir "$DATA_DIR" \
  --admin-port "$ADMIN_PORT" --port "$PORT" \
  --capture-heartbeat-timeout 60 --no-auth

# 4) 等待端口就绪（最多 20s）
for i in $(seq 1 20); do
  PID="$(lsof -ti tcp:$PORT 2>/dev/null | head -1 || true)"
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
