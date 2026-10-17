# Changelog

本仓库只保留最终态，不记录开发过程。逐版本（0.1.0–0.7.1）的历史记录已移除，以下为当前版本的能力概述。

## 当前版本

MockNetPack 中央 Mock 服务器（fork of getmockd/mockd），API 契约版本 v0.14.0（`openapi/mocknetpack.yaml`）。

### 核心能力

- **HTTP Mock 规则**：按 method/path 匹配，支持 canned response、note、enabled 开关与 `sinceVersion` 增量拉取；按 App+设备隔离。
- **流量抓取**：按会话组织实时抓包，48h 保留窗口；每会话独立存档 `mockd-data/traffic/<sid>.json`，跨重启恢复。
- **长连接日志**：业务级长连接采集（与 HTTP 流量独立额度、独立存档 `longconn/<sid>.json`），记录携带采集方式标识（adapter / API 注入）；每会话上限 1000 条，48h 窗口。
- **会话生命周期与观察者租约**：`POST /sessions` 创建、心跳超时清扫、`/viewers` 租约（120s TTL）；最后一个观察者释放后结束会话并停用全设备规则。
- **Push Mock**：独立的 push mock 规则模型与版本号（channelId 必选 + bizType + payload 匹配条件）；「模拟推送」以 pending 指令队列下发（随 SDK 心跳拉取、ack 消费），支持同域多模板共存。
- **MCP Server**：26 个 MCP 工具，覆盖设备/会话/流量/规则/分享/长连接日志/push mock 模拟推送。
- **鉴权**：开放注册仅建 dev 角色；admin 仅能通过 CLI bootstrap；长效 API Key（服务端只存 SHA-256 哈希）与会话 token 并存。
- **Web 控制台**：实时流量、历史查询（服务端过滤 + 分页）、规则管理、长连接日志页签、推送模板与模拟推送、单条分享（7 天 TTL）。
- **QR 配对**：扫码注册设备并复用既有 did。
- **可观测性**：Loki/Grafana 等 observability 组件与基准测试（`benchmarks/`）。
