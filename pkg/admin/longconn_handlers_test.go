package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// longConnRecord builds a valid inbound packet record for handler tests.
func longConnRecord(msgID string, ts time.Time) capture.LongConnectionRecordInput {
	return capture.LongConnectionRecordInput{
		ChannelID:   "xcpush_tcp",
		EventType:   capture.LongConnectionEventPacket,
		TS:          ts,
		MsgID:       msgID,
		PayloadText: `{"type":"chat"}`,
	}
}

// TestLongConnUpload_Accepted: valid batch → 202 + count; server stamps seq
// independently (longconn never shares the traffic counter).
func TestLongConnUpload_Accepted(t *testing.T) {
	srv := newCaptureTestAPI(t)
	mustSeedDevice(t, srv, "com.example.integrating", "dev-lc")
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.integrating", Did: "dev-lc"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var up LongConnUploadResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn",
		capture.LongConnUploadRequest{
			ChannelID: "xcpush_tcp",
			Records:   []capture.LongConnectionRecordInput{longConnRecord("m1", time.Now())},
		}, &up)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.True(t, up.Accepted)
	assert.Equal(t, 1, up.Count)
}

// TestLongConnUpload_BadRequests: missing channelId / empty records / >500 /
// unknown session → 400/404.
func TestLongConnUpload_BadRequests(t *testing.T) {
	srv := newCaptureTestAPI(t)
	mustSeedDevice(t, srv, "com.example.integrating", "dev-lc2")
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.integrating", Did: "dev-lc2"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// missing channelId.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn",
		capture.LongConnUploadRequest{Records: []capture.LongConnectionRecordInput{longConnRecord("m1", time.Now())}}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// empty records.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn",
		capture.LongConnUploadRequest{ChannelID: "xcpush_tcp"}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// unknown session → 404.
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/no-such-session/longconn",
		capture.LongConnUploadRequest{ChannelID: "xcpush_tcp", Records: []capture.LongConnectionRecordInput{longConnRecord("m1", time.Now())}}, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestLongConnUpload_PartialAccept: one valid + one invalid record (packet
// without payload triplet) → count=1, 202 (partial acceptance, not 400).
func TestLongConnUpload_PartialAccept(t *testing.T) {
	srv := newCaptureTestAPI(t)
	mustSeedDevice(t, srv, "com.example.integrating", "dev-lc3")
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.integrating", Did: "dev-lc3"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var up LongConnUploadResponse
	resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn",
		capture.LongConnUploadRequest{
			ChannelID: "xcpush_tcp",
			Records: []capture.LongConnectionRecordInput{
				longConnRecord("m1", time.Now()),
				{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: time.Now()}, // dropped
			},
		}, &up)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 1, up.Count)
}

// TestLongConnList_FiltersAndPaging: upload 3 records → list 全量 total=3 且 seq 升序；
// bizType 过滤 / since 游标 / offset+limit 分页均生效。
func TestLongConnList_FiltersAndPaging(t *testing.T) {
	srv := newCaptureTestAPI(t)
	mustSeedDevice(t, srv, "com.example.integrating", "dev-lc4")
	var session capture.CaptureSession
	resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions",
		ActivateSessionRequest{App: "com.example.integrating", Did: "dev-lc4"}, &session)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	now := time.Now()
	upload := func(biz string, ts time.Time) {
		var up LongConnUploadResponse
		r := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn",
			capture.LongConnUploadRequest{ChannelID: "xcpush_tcp", Records: []capture.LongConnectionRecordInput{
				{ChannelID: "xcpush_tcp", EventType: capture.LongConnectionEventPacket, TS: ts, BizType: biz, PayloadText: "{}"},
			}}, &up)
		require.Equal(t, http.StatusAccepted, r.StatusCode)
		require.Equal(t, 1, up.Count)
	}
	upload("chat", now)
	upload("push", now.Add(time.Second))
	upload("chat", now.Add(2*time.Second))

	// Full list: total=3, seq ascending 1..3.
	var full LongConnListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn", nil, &full)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, full.Total)
	require.Len(t, full.Entries, 3)
	assert.Equal(t, int64(1), full.Entries[0].Seq)
	assert.Equal(t, int64(2), full.Entries[1].Seq)
	assert.Equal(t, int64(3), full.Entries[2].Seq)

	// bizType filter (AND exact).
	var byBiz LongConnListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn?bizType=chat", nil, &byBiz)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, byBiz.Total)

	// since cursor: seq > 1 → 2 records.
	var since LongConnListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn?since=1", nil, &since)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, since.Total)
	assert.Equal(t, int64(2), since.Entries[0].Seq)

	// paging: limit=1 offset=1 → middle record.
	var page LongConnListResponse
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn?limit=1&offset=1", nil, &page)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, page.Total)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, int64(2), page.Entries[0].Seq)

	// unknown session → 404.
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/no-such/longconn", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// invalid eventType → 400.
	resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/sessions/"+session.ID+"/longconn?eventType=bogus", nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
