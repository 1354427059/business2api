package adminauth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type SessionManager struct {
	mu       sync.RWMutex
	ttl      time.Duration
	sessions map[string]SessionInfo
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionManager{
		ttl:      ttl,
		sessions: make(map[string]SessionInfo),
	}
}

func (m *SessionManager) Create(username string) (SessionInfo, error) {
	token, err := generateToken(32)
	if err != nil {
		return SessionInfo{}, err
	}
	now := time.Now().UTC()
	info := SessionInfo{
		Token:     token,
		Username:  username,
		ExpiresAt: now.Add(m.ttl),
	}

	m.mu.Lock()
	m.sessions[token] = info
	m.mu.Unlock()
	return info, nil
}

func (m *SessionManager) Validate(token string) (SessionInfo, bool) {
	m.mu.RLock()
	info, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return SessionInfo{}, false
	}
	if time.Now().UTC().After(info.ExpiresAt) {
		m.Delete(token)
		return SessionInfo{}, false
	}
	return info, true
}

func (m *SessionManager) Delete(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *SessionManager) DeleteByUsername(username string) {
	m.mu.Lock()
	for token, info := range m.sessions {
		if info.Username == username {
			delete(m.sessions, token)
		}
	}
	m.mu.Unlock()
}

func generateToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
