package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
)

// LongConnUploadResponse is returned on successful longconn ingestion (202;
// count is the number of records actually stored — partial acceptance is
// allowed). Contract schema LongConnUploadResponse.
type LongConnUploadResponse struct {
	Accepted bool `json:"accepted"`
	Count    int  `json:"count"`
}

// handleUploadLongConnection handles POST /api/v1/sessions/{id}/longconn (SDK
// batch upload, 业务级长连接日志). No requireAuth — aligned with
// handleUploadTraffic (SDK upload channel). Contract semantics: 202 on
// acceptance; 400 when the batch is empty or every record is invalid;
// 404 session_not_found for an unknown session; 409 session_ended for an
// ended session.
func (a *API) handleUploadLongConnection(w http.ResponseWriter, r *http.Request) {
	var req capture.LongConnUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONDecodeError(w, err, a.logger())
		return
	}
	if req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "channelId is required")
		return
	}
	if len(req.Records) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "records must not be empty")
		return
	}
	if len(req.Records) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "records must be at most 500 items")
		return
	}

	count, err := a.captureManager.UploadLongConn(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	if count == 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "all longconn records are invalid")
		return
	}
	writeJSON(w, http.StatusAccepted, LongConnUploadResponse{Accepted: true, Count: count})
}

// LongConnListResponse is the Web long-connection list payload (contract
// LongConnListResponse): the filtered+page of records plus the filtered total.
type LongConnListResponse struct {
	Entries []*capture.LongConnectionRecord `json:"entries"`
	Total   int                             `json:"total"`
}

// handleListSessionLongConnections handles GET /api/v1/sessions/{id}/longconn
// (Web long-connection tab data source). Filters: channelId/eventType/bizType
// exact (ANDed), from/to RFC3339 inclusive, since=seq cursor (records with
// seq > since); pagination: offset/limit after filtering, total = filtered
// size. Sorted by seq ascending (arrival order). Invalid params → 400;
// unknown/expired session → 404.
func (a *API) handleListSessionLongConnections(w http.ResponseWriter, r *http.Request) {
	// F1: align with /traffic — device-ownership check before any data access.
	if _, ok := a.authorizeSessionAccess(w, r, r.PathValue("id")); !ok {
		return
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be an integer")
		return
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > 500 {
		writeError(w, http.StatusBadRequest, "invalid_field", "limit must be between 1 and 500")
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be an integer")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "offset must be non-negative")
		return
	}
	since, err := queryInt(r, "since", 0)
	if err != nil || since < 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "since must be a non-negative integer")
		return
	}

	q := r.URL.Query()
	query := capture.LongConnectionQuery{
		ChannelID: q.Get("channelId"),
		BizType:   q.Get("bizType"),
		Keyword:   q.Get("keyword"),
		Since:     int64(since),
		Limit:     limit,
		Offset:    offset,
	}
	if raw := q.Get("eventType"); raw != "" {
		switch capture.LongConnectionEventType(raw) {
		case capture.LongConnectionEventConnect, capture.LongConnectionEventDisconnect,
			capture.LongConnectionEventHeartbeat, capture.LongConnectionEventPacket,
			capture.LongConnectionEventAck, capture.LongConnectionEventSnapshot:
			query.EventType = capture.LongConnectionEventType(raw)
		default:
			writeError(w, http.StatusBadRequest, "invalid_field", "eventType must be one of connect|disconnect|heartbeat|packet|ack|snapshot")
			return
		}
	}
	if raw := q.Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", "from must be RFC3339 (e.g. 2026-10-13T13:00:00+08:00)")
			return
		}
		query.From = &t
	}
	if raw := q.Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", "to must be RFC3339 (e.g. 2026-10-13T13:00:00+08:00)")
			return
		}
		query.To = &t
	}

	entries, total, err := a.captureManager.ListSessionLongConn(r.Context(), r.PathValue("id"), query)
	if err != nil {
		writeCaptureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, LongConnListResponse{Entries: entries, Total: total})
}
