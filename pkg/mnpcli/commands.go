package mnpcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/mnpapi"
)

// cliCtx carries the parsed global config into every subcommand.
type cliCtx struct {
	client *mnpapi.Client
	stdout io.Writer
	stderr io.Writer
}

// newFlagSet creates a FlagSet that reports usage errors to stderr and never
// aborts the process (the caller decides the exit code).
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// usageErr writes the flagset usage and returns exit code 2.
func usageErr(fs *flag.FlagSet, msg string) int {
	if msg != "" {
		fmt.Fprintln(fs.Output(), "usage error:", msg)
	}
	fs.Usage()
	return 2
}

// reorderArgs moves bare positional arguments to the end so Go's flag parser
// (which stops at the first non-flag token) still sees every flag: the CLI
// accepts `rule create-from-traffic t1 --app a --note n` exactly like
// `--app a --note n t1`. Flag values (including multi-value --header) are kept
// attached to their flags.
func reorderArgs(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
			if !strings.Contains(a, "=") {
				switch a {
				case "--server", "--api-key", "--header", "-o", "--app", "--did",
					"--method", "--path", "--status", "--body", "--note",
					"--keyword", "--scheme", "--since", "--limit":
					if i+1 < len(args) {
						flags = append(flags, args[i+1])
						i++
					}
				}
			}
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// 默认输出失败：业务错误以 JSON 形式写 stderr 并返回 1
func bizErr(w io.Writer, err error) int {
	writeJSON(w, map[string]any{"error": "business_error", "message": err.Error()})
	return 1
}

// ---------------------------------------------------------------------------
// devices
// ---------------------------------------------------------------------------

func runDevices(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Fprintln(stdout, "用法：mocknetpack devices list")
		return 0
	}
	if args[0] != "list" {
		fmt.Fprintln(stderr, "usage error: unknown devices subcommand", args[0])
		return 2
	}
	fs := newFlagSet("devices list", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	if err := fs.Parse(reorderArgs(args[1:])); err != nil {
		return 2
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{"devices": devices, "total": len(devices)})
	return 0
}

// ---------------------------------------------------------------------------
// traffic
// ---------------------------------------------------------------------------

func runTraffic(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage error: traffic requires a subcommand (list/export/get)")
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprintln(stdout, "用法：mocknetpack traffic list|export|get ...")
		return 0
	case "list":
		return runTrafficList(ctx, args[1:], stdout, stderr, defServer, defKey)
	case "export":
		return runTrafficExport(ctx, args[1:], stdout, stderr, defServer, defKey)
	case "get":
		return runTrafficGet(ctx, args[1:], stdout, stderr, defServer, defKey)
	default:
		fmt.Fprintln(stderr, "usage error: unknown traffic subcommand", args[0])
		return 2
	}
}

// parseGlobals registers the --server/--api-key flags shared by every
// subcommand and preloads them with defaults parsed from the command prefix.
// Returns pointers so the values are read AFTER fs.Parse.
func parseGlobals(fs *flag.FlagSet, defServer, defKey string) (server, key *string) {
	server = fs.String("server", mnpapi.DefaultBaseURL, "MockNetPack 服务地址")
	key = fs.String("api-key", "", "长期 API Key（或用 MOCKNETPACK_API_KEY 环境变量）")
	if defServer != "" {
		_ = fs.Set("server", defServer)
	}
	if defKey != "" {
		_ = fs.Set("api-key", defKey)
	}
	return server, key
}

func makeClient(stderr io.Writer, server, key string) (*mnpapi.Client, int) {
	if key == "" {
		key = apiKeyFromEnv()
	}
	if key == "" {
		fmt.Fprintln(stderr, "缺少 API Key：请用 --api-key 或环境变量 MOCKNETPACK_API_KEY 配置（Web 登录 → 账号 → API Keys 创建）")
		fmt.Fprintln(stderr, "安全配置（不回显明文）：export MOCKNETPACK_API_KEY=$(security find-generic-password -s mocknetpack -w) 或写入 chmod 600 的文件后 source")
		return nil, 2
	}
	return mnpapi.NewClient(server, key), 0
}

// trafficQueryArgs builds the shared --app/--did/filter flags for list/export.
func trafficQueryArgs(fs *flag.FlagSet) (app, did *string, method, scheme, keyword *string, status *int, since *int64, limit *int, full *bool) {
	app = fs.String("app", "", "应用标识（bundle id）")
	did = fs.String("did", "", "设备标识")
	method = fs.String("method", "", "HTTP 方法过滤")
	scheme = fs.String("scheme", "", "URL scheme 过滤")
	keyword = fs.String("keyword", "", "URL 子串过滤")
	status = fs.Int("status", 0, "响应状态码过滤")
	since = fs.Int64("since", 0, "增量游标（只取 seq 大于该值的条目）")
	limit = fs.Int("limit", 100, "分页大小")
	full = fs.Bool("full", false, "输出完整字段（默认 compact）")
	return
}

