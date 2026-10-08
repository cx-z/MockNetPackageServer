package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

func ruleInput(method, path string, enabled bool) *capture.MockRuleInput {
	return &capture.MockRuleInput{
		Method:   method,
		Path:     path,
		Response: capture.MockResponse{StatusCode: 200, Body: `{"ok":true}`},
		Enabled:  enabled,
		//  Step1: existing tests model capture-originated rules ("Mock 此
		// 请求") — they carry a source snapshot and may omit the note (D6
		// exempts source-bearing creates). Hand-authored rules are exercised
		// explicitly in TestMockRule_HandAuthoredNoteRequired below.
		Source: &capture.MockRuleSource{Method: method, Path: path},
	}
}

// updateInput builds an edit payload ( UpdateMockRuleInput). The match key
// (method/path) is intentionally absent: it is immutable on edit. enabled may
// be nil to leave the switch untouched.
func updateInput(body, note string, enabled *bool) *capture.UpdateMockRuleInput {
	return &capture.UpdateMockRuleInput{
		Response: capture.MockResponse{StatusCode: 200, Body: body},
		Note:     note,
		Enabled:  enabled,
	}
}

func TestMockRule_CreateVersionAndEffective(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// 4.9 回归：Mutate 出错时存储保持原样（无半写入、无版本跳动）。
	fs := newTestStore(t)
	if _, err := fs.MockRules().Mutate(ctx, "app", "d1", func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		return nil, false, errors.New("boom")
	}); err == nil {
		t.Fatal("Mutate(error) = nil, want error")
	}
	if v, _ := fs.MockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("version after failed Mutate = %d, want 0", v)
	}
	if rules, _ := fs.MockRules().List(ctx, &store.MockRuleFilter{App: "app", Did: "d1"}); len(rules) != 0 {
		t.Fatalf("rules after failed Mutate = %d, want 0", len(rules))
	}

	// changed=false 的 Mutate 不 bump 版本。
	if _, err := fs.MockRules().Mutate(ctx, "app", "d1", func(rules []*capture.MockRule) ([]*capture.MockRule, bool, error) {
		return rules, false, nil
	}); err != nil {
		t.Fatalf("Mutate(noop) = %v", err)
	}
	if v, _ := fs.MockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("version after noop Mutate = %d, want 0", v)
	}

	r1, v1, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
	if err != nil {
		t.Fatalf("Create(r1) = %v", err)
	}
	if v1 != 1 || !r1.Effective || !r1.Enabled {
		t.Fatalf("r1 = effective=%v enabled=%v version=%v; want true/true/1", r1.Effective, r1.Enabled, v1)
	}

	// A disabled rule does not bump effective; version still increments.
	r2, v2, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/b", false), nil)
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

	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil); err != nil {
		t.Fatalf("Create(active) = %v", err)
	}
	// Same Method+Path while one is already enabled -> conflict.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil); !errors.Is(err, store.ErrRuleConflict) {
		t.Errorf("Create(second enabled same interface) = %v, want ErrRuleConflict", err)
	}
	// Different method -> not the same interface.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/a", true), nil); err != nil {
		t.Errorf("Create(different method) = %v, want nil", err)
	}
	// Same method, different path -> not the same interface.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/other", true), nil); err != nil {
		t.Errorf("Create(different path) = %v, want nil", err)
	}
	// A disabled second rule on the same interface is allowed.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false), nil); err != nil {
		t.Errorf("Create(disabled same interface) = %v, want nil", err)
	}
}

