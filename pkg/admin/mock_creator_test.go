package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getmockd/mockd/pkg/admin/engineclient"
	"github.com/getmockd/mockd/pkg/config"
	"github.com/getmockd/mockd/pkg/mock"
	"github.com/getmockd/mockd/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// MockCreatorFunc unit tests
// ---------------------------------------------------------------------------

// TestMockCreatorFunc_DualWrite verifies that mockCreator() writes to the
// admin store AND pushes to the engine — the core architectural invariant.
func TestMockCreatorFunc_DualWrite(t *testing.T) {
	server := newMockEngineServer()
	defer server.Close()

	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithLocalEngineClient(server.client()),
	)

	create := api.mockCreator()
	ctx := context.Background()

	enabled := true
	m := &config.MockConfiguration{
		Name:    "dual-write-test",
		Type:    mock.TypeHTTP,
		Enabled: &enabled,
		HTTP: &mock.HTTPSpec{
			Matcher: &mock.HTTPMatcher{Method: "GET", Path: "/test"},
		},
	}

	created, err := create(ctx, m)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID, "created mock should have an ID")

	// Verify mock exists in the admin store.
	storeMock, err := api.dataStore.Mocks().Get(ctx, created.ID)
	require.NoError(t, err, "mock must be in admin store after dual-write")
	assert.Equal(t, "dual-write-test", storeMock.Name)

	// Verify mock exists in the engine.
	assert.True(t, server.hasMock(created.ID), "mock must be in engine after dual-write")
}

// TestMockCreatorFunc_StoreOnlyWhenNoEngine verifies that when no engine is
// connected, the mock is still persisted in the store.
func TestMockCreatorFunc_StoreOnlyWhenNoEngine(t *testing.T) {
	api := NewAPI(0, WithDataDir(t.TempDir()))

	create := api.mockCreator()
	ctx := context.Background()

	enabled := true
	m := &config.MockConfiguration{
		Name:    "store-only-test",
		Type:    mock.TypeHTTP,
		Enabled: &enabled,
	}

	created, err := create(ctx, m)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	// Verify mock is in the admin store.
	storeMock, err := api.dataStore.Mocks().Get(ctx, created.ID)
	require.NoError(t, err, "mock must be in admin store even without engine")
	assert.Equal(t, "store-only-test", storeMock.Name)
}

// TestMockCreatorFunc_RollbackOnEngineFailure verifies that when the engine
// rejects a mock, the admin store is rolled back.
func TestMockCreatorFunc_RollbackOnEngineFailure(t *testing.T) {
	// Engine that always rejects POST /mocks.
	rejectEngine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/mocks" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:   "validation_error",
				Message: "invalid mock",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer rejectEngine.Close()

	api := NewAPI(0,
		WithDataDir(t.TempDir()),
		WithLocalEngineClient(engineclient.New(rejectEngine.URL)),
	)

	create := api.mockCreator()
	ctx := context.Background()

	enabled := true
	m := &config.MockConfiguration{
		ID:      "should-rollback",
		Name:    "rollback-test",
		Type:    mock.TypeHTTP,
		Enabled: &enabled,
	}

	_, err := create(ctx, m)
	require.Error(t, err, "should fail when engine rejects")
	assert.Contains(t, err.Error(), "engine create failed")

	// Verify mock was rolled back from the store.
	_, storeErr := api.dataStore.Mocks().Get(ctx, "should-rollback")
	assert.ErrorIs(t, storeErr, store.ErrNotFound,
		"mock must be rolled back from store on engine failure")
}

// TestMockCreatorFunc_SetsDefaults verifies that the creator fills in ID,
// workspace, and timestamps.
func TestMockCreatorFunc_SetsDefaults(t *testing.T) {
	api := NewAPI(0, WithDataDir(t.TempDir()))

	create := api.mockCreator()
	ctx := context.Background()

	m := &config.MockConfiguration{
		Name: "defaults-test",
		Type: mock.TypeHTTP,
	}

	created, err := create(ctx, m)
	require.NoError(t, err)

	assert.NotEmpty(t, created.ID, "ID should be auto-generated")
	assert.True(t, strings.HasPrefix(created.ID, "http_"),
		"ID should be type-prefixed, got: %s", created.ID)
	assert.Equal(t, store.DefaultWorkspaceID, created.WorkspaceID)
	assert.False(t, created.CreatedAt.IsZero(), "createdAt should be set")
	assert.False(t, created.UpdatedAt.IsZero(), "updatedAt should be set")
}

// ---------------------------------------------------------------------------
// End-to-end recording conversion dual-write tests
// ---------------------------------------------------------------------------