func runTrafficList(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("traffic list", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did, method, scheme, keyword, status, since, limit, full := trafficQueryArgs(fs)
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	q := mnpapi.TrafficQuery{
		Method: *method, Scheme: *scheme, Keyword: *keyword,
		Since: *since, Limit: *limit, Compact: !*full,
	}
	if *status != 0 {
		s := *status
		q.Status = &s
	}
	res, err := c.GetDeviceTraffic(ctx, *app, *did, q)
	if err != nil {
		return bizErr(stderr, err)
	}
	if res.SessionID == "" {
		writeJSON(stdout, map[string]any{
			"sessionId": "", "entries": []any{}, "total": 0,
			"message": "该设备没有任何抓包会话（可能未在抓包中）。请先激活会话或确认 app/did。",
		})
		return 0
	}
	writeJSON(stdout, map[string]any{"sessionId": res.SessionID, "entries": res.Entries, "total": res.Total})
	return 0
}

func runTrafficGet(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("traffic get", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "traffic get 需要一个 trafficId 参数")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	e, err := c.GetTraffic(ctx, fs.Arg(0))
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, e)
	return 0
}

func runTrafficExport(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("traffic export", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did, _, _, _, _, since, limit, _ := trafficQueryArgs(fs)
	outFile := fs.String("o", "", "输出文件路径（必填）")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	if *outFile == "" {
		return usageErr(fs, "-o <file.json> 必填")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	q := mnpapi.TrafficQuery{Since: *since, Limit: *limit, Compact: true}
	res, err := c.GetDeviceTraffic(ctx, *app, *did, q)
	if err != nil {
		return bizErr(stderr, err)
	}
	if err := writeTrafficFile(*outFile, res.Entries); err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{
		"file": *outFile, "sessionId": res.SessionID, "total": res.Total, "exported": len(res.Entries),
	})
	return 0
}

// writeTrafficFile writes the compact entries as an indented JSON array so the
// file is directly consumable by jq/grep.
func writeTrafficFile(path string, entries []capture.TrafficEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ---------------------------------------------------------------------------
// share
// ---------------------------------------------------------------------------

func runShare(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage error: share requires create/get")
		return 2
	}
	switch args[0] {
	case "create":
		fs := newFlagSet("share create", stderr)
		server, key := parseGlobals(fs, defServer, defKey)
		if err := fs.Parse(reorderArgs(args[1:])); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			return usageErr(fs, "share create <trafficId>")
		}
		c, code := makeClient(stderr, *server, *key)
		if code != 0 {
			return code
		}
		s, err := c.CreateShare(ctx, fs.Arg(0))
		if err != nil {
			return bizErr(stderr, err)
		}
		writeJSON(stdout, map[string]any{
			"shareId": s.ShareID, "url": s.URL, "expiresAt": s.ExpiresAt.Format(time.RFC3339),
		})
		return 0
	case "get":
		fs := newFlagSet("share get", stderr)
		server, key := parseGlobals(fs, defServer, defKey)
		if err := fs.Parse(reorderArgs(args[1:])); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			return usageErr(fs, "share get <shareId>")
		}
		c, code := makeClient(stderr, *server, *key)
		if code != 0 {
			return code
		}
		s, err := c.GetShare(ctx, fs.Arg(0))
		if err != nil {
			return bizErr(stderr, err)
		}
		writeJSON(stdout, s)
		return 0
	default:
		fmt.Fprintln(stderr, "usage error: unknown share subcommand", args[0])
		return 2
	}
}

// ---------------------------------------------------------------------------
// rule
// ---------------------------------------------------------------------------

func runRule(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage error: rule requires a subcommand (create-from-traffic/create/set-enabled/update)")
		return 2
	}
	switch args[0] {
	case "create-from-traffic":
		return runRuleCreateFromTraffic(ctx, args[1:], stdout, stderr, defServer, defKey)
	case "create":
		return runRuleCreate(ctx, args[1:], stdout, stderr, defServer, defKey)
	case "set-enabled":
		return runRuleSetEnabled(ctx, args[1:], stdout, stderr, defServer, defKey)
	case "update":
		return runRuleUpdate(ctx, args[1:], stdout, stderr, defServer, defKey)
	default:
		fmt.Fprintln(stderr, "usage error: unknown rule subcommand", args[0])
		return 2
	}
}

func ruleDeviceArgs(fs *flag.FlagSet) (app, did *string) {
	return fs.String("app", "", "应用标识（bundle id）"), fs.String("did", "", "设备标识")
}

