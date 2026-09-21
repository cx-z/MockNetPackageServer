package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getmockd/mockd/pkg/capture"
)

// loginDev registers + logs in a dev user, returns its bearer token.
func loginDev(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	return loginToken(t, ts, name, "secret123")
}

func TestCreateManualDeviceSuccess(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginDev(t, ts, "ownerd1")

	res, body := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok, map[string]string{
		"app":  "com.example.integrating",
		"did":  "did-manual-001",
		"name": "张三的 iPhone",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", res.StatusCode, body)
	}
	var d capture.DeviceView
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, body)
	}
	if d.Owner != "ownerd1" {
		t.Fatalf("owner = %q, want ownerd1", d.Owner)
	}
	if d.Name != "张三的 iPhone" {
		t.Fatalf("name = %q", d.Name)
	}
}

func TestCreateManualDeviceConflict(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginDev(t, ts, "ownerd2")

	body := map[string]string{"app": "com.example.integrating", "did": "did-dup", "name": "first"}
	if res, _ := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok, body); res.StatusCode != http.StatusCreated {
		t.Fatalf("first create != 200: %d", res.StatusCode)
	}
	// Same (app,did) again → 409.
	res, b := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok,
		map[string]string{"app": "com.example.integrating", "did": "did-dup", "name": "second"})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409; body=%s", res.StatusCode, b)
	}
}

func TestCreateManualDeviceValidation(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	tok := loginDev(t, ts, "ownerd3")

	cases := []struct {
		name string
		body map[string]string
	}{
		{"missing did", map[string]string{"app": "com.example.integrating", "did": "", "name": "x"}},
		{"missing name", map[string]string{"app": "com.example.integrating", "did": "d-no-name", "name": ""}},
		{"unknown app", map[string]string{"app": "com.other.app", "did": "d-other-app", "name": "x"}},
	}
	for _, c := range cases {
		res, b := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", tok, c.body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400; body=%s", c.name, res.StatusCode, b)
		}
	}
}

func TestCreateManualDeviceRequiresAuth(t *testing.T) {
	_, ts := newAuthRequiredTestAPI(t)
	res, b := doAuthJSON(t, http.MethodPost, ts.URL+"/api/v1/devices", "",
		map[string]string{"app": "com.example.integrating", "did": "d-noauth", "name": "x"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token create = %d, want 401; body=%s", res.StatusCode, b)
	}
}
