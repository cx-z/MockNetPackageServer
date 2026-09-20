package file

import (
	"context"
	"errors"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

func ruleInput(method, path string, enabled bool) *capture.MockRuleInput {
	return &capture.MockRuleInput{
		Method:  method,
		Path:    path,
		Response: capture.MockResponse{StatusCode: 200, Body: `{"ok":true}`},
		Enabled: enabled,
	}
}

func TestMockRule_CreateVersionAndEffective(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	r1, v1, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true))
	if err != nil {
		t.Fatalf("Create(r1) = %v", err)
	}
	if v1 != 1 || !r1.Effective || !r1.Enabled {
		t.Fatalf("r1 = effective=%v enabled=%v version=%v; want true/true/1", r1.Effective, r1.Enabled, v1)
	}

	// A disabled rule does not bump effective; version still increments.
	r2, v2, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/b", false))
	if err != nil {
		t.Fatalf("Create(r2) = %v", err)
	}
	if v2 != 2 || r2.Effective {
		t.Fatalf("r2 = effective=%v version=%v; want false/2", r2.Effective, v2)
	}

	views, conflicts, version, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("ListMockRules() = %v", err)
	}
	if len(views) != 2 || version != 2 || len(conflicts) != 0 {
		t.Errorf("List = n=%d version=%d conflicts=%d; want 2/2/0", len(views), version, len(conflicts))
	}
}

func TestMockRule_SameInterfaceMutex(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true)); err != nil {
		t.Fatalf("Create(active) = %v", err)
	}
	// Same Method+Path while one is already enabled -> conflict.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true)); !errors.Is(err, store.ErrRuleConflict) {
		t.Errorf("Create(second enabled same interface) = %v, want ErrRuleConflict", err)
	}
	// Different method -> not the same interface.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/a", true)); err != nil {
		t.Errorf("Create(different method) = %v, want nil", err)
	}
	// Same method, different path -> not the same interface.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/other", true)); err != nil {
		t.Errorf("Create(different path) = %v, want nil", err)
	}
	// A disabled second rule on the same interface is allowed.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false)); err != nil {
		t.Errorf("Create(disabled same interface) = %v, want nil", err)
	}
}

func TestMockRule_AbnormalConflict(t *testing.T) {
	m, fs := newCaptureManager(t, 0)
	ctx := context.Background()

	// Normal first rule.
	r1, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true))
	if err != nil {
		t.Fatalf("Create(r1) = %v", err)
	}
	// Force a second enabled rule on the same interface, bypassing the create
	// guard (simulates a race / two tabs toggling).
	ghost := &capture.MockRule{
		ID: "ghost-id", App: "app", Did: "d1",
		Method: "POST", Path: "/api/a",
		Response: r1.Response, Enabled: true,
	}
	if err := fs.MockRules().Create(ctx, ghost); err != nil {
		t.Fatalf("inject ghost = %v", err)
	}
	if _, err := fs.MockRules().BumpRuleVersion(ctx, "app", "d1"); err != nil {
		t.Fatalf("bump = %v", err)
	}

	views, conflicts, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(conflicts))
	}
	if conflicts[0].Method != "POST" || conflicts[0].Path != "/api/a" ||
		conflicts[0].Message != store.MockRuleConflictMessage {
		t.Errorf("conflict = %+v", conflicts[0])
	}
	for _, v := range views {
		if v.Method == "POST" && v.Path == "/api/a" && v.Effective {
			t.Errorf("rule %q still effective in abnormal state", v.ID)
		}
	}
}

