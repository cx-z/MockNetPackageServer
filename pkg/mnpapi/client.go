// Package mnpapi is the shared HTTP client for the MockNetPack admin API
// : the MCP server and the `mocknetpack` CLI both talk to the server
// through it, so the two machine-native channels share one set of endpoint
// semantics, error handling and "for the AI" guidance.
//
// Authentication: every request carries `Authorization: Bearer <apiKey>`
// (long-lived API key). 401 responses surface as APIError with a
// self-explanatory message (configure an API key), 403/404/409 keep their
// ownership/conflict semantics from the server contract.
package mnpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// DefaultBaseURL is the local MockNetPack admin address (start.sh default).
const DefaultBaseURL = "http://127.0.0.1:4290"

// Client talks to one MockNetPack server instance.
type Client struct {
	// BaseURL is the server origin, e.g. http://127.0.0.1:4290.
	BaseURL string
	// APIKey is the long-lived API key (Authorization: Bearer <key>).
	APIKey string
	// HTTP is the underlying client; defaults to a client with a 60s
	// per-request timeout (prevents a hung server from hanging tool calls
	// forever). Callers may override after construction.
	HTTP *http.Client
}

// NewClient creates a Client with the given base URL and API key.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// do performs one authenticated request. body and out may be nil.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, u, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("decode response (status %d): %w", res.StatusCode, err)
			}
		}
		return nil
	}
	return ParseError(res.StatusCode, raw)
}

// ParseError converts a non-2xx response body into an *APIError with the
// server's {error, message} plus the AI-facing guidance text.
func ParseError(status int, raw []byte) error {
	var e apiErrorBody
	_ = json.Unmarshal(raw, &e) // non-JSON bodies fall back to the status line
	return &APIError{Status: status, Code: e.Error, Message: Guide(status, e.Error, e.Message)}
}

type apiErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// ============================================================================
// Endpoints
// ============================================================================