func TestMockRule_AbnormalConflict(t *testing.T) {
	m, fs := newCaptureManager(t, 0)
	ctx := context.Background()

	// Normal first rule.
	r1, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
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

	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil); err != nil {
		t.Fatalf("Create(a) = %v", err)
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/b", false), nil); err != nil {
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

	if _, v1, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil); err != nil || v1 != 1 {
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

	r1, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
	if err != nil {
		t.Fatalf("Create(r1) = %v", err)
	}
	// r2 disabled on the same interface is allowed.
	r2, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false), nil)
	if err != nil {
		t.Fatalf("Create(r2 disabled) = %v", err)
	}

	// Trying to enable r2 while r1 is enabled -> conflict.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r2.ID, updateInput(`{"ok":true}`, "edit r2", boolPtr(true)), nil); !errors.Is(err, store.ErrRuleConflict) {
		t.Errorf("enable r2 = %v, want ErrRuleConflict", err)
	}
	// Disable r1 first.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r1.ID, updateInput(`{"ok":true}`, "disable r1", boolPtr(false)), nil); err != nil {
		t.Fatalf("disable r1 = %v", err)
	}
	// Now enabling r2 succeeds.
	if updated, _, err := m.UpdateMockRule(ctx, "app", "d1", r2.ID, updateInput(`{"ok":true}`, "enable r2", boolPtr(true)), nil); err != nil || !updated.Effective {
		t.Errorf("enable r2 after r1 off = %v effective=%v; want ok", err, updated.Effective)
	}
}

func TestMockRule_UpdateNoteRequiredOnlyOnEdit(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false), nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	// Pure toggle (response echoed unchanged) with blank note -> allowed :
	// a rule created from a capture has no note and must be enableable directly.
	updated, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID, updateInput(`{"ok":true}`, "", boolPtr(true)), nil)
	if err != nil {
		t.Fatalf("toggle with blank note = %v, want ok", err)
	}
	if !updated.Enabled {
		t.Errorf("toggle did not enable the rule")
	}

	// Edit (response changed) with blank note -> rejected.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID, updateInput(`{"changed":true}`, "", nil), nil); !errors.Is(err, store.ErrNoteRequired) {
		t.Errorf("edit with blank note = %v, want ErrNoteRequired", err)
	}
	// Whitespace-only note on an edit -> also rejected.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID, updateInput(`{"changed":true}`, "   ", nil), nil); !errors.Is(err, store.ErrNoteRequired) {
		t.Errorf("edit with whitespace note = %v, want ErrNoteRequired", err)
	}
	// Edit with a note -> applied.
	updated, _, err = m.UpdateMockRule(ctx, "app", "d1", r.ID, updateInput(`{"changed":true}`, "why", nil), nil)
	if err != nil || updated.Response.Body != `{"changed":true}` {
		t.Errorf("edit with note = %v body=%q; want ok changed", err, updated.Response.Body)
	}
	// The edited rule still toggles with a blank note (response now unchanged).
	updated, _, err = m.UpdateMockRule(ctx, "app", "d1", r.ID, updateInput(`{"changed":true}`, "", boolPtr(false)), nil)
	if err != nil || updated.Enabled {
		t.Errorf("toggle after edit with blank note = %v enabled=%v; want ok off", err, updated.Enabled)
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
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
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

	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	// Unknown rule id.
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", "no-such", updateInput("x", "note", boolPtr(true)), nil); !errors.Is(err, store.ErrRuleNotFound) {
		t.Errorf("Update(unknown) = %v, want ErrRuleNotFound", err)
	}
	// Same id but a different device -> not found (isolation).
	if _, _, err := m.UpdateMockRule(ctx, "app", "d2", r.ID, updateInput("x", "note", boolPtr(true)), nil); !errors.Is(err, store.ErrRuleNotFound) {
		t.Errorf("Update(cross-device) = %v, want ErrRuleNotFound", err)
	}
}

func intPtr(i int) *int { return &i }

// --- : sliding-window retention & lastUsedAt -----------------------------

func TestMockRule_LastUsedAt_CreateAndEdit(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	before := time.Now()
	view, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	if view.LastUsedAt.Before(before) {
		t.Fatalf("created LastUsedAt not set: %v", view.LastUsedAt)
	}

	editedAt := view.LastUsedAt
	time.Sleep(5 * time.Millisecond)
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", view.ID, updateInput(`{"ok":true}`, "edited note", boolPtr(false)), nil); err != nil {
		t.Fatalf("Update = %v", err)
	}
	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 1 {
		t.Fatalf("List = %v %v", views, err)
	}
	if !views[0].LastUsedAt.After(editedAt) {
		t.Errorf("edit did not refresh LastUsedAt: was=%v now=%v", editedAt, views[0].LastUsedAt)
	}
}

