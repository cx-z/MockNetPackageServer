package file

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// M15 方案 D 阶段二：大图规则的字节治理（blob 存储 + 回源 + media 接口）。
// 规则：>1 MiB 的 image 响应在入库时已丢字节；"Mock 此请求"建规则时服务端按
// 原请求头回源取一次，存成 <ruleID>.bin，规则只带 MediaRef（sha256/size/ct）。

// newAppSessionWithSDK registers a device at the given SDK version so the media
// version gate has a real value to read, then activates a capture session.
func newAppSessionWithSDK(t *testing.T, m *store.CaptureManager, sdkVersion string) *capture.CaptureSession {
	t.Helper()
	if _, err := m.RegisterDevice(context.Background(), &capture.Device{App: "app", Did: "d1", SDKVersion: sdkVersion}); err != nil {
		t.Fatalf("RegisterDevice(%q) = %v", sdkVersion, err)
	}
	s, _, err := m.ActivateSession(context.Background(), "app", "d1")
	if err != nil {
		t.Fatalf("ActivateSession() = %v", err)
	}
	return s
}

// imageWithAuth is a captured big-image entry whose request carried a Bearer
// token: the fetch must forward it so an auth-protected origin still works.
func imageWithAuth(url string, size int64, ts time.Time) *capture.TrafficEntry {
	e := imageEntry(url, "image/jpeg", size)
	e.Timestamp = ts
	e.RequestHeaders = map[string][]string{"Authorization": {"Bearer tok-123"}}
	return e
}

func TestMediaStore_PutGetDeleteQuotaAndSweep(t *testing.T) {
	dir := t.TempDir()
	ms := store.NewMediaStore(dir)

	data := []byte("fake-image-bytes")
	ref, err := ms.Put("rule-1", data, "image/jpeg")
	if err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if ref.Size != int64(len(data)) || len(ref.SHA256) != 64 || ref.ContentType != "image/jpeg" {
		t.Fatalf("ref = %+v, want size/hash/contentType", ref)
	}
	got, err := ms.Get("rule-1")
	if err != nil || string(got) != string(data) {
		t.Fatalf("Get() = %q, %v", got, err)
	}

	// 单文件上限：MaxMediaBytes + 1 字节直接拒绝，不写文件。
	if _, err := ms.Put("rule-big", make([]byte, store.MaxMediaBytes+1), "image/png"); !errors.Is(err, store.ErrMediaTooLarge) {
		t.Fatalf("Put(too large) = %v, want ErrMediaTooLarge", err)
	}

	// 目录配额：替换同一规则不计双份，新增规则超限拒绝。
	ms.SetLimit(int64(len(data)) + 4)
	if _, err := ms.Put("rule-2", make([]byte, 8), "image/png"); !errors.Is(err, store.ErrMediaQuotaExceeded) {
		t.Fatalf("Put(quota) = %v, want ErrMediaQuotaExceeded", err)
	}
	if _, err := ms.Put("rule-1", append(data, "!"...), "image/jpeg"); err != nil {
		t.Fatalf("Put(replace same rule) = %v, want nil", err)
	}

	if err := os.Remove(filepath.Join(dir, "rule-1.bin")); err != nil {
		t.Fatalf("remove blob: %v", err)
	}
	if _, err := ms.Get("rule-1"); !errors.Is(err, store.ErrMediaNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrMediaNotFound", err)
	}

	// SweepOrphans：只保留被引用的规则 + 顺带清掉半写临时文件。
	ms.SetLimit(1 << 20)
	if _, err := ms.Put("keep-me", []byte("keep"), "image/png"); err != nil {
		t.Fatalf("Put(keep-me) = %v", err)
	}
	if _, err := ms.Put("orphan", []byte("orphan"), "image/png"); err != nil {
		t.Fatalf("Put(orphan) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.bin.123.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stale tmp: %v", err)
	}
	ms.SweepOrphans(map[string]bool{"keep-me": true})
	if _, err := os.Stat(filepath.Join(dir, "orphan.bin")); !os.IsNotExist(err) {
		t.Fatalf("orphan blob still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.bin.123.tmp")); !os.IsNotExist(err) {
		t.Fatalf("stale tmp still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep-me.bin")); err != nil {
		t.Fatalf("kept blob removed: %v", err)
	}
	ms.Delete("keep-me")
	if _, err := ms.Get("keep-me"); !errors.Is(err, store.ErrMediaNotFound) {
		t.Fatalf("Get(after delete) = %v, want ErrMediaNotFound", err)
	}
}