func runRuleCreateFromTraffic(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("rule create-from-traffic", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did := ruleDeviceArgs(fs)
	note := fs.String("note", "", "规则备注（必填、非空白）")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	if *note == "" || strings.TrimSpace(*note) == "" {
		return usageErr(fs, "--note 必填且不能为空白（与 Web/MCP 语义一致）")
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "rule create-from-traffic <trafficId>")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	rule, err := c.CreateMockRuleFromTraffic(ctx, *app, *did, fs.Arg(0), *note)
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{
		"rule": rule, "enabled": rule.Enabled, "effective": rule.Effective,
		"hint": "规则已创建且默认停用；命中请执行 rule set-enabled --enable",
	})
	return 0
}

func runRuleCreate(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("rule create", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did := ruleDeviceArgs(fs)
	method := fs.String("method", "", "HTTP 方法（匹配键）")
	path := fs.String("path", "", "URL 路径（匹配键）")
	status := fs.Int("status", 200, "Mock 回包状态码")
	var headers multiFlag
	fs.Var(&headers, "header", "响应头（可重复，\"K: v\" 格式）")
	body := fs.String("body", "", "Mock 回包体")
	note := fs.String("note", "", "规则备注（必填、非空白）")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	if *method == "" || *path == "" {
		return usageErr(fs, "--method 与 --path 必填")
	}
	if *note == "" || strings.TrimSpace(*note) == "" {
		return usageErr(fs, "--note 必填且不能为空白（与 Web/MCP 语义一致）")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	in := &capture.MockRuleInput{
		Method: *method, Path: *path,
		Response: capture.MockResponse{StatusCode: *status, Headers: headers.toMap(), Body: *body},
		Note:     *note,
	}
	rule, err := c.CreateMockRule(ctx, *app, *did, in)
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{
		"rule": rule, "enabled": rule.Enabled, "effective": rule.Effective,
		"hint": "规则已创建且默认停用；命中请执行 rule set-enabled --enable",
	})
	return 0
}

func runRuleSetEnabled(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("rule set-enabled", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did := ruleDeviceArgs(fs)
	enable := fs.Bool("enable", false, "启用规则")
	disable := fs.Bool("disable", false, "停用规则")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	if *enable == *disable {
		return usageErr(fs, "必须且只能指定 --enable 或 --disable 之一")
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "rule set-enabled <ruleId>")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	ruleID := fs.Arg(0)
	// The server PUT is a full replace: a toggle must carry the existing
	// canned response (statusCode 100–599 validated) — read-modify-write,
	// same as MCP update_mock_rule.
	existing, err := c.GetMockRule(ctx, *app, *did, ruleID)
	if err != nil {
		return bizErr(stderr, err)
	}
	on := *enable
	in := &capture.UpdateMockRuleInput{
		Response: existing.Response,
		Enabled:  &on,
		Note:     existing.Note,
	}
	rule, err := c.UpdateMockRule(ctx, *app, *did, ruleID, in)
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{"rule": rule, "enabled": rule.Enabled, "effective": rule.Effective})
	return 0
}

func runRuleUpdate(ctx context.Context, args []string, stdout, stderr io.Writer, defServer, defKey string) int {
	fs := newFlagSet("rule update", stderr)
	server, key := parseGlobals(fs, defServer, defKey)
	app, did := ruleDeviceArgs(fs)
	status := fs.Int("status", 0, "新的 Mock 回包状态码（0=不修改）")
	var headers multiFlag
	fs.Var(&headers, "header", "新的响应头（可重复，\"K: v\" 格式）")
	body := fs.String("body", "", "新的回包体")
	note := fs.String("note", "", "规则备注（改动回包内容时必填）")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if *app == "" || *did == "" {
		return usageErr(fs, "--app 与 --did 必填")
	}
	if fs.NArg() != 1 {
		return usageErr(fs, "rule update <ruleId>")
	}
	c, code := makeClient(stderr, *server, *key)
	if code != 0 {
		return code
	}
	ruleID := fs.Arg(0)
	existing, err := c.GetMockRule(ctx, *app, *did, ruleID)
	if err != nil {
		return bizErr(stderr, err)
	}
	resp := existing.Response
	if *status != 0 {
		resp.StatusCode = *status
	}
	if len(headers) > 0 {
		resp.Headers = headers.toMap()
	}
	if *body != "" {
		resp.Body = *body
	}
	in := &capture.UpdateMockRuleInput{Response: resp, Note: *note}
	rule, err := c.UpdateMockRule(ctx, *app, *did, ruleID, in)
	if err != nil {
		return bizErr(stderr, err)
	}
	writeJSON(stdout, map[string]any{"rule": rule, "enabled": rule.Enabled, "effective": rule.Effective})
	return 0
}

// multiFlag collects repeated flags (e.g. --header "K: v" --header "K2: v2").
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// toMap converts "K: v" entries into a header map; malformed entries are
// skipped (usage error surfaces via missing keys, kept simple).
func (m multiFlag) toMap() map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for _, kv := range m {
		if i := strings.Index(kv, ":"); i > 0 {
			out[strings.TrimSpace(kv[:i])] = strings.TrimSpace(kv[i+1:])
		}
	}
	return out
}
