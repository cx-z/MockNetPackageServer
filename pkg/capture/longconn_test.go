package capture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLongConnectionRecordInput_Normalize(t *testing.T) {
	ts := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	r := &LongConnectionRecordInput{ChannelID: "xcpush_tcp", EventType: LongConnectionEventPacket, TS: ts, PayloadText: "x"}
	r.Normalize()
	if r.Direction != LongConnectionDirectionIn {
		t.Errorf("Direction = %q, want %q", r.Direction, LongConnectionDirectionIn)
	}
	if r.Status != LongConnectionStatusReceived {
		t.Errorf("Status = %q, want %q", r.Status, LongConnectionStatusReceived)
	}
	// Idempotent.
	before := *r
	r.Normalize()
	if r.Direction != before.Direction || r.Status != before.Status {
		t.Error("Normalize() not idempotent")
	}
}

func TestLongConnectionRecordInput_Validate(t *testing.T) {
	ts := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	base := func() *LongConnectionRecordInput {
		return &LongConnectionRecordInput{ChannelID: "xcpush_tcp", EventType: LongConnectionEventPacket, TS: ts, PayloadText: "hello"}
	}
	tests := []struct {
		name    string
		mut     func(*LongConnectionRecordInput)
		wantErr bool
	}{
		{"valid packet with payloadText", func(*LongConnectionRecordInput) {}, false},
		{"valid packet with payloadBase64", func(r *LongConnectionRecordInput) { r.PayloadText = ""; r.PayloadBase64 = "AAA=" }, false},
		{"valid packet with payloadPreview", func(r *LongConnectionRecordInput) { r.PayloadText = ""; r.PayloadPreview = "preview" }, false},
		{"valid ack", func(r *LongConnectionRecordInput) { r.EventType = LongConnectionEventAck }, false},
		{"valid connect without payload", func(r *LongConnectionRecordInput) { r.EventType = LongConnectionEventConnect; r.PayloadText = "" }, false},
		{"valid heartbeat without payload", func(r *LongConnectionRecordInput) { r.EventType = LongConnectionEventHeartbeat; r.PayloadText = "" }, false},
		{"valid snapshot with snapshot", func(r *LongConnectionRecordInput) {
			r.EventType = LongConnectionEventSnapshot
			r.PayloadText = ""
			r.Snapshot = &LongConnectionSnapshot{State: "connected", ClientID: "c1"}
		}, false},
		{"missing channelId", func(r *LongConnectionRecordInput) { r.ChannelID = "" }, true},
		{"invalid eventType", func(r *LongConnectionRecordInput) { r.EventType = "unknown" }, true},
		{"zero ts", func(r *LongConnectionRecordInput) { r.TS = time.Time{} }, true},
		{"packet without payload", func(r *LongConnectionRecordInput) { r.PayloadText = "" }, true},
		{"ack without payload", func(r *LongConnectionRecordInput) { r.EventType = LongConnectionEventAck; r.PayloadText = "" }, true},
		{"invalid direction", func(r *LongConnectionRecordInput) { r.Direction = "sideways" }, true},
		{"invalid status", func(r *LongConnectionRecordInput) { r.Status = "processed" }, true},
		{"snapshot without snapshot", func(r *LongConnectionRecordInput) { r.EventType = LongConnectionEventSnapshot; r.PayloadText = "" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.mut(r)
			r.Normalize()
			err := r.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLongConnectionRecord_JSONInlinesInput(t *testing.T) {
	ts := time.Date(2026, 10, 13, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	rec := LongConnectionRecord{
		ID: "01ABCDEF", SessionID: "s1", Seq: 7,
		LongConnectionRecordInput: LongConnectionRecordInput{
			ChannelID: "xcpush_tcp", EventType: LongConnectionEventPacket, TS: ts,
			MsgID: "1581291347372367872", BizType: "chat", PayloadBase64: "CgkzMDIxNDMxNjkQARk=",
			Status: LongConnectionStatusHandled, Receiver: "ChatMessageService",
		},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// Server fields and inlined input fields must all appear at top level.
	for _, want := range []string{
		`"id":"01ABCDEF"`, `"sessionId":"s1"`, `"seq":7`,
		`"channelId":"xcpush_tcp"`, `"eventType":"packet"`, `"ts":"2026-10-13T10:00:00+08:00"`,
		`"msgId":"1581291347372367872"`, `"bizType":"chat"`, `"payloadBase64":"CgkzMDIxNDMxNjkQARk="`,
		`"status":"handled"`, `"receiver":"ChatMessageService"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s: %s", want, s)
		}
	}
	// Round-trip preserves the record.
	var back LongConnectionRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != rec.ID || back.SessionID != rec.SessionID || back.Seq != rec.Seq ||
		back.ChannelID != rec.ChannelID || !back.TS.Equal(rec.TS) || back.Receiver != rec.Receiver {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestLongConnectionRecordInput_JSONOmitsDefaults(t *testing.T) {
	ts := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	r := LongConnectionRecordInput{ChannelID: "xcpush_tcp", EventType: LongConnectionEventConnect, TS: ts}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// Zero/optional fields must be omitted (omitempty) — defaults stay server-side.
	for _, forbid := range []string{`"direction"`, `"status"`, `"duplicate"`, `"payloadText"`, `"snapshot"`} {
		if strings.Contains(s, forbid) {
			t.Errorf("JSON should omit %s: %s", forbid, s)
		}
	}
}

func TestLongConnectionSnapshot_JSON(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	s := LongConnectionSnapshot{
		State: "connected", IsSocketConnected: true, RouterHost: "r1", RouterPort: 8080,
		HeartbeatInterval: 15, ClientID: "c1", HasToken: true,
		ReceivedMessageCount: 42, ReceiveServiceCount: 40, LastConnectAt: &now,
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"state":"connected"`, `"isSocketConnected":true`, `"routerHost":"r1"`, `"routerPort":8080`,
		`"heartbeatInterval":15`, `"clientId":"c1"`, `"hasToken":true`,
		`"receivedMessageCount":42`, `"receiveServiceCount":40`, `"lastConnectAt":"2026-10-13T10:00:00Z"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s: %s", want, b)
		}
	}
}

// TestLongConnectionRecord_PushLogSampleMapping maps the real KK long-connection
// log sample (tasks/push_log.txt) onto the model to prove the fields cover the
// 3.2-table semantics: Message ID / Biz Type / Zip Type / base64 payload /
// Duplicate / ACK / Handling Result / Handled Services.
func TestLongConnectionRecord_PushLogSampleMapping(t *testing.T) {
	ts := time.Date(2026, 10, 12, 20, 47, 16, 301000000, time.UTC)
	r := LongConnectionRecordInput{
		ChannelID: "xcpush_tcp", EventType: LongConnectionEventPacket, TS: ts,
		MsgID: "1581291347372367872", BizType: "chat", ZipType: "0",
		PayloadBase64:    "CgkzMDIxNDMxNjkQARk=",
		Duplicate:        false,
		AckSent:          true,
		Status:           LongConnectionStatusHandled,
		Receiver:         "ChatMessageService",
		ReceiverResult:   "handled",
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"channelId":"xcpush_tcp"`, `"eventType":"packet"`, `"ts":"2026-10-12T20:47:16.301Z"`,
		`"msgId":"1581291347372367872"`, `"bizType":"chat"`, `"zipType":"0"`,
		`"ackSent":true`, `"status":"handled"`, `"receiver":"ChatMessageService"`, `"receiverResult":"handled"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s: %s", want, b)
		}
	}
}