func TestMediaFetch_ReasonCodes(t *testing.T) {
	body := []byte("png-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png; charset=binary")
			_, _ = w.Write(body)
		case "/forbidden.png":
			w.WriteHeader(http.StatusForbidden)
		case "/boom.png":
			w.WriteHeader(http.StatusInternalServerError)
		case "/redir.png":
			http.Redirect(w, r, "/redir.png", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	data, ct, err := store.FetchMedia(context.Background(), srv.URL+"/ok.png", nil)
	if err != nil || string(data) != string(body) || ct != "image/png" {
		t.Fatalf("FetchMedia(ok) = %q, %q, %v", data, ct, err)
	}

	data, _, err = store.FetchMedia(context.Background(), srv.URL+"/ok.png",
		map[string][]string{"Authorization": {"Bearer t"}})
	if err != nil || len(data) != len(body) {
		t.Fatalf("FetchMedia(with auth header) = %v, %v", data, err)
	}

	var mfe *store.MediaFetchError
	if _, _, err := store.FetchMedia(context.Background(), srv.URL+"/forbidden.png", nil); !errors.As(err, &mfe) ||
		mfe.Code != store.MediaReasonMissingAuth {
		t.Fatalf("403 without credential = %v, want missing_auth", err)
	}
	mfe = nil
	if _, _, err := store.FetchMedia(context.Background(), srv.URL+"/forbidden.png",
		map[string][]string{"Cookie": {"sid=1"}}); !errors.As(err, &mfe) || mfe.Code != store.MediaReasonHTTP4xx {
		t.Fatalf("403 with credential = %v, want http_4xx", err)
	}
	mfe = nil
	if _, _, err := store.FetchMedia(context.Background(), srv.URL+"/boom.png", nil); !errors.As(err, &mfe) ||
		mfe.Code != store.MediaReasonHTTP5xx {
		t.Fatalf("500 = %v, want http_5xx", err)
	}
	mfe = nil
	if _, _, err := store.FetchMedia(context.Background(), srv.URL+"/redir.png", nil); !errors.As(err, &mfe) ||
		mfe.Code != store.MediaReasonRedirectLimit {
		t.Fatalf("redirect loop = %v, want redirect_limit", err)
	}
	mfe = nil
	if _, _, err := store.FetchMedia(context.Background(), "", nil); !errors.As(err, &mfe) ||
		mfe.Code != store.MediaReasonURLEmpty {
		t.Fatalf("empty url = %v, want url_empty", err)
	}
}

// 契约写的是「最多跟 3 跳」：3 跳必须成功，第 4 跳才 redirect_limit。
// 此前 CheckRedirect 用 len(via) >= 3，实际只跟 2 跳，且唯一的重定向用例是
// 自环（必然超限），抓不到这个 off-by-one。
func TestFetchMedia_RedirectBudgetIsThreeHops(t *testing.T) {
	body := []byte("\x89PNG-hop")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hop1":
			http.Redirect(w, r, "/hop2", http.StatusFound)
		case "/hop2":
			http.Redirect(w, r, "/hop3", http.StatusFound)
		case "/hop3":
			http.Redirect(w, r, "/ok.png", http.StatusFound)
		case "/far1":
			http.Redirect(w, r, "/far2", http.StatusFound)
		case "/far2":
			http.Redirect(w, r, "/far3", http.StatusFound)
		case "/far3":
			http.Redirect(w, r, "/far4", http.StatusFound)
		case "/far4":
			http.Redirect(w, r, "/ok.png", http.StatusFound)
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if data, _, err := store.FetchMedia(context.Background(), srv.URL+"/hop1", nil); err != nil || string(data) != string(body) {
		t.Fatalf("FetchMedia(3 hops) = %q, %v; want success", data, err)
	}
	var mfe *store.MediaFetchError
	if _, _, err := store.FetchMedia(context.Background(), srv.URL+"/far1", nil); !errors.As(err, &mfe) ||
		mfe.Code != store.MediaReasonRedirectLimit {
		t.Fatalf("FetchMedia(4 hops) = %v, want redirect_limit", err)
	}
}

func TestMockRule_CreateFromBigImage_FetchesBlob(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ms := store.NewMediaStore(filepath.Join(t.TempDir(), "media"))
	m.SetMediaStore(ms)
	ctx := context.Background()
	s := newAppSessionWithSDK(t, m, "1.1.1")

	img := []byte("\x89PNG-fetched-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	now := time.Now()
	// 声明 2 MB（入库时已丢字节），实际回源得到的字节由测试服务器决定。
	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{imageWithAuth(srv.URL+"/big.jpg", 2_000_000, now)}); err != nil {
		t.Fatalf("UploadTraffic(image) = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessionTraffic() = %d entries, %v", len(list), err)
	}
	if list[0].ResponseStaticKind != capture.StaticImage {
		t.Fatalf("staticKind = %q, want %q", list[0].ResponseStaticKind, capture.StaticImage)
	}

	in := &capture.MockRuleInput{
		Method: "GET",
		Path:   "/big.jpg",
		// Web 回传的是入库后的占位回包（无字节）；服务端应以 media 分支整体替换。
		Response:        capture.MockResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": "image/jpeg"}, DelayMs: 800},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/big.jpg"},
		OriginTrafficID: list[0].ID,
	}
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
	if err != nil {
		t.Fatalf("CreateMockRule(big image) = %v", err)
	}
	if view.Response.Media == nil {
		t.Fatal("Response.Media = nil, want MediaRef")
	}
	if view.Response.Media.Size != int64(len(img)) || view.Response.Media.ContentType != "image/jpeg" {
		t.Errorf("media ref = %+v, want size=%d ct=image/jpeg", view.Response.Media, len(img))
	}
	if view.Response.Body != "" || view.Response.BodyBase64 != "" {
		t.Errorf("media rule body = %q/%q, want empty (bytes live in the blob)",
			view.Response.Body, view.Response.BodyBase64)
	}
	if view.Response.StatusCode != 200 || view.Response.Headers["Content-Type"] != "image/jpeg" {
		t.Errorf("media rule response = %+v, want 200 image/jpeg", view.Response)
	}
	// 延迟是用户输入，media 分支替换回包时不能顺带丢掉（否则「加延迟」静默失效）。
	if view.Response.DelayMs != 800 {
		t.Errorf("media rule delayMs = %d, want 800 (user input must survive the media branch)", view.Response.DelayMs)
	}

	// media 接口返回原始字节，且哈希校验通过。
	got, ref, err := m.GetRuleMedia(ctx, "app", "d1", view.ID)
	if err != nil || string(got) != string(img) || ref.SHA256 != view.Response.Media.SHA256 {
		t.Fatalf("GetRuleMedia() = %q, %+v, %v", got, ref, err)
	}
	// 跨设备访问同一 ruleID → 不存在（不泄露、也不串设备）。
	if _, _, err := m.GetRuleMedia(ctx, "app", "other-did", view.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRuleMedia(other device) = %v, want ErrNotFound", err)
	}

	// 删除规则 → blob 同步删除，不留配额占用。
	if _, err := m.DeleteMockRule(ctx, "app", "d1", view.ID, nil); err != nil {
		t.Fatalf("DeleteMockRule() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ms.Dir(), view.ID+".bin")); !os.IsNotExist(err) {
		t.Fatalf("blob survived rule delete (err=%v)", err)
	}
}

func TestMockRule_CreateFromBigImage_PrefersCapturedContentType(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ms := store.NewMediaStore(filepath.Join(t.TempDir(), "media"))
	m.SetMediaStore(ms)
	ctx := context.Background()
	s := newAppSessionWithSDK(t, m, "1.1.1")

	img := []byte("\x89PNG-fetched-bytes")
	// 抓包设备的请求头不带 Accept: image/* 时，CDN 可能回 application/octet-stream。
	// 同一 URL 抓包时明确是 image/jpeg，规则回放类型应以抓包记录为准。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID,
		[]*capture.TrafficEntry{imageEntry(srv.URL+"/big.jpg", "image/jpeg", 2_000_000)}); err != nil {
		t.Fatalf("UploadTraffic(image) = %v", err)
	}
	list, _, err := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessionTraffic() = %d entries, %v", len(list), err)
	}

	view, _, err := m.CreateMockRule(ctx, "app", "d1", &capture.MockRuleInput{
		Method:          "GET",
		Path:            "/big.jpg",
		Response:        capture.MockResponse{StatusCode: 200},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/big.jpg"},
		OriginTrafficID: list[0].ID,
	}, nil)
	if err != nil {
		t.Fatalf("CreateMockRule(big image) = %v", err)
	}
	if view.Response.Media == nil || view.Response.Media.ContentType != "image/jpeg" {
		t.Fatalf("media ref = %+v, want contentType from captured headers (image/jpeg)", view.Response.Media)
	}
	if got := view.Response.Headers["Content-Type"]; got != "image/jpeg" {
		t.Fatalf("response Content-Type = %q, want image/jpeg (captured type wins over generic octet-stream)", got)
	}
}

