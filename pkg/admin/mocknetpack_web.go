// MockNetPack Web UI 静态资源服务。
//
// Web 独立实现（D1，见 tasks/M1-mockd改造可行性分析.md §7）：不依赖
// mockd 内置 dashboard（前端闭源）。本路由把磁盘目录（默认
// "web/mocknetpack" 相对工作目录）挂到 admin :4290 的 /mocknetpack/ 下，
// 与 capture API (/api/v1) 同源，天然免 CORS。开发期改页面即刷新生效，
// 无需重新编译。

package admin

import (
	"net/http"
)

// defaultMockNetPackWebDir is the default directory serving the MockNetPack
// web UI, relative to the server working directory.
const defaultMockNetPackWebDir = "web/mocknetpack"

// registerMockNetPackWeb serves the MockNetPack web UI. API routes registered
// in registerRoutes take priority (more specific ServeMux patterns); anything
// under /mocknetpack/ falls through to the static file server.
func (a *API) registerMockNetPackWeb(mux *http.ServeMux) {
	dir := a.mockNetPackWebDir
	if dir == "" {
		dir = defaultMockNetPackWebDir
	}
	fileServer := http.StripPrefix("/mocknetpack/", http.FileServer(http.Dir(dir)))

	// 全局 SecurityHeadersMiddleware 的严格 "default-src 'self'"
	// 会拦截详情页/分享页的 data: 图片预览（base64 内联），这里对
	// MockNetPack Web 覆盖为放行 data: 图片的 CSP；style 放行 inline
	// （前端细节样式内联），font 放行 data:（图标字体），其余保持同源。
	mux.Handle("GET /mocknetpack/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:")
		fileServer.ServeHTTP(w, r)
	}))
}
