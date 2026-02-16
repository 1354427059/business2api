package adminauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Store struct {
	mu     sync.RWMutex
	path   string
	record CredentialRecord
}

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("创建认证目录失败: %w", err)
	}

	store := &Store{path: filepath.Join(dataDir, StorageFileName)}
	if err := store.loadOrInit(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Store) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.record.Username
}

func (s *Store) Verify(username, password string) bool {
	s.mu.RLock()
	rec := s.record
	s.mu.RUnlock()

	if strings.TrimSpace(username) != rec.Username {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(rec.PasswordHash), []byte(password)) == nil
}

func (s *Store) ChangePassword(newPassword string) (time.Time, error) {
	newPassword = strings.TrimSpace(newPassword)
	if len(newPassword) < MinPasswordLength {
		return time.Time{}, fmt.Errorf("新密码长度至少 %d 位", MinPasswordLength)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("生成密码哈希失败: %w", err)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.record.PasswordHash = string(hash)
	s.record.UpdatedAt = now
	rec := s.record
	s.mu.Unlock()

	if err := s.save(rec); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

func (s *Store) loadOrInit() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.initDefaultRecord()
		}
		return fmt.Errorf("读取管理员配置失败: %w", err)
	}

	var rec CredentialRecord
	if err := json.Unmarshal(raw, &rec); err != nil || strings.TrimSpace(rec.Username) == "" || strings.TrimSpace(rec.PasswordHash) == "" {
		backupPath := s.path + ".broken." + time.Now().Format("20060102150405")
		_ = os.Rename(s.path, backupPath)
		return s.initDefaultRecord()
	}

	s.mu.Lock()
	s.record = rec
	s.mu.Unlock()
	return nil
}

func (s *Store) initDefaultRecord() error {
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), 12)
	if err != nil {
		return fmt.Errorf("初始化默认密码失败: %w", err)
	}
	rec := CredentialRecord{
		Version:      1,
		Username:     DefaultUsername,
		PasswordHash: string(hash),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.save(rec); err != nil {
		return err
	}
	s.mu.Lock()
	s.record = rec
	s.mu.Unlock()
	return nil
}

func (s *Store) save(rec CredentialRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化管理员配置失败: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0600); err != nil {
		return fmt.Errorf("写入管理员配置临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("替换管理员配置失败: %w", err)
	}
	return nil
}