func TestMockRule_CreateFromBigImage_LegacySDKKeepsResponse(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	m.SetMediaStore(store.NewMediaStore(filepath.Join(t.TempDir(), "media")))
	ctx := context.Background()
	s := newAppSessionWithSDK(t, m, "1.1.0") // 低于 MockMediaMinSDKVersion

	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		imageEntry("https://cdn.example.com/a.jpg", "image/jpeg", 2_000_000),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})

	orig := capture.MockResponse{StatusCode: 200, Body: "[binary 2000000 bytes]"}
	in := &capture.MockRuleInput{
		Method: "GET", Path: "/a.jpg", Response: orig,
		Enabled: true, Source: &capture.MockRuleSource{Method: "GET", Path: "/a.jpg"},
		OriginTrafficID: list[0].ID,
	}
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
	if err != nil {
		t.Fatalf("CreateMockRule() = %v", err)
	}
	if view.Response.Media != nil {
		t.Fatalf("Media = %+v on legacy SDK rule, want nil", view.Response.Media)
	}
	// M15：老 SDK 没有 media 拉取能力 → 回落 302 → 原 URL（与视频快路径同结构），
	// 而不是下发一条「只有占位文本」的不可用规则。
	if view.Response.StatusCode != http.StatusFound {
		t.Fatalf("legacy status = %d, want 302", view.Response.StatusCode)
	}
	if view.Response.Headers["Location"] != "https://cdn.example.com/a.jpg" {
		t.Fatalf("legacy Location = %q, want original URL", view.Response.Headers["Location"])
	}
}

