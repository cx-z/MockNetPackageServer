package requestlog

// GRPCMeta contains gRPC-specific request metadata.
type GRPCMeta struct {
	// Service is the gRPC service name (e.g., "mypackage.MyService").
	Service string `json:"service"`

	// MethodName is the gRPC method name (e.g., "GetUser").
	MethodName string `json:"methodName"`

	// StreamType is the gRPC stream type (unary, server_stream, client_stream, bidi).
	StreamType string `json:"streamType,omitempty"`

	// StatusCode is the gRPC status code name (e.g., "OK", "NOT_FOUND").
	StatusCode string `json:"statusCode,omitempty"`

	// StatusMessage is the gRPC status message.
	StatusMessage string `json:"statusMessage,omitempty"`
}

// WebSocketMeta contains WebSocket-specific request metadata.
type WebSocketMeta struct {
	// ConnectionID is the WebSocket connection identifier.
	ConnectionID string `json:"connectionId"`

	// MessageType is the WebSocket message type (text, binary, ping, pong, close).
	MessageType string `json:"messageType"`

	// Direction is the message direction (inbound, outbound).
	Direction string `json:"direction"`

	// Subprotocol is the negotiated WebSocket subprotocol.
	Subprotocol string `json:"subprotocol,omitempty"`

	// CloseCode is the WebSocket close code (if closing).
	CloseCode int `json:"closeCode,omitempty"`
}

// SSEMeta contains SSE-specific request metadata.
type SSEMeta struct {
	// ConnectionID is the SSE connection identifier.
	ConnectionID string `json:"connectionId"`

	// EventType is the SSE event type (e.g., "message", "update").
	EventType string `json:"eventType,omitempty"`

	// EventID is the SSE event ID.
	EventID string `json:"eventId,omitempty"`

	// IsConnection indicates if this is a connection event (open/close) vs data event.
	IsConnection bool `json:"isConnection,omitempty"`

	// EventCount is the number of events sent (for connection close).
	EventCount int `json:"eventCount,omitempty"`
}
