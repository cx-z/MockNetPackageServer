package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getmockd/mockd/pkg/requestlog"
)

func TestInMemoryRequestLogger_Log(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	entry := &requestlog.Entry{
		Method:         "GET",
		Path:           "/api/test",
		ResponseStatus: 200,
	}

	logger.Log(entry)

	assert.Equal(t, 1, logger.Count())
	assert.NotEmpty(t, entry.ID)
	assert.False(t, entry.Timestamp.IsZero())
}

func TestInMemoryRequestLogger_Get(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	entry := &requestlog.Entry{
		Method: "GET",
		Path:   "/api/test",
	}
	logger.Log(entry)

	retrieved := logger.Get(entry.ID)
	require.NotNil(t, retrieved)
	assert.Equal(t, entry.Path, retrieved.Path)
}

func TestInMemoryRequestLogger_GetNotFound(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	retrieved := logger.Get("nonexistent")
	assert.Nil(t, retrieved)
}

func TestInMemoryRequestLogger_List(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Log multiple entries
	for i := 0; i < 5; i++ {
		logger.Log(&requestlog.Entry{
			Method: "GET",
			Path:   "/api/test",
		})
	}

	entries := logger.List(nil)
	assert.Len(t, entries, 5)

	// Verify reverse order (newest first)
	for i := 0; i < len(entries)-1; i++ {
		assert.True(t, entries[i].Timestamp.After(entries[i+1].Timestamp) ||
			entries[i].Timestamp.Equal(entries[i+1].Timestamp))
	}
}

func TestInMemoryRequestLogger_ListWithFilter(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Log mixed entries
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/api/users"})
	logger.Log(&requestlog.Entry{Method: "POST", Path: "/api/users"})
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/api/orders"})

	// Filter by method
	entries := logger.List(&requestlog.Filter{Method: "GET"})
	assert.Len(t, entries, 2)

	// Filter by path prefix
	entries = logger.List(&requestlog.Filter{Path: "/api/users"})
	assert.Len(t, entries, 2)

	// Combined filter
	entries = logger.List(&requestlog.Filter{Method: "GET", Path: "/api/users"})
	assert.Len(t, entries, 1)
}

func TestInMemoryRequestLogger_ListWithLimit(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	for i := 0; i < 10; i++ {
		logger.Log(&requestlog.Entry{Method: "GET", Path: "/api/test"})
	}

	entries := logger.List(&requestlog.Filter{Limit: 3})
	assert.Len(t, entries, 3)
}

func TestInMemoryRequestLogger_ListWithOffset(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	for i := 0; i < 10; i++ {
		logger.Log(&requestlog.Entry{Method: "GET", Path: "/api/test"})
	}

	entries := logger.List(&requestlog.Filter{Offset: 3})
	assert.Len(t, entries, 7)
}

func TestInMemoryRequestLogger_Clear(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	for i := 0; i < 5; i++ {
		logger.Log(&requestlog.Entry{Method: "GET"})
	}
	assert.Equal(t, 5, logger.Count())

	logger.Clear()
	assert.Equal(t, 0, logger.Count())
}

func TestInMemoryRequestLogger_FIFOEviction(t *testing.T) {
	logger := NewInMemoryRequestLogger(3)

	// Log more than capacity
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/first"})
	time.Sleep(1 * time.Millisecond)
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/second"})
	time.Sleep(1 * time.Millisecond)
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/third"})
	time.Sleep(1 * time.Millisecond)
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/fourth"})

	assert.Equal(t, 3, logger.Count())

	entries := logger.List(nil)
	// Newest first
	assert.Equal(t, "/fourth", entries[0].Path)
	assert.Equal(t, "/third", entries[1].Path)
	assert.Equal(t, "/second", entries[2].Path)
	// First should be evicted
}

func TestInMemoryRequestLogger_FilterByMatchedID(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-2"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: ""}) // no match

	entries := logger.List(&requestlog.Filter{MatchedID: "mock-1"})
	assert.Len(t, entries, 1)
}