func TestMockRule_CreateFromBigImage_FetchFailureCreatesNothing(t *testing.T) {
	m, fs := newCaptureManager(t, 0)
	m.SetMediaStore(store.NewMediaStore(filepath.Join(t.TempDir(), "media")))
	ctx := context.Background()
	s := newAppSessionWithSDK(t, m, "1.1.1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		imageEntry(srv.URL+"/gone.jpg", "image/jpeg", 2_000_000),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})

	in := &capture.MockRuleInput{
		Method: "GET", Path: "/gone.jpg",
		Response:        capture.MockResponse{StatusCode: 200},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/gone.jpg"},
		OriginTrafficID: list[0].ID,
	}
	_, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
	var mfe *store.MediaFetchError
	if !errors.As(err, &mfe) || mfe.Code != store.MediaReasonHTTP4xx {
		t.Fatalf("CreateMockRule(fetch 404) = %v, want MediaFetchError http_4xx", err)
	}

	// 规则未落库、版本未推进。
	rules, _ := fs.MockRules().List(ctx, nil)
	if len(rules) != 0 {
		t.Fatalf("rules = %d, want 0 (failed fetch must not create a rule)", len(rules))
	}
	if v, _ := fs.MockRules().GetRuleVersion(ctx, "app", "d1"); v != 0 {
		t.Fatalf("rule version = %d, want 0", v)
	}

	if !strings.HasPrefix(mfe.Error(), "media fetch failed (http_4xx)") {
		t.Errorf("error message = %q", mfe.Error())
	}
}

func TestMockRule_UpdateAwayFromMedia_RemovesBlob(t *testing.T) {
	m, _ := newCaptureManager(t, 0)
	ms := store.NewMediaStore(filepath.Join(t.TempDir(), "media"))
	m.SetMediaStore(ms)
	ctx := context.Background()
	s := newAppSessionWithSDK(t, m, "1.1.1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("blob"))
	}))
	defer srv.Close()

	if _, err := m.UploadTraffic(ctx, "app", "d1", s.ID, []*capture.TrafficEntry{
		imageEntry(srv.URL+"/a.png", "image/png", 2_000_000),
	}); err != nil {
		t.Fatalf("UploadTraffic() = %v", err)
	}
	list, _, _ := m.ListSessionTraffic(ctx, s.ID, 0, 0, store.TrafficFilter{})
	in := &capture.MockRuleInput{
		Method: "GET", Path: "/a.png",
		Response:        capture.MockResponse{StatusCode: 200},
		Enabled:         true,
		Source:          &capture.MockRuleSource{Method: "GET", Path: "/a.png"},
		OriginTrafficID: list[0].ID,
	}
	view, _, err := m.CreateMockRule(ctx, "app", "d1", in, nil)
	if err != nil || view.Response.Media == nil {
		t.Fatalf("CreateMockRule() = %+v, %v", view, err)
	}

	// 用户把回包改成普通文本（Web 编辑不携带 media）→ 规则脱离 media 路径。
	edited := &capture.UpdateMockRuleInput{
		Response: capture.MockResponse{StatusCode: 200, Body: "edited"},
		Note:     "改为文本回包",
	}
	if _, _, err := m.UpdateMockRule(ctx, "app", "d1", view.ID, edited, nil); err != nil {
		t.Fatalf("UpdateMockRule() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ms.Dir(), view.ID+".bin")); !os.IsNotExist(err) {
		t.Fatalf("blob survived edit away from media (err=%v)", err)
	}
	if _, _, err := m.GetRuleMedia(ctx, "app", "d1", view.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRuleMedia(after edit) = %v, want ErrNotFound", err)
	}
}
