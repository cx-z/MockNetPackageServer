#!/usr/bin/env bash
# ============================================================================
# MockNetPack 服务器 关闭脚本
#
# 用法：./stop.sh
#   停止监听 4280/4290 的服务进程（先优雅退出，超时强制结束），并确认端口释放。
#   只处理真正的监听进程（-sTCP:LISTEN），不触碰浏览器等客户端连接。
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

PORT=4280
ADMIN_PORT=4290

# 只取监听者，避免误伤客户端连接（如浏览器对端口的轮询连接）
PIDS="$(lsof -ti tcp:${PORT} -sTCP:LISTEN 2>/dev/null || true; lsof -ti tcp:${ADMIN_PORT} -sTCP:LISTEN 2>/dev/null || true)"
if [ -z "$PIDS" ]; then
  echo "没有检测到监听 ${PORT}/${ADMIN_PORT} 的服务，无需关闭。"
  exit 0
fi

echo "==> 停止监听 ${PORT}/${ADMIN_PORT} 的进程: $(echo "$PIDS" | sort -u | tr '\n' ' ')"
for p in $(echo "$PIDS" | sort -u); do
  kill "$p" 2>/dev/null || true
done
sleep 2

# 仍占用则强制结束
STILL="$(lsof -ti tcp:${PORT} -sTCP:LISTEN 2>/dev/null || true; lsof -ti tcp:${ADMIN_PORT} -sTCP:LISTEN 2>/dev/null || true)"
if [ -n "$STILL" ]; then
  echo "==> 进程未退出，强制结束: $(echo "$STILL" | sort -u | tr '\n' ' ')"
  for p in $(echo "$STILL" | sort -u); do
    kill -9 "$p" 2>/dev/null || true
  done
  sleep 1
fi

# 验证端口已释放
if lsof -ti tcp:${PORT} -sTCP:LISTEN >/dev/null 2>&1 || lsof -ti tcp:${ADMIN_PORT} -sTCP:LISTEN >/dev/null 2>&1; then
  echo "ERR 端口 ${PORT}/${ADMIN_PORT} 仍有监听进程。" >&2
  exit 1
fi
echo "OK 服务已关闭，${PORT}/${ADMIN_PORT} 端口已释放。"
