package pool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