func TestInMemoryRequestLogger_FilterByStatusCode(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	logger.Log(&requestlog.Entry{Method: "GET", ResponseStatus: 200})
	logger.Log(&requestlog.Entry{Method: "GET", ResponseStatus: 404})
	logger.Log(&requestlog.Entry{Method: "GET", ResponseStatus: 200})

	entries := logger.List(&requestlog.Filter{StatusCode: 200})
	assert.Len(t, entries, 2)
}

func TestInMemoryRequestLogger_NilEntry(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	logger.Log(nil)
	assert.Equal(t, 0, logger.Count())
}

func TestInMemoryRequestLogger_DefaultCapacity(t *testing.T) {
	logger := NewInMemoryRequestLogger(0)
	assert.NotNil(t, logger)

	// Should use default capacity
	for i := 0; i < 100; i++ {
		logger.Log(&requestlog.Entry{Method: "GET"})
	}
	assert.Equal(t, 100, logger.Count())
}

func TestInMemoryRequestLogger_ConcurrentAccess(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 50; i++ {
			logger.Log(&requestlog.Entry{Method: "GET"})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 50; i++ {
			_ = logger.List(nil)
			_ = logger.Count()
		}
		done <- true
	}()

	<-done
	<-done

	// Should not panic
	assert.GreaterOrEqual(t, logger.Count(), 0)
}

func TestInMemoryRequestLogger_ClearByMockID(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Log entries for different mocks
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-2"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: ""}) // unmatched

	assert.Equal(t, 4, logger.Count())

	// Clear only mock-1 entries
	logger.ClearByMockID("mock-1")

	assert.Equal(t, 2, logger.Count())

	// Verify mock-1 entries are gone
	entries := logger.List(&requestlog.Filter{MatchedID: "mock-1"})
	assert.Len(t, entries, 0)

	// Verify mock-2 entries remain
	entries = logger.List(&requestlog.Filter{MatchedID: "mock-2"})
	assert.Len(t, entries, 1)
}

func TestInMemoryRequestLogger_CountByMockID(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Log entries for different mocks
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-1"})
	logger.Log(&requestlog.Entry{Method: "GET", MatchedMockID: "mock-2"})

	assert.Equal(t, 3, logger.CountByMockID("mock-1"))
	assert.Equal(t, 1, logger.CountByMockID("mock-2"))
	assert.Equal(t, 0, logger.CountByMockID("mock-3"))
}

func TestInMemoryRequestLogger_Subscribe(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Subscribe before logging
	sub, unsubscribe := logger.Subscribe()
	defer unsubscribe()

	// Log an entry
	entry := &requestlog.Entry{Method: "GET", Path: "/api/test"}
	logger.Log(entry)

	// Should receive the entry
	select {
	case received := <-sub:
		assert.Equal(t, entry.Path, received.Path)
		assert.NotEmpty(t, received.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected to receive entry from subscriber")
	}
}

func TestInMemoryRequestLogger_SubscribeMultiple(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Create two subscribers
	sub1, unsub1 := logger.Subscribe()
	defer unsub1()
	sub2, unsub2 := logger.Subscribe()
	defer unsub2()

	// Log an entry
	entry := &requestlog.Entry{Method: "POST", Path: "/api/users"}
	logger.Log(entry)

	// Both should receive the entry
	for _, sub := range []requestlog.Subscriber{sub1, sub2} {
		select {
		case received := <-sub:
			assert.Equal(t, entry.Path, received.Path)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected to receive entry from subscriber")
		}
	}
}

func TestInMemoryRequestLogger_Unsubscribe(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	sub, unsubscribe := logger.Subscribe()

	// Unsubscribe
	unsubscribe()

	// Log an entry
	logger.Log(&requestlog.Entry{Method: "GET", Path: "/api/test"})

	// Channel should be closed, not receiving new entries
	select {
	case _, ok := <-sub:
		assert.False(t, ok, "Channel should be closed after unsubscribe")
	case <-time.After(100 * time.Millisecond):
		// Expected - channel is closed and doesn't block
	}
}

func TestMatchesPathPrefix(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		prefix string
		want   bool
	}{
		{name: "exact match", path: "/api/users", prefix: "/api/users", want: true},
		{name: "prefix matches", path: "/api/users/123", prefix: "/api/users", want: true},
		{name: "root prefix", path: "/api/users", prefix: "/", want: true},
		{name: "no match", path: "/api/users", prefix: "/api/orders", want: false},
		{name: "prefix longer than path", path: "/api", prefix: "/api/users", want: false},
		{name: "empty prefix", path: "/api/users", prefix: "", want: true},
		{name: "both empty", path: "", prefix: "", want: true},
		{name: "partial segment match", path: "/api/users", prefix: "/api/u", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesPathPrefix(tc.path, tc.prefix)
			assert.Equal(t, tc.want, got, "matchesPathPrefix(%q, %q)", tc.path, tc.prefix)
		})
	}
}

