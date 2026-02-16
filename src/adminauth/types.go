package adminauth

import "time"

const (
	DefaultUsername   = "admin"
	DefaultPassword   = "admin123"
	StorageFileName   = "admin_panel_auth.json"
	SessionCookieName = "b2a_admin_session"
	DefaultSessionTTL = 12 * time.Hour
	MinPasswordLength = 6
)

type CredentialRecord struct {
	Version      int       `json:"version"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type SessionInfo struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}
