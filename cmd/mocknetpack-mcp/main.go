// mocknetpack-mcp is the MCP (Model Context Protocol) gateway for MockNetPack
// : it wraps the admin API's high-frequency operations into 12 tools so an
// Agent platform (Codex, Claude Desktop, IDE agents, etc.) can natively inspect
// capture traffic and manage mock rules.
//
// Transport: HTTP only (Streamable HTTP, endpoint /mcp). Business-side Agents
// configure only a URL + Authorization header — no local binary. The gateway is
// a long-running process on the MockNetPack host.
//
// Auth model (per-account, passthrough): the gateway does NOT own a key and
// does NOT whitelist one. It requires every request to carry
// Authorization: Bearer <API Key>, injects that key into the request context
// (WithHTTPContextFunc), and each tool handler builds its admin client from the
// CALLER's key. Key validity and owner/admin permissions are therefore
// enforced by the mockd admin API per account — an invalid or unauthorized key
// surfaces as the mockd 401/403 error with "给 AI 的指引" text.
//
// Usage:
//
//	MOCKNETPACK_SERVER=http://127.0.0.1:4290 mocknetpack-mcp --http-addr :4291
//
// Config via environment:
//
//	MOCKNETPACK_SERVER   optional — mockd admin API base, default http://127.0.0.1:4290
//
// Error convention (给 AI 的指引): 401 → configure an API key; 404/409/403 →
// the APIError carries the cause AND the next action, so the model can
// self-correct.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/getmockd/mockd/pkg/mnpapi"
)

func main() {
	httpAddr := flag.String("http-addr", "", "HTTP (Streamable) MCP 网关监听地址，如 :4291（端点 /mcp）。必填")
	flag.Parse()

	base := os.Getenv("MOCKNETPACK_SERVER")
	if base == "" {
		base = mnpapi.DefaultBaseURL
	}

	if *httpAddr == "" {
		fmt.Fprintln(os.Stderr, "mocknetpack-mcp: --http-addr 必填（MCP 仅支持 HTTP 远程形态，业务侧只填 URL；接入方式见 tasks/ 目录下的业务侧 AI-MCP 远程接入指引）")
		flag.Usage()
		os.Exit(2)
	}

	srv := server.NewMCPServer(
		"mocknetpack",
		"0.12.0",
		server.WithInstructions("MockNetPack 抓包/Mock 平台。可查设备与抓包流量、从真实流量构造 Mock 规则、建分享链接。认证失败（401）时请确认 Authorization 头已配置为有效的长期 API Key。"),
	)
	registerTools(srv, base)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	log.SetOutput(os.Stderr)
	runHTTP(srv, base, *httpAddr, ctx)
}

// runHTTP serves the same 12 tools over Streamable HTTP at /mcp. It only
// enforces the presence of a well-formed "Bearer <key>" header (no whitelist);
// the caller's key is injected into the request context and validated by the
// mockd admin API per account inside each tool handler.
func runHTTP(srv *server.MCPServer, base, addr string, ctx context.Context) {
	mcpHTTP := server.NewStreamableHTTPServer(srv,
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, ctxBearerKey, keyFromAuthorization(r))
		}),
	)
	httpServer := &http.Server{Addr: addr, Handler: authGateway(mcpHTTP)}
	go func() {
		<-ctx.Done()
		// Bounded shutdown: don't block SIGTERM indefinitely on long-lived
		// connections (e.g. an SSE GET stream); start.sh's stop_port covers
		// the force-kill fallback.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("mocknetpack-mcp: HTTP server started on %s (endpoint /mcp)", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("mocknetpack-mcp: http: %v", err)
	}
}

// authGateway wraps the Streamable HTTP handler with a format-only gate:
//   - non-/mcp paths → 404 (the gateway serves exactly one endpoint)
//   - /mcp without a well-formed "Bearer <key>" header → 401 JSON-RPC
//
// Key validity is enforced per account by mockd inside tool handlers.
func authGateway(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32601,"message":"未找到端点：MCP 端点固定为 /mcp"}}`))
			return
		}
		if keyFromAuthorization(r) == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"未授权：请在 Authorization 头配置 Bearer <你的长效APIKey>（Web 登录 → 账号 → API Keys 创建）"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// registerTools wires the 12 MCP tools. Handlers build a per-request admin
// client from the caller's bearer key (base is the mockd API base URL). Every
// tool's description documents WHEN to use it, key parameter meanings, and
// error handling — the tool contract itself is the model-facing doc ( §三 E).
func registerTools(srv *server.MCPServer, base string) {
	srv.AddTool(toolListDevices(), mcpListDevices(base))
	srv.AddTool(toolGetDeviceTraffic(), mcpGetDeviceTraffic(base))
	srv.AddTool(toolGetTraffic(), mcpGetTraffic(base))
	srv.AddTool(toolCreateMockRuleFromTraffic(), mcpCreateMockRuleFromTraffic(base))
	srv.AddTool(toolCreateMockRule(), mcpCreateMockRule(base))
	srv.AddTool(toolCreateShare(), mcpCreateShare(base))
	srv.AddTool(toolGetShare(), mcpGetShare(base))
	srv.AddTool(toolSetMockRuleEnabled(), mcpSetMockRuleEnabled(base))
	srv.AddTool(toolUpdateMockRule(), mcpUpdateMockRule(base))
	srv.AddTool(toolListMockRules(), mcpListMockRules(base))
	srv.AddTool(toolGetMockRule(), mcpGetMockRule(base))
	srv.AddTool(toolDeleteMockRule(), mcpDeleteMockRule(base))
}
