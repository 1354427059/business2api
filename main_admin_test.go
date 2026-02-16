package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"business2api/src/pool"
)

const testAdminAPIKey = "test-admin-key"

func newAdminTestRouter(t *testing.T) (*gin.Engine, string, func()) {
	t.Helper()

	oldDataDir := DataDir
	oldPoolDataDir := pool.DataDir
	oldAPIKeys := append([]string(nil), appConfig.APIKeys...)
	oldRegistrarURL := appConfig.Pool.RegistrarBaseURL

	tmpDir := t.TempDir()
	DataDir = tmpDir
	pool.DataDir = tmpDir
	appConfig.APIKeys = []string{testAdminAPIKey}
	appConfig.Pool.RegistrarBaseURL = defaultRegistrarBaseURL
	if err := pool.Pool.Load(tmpDir); err != nil {
		t.Fatalf("load empty pool: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	setupAPIRoutes(r)

	restore := func() {
		DataDir = oldDataDir
		pool.DataDir = oldPoolDataDir
		appConfig.APIKeys = oldAPIKeys
		appConfig.Pool.RegistrarBaseURL = oldRegistrarURL
		if oldDataDir != "" {
			_ = pool.Pool.Load(oldDataDir)
		}
	}
	return r, tmpDir, restore
}

func writeAccountFile(t *testing.T, dir string, data pool.AccountData) string {
	t.Helper()
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal account: %v", err)
	}
	path := filepath.Join(dir, data.Email+".json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("write account file: %v", err)
	}
	return path
}

func makeAccount(email, configID, csesidx, auth string) pool.AccountData {
	return pool.AccountData{
		Email:         email,
		FullName:      "Tester",
		Authorization: auth,
		Cookies: []pool.Cookie{{
			Name:   "__Secure-C_SES",
			Value:  "cookie-value",
			Domain: ".gemini.google",
		}},
		ConfigID:  configID,
		CSESIDX:   csesidx,
		Timestamp: "2026-02-16T12:00:00Z",
	}
}

func doAuthedJSONRequest(t *testing.T, r *gin.Engine, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reqBody)
	req.Header.Set("Authorization", "Bearer "+testAdminAPIKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func makeImportZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	zw := zip.NewWriter(buf)
	for name, content := range files {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func doAuthedMultipartRequest(t *testing.T, r *gin.Engine, target, filename string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBuffer(nil)
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write form payload: %v", err)
	}
	if err := mw.WriteField("overwrite", "true"); err != nil {
		t.Fatalf("write overwrite field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Authorization", "Bearer "+testAdminAPIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeJSONBody(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode json body failed: %v\nbody=%s", err, raw)
	}
	return out
}

func TestAdminAccountsStateFilter(t *testing.T) {
	r, dir, restore := newAdminTestRouter(t)
	defer restore()

	writeAccountFile(t, dir, makeAccount("active@example.com", "cfg-active", "1001", "Bearer active"))
	if err := os.WriteFile(filepath.Join(dir, "broken@example.com.json"), []byte(`{"email":"broken@example.com","authorization":"Bearer broken","cookies":[]}`), 0644); err != nil {
		t.Fatalf("write invalid account file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad-json@example.com.json"), []byte(`{"email":`), 0644); err != nil {
		t.Fatalf("write broken json file: %v", err)
	}

	if err := pool.Pool.Load(dir); err != nil {
		t.Fatalf("load pool data: %v", err)
	}

	activeResp := doAuthedJSONRequest(t, r, http.MethodGet, "/admin/accounts?state=active", "")
	if activeResp.Code != http.StatusOK {
		t.Fatalf("active response status=%d body=%s", activeResp.Code, activeResp.Body.String())
	}
	activeBody := decodeJSONBody(t, activeResp.Body.String())
	activeTotal := int(activeBody["total"].(float64))
	if activeTotal != 1 {
		t.Fatalf("expected 1 active account, got %d", activeTotal)
	}

	invalidResp := doAuthedJSONRequest(t, r, http.MethodGet, "/admin/accounts?state=invalid", "")
	if invalidResp.Code != http.StatusOK {
		t.Fatalf("invalid response status=%d body=%s", invalidResp.Code, invalidResp.Body.String())
	}
	invalidBody := decodeJSONBody(t, invalidResp.Body.String())
	invalidTotal := int(invalidBody["total"].(float64))
	if invalidTotal < 2 {
		t.Fatalf("expected at least 2 invalid accounts, got %d", invalidTotal)
	}
}

func TestPoolFilesImportPartialSuccessAndOverwrite(t *testing.T) {
	r, dir, restore := newAdminTestRouter(t)
	defer restore()

	existing := makeAccount("overwrite@example.com", "cfg-old", "1101", "Bearer old-auth")
	writeAccountFile(t, dir, existing)
	if err := pool.Pool.Load(dir); err != nil {
		t.Fatalf("load pool before import: %v", err)
	}

	validNew := makeAccount("overwrite@example.com", "cfg-new", "2202", "Bearer new-auth")
	validRaw, _ := json.Marshal(validNew)
	invalidRaw := `{"email":"invalid@example.com","authorization":"Bearer invalid","cookies":[{"name":"__Secure-C_SES","value":"x","domain":".gemini.google"}],"configId":"cfg-only"}`
	zipPayload := makeImportZip(t, map[string]string{
		"ok.json":      string(validRaw),
		"invalid.json": invalidRaw,
	})

	resp := doAuthedMultipartRequest(t, r, "/admin/pool-files/import", "accounts.zip", zipPayload)
	if resp.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", resp.Code, resp.Body.String())
	}
	body := decodeJSONBody(t, resp.Body.String())

	if int(body["total"].(float64)) != 2 {
		t.Fatalf("expected total=2, got %v", body["total"])
	}
	if int(body["success"].(float64)) != 1 {
		t.Fatalf("expected success=1, got %v", body["success"])
	}
	if int(body["failed"].(float64)) != 1 {
		t.Fatalf("expected failed=1, got %v", body["failed"])
	}

	raw, err := os.ReadFile(filepath.Join(dir, "overwrite@example.com.json"))
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	var got pool.AccountData
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal overwritten file: %v", err)
	}
	if got.Authorization != "Bearer new-auth" {
		t.Fatalf("authorization should be overwritten, got %q", got.Authorization)
	}
}

func TestDeleteInvalidPreviewAndExecute(t *testing.T) {
	r, dir, restore := newAdminTestRouter(t)
	defer restore()

	writeAccountFile(t, dir, makeAccount("ok@example.com", "cfg-ok", "3303", "Bearer ok"))
	if err := os.WriteFile(filepath.Join(dir, "bad@example.com.json"), []byte(`{"email":"bad@example.com","authorization":"Bearer bad","cookies":[]}`), 0644); err != nil {
		t.Fatalf("write bad file: %v", err)
	}
	if err := pool.Pool.Load(dir); err != nil {
		t.Fatalf("load pool before delete: %v", err)
	}

	previewResp := doAuthedJSONRequest(t, r, http.MethodPost, "/admin/pool-files/delete-invalid/preview", "{}")
	if previewResp.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResp.Code, previewResp.Body.String())
	}
	previewBody := decodeJSONBody(t, previewResp.Body.String())
	candidates, ok := previewBody["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		t.Fatalf("expected non-empty candidates, body=%s", previewResp.Body.String())
	}

	files := make([]string, 0)
	for _, item := range candidates {
		m := item.(map[string]interface{})
		if name, ok := m["file_name"].(string); ok {
			files = append(files, name)
		}
	}

	execPayload, _ := json.Marshal(map[string]interface{}{
		"files":       files,
		"auto_backup": true,
	})
	execResp := doAuthedJSONRequest(t, r, http.MethodPost, "/admin/pool-files/delete-invalid/execute", string(execPayload))
	if execResp.Code != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", execResp.Code, execResp.Body.String())
	}
	execBody := decodeJSONBody(t, execResp.Body.String())
	if int(execBody["deleted_count"].(float64)) <= 0 {
		t.Fatalf("expected deleted_count > 0, body=%s", execResp.Body.String())
	}

	backupFile, ok := execBody["backup_file"].(string)
	if !ok || backupFile == "" {
		t.Fatalf("backup_file should exist, body=%s", execResp.Body.String())
	}
	if _, err := os.Stat(backupFile); err != nil {
		t.Fatalf("backup file not found: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "bad@example.com.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid file should be deleted, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok@example.com.json")); err != nil {
		t.Fatalf("valid file should remain: %v", err)
	}
}

