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
	mux.Handle("GET /mocknetpack/",
		http.StripPrefix("/mocknetpack/", http.FileServer(http.Dir(dir))))
}
