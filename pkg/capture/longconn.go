package capture

import (
	"errors"
	"time"
)

// ============================================================================
// Long-connection logs (长连接日志, capability line L1)
// ============================================================================

// LongConnectionDirection is the packet direction of a long-connection log
// record (contract LongConnectionRecordInput.direction). Only inbound packets
// plus lifecycle events are collected in the first phase (decision #1: out
// packets are not recorded); "out" exists so the model stays complete.
type LongConnectionDirection string

const (
	// LongConnectionDirectionIn is an inbound packet or lifecycle event
	// (the only direction collected in the first phase).
	LongConnectionDirectionIn LongConnectionDirection = "in"
	// LongConnectionDirectionOut is an outbound packet; reserved, not recorded
	// in the first phase.
	LongConnectionDirectionOut LongConnectionDirection = "out"
)

// LongConnectionEventType is the platform-agnostic event type of a
// long-connection log record (contract LongConnectionRecordInput.eventType).
type LongConnectionEventType string

const (
	// LongConnectionEventConnect marks a connection established.
	LongConnectionEventConnect LongConnectionEventType = "connect"
	// LongConnectionEventDisconnect marks a disconnection.
	LongConnectionEventDisconnect LongConnectionEventType = "disconnect"
	// LongConnectionEventHeartbeat marks a heartbeat event.
	LongConnectionEventHeartbeat LongConnectionEventType = "heartbeat"
	// LongConnectionEventPacket marks a received business packet.
	LongConnectionEventPacket LongConnectionEventType = "packet"
	// LongConnectionEventAck marks an ACK sent for a received packet.
	LongConnectionEventAck LongConnectionEventType = "ack"
	// LongConnectionEventSnapshot carries a full connection snapshot.
	LongConnectionEventSnapshot LongConnectionEventType = "snapshot"
)

// LongConnectionStatus is the generic handling status of a record, aligned
// with KK's record-point semantics (contract
// LongConnectionRecordInput.status).
type LongConnectionStatus string

const (
	// LongConnectionStatusReceived means the packet was received (default).
	LongConnectionStatusReceived LongConnectionStatus = "received"
	// LongConnectionStatusDuplicate means the packet was a duplicate.
	LongConnectionStatusDuplicate LongConnectionStatus = "duplicate"
	// LongConnectionStatusEmptyData means the packet carried no business data.
	LongConnectionStatusEmptyData LongConnectionStatus = "emptyData"
	// LongConnectionStatusHandled means the packet was handled by business.
	LongConnectionStatusHandled LongConnectionStatus = "handled"
	// LongConnectionStatusAckSkipped means the ACK was skipped.
	LongConnectionStatusAckSkipped LongConnectionStatus = "ackSkipped"
)

// LongConnectionSnapshot is the connection snapshot carried by records with
// EventType=snapshot (contract LongConnectionSnapshot), aligned with KK's
// KKPushServiceDebugSnapshot. All fields are optional; the business side
// reports what it observes.
type LongConnectionSnapshot struct {
	// State is the business-side connection state
	// (e.g. connected/connecting/disconnected).
	State string `json:"state,omitempty"`
	// IsSocketConnected reports whether the underlying socket is connected.
	IsSocketConnected bool `json:"isSocketConnected,omitempty"`
	// RouterHost is the router address (host).
	RouterHost string `json:"routerHost,omitempty"`
	// RouterPort is the router port.
	RouterPort int `json:"routerPort,omitempty"`
	// HeartbeatInterval is the heartbeat interval in seconds.
	HeartbeatInterval int `json:"heartbeatInterval,omitempty"`
	// ClientID is the client identifier.
	ClientID string `json:"clientId,omitempty"`
	// AccountMid is the account identifier (mid), optional.
	AccountMid string `json:"accountMid,omitempty"`
	// HasToken reports whether a connection token is held.
	HasToken bool `json:"hasToken,omitempty"`
	// ReceivedMessageCount is the cumulative inbound message count.
	ReceivedMessageCount int64 `json:"receivedMessageCount,omitempty"`
	// ReceiveServiceCount is the cumulative business-dispatch count.
	ReceiveServiceCount int64 `json:"receiveServiceCount,omitempty"`
	// LastConnectAt is the time of the most recent connection.
	LastConnectAt *time.Time `json:"lastConnectAt,omitempty"`
	// LastDisconnectAt is the time of the most recent disconnection.
	LastDisconnectAt *time.Time `json:"lastDisconnectAt,omitempty"`
	// LastPacketAt is the time of the most recent packet.
	LastPacketAt *time.Time `json:"lastPacketAt,omitempty"`
	// LastAckMsgID is the msgId of the most recent ACK.
	LastAckMsgID string `json:"lastAckMsgId,omitempty"`
	// LastError is the most recent error message.
	LastError string `json:"lastError,omitempty"`
}