func TestPoolFilesExportZipContainsManifest(t *testing.T) {
	r, dir, restore := newAdminTestRouter(t)
	defer restore()

	writeAccountFile(t, dir, makeAccount("export@example.com", "cfg-export", "4404", "Bearer export"))
	if err := pool.Pool.Load(dir); err != nil {
		t.Fatalf("load pool before export: %v", err)
	}

	resp := doAuthedJSONRequest(t, r, http.MethodGet, "/admin/pool-files/export?state=all", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", resp.Code, resp.Body.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(resp.Body.Bytes()), int64(resp.Body.Len()))
	if err != nil {
		t.Fatalf("read zip response: %v", err)
	}

	hasManifest := false
	hasExportFile := false
	for _, file := range zr.File {
		switch file.Name {
		case "manifest.json":
			hasManifest = true
			f, _ := file.Open()
			raw, _ := io.ReadAll(f)
			_ = f.Close()
			var manifest map[string]interface{}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("manifest parse failed: %v", err)
			}
			if int(manifest["total"].(float64)) < 1 {
				t.Fatalf("manifest total should be >=1, got %v", manifest["total"])
			}
		case "export@example.com.json":
			hasExportFile = true
		}
	}

	if !hasManifest {
		t.Fatalf("manifest.json not found in zip")
	}
	if !hasExportFile {
		t.Fatalf("exported account file not found in zip")
	}
}

