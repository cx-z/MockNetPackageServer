// Package recording provides conversion from recordings to mock configurations.
package recording

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/getmockd/mockd/pkg/mock"
)

// ErrRecordingNotFound indicates the recording was not found or is invalid.
var ErrRecordingNotFound = errors.New("recording not found or invalid for conversion")

type StreamConvertOptions struct {
	// SimplifyTiming normalizes timing to reduce noise
	SimplifyTiming bool `json:"simplifyTiming,omitempty"`

	// MinDelay is the minimum delay to preserve (ms) when simplifying
	MinDelay int `json:"minDelay,omitempty"`

	// MaxDelay caps delays at this value (ms) when simplifying
	MaxDelay int `json:"maxDelay,omitempty"`

	// IncludeClientMessages includes client-to-server messages as "expect" steps
	IncludeClientMessages bool `json:"includeClientMessages,omitempty"`

	// DeduplicateMessages removes consecutive duplicate messages
	DeduplicateMessages bool `json:"deduplicateMessages,omitempty"`

	// Format is the output format: "json" or "yaml"
	Format string `json:"format,omitempty"`
}

// DefaultStreamConvertOptions returns sensible defaults for stream conversion.
func DefaultStreamConvertOptions() StreamConvertOptions {
	return StreamConvertOptions{
		SimplifyTiming:        false,
		MinDelay:              10,
		MaxDelay:              5000,
		IncludeClientMessages: true,
		DeduplicateMessages:   false,
		Format:                "json",
	}
}

// ConversionMetadata contains info about the source recording.
// This is returned alongside the mock config in StreamConvertResult.

type ConversionMetadata struct {
	SourceRecordingID string    `json:"sourceRecordingId"`
	Protocol          Protocol  `json:"protocol"`
	OriginalPath      string    `json:"originalPath,omitempty"`
	RecordedAt        time.Time `json:"recordedAt"`
	ItemCount         int       `json:"itemCount"`                // frames for WS, events for SSE
	DurationMs        int64     `json:"durationMs"`               // total duration in milliseconds
	DetectedFormat    string    `json:"detectedFormat,omitempty"` // e.g., "openai" for SSE
}

// ToWebSocketScenario converts a WebSocket stream recording to a mock scenario config.
// Returns the scenario config and metadata about the conversion.
func ToWebSocketScenario(recording *StreamRecording, opts StreamConvertOptions) (*mock.WSScenarioConfig, *ConversionMetadata, error) {
	if recording == nil || recording.Protocol != ProtocolWebSocket || recording.WebSocket == nil {
		return nil, nil, ErrRecordingNotFound
	}

	ws := recording.WebSocket
	scenario := &mock.WSScenarioConfig{
		Name:  "Recorded: " + recording.ID,
		Steps: make([]mock.WSScenarioStepConfig, 0, len(ws.Frames)),
	}

	// Build metadata
	metadata := &ConversionMetadata{
		SourceRecordingID: recording.ID,
		Protocol:          ProtocolWebSocket,
		OriginalPath:      recording.Metadata.Path,
		RecordedAt:        recording.StartTime,
		ItemCount:         len(ws.Frames),
	}
	if len(ws.Frames) > 0 {
		metadata.DurationMs = ws.Frames[len(ws.Frames)-1].RelativeMs
	}

	var lastMsg string
	var lastRelativeMs int64

	for i, frame := range ws.Frames {
		// Skip ping/pong/close frames for scenario conversion
		if frame.MessageType == MessageTypePing || frame.MessageType == MessageTypePong || frame.MessageType == MessageTypeClose {
			continue
		}

		// Get message data
		data, err := frame.GetData()
		if err != nil {
			continue
		}
		msgStr := string(data)

		// Deduplicate if enabled
		if opts.DeduplicateMessages && msgStr == lastMsg {
			continue
		}
		lastMsg = msgStr

		// Calculate delay from previous frame
		var delayMs int64
		if i > 0 && frame.RelativeMs > lastRelativeMs {
			delayMs = frame.RelativeMs - lastRelativeMs
		}
		lastRelativeMs = frame.RelativeMs

		// Apply timing simplification
		if opts.SimplifyTiming {
			if delayMs < int64(opts.MinDelay) {
				delayMs = 0
			} else if delayMs > int64(opts.MaxDelay) {
				delayMs = int64(opts.MaxDelay)
			}
		}

		if frame.Direction == DirectionServerToClient {
			// Server message -> "send" step
			step := mock.WSScenarioStepConfig{
				Type: "send",
				Message: &mock.WSMessageResponse{
					Type:  frameMessageTypeToString(frame.MessageType),
					Value: frameValueForConfig(frame),
				},
			}

			// Add delay if significant
			if delayMs > 0 {
				step.Message.Delay = formatDuration(delayMs)
			}

			scenario.Steps = append(scenario.Steps, step)

		} else if opts.IncludeClientMessages {
			// Client message -> "expect" step
			step := mock.WSScenarioStepConfig{
				Type:     "expect",
				Timeout:  "30s",
				Optional: false,
				Match: &mock.WSMatchCriteria{
					Type:  frameMessageTypeToString(frame.MessageType),
					Value: msgStr,
				},
			}
			scenario.Steps = append(scenario.Steps, step)
		}
	}

	return scenario, metadata, nil
}