// LongConnectionRecordInput is an upload entry injected by the business-side
// bridge (contract LongConnectionRecordInput). Required fields are ≤4:
// ChannelID, EventType and TS are mandatory; the payload triplet
// (payloadText / payloadBase64 / payloadPreview) is conditionally required —
// business-packet events (packet/ack) must carry at least one (enforced by
// Validate), lifecycle events (connect/disconnect/heartbeat/snapshot) may
// omit it. Everything else is optional with a sensible default. It carries no
// ID/SessionID/Seq — those are server-generated and stamped onto
// LongConnectionRecord at ingestion.
type LongConnectionRecordInput struct {
	// ChannelID is the business-registered channel identifier
	// (e.g. "xcpush_tcp"); the distinguishing dimension when several
	// long-connection frameworks coexist.
	ChannelID string `json:"channelId"`
	// Direction is the packet direction; "in" is the default and the only
	// direction collected in the first phase.
	Direction LongConnectionDirection `json:"direction,omitempty"`
	// EventType is the generic platform-agnostic event type.
	EventType LongConnectionEventType `json:"eventType"`
	// TS is the record time (RFC3339, business-side point-in-time).
	TS time.Time `json:"ts"`
	// MsgID is the message ID as a string (avoids cross-platform 64-bit
	// overflow); optional.
	MsgID string `json:"msgId,omitempty"`
	// BizType is the business semantic (chat/push/feedback/logRetrieval/
	// command/businessPush…; numeric enums are mapped to strings by the
	// business side).
	BizType string `json:"bizType,omitempty"`
	// ZipType is the compression type; optional.
	ZipType string `json:"zipType,omitempty"`
	// PayloadText is the readable payload (UTF-8 or business-decoded);
	// one of the payload triplet required for packet/ack events.
	PayloadText string `json:"payloadText,omitempty"`
	// PayloadBase64 is the base64 of a binary payload that cannot be
	// textified; one of the payload triplet.
	PayloadBase64 string `json:"payloadBase64,omitempty"`
	// PayloadPreview is the truncated preview kept when the full payload
	// exceeds 10KB (≤1KB); one of the payload triplet.
	PayloadPreview string `json:"payloadPreview,omitempty"`
	// Duplicate marks a duplicate packet (aligned with KK record-point
	// semantics).
	Duplicate bool `json:"duplicate,omitempty"`
	// Empty marks a packet with no business data.
	Empty bool `json:"empty,omitempty"`
	// AckSent marks whether the ACK was sent.
	AckSent bool `json:"ackSent,omitempty"`
	// Mocked marks a packet that was replaced by a push mock rule (能力线 L7:
	// SDK 旁路采集时标记；Web 显示 "Mock 命中"，对齐 HTTP mocked 标识)。
	Mocked bool `json:"mocked,omitempty"`
	// MockRuleID is the push mock rule ID that replaced this packet (SDK 上报；
	// 服务端据此 RecordPushMockHit 递增 hitCount/lastHitAt，Web 可溯源)。
	MockRuleID string `json:"mockRuleId,omitempty"`
	// Adapter identifies which adapter collected this record (L6.2 采集方式
	// 如实标注：适配器标识如 "xcpush" / 业务 API 注入 "unknown-inject"；
	// SDK 填充、服务端透传；老 SDK 不携带 → 空)。
	Adapter string `json:"adapter,omitempty"`
	// Receiver is the business receiver identifier (business-layer dispatch
	// detail; empty in zero-intrusion mode, Web labels the capture method
	// truthfully).
	Receiver string `json:"receiver,omitempty"`
	// ReceiverResult is the business receiver's handling return value
	// (business-layer dispatch detail; empty in zero-intrusion mode).
	ReceiverResult string `json:"receiverResult,omitempty"`
	// Status is the generic handling status; defaults to received.
	Status LongConnectionStatus `json:"status,omitempty"`
	// Snapshot is the connection snapshot; required (non-nil) when
	// EventType=snapshot.
	Snapshot *LongConnectionSnapshot `json:"snapshot,omitempty"`
}

