package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionCookieName = "pp_session"

// sessionKeyFile is the filename, under the data dir, where an auto-generated
// session secret is persisted so cookies survive restarts.
const sessionKeyFile = "session.key"

// ResolveSessionSecret returns the cookie-signing secret, preferring an
// explicit value (e.g. from SESSION_SECRET) and otherwise reading a persisted
// key from dataDir, generating and storing a new random one on first run.
//
// This mirrors how Overseerr and the *arr apps manage their signing keys: the
// operator never has to supply one, but can override it via env if desired.
func ResolveSessionSecret(explicit, dataDir string) (string, error) {
	if s := strings.TrimSpace(explicit); s != "" {
		return s, nil
	}

	path := filepath.Join(dataDir, sessionKeyFile)
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read session key: %w", err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session key: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)

	// 0600: the key is sensitive and only the app needs it.
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("persist session key: %w", err)
	}
	return secret, nil
}

// sessionTTL is how long an issued session remains valid.
const sessionTTL = 30 * 24 * time.Hour

// Session is the signed payload stored in the cookie.
type Session struct {
	Username string `json:"u"`
	Email    string `json:"e"`
	IsAdmin  bool   `json:"a"`
	Expires  int64  `json:"x"`
}

// SessionManager signs and verifies session cookies using HMAC-SHA256.
type SessionManager struct {
	secret []byte
	secure bool
}

// NewSessionManager creates a manager with the given signing secret. secure
// controls whether the Secure cookie flag is set (disable for local HTTP dev).
func NewSessionManager(secret string, secure bool) *SessionManager {
	return &SessionManager{secret: []byte(secret), secure: secure}
}

func (m *SessionManager) sign(payload []byte) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Set issues a signed session cookie for the given user.
func (m *SessionManager) Set(w http.ResponseWriter, u *User) error {
	s := Session{
		Username: u.Username,
		Email:    u.Email,
		IsAdmin:  u.IsAdmin,
		Expires:  time.Now().Add(sessionTTL).Unix(),
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	value := encoded + "." + m.sign(payload)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(s.Expires, 0),
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

// Read verifies and decodes the session cookie from the request.
func (m *SessionManager) Read(r *http.Request) (*Session, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, errors.New("no session cookie")
	}

	var encoded, sig string
	for i := len(c.Value) - 1; i >= 0; i-- {
		if c.Value[i] == '.' {
			encoded, sig = c.Value[:i], c.Value[i+1:]
			break
		}
	}
	if encoded == "" || sig == "" {
		return nil, errors.New("malformed session cookie")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}

	expected := m.sign(payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return nil, errors.New("invalid session signature")
	}

	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	if time.Now().Unix() > s.Expires {
		return nil, errors.New("session expired")
	}
	return &s, nil
}

// Clear removes the session cookie.
func (m *SessionManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