func TestMockRule_Janitor_PurgesExpired(t *testing.T) {
	fs := newTestStore(t)
	cfg := store.DefaultCaptureConfig()
	cfg.MockRuleRetention = 50 * time.Millisecond
	m := store.NewCaptureManager(fs.Devices(), fs.CaptureSessions(), fs.MockRules(), fs.PairingTokens(), fs.Shares(), cfg)
	t.Cleanup(m.Stop)
	ctx := context.Background()

	fresh, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/fresh", true), nil)
	if err != nil {
		t.Fatalf("Create fresh = %v", err)
	}
	stale, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/stale", true), nil)
	if err != nil {
		t.Fatalf("Create stale = %v", err)
	}
	// Backdate the stale rule so it falls outside the retention window.
	backdated := *stale.MockRule
	backdated.LastUsedAt = time.Now().Add(-1 * time.Hour)
	if err := fs.MockRules().Update(ctx, &backdated); err != nil {
		t.Fatalf("backdate = %v", err)
	}

	m.PurgeExpiredRules(ctx)

	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(views) != 1 || views[0].ID != fresh.ID {
		t.Fatalf("want only fresh rule kept, got %d rules", len(views))
	}
}

func TestMockRule_HitTouchesLastUsedAt(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("Register = %v", err)
	}
	sess, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("Activate = %v", err)
	}
	rule, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/hit", true), nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	oldUsed := rule.LastUsedAt
	time.Sleep(5 * time.Millisecond)

	now := time.Now()
	hit := &capture.TrafficEntry{
		Method: "POST", URL: "http://x/api/hit", Path: "/api/hit",
		Timestamp: now, Mocked: true, StatusCode: 200,
	}
	if _, err := m.UploadTraffic(ctx, "app", "d1", sess.ID, []*capture.TrafficEntry{hit}); err != nil {
		t.Fatalf("Upload = %v", err)
	}

	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 1 {
		t.Fatalf("List = %v %v", views, err)
	}
	if !views[0].LastUsedAt.After(oldUsed) {
		t.Errorf("mocked hit did not refresh LastUsedAt: old=%v now=%v", oldUsed, views[0].LastUsedAt)
	}
}

// --- : session end disables all device rules ----------------------------

func TestMockRule_SessionEndDisablesRules(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	if _, err := m.RegisterDevice(ctx, &capture.Device{App: "app", Did: "d1"}); err != nil {
		t.Fatalf("Register = %v", err)
	}
	sess, _, err := m.ActivateSession(ctx, "app", "d1")
	if err != nil {
		t.Fatalf("Activate = %v", err)
	}
	r1, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", true), nil)
	if err != nil {
		t.Fatalf("Create r1 = %v", err)
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("GET", "/api/b", true), nil); err != nil {
		t.Fatalf("Create r2 = %v", err)
	}
	// A third rule that stays disabled should remain disabled, not deleted.
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("DELETE", "/api/c", false), nil); err != nil {
		t.Fatalf("Create r3 = %v", err)
	}

	// End the capture session.
	if err := m.EndSession(ctx, sess.ID); err != nil {
		t.Fatalf("EndSession = %v", err)
	}

	views, _, ver, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 3 {
		t.Fatalf("want 3 rules still in history, got %d (err=%v)", len(views), err)
	}
	for _, v := range views {
		if v.Enabled {
			t.Errorf("rule %s still enabled after session end", v.ID)
		}
		if v.Effective {
			t.Errorf("rule %s still effective after session end", v.ID)
		}
	}
	// Version was bumped so the SDK drops them.
	_ = r1
	if ver <= 2 {
		t.Errorf("expected rule version bumped on disable, got %d", ver)
	}
}

