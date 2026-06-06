package auth

import "net/http"

// User is the authenticated identity returned by a Provider.
type User struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"isAdmin"`
}

// Provider abstracts an authentication backend (real Plex SSO or a dev mock).
type Provider interface {
	// StartLogin begins the login flow and returns the URL the browser should be
	// redirected to in order to authenticate. For providers without an external
	// step (mock), it may complete immediately and return the local callback URL.
	StartLogin(w http.ResponseWriter, r *http.Request) (redirectURL string, err error)

	// HandleCallback completes the login flow from the callback request and
	// returns the authenticated user.
	HandleCallback(w http.ResponseWriter, r *http.Request) (*User, error)
}
