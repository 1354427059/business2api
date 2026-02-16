package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"business2api/src/logger"
)

func doJSONRequest(t *testing.T, r *gin.Engine, method, target string, body io.Reader, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(cookie) != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func loginAndGetCookie(t *testing.T, r *gin.Engine, username, password string) string {
	t.Helper()
	payload := `{"username":"` + username + `","password":"` + password + `"}`
	resp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/login", strings.NewReader(payload), "")
	if resp.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", resp.Code, resp.Body.String())
	}
	result := resp.Result()
	defer result.Body.Close()
	for _, ck := range result.Cookies() {
		if ck.Name == "b2a_admin_session" {
			return ck.Name + "=" + ck.Value
		}
	}
	t.Fatalf("session cookie not found")
	return ""
}

func TestPanelLoginSuccessAndFailure(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	okPayload := `{"username":"admin","password":"admin123"}`
	okResp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/login", strings.NewReader(okPayload), "")
	if okResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", okResp.Code, okResp.Body.String())
	}

	failPayload := `{"username":"admin","password":"wrong"}`
	failResp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/login", strings.NewReader(failPayload), "")
	if failResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", failResp.Code, failResp.Body.String())
	}
}

func TestAdminRequiresAuthWithoutSessionOrAPIKey(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	resp := doJSONRequest(t, r, http.MethodGet, "/admin/accounts", nil, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSessionCanAccessAdminAccounts(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	cookie := loginAndGetCookie(t, r, "admin", "admin123")
	resp := doJSONRequest(t, r, http.MethodGet, "/admin/accounts?state=all", nil, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAPIKeyStillCanAccessAdminAccounts(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	resp := doAuthedJSONRequest(t, r, http.MethodGet, "/admin/accounts?state=all", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestChangePasswordInvalidatesOldPassword(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	cookie := loginAndGetCookie(t, r, "admin", "admin123")
	changeResp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/change-password", strings.NewReader(`{"new_password":"newpass123"}`), cookie)
	if changeResp.Code != http.StatusOK {
		t.Fatalf("change password status=%d body=%s", changeResp.Code, changeResp.Body.String())
	}

	oldLoginResp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/login", strings.NewReader(`{"username":"admin","password":"admin123"}`), "")
	if oldLoginResp.Code != http.StatusUnauthorized {
		t.Fatalf("old password should fail, got %d body=%s", oldLoginResp.Code, oldLoginResp.Body.String())
	}

	newLoginResp := doJSONRequest(t, r, http.MethodPost, "/admin/panel/login", strings.NewReader(`{"username":"admin","password":"newpass123"}`), "")
	if newLoginResp.Code != http.StatusOK {
		t.Fatalf("new password should succeed, got %d body=%s", newLoginResp.Code, newLoginResp.Body.String())
	}
}

func TestLogsStreamContainsBusiness2apiLog(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	cookie := loginAndGetCookie(t, r, "admin", "admin123")
	marker := "unit-test-business2api-log"
	logger.AppendRaw("business2api", marker)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream?source=business2api&bootstrap_limit=20&poll_ms=500", nil)
	req = req.WithContext(ctx)
	req.Header.Set("Cookie", cookie)

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: logs") {
		t.Fatalf("expected logs event, body=%s", body)
	}
	if !strings.Contains(body, marker) {
		t.Fatalf("expected marker in stream body=%s", body)
	}
}

func TestLogsStreamSendsSystemEventWhenRegistrarUnavailable(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	cookie := loginAndGetCookie(t, r, "admin", "admin123")
	oldURL := appConfig.Pool.RegistrarBaseURL
	appConfig.Pool.RegistrarBaseURL = "http://127.0.0.1:1"
	defer func() {
		appConfig.Pool.RegistrarBaseURL = oldURL
	}()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream?source=registrar&bootstrap_limit=20&poll_ms=500", nil)
	req = req.WithContext(ctx)
	req.Header.Set("Cookie", cookie)

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	time.Sleep(220 * time.Millisecond)
	cancel()
	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: system") {
		t.Fatalf("expected system event, body=%s", body)
	}
	if !strings.Contains(body, "registrar bootstrap error") {
		t.Fatalf("expected registrar error message, body=%s", body)
	}
}

func TestPanelMeAfterLogin(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	cookie := loginAndGetCookie(t, r, "admin", "admin123")
	resp := doJSONRequest(t, r, http.MethodGet, "/admin/panel/me", nil, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("/me status=%d body=%s", resp.Code, resp.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode me response failed: %v", err)
	}
	if authed, ok := body["authenticated"].(bool); !ok || !authed {
		t.Fatalf("expected authenticated=true, body=%s", resp.Body.String())
	}
}
