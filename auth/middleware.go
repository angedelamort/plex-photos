package auth

import (
	"context"
	"net/http"
)

type ctxKey int

const userCtxKey ctxKey = 0

// Middleware wires session verification into the HTTP handler chain.
type Middleware struct {
	sessions *SessionManager
}

// NewMiddleware creates auth middleware backed by the given session manager.
func NewMiddleware(s *SessionManager) *Middleware {
	return &Middleware{sessions: s}
}

// RequireAuth rejects requests without a valid session (401) and injects the
// session into the request context.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := m.sessions.Read(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, s)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin requires a valid session whose user is an admin (403 otherwise).
func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := FromContext(r.Context())
		if s == nil || !s.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// FromContext returns the session stored in the request context, or nil.
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(userCtxKey).(*Session)
	return s
}
