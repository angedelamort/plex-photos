package auth

import "net/http"

// MockProvider is a development auth provider that logs in automatically as a
// fixed configured user without contacting any external service.
type MockProvider struct {
	Username string
	IsAdmin  bool
}

// NewMockProvider creates a mock provider for the given user.
func NewMockProvider(username string, isAdmin bool) *MockProvider {
	if username == "" {
		username = "dev"
	}
	return &MockProvider{Username: username, IsAdmin: isAdmin}
}

// StartLogin skips any external step and sends the browser straight to the callback.
func (p *MockProvider) StartLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	return "/auth/callback", nil
}

// HandleCallback returns the configured mock user.
func (p *MockProvider) HandleCallback(w http.ResponseWriter, r *http.Request) (*User, error) {
	return &User{
		Username: p.Username,
		Email:    p.Username + "@mock.local",
		IsAdmin:  p.IsAdmin,
	}, nil
}
