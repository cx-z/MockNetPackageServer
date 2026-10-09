# MockNetPack Server — AI Agent 约束

MockNetPack 的中央 Mock 服务端（Go，fork 自 getmockd/mockd），与 iOS Debug SDK、Web 管理台共同组成抓包与 Mock 平台。产品背景与知识库：`knowledgebase/`（入口 `knowledgebase/overview.md`）、`ARCHITECTURE.md`。

## 提交 / 推送前必须通过的门禁

1. **Lint（golangci-lint）**
   - 本地命令：`golangci-lint run`。与 CI 完全一致：`.github/workflows/ci.yaml` 使用 `golangci/golangci-lint-action@v7`、`version: v2.11.2`、`args: --timeout=5m`，规则配置在 `.golangci.yml`。
   - 任何改动在提交 / 推送前必须 `golangci-lint run` 通过；CI 会在 push 时拦截失败。
   - 修 lint 问题只做最小改动（重命名、注释、格式），**不得改变任何运行逻辑**。
   - 禁止用行内 `//nolint` 绕过；若某条规则确属误报且影响整个项目，才允许修改 `.golangci.yml` 并说明理由。
   - 本机未安装时：`brew install golangci-lint`（或 `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.2` 对齐 CI 版本）。
2. **编译**：`go build ./...` 必须通过。
3. **测试**：`go test ./...` 必须通过。

## 代码风格约定

- 同一类型的所有方法接收者名必须一致（staticcheck ST1016）。现状约定：
  - `FileStore` 的方法接收者统一用 `fs`（如 `func (fs *FileStore) Traffic()`）。
  - 各子 store（`workspaceStore`、`mockStore`、`trafficStore` 等）统一用 `s`。
  - `fileTransaction` 统一用 `t`。
- 新增方法时沿用所属类型的既有接收者名，不要引入新写法。

## 命令速查

```bash
go build ./...      # 编译全部
go test ./...       # 全部测试
golangci-lint run   # lint 门禁（见上）
```