// ListDevices returns every device with its derived status.
func (c *Client) ListDevices(ctx context.Context) ([]capture.DeviceView, error) {
	var out struct {
		Devices []capture.DeviceView `json:"devices"`
		Total   int                  `json:"total"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/devices", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// TrafficQuery mirrors the  query parameters of the traffic list endpoint.
type TrafficQuery struct {
	Method  string
	Scheme  string
	Keyword string
	Status  *int
	From    *time.Time
	To      *time.Time
	Since   int64
	Limit   int
	Offset  int
	Compact bool
}

// TrafficResult is the traffic list of the most recent session of a device.
type TrafficResult struct {
	// SessionID is the session the entries were read from (useful context for
	// follow-up calls).
	SessionID string
	// Entries is the compact (or full) projection, in arrival order.
	Entries []capture.TrafficEntry
	// Total is the filtered count before paging.
	Total int
}

// GetDeviceTraffic implements the two-step orchestration
// GET /sessions?app&did → GET /sessions/{id}/traffic?<query>.
// The most recent session wins (capturing sessions first); a device with no
// session yields an empty result rather than an error — an Agent should read
// this as "no capture data yet, start a session first".
func (c *Client) GetDeviceTraffic(ctx context.Context, app, did string, q TrafficQuery) (*TrafficResult, error) {
	query := url.Values{}
	query.Set("app", app)
	query.Set("did", did)
	var sessions struct {
		Sessions []capture.CaptureSession `json:"sessions"`
		Total    int                      `json:"total"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/sessions", query, nil, &sessions); err != nil {
		return nil, err
	}
	if len(sessions.Sessions) == 0 {
		return &TrafficResult{}, nil
	}
	sid := sessions.Sessions[0].ID
	for _, s := range sessions.Sessions {
		if s.Status == capture.SessionStatusCapturing {
			sid = s.ID
			break
		}
	}

	p := url.Values{}
	if q.Method != "" {
		p.Set("method", q.Method)
	}
	if q.Scheme != "" {
		p.Set("scheme", q.Scheme)
	}
	if q.Keyword != "" {
		p.Set("keyword", q.Keyword)
	}
	if q.Status != nil {
		p.Set("statusCode", fmt.Sprintf("%d", *q.Status))
	}
	if q.From != nil {
		p.Set("from", q.From.Format(time.RFC3339))
	}
	if q.To != nil {
		p.Set("to", q.To.Format(time.RFC3339))
	}
	if q.Since > 0 {
		p.Set("since", fmt.Sprintf("%d", q.Since))
	}
	if q.Limit > 0 {
		p.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	if q.Offset > 0 {
		p.Set("offset", fmt.Sprintf("%d", q.Offset))
	}
	if q.Compact {
		p.Set("projection", "compact")
	}
	var out struct {
		Entries []capture.TrafficEntry `json:"entries"`
		Total   int                    `json:"total"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/sessions/"+sid+"/traffic", p, nil, &out); err != nil {
		return nil, err
	}
	return &TrafficResult{SessionID: sid, Entries: out.Entries, Total: out.Total}, nil
}

// GetTraffic returns a single traffic entry (full detail) by ID.
func (c *Client) GetTraffic(ctx context.Context, id string) (*capture.TrafficEntry, error) {
	var out capture.TrafficEntry
	if err := c.do(ctx, http.MethodGet, "/api/v1/traffic/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMockRules returns every rule of a device (Web full list, no sinceVersion).
func (c *Client) ListMockRules(ctx context.Context, app, did string) ([]*capture.MockRuleView, error) {
	var out struct {
		Version   int                        `json:"version"`
		Rules     []*capture.MockRuleView    `json:"rules"`
		Conflicts []capture.MockRuleConflict `json:"conflicts,omitempty"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/devices/"+url.PathEscape(app)+"/"+url.PathEscape(did)+"/mock-rules", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

// GetMockRule returns a single rule by ID. The contract exposes rules only
// through the device list, so this scans that list (rule counts are small).
func (c *Client) GetMockRule(ctx context.Context, app, did, ruleID string) (*capture.MockRuleView, error) {
	rules, err := c.ListMockRules(ctx, app, did)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if r != nil && r.ID == ruleID {
			return r, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Code: "not_found",
		Message: "未找到（404）：规则 " + ruleID + " 不存在（或不属于该设备）。请先 list 确认规则 ID。"}
}

// CreateMockRule creates a hand-authored rule (note required when the body
// starts with "{" or "[" — server-side JSON check,  semantics).
func (c *Client) CreateMockRule(ctx context.Context, app, did string, in *capture.MockRuleInput) (*capture.MockRuleView, error) {
	var out capture.MockRuleView
	if err := c.do(ctx, http.MethodPost, "/api/v1/devices/"+url.PathEscape(app)+"/"+url.PathEscape(did)+"/mock-rules", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateMockRuleFromTraffic implements the two-step orchestration
// GET /traffic/{id} → POST /devices/{app}/{did}/mock-rules with a source
// snapshot frozen from the captured entry ( "Mock 此请求").
func (c *Client) CreateMockRuleFromTraffic(ctx context.Context, app, did, trafficID, note string) (*capture.MockRuleView, error) {
	e, err := c.GetTraffic(ctx, trafficID)
	if err != nil {
		return nil, err
	}
	// The mock response is built from the real response (b1); a binary body
	// keeps its base64 so the SDK replays the exact bytes.
	resp := capture.MockResponse{StatusCode: e.StatusCode, Headers: flattenHeaders(e.ResponseHeaders)}
	if e.ResponseBody != "" || e.ResponseBodyBase64 == "" {
		resp.Body = e.ResponseBody
	}
	if e.ResponseBodyBase64 != "" {
		resp.BodyBase64 = e.ResponseBodyBase64
	}
	src := &capture.MockRuleSource{
		Method:              e.Method,
		Path:                e.Path,
		URL:                 e.URL,
		Query:               e.Query,
		RequestHeaders:      e.RequestHeaders,
		RequestBody:         e.RequestBody,
		RequestBodyBase64:   e.RequestBodyBase64,
		RequestBodyDecoded:  e.RequestBodyDecoded,
		StatusCode:          &e.StatusCode,
		ResponseHeaders:     e.ResponseHeaders,
		ResponseBody:        &e.ResponseBody,
		ResponseBodyBase64:  e.ResponseBodyBase64,
		ResponseBodyDecoded: e.ResponseBodyDecoded,
		CapturedAt:          e.Timestamp,
	}
	if e.StatusCode == 0 {
		src.StatusCode = nil
	}
	var respBody *string
	if e.ResponseBody != "" {
		respBody = &e.ResponseBody
	}
	src.ResponseBody = respBody

	in := &capture.MockRuleInput{
		Method:   e.Method,
		Path:     e.Path,
		Response: resp,
		Note:     note,
		Source:   src,
	}
	return c.CreateMockRule(ctx, app, did, in)
}

// UpdateMockRule edits a rule (response/note/enabled; match keys immutable).
func (c *Client) UpdateMockRule(ctx context.Context, app, did, ruleID string, in *capture.UpdateMockRuleInput) (*capture.MockRuleView, error) {
	var out capture.MockRuleView
	if err := c.do(ctx, http.MethodPut, "/api/v1/devices/"+url.PathEscape(app)+"/"+url.PathEscape(did)+"/mock-rules/"+url.PathEscape(ruleID), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMockRule removes a rule (server returns 204). After deletion the
// interface is no longer mocked; 403 for non-owner, 404 when absent.
func (c *Client) DeleteMockRule(ctx context.Context, app, did, ruleID string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/devices/"+url.PathEscape(app)+"/"+url.PathEscape(did)+"/mock-rules/"+url.PathEscape(ruleID), nil, nil, nil)
}

// CreateShare snapshots a traffic entry into an independent share link.
func (c *Client) CreateShare(ctx context.Context, trafficID string) (*ShareCreated, error) {
	var out ShareCreated
	if err := c.do(ctx, http.MethodPost, "/api/v1/shares", nil, map[string]string{"trafficId": trafficID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetShare reads a public share snapshot by ID (no auth needed on the server;
// the client still sends the API key when configured).
func (c *Client) GetShare(ctx context.Context, shareID string) (*ShareSnapshot, error) {
	var out ShareSnapshot
	if err := c.do(ctx, http.MethodGet, "/api/v1/shares/"+url.PathEscape(shareID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// flattenHeaders converts multi-value captured headers into the single-value
// mock header map the contract uses.
func flattenHeaders(h map[string][]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}