func TestMockRule_IncrementalPull(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true)); err != nil {
		t.Fatalf("Create(a) = %v", err)
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/b", false)); err != nil {
		t.Fatalf("Create(b) = %v", err)
	}

	// Pull at the current version: nothing changed.
	active, ver, changed, err := m.ListActiveMockRules(ctx, "app", "d1", 2)
	if err != nil || changed || ver != 2 || len(active) != 0 {
		t.Errorf("pull@2 = n=%d ver=%d changed=%v err=%v; want 0/2/false", len(active), ver, changed, err)
	}
	// Fresh SDK (sinceVersion=0): gets only the effective rule.
	active, ver, changed, err = m.ListActiveMockRules(ctx, "app", "d1", 0)
	if err != nil || !changed || ver != 2 || len(active) != 1 {
		t.Fatalf("pull@0 = n=%d ver=%d changed=%v err=%v; want 1/2/true", len(active), ver, changed, err)
	}
	if active[0].Path != "/api/a" || !active[0].Effective {
		t.Errorf("active rule = %+v; want effective POST /api/a", active[0])
	}
}

func TestMockRule_CrossDeviceIsolation(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, v1, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true)); err != nil || v1 != 1 {
		t.Fatalf("Create(d1) = %v v=%v", err, v1)
	}

	// d2 has no rules and its own version counter.
	views, conflicts, v2, err := m.ListMockRules(ctx, "app", "d2")
	if err != nil || len(views) != 0 || len(conflicts) != 0 || v2 != 0 {
		t.Errorf("List(d2) = n=%d v=%d c=%d; want 0/0/0", len(views), v2, len(conflicts))
	}
	active, _, changed, err := m.ListActiveMockRules(ctx, "app", "d2", 0)
	if err != nil || changed || len(active) != 0 {
		t.Errorf("pull(d2) = n=%d changed=%v; want 0/false", len(active), changed)
	}
}

func TestMockRule_UpdateToggleConflict(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	r1, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true))
	if err != nil {
		t.Fatalf("Create(r1) = %v", err)
	}
	// r2 disabled on the same interface is allowed.
	r2, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false))
	if err != nil {
		t.Fatalf("Create(r2 disabled) = %v", err)
	}

	// Trying to enable r2 while r1 is enabled -> conflict.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r2.ID, ruleInput("POST", "/api/a", true)); !errors.Is(err, store.ErrRuleConflict) {
		t.Errorf("enable r2 = %v, want ErrRuleConflict", err)
	}
	// Disable r1 first.
	r1off := ruleInput("POST", "/api/a", false)
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r1.ID, r1off); err != nil {
		t.Fatalf("disable r1 = %v", err)
	}
	// Now enabling r2 succeeds.
	if updated, _, err := m.UpdateMockRule(ctx, "app", "d1", r2.ID, ruleInput("POST", "/api/a", true)); err != nil || !updated.Effective {
		t.Errorf("enable r2 after r1 off = %v effective=%v; want ok", err, updated.Effective)
	}
}

func TestMockRule_SourcePersisted(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	body := "hello"
	src := &capture.MockRuleSource{
		Method: "POST", Path: "/api/a", URL: "http://x/api/a?q=1",
		RequestBody: "orig-body", StatusCode: intPtr(200), ResponseBody: &body,
	}
	in := ruleInput("POST", "/api/a", true)
	in.Source = src
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in)
	if err != nil {
		t.Fatalf("Create(with source) = %v", err)
	}
	if view.Source == nil || view.Source.URL != "http://x/api/a?q=1" ||
		view.Source.RequestBody != "orig-body" || view.Source.StatusCode == nil || *view.Source.StatusCode != 200 {
		t.Fatalf("source not round-tripped: %+v", view.Source)
	}

	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 1 || views[0].Source == nil || views[0].Source.URL != view.Source.URL {
		t.Errorf("list source = %+v", views)
	}
}

func TestMockRule_UpdateNotFoundAndCrossDevice(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	// Unknown rule id.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", "no-such", ruleInput("POST", "/api/a", true)); !errors.Is(err, store.ErrRuleNotFound) {
		t.Errorf("Update(unknown) = %v, want ErrRuleNotFound", err)
	}
	// Same id but a different device -> not found (isolation).
	if _, _, err := m.UpdateMockRule(ctx, "app", "d2", r.ID, ruleInput("POST", "/api/a", true)); !errors.Is(err, store.ErrRuleNotFound) {
		t.Errorf("Update(cross-device) = %v, want ErrRuleNotFound", err)
	}
}

func intPtr(i int) *int { return &i }
