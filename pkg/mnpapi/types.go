package mnpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// ShareCreated is the response of POST /api/v1/shares (contract
// CreateShareResponse): the share ID, the page path and the 7-day expiry.
type ShareCreated struct {
	ShareID   string    `json:"shareId"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ShareSnapshot is the response of GET /api/v1/shares/{id} (contract
// ShareSnapshot): a read-only deep copy of the traffic entry.
type ShareSnapshot struct {
	ShareID   string                `json:"shareId"`
	CreatedAt time.Time             `json:"createdAt"`
	ExpiresAt time.Time             `json:"expiresAt"`
	Entry     *capture.TrafficEntry `json:"entry"`
}

// APIError is a non-2xx response from the MockNetPack server. Message is the
// AI-facing guidance text ("给 AI 的指引"): it explains the cause AND the next
// action so an Agent can self-correct.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the machine error code from the server (e.g. "not_found").
	Code string
	// Message is the guidance text.
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("MockNetPack API %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("MockNetPack API %d (%s)", e.Status, e.Code)
}

// Guide builds the AI-facing message for a status from the server's message
// when available, else from the status semantics.
func Guide(status int, code, serverMsg string) string {
	switch status {
	case http.StatusUnauthorized:
		return "认证失败（401）：未提供或无效的 API Key。请先创建 API Key（Web 登录 → 账号 → API Keys，或 POST /api/v1/auth/keys）并配置 MOCKNETPACK_API_KEY 后重试。"
	case http.StatusForbidden:
		if serverMsg != "" {
			return serverMsg + "（仅规则 owner 或 admin 可执行该操作；确认 API Key 对应账号的权限）"
		}
		return "权限不足（403）：仅规则 owner 或 admin 可执行该操作。请确认 API Key 对应账号是规则创建者或管理员。"
	case http.StatusNotFound:
		if serverMsg != "" {
			return serverMsg + "（资源不存在或已过期：分享 7 天过期、会话保留期 48h 已过、或 id 拼写有误。先查询确认 id 有效再重试）"
		}
		return "未找到（404）：资源不存在或已过期（分享 7 天过期 / 会话保留期已过 / id 错误）。请先查询确认 id 有效。"
	case http.StatusConflict:
		return "互斥冲突（409）：同一接口（Method+Path）已存在启用中的 Mock 规则。" + serverMsg + " 请先停用现有规则（set_mock_rule_enabled / rule set-enabled --disable）或编辑该规则，再重试。"
	default:
		if serverMsg != "" {
			return serverMsg
		}
		return "请求失败（HTTP " + fmt.Sprintf("%d", status) + "）。"
	}
}
