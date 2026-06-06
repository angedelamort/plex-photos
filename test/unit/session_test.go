package unit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"plex-photos/auth"
)

func TestSessionRoundTrip(t *testing.T) {
	mgr := auth.NewSessionManager("test-secret-test-secret-test-secret", false)

	rec := httptest.NewRecorder()
	user := &auth.User{Username: "alice", Email: "alice@example.com", IsAdmin: true}
	if err := mgr.Set(rec, user); err != nil {
		t.Fatalf("set session: %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie to be set")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}

	s, err := mgr.Read(req)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if s.Username != "alice" || !s.IsAdmin || s.Email != "alice@example.com" {
		t.Fatalf("unexpected session payload: %+v", s)
	}
}

func TestSessionRejectsTamperedCookie(t *testing.T) {
	mgr := auth.NewSessionManager("test-secret-test-secret-test-secret", false)

	rec := httptest.NewRecorder()
	if err := mgr.Set(rec, &auth.User{Username: "bob"}); err != nil {
		t.Fatalf("set session: %v", err)
	}
	cookie := rec.Result().Cookies()[0]

	// Tamper with the signature portion.
	cookie.Value = cookie.Value + "x"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	if _, err := mgr.Read(req); err == nil {
		t.Fatal("expected tampered cookie to be rejected")
	}
}

func TestSessionRejectsWrongSecret(t *testing.T) {
	signer := auth.NewSessionManager("secret-one-secret-one-secret-one", false)
	verifier := auth.NewSessionManager("secret-two-secret-two-secret-two", false)

	rec := httptest.NewRecorder()
	if err := signer.Set(rec, &auth.User{Username: "carol"}); err != nil {
		t.Fatalf("set session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	if _, err := verifier.Read(req); err == nil {
		t.Fatal("expected session signed with a different secret to be rejected")
	}
}

func TestResolveSessionSecretPrefersExplicit(t *testing.T) {
	dir := t.TempDir()
	got, err := auth.ResolveSessionSecret("  explicit-secret  ", dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "explicit-secret" {
		t.Fatalf("got %q, want trimmed explicit secret", got)
	}
	// Explicit value must not be written to disk.
	if _, err := os.Stat(filepath.Join(dir, "session.key")); !os.IsNotExist(err) {
		t.Fatal("explicit secret should not create a session.key file")
	}
}

func TestResolveSessionSecretGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()

	first, err := auth.ResolveSessionSecret("", dir)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if len(first) < 32 {
		t.Fatalf("generated secret too short: %d chars", len(first))
	}

	keyPath := filepath.Join(dir, "session.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected session.key to be persisted: %v", err)
	}

	// A second call must reuse the persisted key (stable across restarts).
	second, err := auth.ResolveSessionSecret("", dir)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if first != second {
		t.Fatalf("secret changed between runs: %q != %q", first, second)
	}
}

func TestReadWithoutCookie(t *testing.T) {
	mgr := auth.NewSessionManager("test-secret-test-secret-test-secret", false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := mgr.Read(req); err == nil {
		t.Fatal("expected error when no cookie present")
	}
}
