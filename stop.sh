#!/usr/bin/env bash
# ============================================================================
# MockNetPack 服务器 关闭脚本
#
# 用法：./stop.sh
#   停止监听 4280/4290 的服务进程（先优雅退出，超时强制结束），并确认端口释放。
#   只处理真正的监听进程（LISTEN 状态），不触碰浏览器等客户端连接。
# 跨平台：macOS / Linux 均可运行，端口 PID 查询优先 ss，缺失时回退 lsof。
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

PORT=4280
ADMIN_PORT=4290

# 跨平台端口 PID 查询（仅 LISTEN 状态）：优先 ss，命令缺失时回退 lsof。
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

# 只取监听者，避免误伤客户端连接（如浏览器对端口的轮询连接）
PIDS="$(listener_pids "${PORT}"; listener_pids "${ADMIN_PORT}")"
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
STILL="$(listener_pids "${PORT}"; listener_pids "${ADMIN_PORT}")"
if [ -n "$STILL" ]; then
  echo "==> 进程未退出，强制结束: $(echo "$STILL" | sort -u | tr '\n' ' ')"
  for p in $(echo "$STILL" | sort -u); do
    kill -9 "$p" 2>/dev/null || true
  done
  sleep 1
fi

# 验证端口已释放
if [ -n "$(listener_pids "${PORT}")" ] || [ -n "$(listener_pids "${ADMIN_PORT}")" ]; then
  echo "ERR 端口 ${PORT}/${ADMIN_PORT} 仍有监听进程。" >&2
  exit 1
fi
echo "OK 服务已关闭，${PORT}/${ADMIN_PORT} 端口已释放。"
