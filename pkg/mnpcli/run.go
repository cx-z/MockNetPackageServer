// Package mnpcli implements the `mocknetpack` CLI : a
// machine-native channel for scripts and Agent tooling covering the 7
// high-frequency operations of MockNetPack.
//
// Conventions (方案 §三 D):
//   - stdout carries pure JSON (pipe-able); logs/errors go to stderr;
//   - exit codes: 0 success / 1 business error (server 4xx/5xx with its
//     message) / 2 usage error;
//   - config: --server (default http://127.0.0.1:4290) and --api-key, or the
//     MOCKNETPACK_API_KEY environment variable.
package mnpcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Version, BuildTime, ContractVersion are injected at build time via
//
//	-ldflags "-X github.com/getmockd/mockd/pkg/mnpcli.Version=… \
//	         -X github.com/getmockd/mockd/pkg/mnpcli.BuildTime=…"
//
// Defaults keep local dev builds usable without ldflags.
var (
	Version         = "dev"
	BuildTime       = "unknown"
	ContractVersion = "v0.12.0"
)

// Run executes the CLI with the given arguments (excluding the program name)
// and returns the process exit code. Output goes to stdout (JSON) and stderr
// (usage/logs/errors). Global flags may appear before the subcommand
// (`mocknetpack --server X --api-key Y traffic list ...`) or after it.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// Consume leading global flags so `mocknetpack --server <url> <command>`
	// works; subcommands re-parse them (with the same defaults) when repeated.
	var server, key string
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--server":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "usage error: --server needs a URL")
				return 2
			}
			server, args = args[1], args[2:]
		case "--api-key":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "usage error: --api-key needs a value")
				return 2
			}
			key, args = args[1], args[2:]
		case "-v", "--version":
			printVersion(stdout)
			return 0
		case "-h", "--help", "help":
			printUsage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unknown global flag %q\n\n", args[0])
			printUsage(stderr)
			return 2
		}
	}
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	// Pass the leading globals down as defaults: subcommands register the same
	// --server/--api-key flags and preload them with these values, so an
	// explicit flag anywhere on the command line wins over the leading one.
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "devices":
		return runDevices(ctx, rest, stdout, stderr, server, key)
	case "traffic":
		return runTraffic(ctx, rest, stdout, stderr, server, key)
	case "share":
		return runShare(ctx, rest, stdout, stderr, server, key)
	case "rule":
		return runRule(ctx, rest, stdout, stderr, server, key)
	case "version":
		printVersion(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// printVersion writes build identity as pure JSON (stdout convention).
func printVersion(w io.Writer) {
	writeJSON(w, map[string]string{
		"name":        "mocknetpack",
		"version":     Version,
		"contract":    ContractVersion,
		"buildTime":   BuildTime,
		"configPaths": "--server / MOCKNETPACK_SERVER, --api-key / MOCKNETPACK_API_KEY",
		"installHint": "构建: go build -o /usr/local/bin/mocknetpack ./cmd/mocknetpack (server 目录)",
	})
}

// printUsage writes the full command help to w.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `mocknetpack — MockNetPack 命令行通道

用法：
  mocknetpack [--server <url>] [--api-key <key>] <command> [args]

全局参数：
  --server <url>    MockNetPack 服务地址（默认 http://127.0.0.1:4290）
  --api-key <key>   长期 API Key；也可用环境变量 MOCKNETPACK_API_KEY
                    （创建：Web 登录 → 账号 → API Keys，或 POST /api/v1/auth/keys）

输出约定：
  stdout 纯 JSON（可管道 / jq）；日志与错误走 stderr；
  退出码：0 成功 / 1 业务错误（含服务端 4xx/5xx 说明）/ 2 用法错误

子命令：
  version                                        输出版本与契约信息（JSON）
  devices list                                   设备列表（含状态）
  traffic list --app <app> --did <did>           最近流量（默认 compact）
          [--method M] [--scheme S] [--keyword K] [--status N] [--since SEQ]
          [--limit N] [--full]
  traffic export --app <app> --did <did>         流量落盘（供 jq/grep 二次检索）
          [--since SEQ] [--limit N] -o <file.json>
  traffic get <trafficId>                        单条完整详情
  share create <trafficId>                       生成分享链接（7 天免登录）
  share get <shareId>                            读取分享快照
  rule create-from-traffic <trafficId>           从真实请求日志建规则
          --app <app> --did <did> --note <note>（必填）
  rule create --app <app> --did <did>            凭空创建规则（note 必填）
          --method M --path P [--status N] [--header "K: v"]... [--body B] [--note N]
  rule set-enabled <ruleId> --app <app> --did <did> --enable|--disable
  rule update <ruleId> --app <app> --did <did>   编辑回包（匹配键不可改）
          [--status N] [--header "K: v"]... [--body B] [--note N]

示例：
  MOCKNETPACK_API_KEY=mnpk_... mocknetpack traffic list --app com.example.app --did dev-1 --scheme https
  mocknetpack traffic export --app com.example.app --did dev-1 --since 42 -o traffic.json
`)
}

// writeJSON encodes v as the sole stdout payload.
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// apiKeyFromEnv returns MOCKNETPACK_API_KEY when set.
func apiKeyFromEnv() string {
	return os.Getenv("MOCKNETPACK_API_KEY")
}