func TestRegistrarTriggerRegisterProxyAndErrorPassthrough(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		hit := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit = true
			if r.URL.Path != "/trigger/register" {
				http.Error(w, "bad path", http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("count") != "3" {
				http.Error(w, "bad count", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"queued":3}`))
		}))
		defer srv.Close()

		r, _, restore := newAdminTestRouter(t)
		defer restore()
		appConfig.Pool.RegistrarBaseURL = srv.URL

		resp := doAuthedJSONRequest(t, r, http.MethodPost, "/admin/registrar/trigger-register", `{"count":3}`)
		if resp.Code != http.StatusOK {
			t.Fatalf("success status=%d body=%s", resp.Code, resp.Body.String())
		}
		if !hit {
			t.Fatalf("registrar server should be hit")
		}
		body := decodeJSONBody(t, resp.Body.String())
		if accepted, ok := body["accepted"].(bool); !ok || !accepted {
			t.Fatalf("expected accepted=true, body=%s", resp.Body.String())
		}
	})

	t.Run("error_passthrough", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream failed", http.StatusBadGateway)
		}))
		defer srv.Close()

		r, _, restore := newAdminTestRouter(t)
		defer restore()
		appConfig.Pool.RegistrarBaseURL = srv.URL

		resp := doAuthedJSONRequest(t, r, http.MethodPost, "/admin/registrar/trigger-register", `{"count":2}`)
		if resp.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d body=%s", resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "upstream failed") {
			t.Fatalf("expected passthrough body, got %s", resp.Body.String())
		}
	})
}

func TestPoolFilesImportSingleJSON(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	acc := makeAccount("single@example.com", "cfg-single", "5505", "Bearer single")
	raw, err := json.Marshal(acc)
	if err != nil {
		t.Fatalf("marshal single account: %v", err)
	}

	resp := doAuthedMultipartRequest(t, r, "/admin/pool-files/import", "single.json", raw)
	if resp.Code != http.StatusOK {
		t.Fatalf("single import status=%d body=%s", resp.Code, resp.Body.String())
	}

	body := decodeJSONBody(t, resp.Body.String())
	if got := int(body["success"].(float64)); got != 1 {
		t.Fatalf("expected success=1 got %d body=%s", got, resp.Body.String())
	}
}

func TestDeleteInvalidExecuteRejectsUnknownFiles(t *testing.T) {
	r, _, restore := newAdminTestRouter(t)
	defer restore()

	payload := fmt.Sprintf(`{"files":["%s"],"auto_backup":false}`, "not-from-preview.json")
	resp := doAuthedJSONRequest(t, r, http.MethodPost, "/admin/pool-files/delete-invalid/execute", payload)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
}