// Normalize fills documented defaults in place (idempotent): empty Direction
// becomes "in", empty Status becomes "received".
func (r *LongConnectionRecordInput) Normalize() {
	if r.Direction == "" {
		r.Direction = LongConnectionDirectionIn
	}
	if r.Status == "" {
		r.Status = LongConnectionStatusReceived
	}
}

// Validate checks the contract's required-fields rule: ChannelID, EventType
// and TS are mandatory; the payload triplet is conditionally required
// (packet/ack events must carry at least one of payloadText/payloadBase64/
// payloadPreview, lifecycle events may omit it); enum fields, when present,
// must be in the documented sets; eventType=snapshot requires a Snapshot.
// It does not check server-generated fields (ID/SessionID/Seq).
func (r *LongConnectionRecordInput) Validate() error {
	if r.ChannelID == "" {
		return errors.New("channelId is required")
	}
	switch r.EventType {
	case LongConnectionEventConnect, LongConnectionEventDisconnect,
		LongConnectionEventHeartbeat, LongConnectionEventPacket,
		LongConnectionEventAck, LongConnectionEventSnapshot:
	default:
		return errors.New("eventType must be one of connect|disconnect|heartbeat|packet|ack|snapshot")
	}
	if r.TS.IsZero() {
		return errors.New("ts is required")
	}
	if r.Direction != "" && r.Direction != LongConnectionDirectionIn && r.Direction != LongConnectionDirectionOut {
		return errors.New("direction must be in|out")
	}
	if r.Status != "" {
		switch r.Status {
		case LongConnectionStatusReceived, LongConnectionStatusDuplicate,
			LongConnectionStatusEmptyData, LongConnectionStatusHandled,
			LongConnectionStatusAckSkipped:
		default:
			return errors.New("status must be one of received|duplicate|emptyData|handled|ackSkipped")
		}
	}
	switch r.EventType {
	case LongConnectionEventPacket, LongConnectionEventAck:
		if r.PayloadText == "" && r.PayloadBase64 == "" && r.PayloadPreview == "" {
			return errors.New("payload is required for packet/ack events: at least one of payloadText|payloadBase64|payloadPreview")
		}
	case LongConnectionEventSnapshot:
		if r.Snapshot == nil {
			return errors.New("snapshot is required when eventType=snapshot")
		}
	default:
		// connect/disconnect/heartbeat events carry no conditional
		// payload/snapshot requirements.
	}
	return nil
}

// LongConnectionRecord is the server-side full record (contract
// LongConnectionRecord): the upload entry plus the server-generated fields
// ID / SessionID / Seq. JSON encoding inlines the input fields.
type LongConnectionRecord struct {
	// ID is the server-generated record ID.
	ID string `json:"id"`
	// SessionID is the capture session this record belongs to.
	SessionID string `json:"sessionId"`
	// Seq is the server-assigned per-session monotonic sequence number,
	// independent from the traffic seq counter (longconn and traffic are
	// stored separately and never share a counter). `since=<seq>` incremental
	// queries filter on it. It is a server-side + query-side concern only —
	// the SDK never sends it and never consumes it.
	Seq int64 `json:"seq"`
	LongConnectionRecordInput
}

// LongConnUploadRequest is the upload payload (contract
// LongConnUploadRequest): the channel the batch belongs to plus the records.
// Session attribution comes from the request path /sessions/{id} — the body
// carries no app/did.
type LongConnUploadRequest struct {
	// ChannelID is the channel this batch belongs to; expected to equal the
	// per-record ChannelID.
	ChannelID string `json:"channelId"`
	// Records is one batched upload of long-connection records (≤500, aligned
	// with the /traffic batching contract).
	Records []LongConnectionRecordInput `json:"records"`
}

// LongConnectionQuery is the server-side filter for the long-connection list
// (contract GET /sessions/{id}/longconn). Filters combine with AND and are
// applied before pagination; Since filters by seq before offset/limit.
type LongConnectionQuery struct {
	// ChannelID filters by exact channel identifier.
	ChannelID string
	// EventType filters by exact event type.
	EventType LongConnectionEventType
	// BizType filters by exact business semantic (e.g. "chat").
	BizType string
	// Keyword does case-insensitive substring match across msgId/bizType/
	// eventType/channelId (F3: history overlay longconn mode keyword box).
	Keyword string
	// From / To bound the record time (RFC3339, inclusive).
	From *time.Time
	To   *time.Time
	// Since returns only records with Seq > Since (incremental cursor).
	Since int64
	// Limit is the max number of records to return (default 100, max 500).
	Limit int
	// Offset skips records (after filtering, after since).
	Offset int
}