// ToSSEConfig converts an SSE stream recording to an SSE mock config.
// Returns the SSE config and metadata about the conversion.
func ToSSEConfig(recording *StreamRecording, opts StreamConvertOptions) (*mock.SSEConfig, *ConversionMetadata, error) {
	if recording == nil || recording.Protocol != ProtocolSSE || recording.SSE == nil {
		return nil, nil, ErrRecordingNotFound
	}

	sse := recording.SSE
	cfg := &mock.SSEConfig{
		Events: make([]mock.SSEEventDef, 0, len(sse.Events)),
		Timing: mock.SSETimingConfig{
			PerEventDelays: make([]int, 0, len(sse.Events)),
		},
		Lifecycle: mock.SSELifecycleConfig{
			MaxEvents: len(sse.Events),
		},
		Resume: mock.SSEResumeConfig{
			Enabled:    true,
			BufferSize: len(sse.Events),
		},
	}

	// Build metadata
	metadata := &ConversionMetadata{
		SourceRecordingID: recording.ID,
		Protocol:          ProtocolSSE,
		OriginalPath:      recording.Metadata.Path,
		RecordedAt:        recording.StartTime,
		ItemCount:         len(sse.Events),
		DetectedFormat:    recording.Metadata.DetectedTemplate,
	}
	if len(sse.Events) > 0 {
		metadata.DurationMs = sse.Events[len(sse.Events)-1].RelativeMs
	}

	var lastData string
	var lastRelativeMs int64
	usePerEventDelays := !opts.SimplifyTiming
	var totalDelay int64
	var delayCount int

	for i, event := range sse.Events {
		// Deduplicate if enabled
		if opts.DeduplicateMessages && event.Data == lastData {
			continue
		}
		lastData = event.Data

		// Calculate delay from previous event
		var delayMs int64
		if i > 0 && event.RelativeMs > lastRelativeMs {
			delayMs = event.RelativeMs - lastRelativeMs
		}
		lastRelativeMs = event.RelativeMs

		// Apply timing simplification
		if opts.SimplifyTiming {
			if delayMs < int64(opts.MinDelay) {
				delayMs = 0
			} else if delayMs > int64(opts.MaxDelay) {
				delayMs = int64(opts.MaxDelay)
			}
		}

		// Track for average calculation
		totalDelay += delayMs
		delayCount++

		eventCfg := mock.SSEEventDef{
			Type:    event.EventType,
			Data:    parseEventData(event.Data),
			ID:      event.ID,
			Comment: event.Comment,
		}

		// Handle retry pointer
		if event.Retry != nil {
			eventCfg.Retry = *event.Retry
		}

		cfg.Events = append(cfg.Events, eventCfg)

		if usePerEventDelays {
			cfg.Timing.PerEventDelays = append(cfg.Timing.PerEventDelays, int(delayMs))
		}
	}

	// If simplifying, use average fixed delay instead of per-event
	if opts.SimplifyTiming && delayCount > 0 {
		avgDelay := int(totalDelay / int64(delayCount))
		cfg.Timing.FixedDelay = &avgDelay
		cfg.Timing.PerEventDelays = nil
	}

	return cfg, metadata, nil
}

// StreamConvertResult holds the result of converting a stream recording.
type StreamConvertResult struct {
	Protocol   Protocol            `json:"protocol"`
	Config     interface{}         `json:"config"`     // *mock.WSScenarioConfig or *mock.SSEConfig
	Metadata   *ConversionMetadata `json:"metadata"`   // Info about the source recording
	ConfigJSON []byte              `json:"configJson"` // JSON-encoded config
	ConfigYAML []byte              `json:"configYaml"` // YAML-encoded config (if requested)
}

// ConvertStreamRecording converts a stream recording to the appropriate mock config.
func ConvertStreamRecording(recording *StreamRecording, opts StreamConvertOptions) (*StreamConvertResult, error) {
	result := &StreamConvertResult{
		Protocol: recording.Protocol,
	}

	var err error
	switch recording.Protocol {
	case ProtocolWebSocket:
		result.Config, result.Metadata, err = ToWebSocketScenario(recording, opts)
	case ProtocolSSE:
		result.Config, result.Metadata, err = ToSSEConfig(recording, opts)
	default:
		return nil, ErrRecordingNotFound
	}

	if err != nil {
		return nil, err
	}

	// Encode to JSON
	result.ConfigJSON, err = json.MarshalIndent(result.Config, "", "  ")
	if err != nil {
		return nil, err
	}

	// TODO: Add YAML encoding if opts.Format == "yaml"

	return result, nil
}

// frameMessageTypeToString converts MessageType to string for config.
func frameMessageTypeToString(mt MessageType) string {
	switch mt {
	case MessageTypeText:
		return "text"
	case MessageTypeBinary:
		return "binary"
	default:
		return "text"
	}
}

// frameValueForConfig returns the appropriate value for the config.
func frameValueForConfig(frame WebSocketFrame) interface{} {
	data, err := frame.GetData()
	if err != nil {
		return ""
	}

	if frame.MessageType == MessageTypeBinary {
		// Return base64 for binary
		return base64.StdEncoding.EncodeToString(data)
	}

	// Try to parse as JSON for better formatting
	var jsonVal interface{}
	if err := json.Unmarshal(data, &jsonVal); err == nil {
		return jsonVal
	}

	return string(data)
}

// formatDuration formats milliseconds as a duration string.
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	return d.String()
}

// parseEventData tries to parse SSE data as JSON, otherwise returns as string.
func parseEventData(data string) interface{} {
	var jsonVal interface{}
	if err := json.Unmarshal([]byte(data), &jsonVal); err == nil {
		return jsonVal
	}
	return data
}
