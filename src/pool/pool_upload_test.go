package pool

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestPool() *AccountPool {
	return &AccountPool{
		refreshInterval: 5 * time.Second,
		refreshWorkers:  1,
		stopChan:        make(chan struct{}),
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newExternalPendingAccount(email string) *Account {
	return &Account{
		Data: AccountData{
			Email:         email,
			FullName:      "Tester",
			MailProvider:  "chatgpt",
			MailPassword:  "",
			Authorization: "Bearer old-auth",
			ConfigID:      "cfg-old",
			CSESIDX:       "1001",
			Cookies:       []Cookie{{Name: "__Secure-C_SES", Value: "cookie", Domain: ".gemini.google"}},
			Timestamp:     time.Now().Format(time.RFC3339),
		},
		FilePath: "test.json",
		CSESIDX:  "1001",
		ConfigID: "cfg-old",
		Status:   StatusPendingExternal,
	}
}

func TestProcessAccountUploadValidation(t *testing.T) {
	p := newTestPool()
	req := &AccountUploadRequest{
		Email: "demo@example.com",
	}
	err := ProcessAccountUpload(p, t.TempDir(), req)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !errors.Is(err, ErrInvalidAccountUpload) {
		t.Fatalf("expected ErrInvalidAccountUpload, got: %v", err)
	}
}

func TestProcessAccountUploadPreserveFieldsForRefresh(t *testing.T) {
	dir := t.TempDir()
	email := "refresh@example.com"
	initial := AccountData{
		Email:         email,
		FullName:      "Old Name",
		MailProvider:  "duckmail",
		MailPassword:  "old-password",
		Authorization: "Bearer old-auth",
		Cookies: []Cookie{
			{Name: "__Secure-C_SES", Value: "old", Domain: ".gemini.google"},
		},
		ConfigID:  "cfg-old",
		CSESIDX:   "111",
		Timestamp: time.Now().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(filepath.Join(dir, email+".json"), raw, 0644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	p := newTestPool()
	if err := p.Load(dir); err != nil {
		t.Fatalf("load seed: %v", err)
	}
	if len(p.pendingAccounts) != 1 {
		t.Fatalf("expected 1 pending account, got %d", len(p.pendingAccounts))
	}
	p.pendingAccounts[0].Status = StatusPendingExternal

	req := &AccountUploadRequest{
		Email:         email,
		FullName:      "",
		MailProvider:  "",
		MailPassword:  "",
		Cookies:       []Cookie{{Name: "__Secure-C_SES", Value: "new", Domain: ".gemini.google"}},
		Authorization: "Bearer new-auth",
		ConfigID:      "cfg-new",
		CSESIDX:       "222",
		IsNew:         false,
	}
	if err := ProcessAccountUpload(p, dir, req); err != nil {
		t.Fatalf("process upload failed: %v", err)
	}

	fileRaw, err := os.ReadFile(filepath.Join(dir, email+".json"))
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	var got AccountData
	if err := json.Unmarshal(fileRaw, &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if got.FullName != "Old Name" {
		t.Fatalf("full name should be preserved, got %q", got.FullName)
	}
	if got.MailProvider != "duckmail" {
		t.Fatalf("mail provider should be preserved, got %q", got.MailProvider)
	}
	if got.MailPassword != "old-password" {
		t.Fatalf("mail password should be preserved, got %q", got.MailPassword)
	}
	if got.Authorization != "Bearer new-auth" {
		t.Fatalf("authorization should be updated, got %q", got.Authorization)
	}

	found := false
	for _, acc := range p.pendingAccounts {
		if acc.Data.Email == email {
			found = true
			if acc.Status != StatusPending {
				t.Fatalf("expected status pending, got %v", acc.Status)
			}
		}
	}
	if !found {
		t.Fatalf("updated account not found in pending queue")
	}
}

func TestLoadSkipsAdminPanelAuthFile(t *testing.T) {
	dir := t.TempDir()

	// 面板认证文件应被号池加载逻辑忽略
	adminAuthRaw := []byte(`{"version":1,"username":"admin","password_hash":"x","updated_at":"2026-02-16T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, "admin_panel_auth.json"), adminAuthRaw, 0644); err != nil {
		t.Fatalf("write admin auth file: %v", err)
	}

	account := AccountData{
		Email:         "valid@example.com",
		FullName:      "Tester",
		Authorization: "Bearer token-CSESIDX=101",
		CSESIDX:       "101",
		Cookies: []Cookie{
			{Name: "__Secure-C_SES", Value: "cookie", Domain: ".gemini.google"},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(account)
	if err := os.WriteFile(filepath.Join(dir, "valid@example.com.json"), raw, 0644); err != nil {
		t.Fatalf("write account file: %v", err)
	}

	p := newTestPool()
	if err := p.Load(dir); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got := len(p.pendingAccounts); got != 1 {
		t.Fatalf("expected 1 pending account, got %d", got)
	}
	if p.pendingAccounts[0].Data.Email != "valid@example.com" {
		t.Fatalf("unexpected pending email: %s", p.pendingAccounts[0].Data.Email)
	}
}

func TestExternalRefreshTasksAndQueueSelection(t *testing.T) {
	p := newTestPool()
	external := &Account{
		Data: AccountData{
			Email:         "external@example.com",
			Authorization: "Bearer ext",
			ConfigID:      "cfg-ext",
			CSESIDX:       "100",
			Cookies:       []Cookie{{Name: "__Secure-C_SES", Value: "x", Domain: ".gemini.google"}},
		},
		FilePath:  "external.json",
		Refreshed: false,
		Status:    StatusPendingExternal,
	}
	normal := &Account{
		Data: AccountData{
			Email:         "normal@example.com",
			Authorization: "Bearer n",
			ConfigID:      "cfg-normal",
			CSESIDX:       "200",
			Cookies:       []Cookie{{Name: "__Secure-C_SES", Value: "y", Domain: ".gemini.google"}},
		},
		FilePath:  "normal.json",
		Refreshed: false,
		Status:    StatusPending,
	}
	p.pendingAccounts = []*Account{external, normal}

	tasks := p.ExternalRefreshTasks(10)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 external task, got %d", len(tasks))
	}
	if tasks[0].Email != "external@example.com" {
		t.Fatalf("unexpected external task email: %s", tasks[0].Email)
	}

	next := p.GetPendingAccount()
	if next == nil || next.Data.Email != "normal@example.com" {
		t.Fatalf("expected normal pending account, got %+v", next)
	}

	if len(p.pendingAccounts) != 1 || p.pendingAccounts[0].Data.Email != "external@example.com" {
		t.Fatalf("external pending account should remain in queue")
	}
}

func TestClaimExternalRefreshTasksExclusive(t *testing.T) {
	p := newTestPool()
	p.pendingAccounts = []*Account{
		newExternalPendingAccount("exclusive@example.com"),
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([][]AccountUploadRequest, 2)
	workers := []string{"worker-a", "worker-b"}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = p.ClaimExternalRefreshTasks(workers[idx], 1, 120)
		}(i)
	}
	close(start)
	wg.Wait()

	totalClaimed := len(results[0]) + len(results[1])
	if totalClaimed != 1 {
		t.Fatalf("expected only one worker claimed task, got %d", totalClaimed)
	}

	claimedAgain := p.ClaimExternalRefreshTasks("worker-c", 1, 120)
	if len(claimedAgain) != 0 {
		t.Fatalf("task should be leased and unavailable, got %d", len(claimedAgain))
	}
}

func TestClaimExternalRefreshTasksRecycleExpiredLease(t *testing.T) {
	p := newTestPool()
	acc := newExternalPendingAccount("expired@example.com")
	acc.ExternalTaskID = "expired-task"
	acc.ExternalLeaseOwner = "old-worker"
	acc.ExternalLeaseUntil = time.Now().Add(-1 * time.Minute)
	p.pendingAccounts = []*Account{acc}

	tasks := p.ClaimExternalRefreshTasks("new-worker", 1, 120)
	if len(tasks) != 1 {
		t.Fatalf("expected reclaimed task, got %d", len(tasks))
	}
	if tasks[0].TaskID == "" || tasks[0].TaskID == "expired-task" {
		t.Fatalf("expected a new task id, got %q", tasks[0].TaskID)
	}

	metrics := p.CollectExternalRefreshMetrics()
	if got, ok := metrics["refresh_lease_expired_total"].(int64); !ok || got != 1 {
		t.Fatalf("expected refresh_lease_expired_total=1, got %#v", metrics["refresh_lease_expired_total"])
	}
}

func TestMarkExternalRefreshFailedBackoff(t *testing.T) {
	p := newTestPool()
	acc := newExternalPendingAccount("backoff@example.com")
	p.pendingAccounts = []*Account{acc}

	tasks := p.ClaimExternalRefreshTasks("worker-backoff", 1, 120)
	if len(tasks) != 1 {
		t.Fatalf("expected one claimed task, got %d", len(tasks))
	}
	firstTaskID := tasks[0].TaskID

	start := time.Now()
	if err := p.MarkExternalRefreshFailed(firstTaskID, "worker-backoff", "first-fail"); err != nil {
		t.Fatalf("mark fail first: %v", err)
	}

	acc.Mu.Lock()
	firstFailCount := acc.ExternalFailCount
	firstRetryAt := acc.ExternalRetryAt
	taskCleared := acc.ExternalTaskID == ""
	acc.Mu.Unlock()
	if firstFailCount != 1 {
		t.Fatalf("expected fail count 1, got %d", firstFailCount)
	}
	if !taskCleared {
		t.Fatalf("task id should be cleared after fail")
	}
	if delta := firstRetryAt.Sub(start); delta < 28*time.Second || delta > 35*time.Second {
		t.Fatalf("expected backoff around 30s, got %v", delta)
	}

	if got := p.ClaimExternalRefreshTasks("worker-backoff", 1, 120); len(got) != 0 {
		t.Fatalf("should not claim during retry window, got %d", len(got))
	}

	acc.Mu.Lock()
	acc.ExternalRetryAt = time.Now().Add(-1 * time.Second)
	acc.Mu.Unlock()

	tasks2 := p.ClaimExternalRefreshTasks("worker-backoff", 1, 120)
	if len(tasks2) != 1 {
		t.Fatalf("expected claim after retry window, got %d", len(tasks2))
	}

	start2 := time.Now()
	if err := p.MarkExternalRefreshFailed(tasks2[0].TaskID, "worker-backoff", "second-fail"); err != nil {
		t.Fatalf("mark fail second: %v", err)
	}

	acc.Mu.Lock()
	secondFailCount := acc.ExternalFailCount
	secondRetryAt := acc.ExternalRetryAt
	acc.Mu.Unlock()
	if secondFailCount != 2 {
		t.Fatalf("expected fail count 2, got %d", secondFailCount)
	}
	if delta := secondRetryAt.Sub(start2); delta < 58*time.Second || delta > 65*time.Second {
		t.Fatalf("expected backoff around 60s, got %v", delta)
	}
}

func TestExternalRefreshUploadThenWorkerMovesToReady(t *testing.T) {
	dir := t.TempDir()
	email := "cycle@example.com"
	initial := AccountData{
		Email:         email,
		FullName:      "Cycle User",
		MailProvider:  "chatgpt",
		Authorization: "Bearer old-auth",
		Cookies: []Cookie{
			{Name: "__Secure-C_SES", Value: "old-cookie", Domain: ".gemini.google"},
		},
		ConfigID:  "cfg-old",
		CSESIDX:   "3101",
		Timestamp: time.Now().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(filepath.Join(dir, email+".json"), raw, 0644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	p := newTestPool()
	if err := p.Load(dir); err != nil {
		t.Fatalf("load pool: %v", err)
	}
	if len(p.pendingAccounts) != 1 {
		t.Fatalf("expected one pending account, got %d", len(p.pendingAccounts))
	}

	acc := p.pendingAccounts[0]
	acc.Mu.Lock()
	acc.Status = StatusPendingExternal
	acc.Mu.Unlock()

	claimed := p.ClaimExternalRefreshTasks("worker-cycle", 1, 120)
	if len(claimed) != 1 {
		t.Fatalf("expected one claimed task, got %d", len(claimed))
	}
	taskID := claimed[0].TaskID
	if taskID == "" {
		t.Fatalf("task id should not be empty")
	}

	req := &AccountUploadRequest{
		Email:               email,
		FullName:            "Cycle User",
		MailProvider:        "chatgpt",
		MailPassword:        "",
		Cookies:             []Cookie{{Name: "__Secure-C_SES", Value: "new-cookie", Domain: ".gemini.google"}},
		CookieString:        "__Secure-C_SES=new-cookie",
		Authorization:       "Bearer refreshed-auth",
		AuthorizationSource: "network",
		ConfigID:            "cfg-new",
		CSESIDX:             "4101",
		IsNew:               false,
		TaskID:              taskID,
		WorkerID:            "worker-cycle",
	}
	if err := ProcessAccountUpload(p, dir, req); err != nil {
		t.Fatalf("process refresh upload: %v", err)
	}

	if len(p.pendingAccounts) != 1 {
		t.Fatalf("expected one pending account after upload, got %d", len(p.pendingAccounts))
	}
	pendingAcc := p.pendingAccounts[0]
	pendingAcc.Mu.Lock()
	if pendingAcc.Status != StatusPending {
		pendingAcc.Mu.Unlock()
		t.Fatalf("expected status pending after upload, got %v", pendingAcc.Status)
	}
	if pendingAcc.ExternalTaskID != "" || pendingAcc.ExternalLeaseOwner != "" {
		pendingAcc.Mu.Unlock()
		t.Fatalf("external lease should be cleared after upload")
	}
	pendingAcc.Mu.Unlock()

	oldHTTPClient := HTTPClient
	defer func() {
		HTTPClient = oldHTTPClient
	}()

	xsrfToken := base64.URLEncoding.EncodeToString([]byte("0123456789abcdef"))
	HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL == nil || !strings.Contains(req.URL.String(), "getoxsrf") {
				return nil, errors.New("unexpected request url")
			}
			body := ")]}'\n" + `{"xsrfToken":"` + xsrfToken + `","keyId":"kid-test"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	workerDone := make(chan struct{})
	go func() {
		p.refreshWorker(1)
		close(workerDone)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.ReadyCount() == 1 && p.PendingCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(p.stopChan)
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("refresh worker did not stop in time")
	}

	if p.ReadyCount() != 1 {
		t.Fatalf("expected ready count 1 after worker refresh, got %d", p.ReadyCount())
	}
	if p.PendingCount() != 0 {
		t.Fatalf("expected pending count 0 after worker refresh, got %d", p.PendingCount())
	}

	p.mu.RLock()
	readyAcc := p.readyAccounts[0]
	p.mu.RUnlock()

	readyAcc.Mu.Lock()
	defer readyAcc.Mu.Unlock()
	if readyAcc.Status != StatusReady {
		t.Fatalf("expected status ready, got %v", readyAcc.Status)
	}
	if readyAcc.JWT == "" {
		t.Fatalf("expected jwt to be refreshed")
	}
	if readyAcc.Data.Authorization != "Bearer refreshed-auth" {
		t.Fatalf("expected authorization to keep uploaded value, got %q", readyAcc.Data.Authorization)
	}
}

func TestMarkNeedsRefreshExternalMode(t *testing.T) {
	p := newTestPool()
	acc := &Account{
		Data: AccountData{
			Email: "ready@example.com",
		},
		FilePath:  "ready.json",
		Refreshed: true,
		Status:    StatusReady,
	}
	p.readyAccounts = []*Account{acc}

	originalMode := ExternalRefreshMode
	ExternalRefreshMode = true
	defer func() {
		ExternalRefreshMode = originalMode
	}()

	p.MarkNeedsRefresh(acc)

	if len(p.readyAccounts) != 0 {
		t.Fatalf("ready account should be removed when external refresh mode enabled")
	}
	if len(p.pendingAccounts) != 1 {
		t.Fatalf("expected 1 pending account, got %d", len(p.pendingAccounts))
	}
	if p.pendingAccounts[0].Status != StatusPendingExternal {
		t.Fatalf("expected pending external status, got %v", p.pendingAccounts[0].Status)
	}
}