// ---------------------------------------------------------------------------
// generateLogID tests
// ---------------------------------------------------------------------------

func TestGenerateLogID(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{name: "first ID", n: 1, want: "req-1"},
		{name: "base-36 boundary", n: 36, want: "req-10"},
		{name: "large number", n: 1000, want: "req-" + generateShortID(1000)},
		{name: "zero", n: 0, want: "req-0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := generateLogID(tc.n)
			assert.Equal(t, tc.want, got)
			assert.Contains(t, got, "req-")
		})
	}
}

// ---------------------------------------------------------------------------
// matchesFilter protocol-specific branch tests
// ---------------------------------------------------------------------------

func TestMatchesFilter_GRPCService(t *testing.T) {
	tests := []struct {
		name   string
		entry  *requestlog.Entry
		filter *requestlog.Filter
		want   bool
	}{
		{
			name: "gRPC service matches",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
				GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService"},
			},
			filter: &requestlog.Filter{GRPCService: "mypackage.UserService"},
			want:   true,
		},
		{
			name: "gRPC service does not match",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
				GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService"},
			},
			filter: &requestlog.Filter{GRPCService: "mypackage.OrderService"},
			want:   false,
		},
		{
			name: "gRPC service filter on entry with nil GRPC meta",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
			},
			filter: &requestlog.Filter{GRPCService: "mypackage.UserService"},
			want:   false,
		},
		{
			name: "no gRPC filter passes through",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
				GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService"},
			},
			filter: &requestlog.Filter{},
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesFilter(tc.entry, tc.filter)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMatchesFilter_WSConnectionID(t *testing.T) {
	tests := []struct {
		name   string
		entry  *requestlog.Entry
		filter *requestlog.Filter
		want   bool
	}{
		{
			name: "WebSocket connection ID matches",
			entry: &requestlog.Entry{
				Protocol:  requestlog.ProtocolWebSocket,
				WebSocket: &requestlog.WebSocketMeta{ConnectionID: "ws-conn-42"},
			},
			filter: &requestlog.Filter{WSConnectionID: "ws-conn-42"},
			want:   true,
		},
		{
			name: "WebSocket connection ID does not match",
			entry: &requestlog.Entry{
				Protocol:  requestlog.ProtocolWebSocket,
				WebSocket: &requestlog.WebSocketMeta{ConnectionID: "ws-conn-42"},
			},
			filter: &requestlog.Filter{WSConnectionID: "ws-conn-99"},
			want:   false,
		},
		{
			name: "WebSocket connection ID filter on nil meta",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolWebSocket,
			},
			filter: &requestlog.Filter{WSConnectionID: "ws-conn-42"},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesFilter(tc.entry, tc.filter)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMatchesFilter_SSEConnectionID(t *testing.T) {
	tests := []struct {
		name   string
		entry  *requestlog.Entry
		filter *requestlog.Filter
		want   bool
	}{
		{
			name: "SSE connection ID matches",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolSSE,
				SSE:      &requestlog.SSEMeta{ConnectionID: "sse-conn-7"},
			},
			filter: &requestlog.Filter{SSEConnectionID: "sse-conn-7"},
			want:   true,
		},
		{
			name: "SSE connection ID does not match",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolSSE,
				SSE:      &requestlog.SSEMeta{ConnectionID: "sse-conn-7"},
			},
			filter: &requestlog.Filter{SSEConnectionID: "sse-conn-99"},
			want:   false,
		},
		{
			name: "SSE connection ID filter on nil meta",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolSSE,
			},
			filter: &requestlog.Filter{SSEConnectionID: "sse-conn-7"},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesFilter(tc.entry, tc.filter)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMatchesFilter_CombinedProtocolAndCommon(t *testing.T) {
	tests := []struct {
		name   string
		entry  *requestlog.Entry
		filter *requestlog.Filter
		want   bool
	}{
		{
			name: "protocol + gRPC service both match",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
				Method:   "POST",
				GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService"},
			},
			filter: &requestlog.Filter{
				Protocol:    requestlog.ProtocolGRPC,
				GRPCService: "mypackage.UserService",
			},
			want: true,
		},
		{
			name: "protocol matches but gRPC service doesn't",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolGRPC,
				GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService"},
			},
			filter: &requestlog.Filter{
				Protocol:    requestlog.ProtocolGRPC,
				GRPCService: "mypackage.OrderService",
			},
			want: false,
		},
		{
			name: "protocol filter mismatch rejects early",
			entry: &requestlog.Entry{
				Protocol: requestlog.ProtocolHTTP,
			},
			filter: &requestlog.Filter{
				Protocol:    requestlog.ProtocolGRPC,
				GRPCService: "anything",
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesFilter(tc.entry, tc.filter)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Integration: List() with protocol-specific filters
// ---------------------------------------------------------------------------

func TestLoggerListWithProtocolFilters(t *testing.T) {
	logger := NewInMemoryRequestLogger(100)

	// Log entries for various protocols
	logger.Log(&requestlog.Entry{
		Protocol: requestlog.ProtocolGRPC,
		Method:   "POST",
		Path:     "/mypackage.UserService/GetUser",
		GRPC:     &requestlog.GRPCMeta{Service: "mypackage.UserService", MethodName: "GetUser"},
	})
	logger.Log(&requestlog.Entry{
		Protocol: requestlog.ProtocolGRPC,
		Method:   "POST",
		Path:     "/mypackage.OrderService/CreateOrder",
		GRPC:     &requestlog.GRPCMeta{Service: "mypackage.OrderService", MethodName: "CreateOrder"},
	})
	logger.Log(&requestlog.Entry{
		Protocol:  requestlog.ProtocolWebSocket,
		Path:      "/ws/chat",
		WebSocket: &requestlog.WebSocketMeta{ConnectionID: "ws-conn-1", MessageType: "text"},
	})
	logger.Log(&requestlog.Entry{
		Protocol: requestlog.ProtocolSSE,
		Path:     "/events",
		SSE:      &requestlog.SSEMeta{ConnectionID: "sse-conn-1", EventType: "update"},
	})

	require.Equal(t, 4, logger.Count())

	t.Run("filter by gRPC service", func(t *testing.T) {
		entries := logger.List(&requestlog.Filter{GRPCService: "mypackage.UserService"})
		require.Len(t, entries, 1)
		assert.Equal(t, "mypackage.UserService", entries[0].GRPC.Service)
	})

	t.Run("filter by WebSocket connection ID", func(t *testing.T) {
		entries := logger.List(&requestlog.Filter{WSConnectionID: "ws-conn-1"})
		require.Len(t, entries, 1)
		assert.Equal(t, "ws-conn-1", entries[0].WebSocket.ConnectionID)
	})

	t.Run("filter by SSE connection ID", func(t *testing.T) {
		entries := logger.List(&requestlog.Filter{SSEConnectionID: "sse-conn-1"})
		require.Len(t, entries, 1)
		assert.Equal(t, "sse-conn-1", entries[0].SSE.ConnectionID)
	})

	t.Run("non-matching filter returns empty", func(t *testing.T) {
		entries := logger.List(&requestlog.Filter{GRPCService: "nonexistent.Service"})
		assert.Empty(t, entries)
	})
}
