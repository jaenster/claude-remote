package auth

import (
	"net/http"
	"strings"

	"github.com/jaenster/claude-remote/internal/config"
)

func Middleware(cfg config.AuthConfig, oauth *OAuthServer, next http.Handler) http.Handler {
	if cfg.Issuer == "" && cfg.Token == "" {
		return next
	}

	if cfg.Token != "" {
		return &tokenMiddleware{token: cfg.Token, next: next}
	}

	return &oauthMiddleware{oauth: oauth, next: next}
}

type tokenMiddleware struct {
	token string
	next  http.Handler
}

func (m *tokenMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if skipAuth(r) {
		m.next.ServeHTTP(w, r)
		return
	}

	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.TrimPrefix(bearer, "Bearer ") != m.token {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	m.next.ServeHTTP(w, r)
}

type oauthMiddleware struct {
	oauth *OAuthServer
	next  http.Handler
}

func (m *oauthMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if skipAuth(r) {
		m.next.ServeHTTP(w, r)
		return
	}

	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(bearer, "Bearer ")
	if !m.oauth.ValidateToken(token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	m.next.ServeHTTP(w, r)
}

func skipAuth(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/.well-known/") ||
		strings.HasPrefix(p, "/oauth/")
}
