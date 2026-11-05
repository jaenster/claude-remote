package auth

import (
	"net/http"
	"strings"

	"github.com/jaenster/claude-remote/internal/config"
)

func Middleware(cfg config.AuthConfig, next http.Handler) http.Handler {
	if cfg.Issuer == "" && cfg.Token == "" {
		return next
	}

	if cfg.Token != "" {
		return &tokenMiddleware{token: cfg.Token, next: next}
	}

	return &oidcMiddleware{issuer: cfg.Issuer, next: next}
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

type oidcMiddleware struct {
	issuer string
	next   http.Handler
}

func (m *oidcMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if skipAuth(r) {
		m.next.ServeHTTP(w, r)
		return
	}

	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// TODO: validate JWT against the OIDC issuer JWKS.
	// For now, accept any bearer token when issuer is configured.
	// Add github.com/coreos/go-oidc/v3 for proper validation.

	m.next.ServeHTTP(w, r)
}

func skipAuth(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/.well-known/")
}