// --- : note persistence & immutable match key ------------------------------

func TestMockRule_UpdateNoteAndKeepEnabled(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// Create a disabled rule with no note ( legacy row).
	r, _, err := m.CreateMockRule(ctx, "app", "d1", ruleInput("POST", "/api/a", false), nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	if r.Note != "" {
		t.Fatalf("expected empty note on create, got %q", r.Note)
	}

	// Edit: change the body and set a note; OMIT enabled so the switch stays
	// off (the contract: absent Enabled pointer = leave as-is).
	edited, _, err := m.UpdateMockRule(ctx, "app", "d1", r.ID,
		updateInput(`{"edited":true}`, "debugging feed list", nil), nil)
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	if edited.Note != "debugging feed list" {
		t.Errorf("note not persisted: got %q", edited.Note)
	}
	if edited.Response.Body != `{"edited":true}` {
		t.Errorf("response body not updated: got %q", edited.Response.Body)
	}
	if edited.Enabled {
		t.Errorf("enabled must stay false when Enabled pointer is absent")
	}
	// Match key must be untouched.
	if edited.Method != "POST" || edited.Path != "/api/a" {
		t.Errorf("match key mutated: %s %s", edited.Method, edited.Path)
	}

	// List round-trips the note.
	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 1 {
		t.Fatalf("List = %v %v", views, err)
	}
	if views[0].Note != "debugging feed list" {
		t.Errorf("listed note = %q", views[0].Note)
	}
}

// ---  Step1: hand-authored rules (Source == nil) --------------------------

func TestMockRule_HandAuthoredNoteRequired(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ctx := context.Background()

	// S2: hand-authored create with a blank note -> rejected .
	blank := &capture.MockRuleInput{
		Method:   "GET",
		Path:     "/api/hello",
		Response: capture.MockResponse{StatusCode: 200, Body: `{"hello":"world"}`},
	}
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", blank, nil); !errors.Is(err, store.ErrNoteRequired) {
		t.Errorf("create no-source blank note = %v, want ErrNoteRequired", err)
	}
	// Whitespace-only note is also blank.
	ws := *blank
	ws.Note = "   "
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", &ws, nil); !errors.Is(err, store.ErrNoteRequired) {
		t.Errorf("create no-source whitespace note = %v, want ErrNoteRequired", err)
	}
	// Rejected creates must leave no row and no version bump.
	if views, _, v, err := m.ListMockRules(ctx, "app", "d1"); err != nil || len(views) != 0 || v != 0 {
		t.Fatalf("after rejected creates: n=%d version=%d err=%v; want 0/0", len(views), v, err)
	}

	// S1: hand-authored create with a note -> succeeds, Source stays nil.
	withNote := *blank
	withNote.Note = "mock greeting endpoint for UI work"
	view, ver, err := m.CreateMockRule(ctx, "app", "d1", &withNote, nil)
	if err != nil {
		t.Fatalf("create no-source with note = %v", err)
	}
	if view.Source != nil {
		t.Errorf("hand-authored rule Source = %+v, want nil", view.Source)
	}
	if view.Note != "mock greeting endpoint for UI work" {
		t.Errorf("note = %q, want round-tripped", view.Note)
	}
	if ver != 1 {
		t.Errorf("version after first create = %d, want 1", ver)
	}

	// S2: capture-originated create (Source set) with a blank note still
	// succeeds — the D6 exemption is the status quo.
	logIn := ruleInput("GET", "/api/from-log", false)
	if _, _, err := m.CreateMockRule(ctx, "app", "d1", logIn, nil); err != nil {
		t.Errorf("create with source blank note = %v, want nil", err)
	}
	views, _, _, err := m.ListMockRules(ctx, "app", "d1")
	if err != nil || len(views) != 2 {
		t.Fatalf("List = %d rules (err=%v), want 2", len(views), err)
	}
}
