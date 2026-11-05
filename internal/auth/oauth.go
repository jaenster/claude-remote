package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jaenster/claude-remote/internal/config"
)

type OAuthServer struct {
	cfg              config.AuthConfig
	upstreamMeta     *oidcMeta
	clients          sync.Map
	authCodes        sync.Map
	tokens           sync.Map
	upstreamInitOnce sync.Once
	upstreamInitErr  error
}

type clientRegistration struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name,omitempty"`
	TokenMethod  string   `json:"token_endpoint_auth_method"`
}

type authCode struct {
	Code                string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	UpstreamCode        string
	ExpiresAt           time.Time
}

type accessToken struct {
	Token     string
	ClientID  string
	ExpiresAt time.Time
}

type oidcMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

func NewOAuthServer(cfg config.AuthConfig) *OAuthServer {
	return &OAuthServer{cfg: cfg}
}

func (s *OAuthServer) fetchUpstreamMeta() error {
	s.upstreamInitOnce.Do(func() {
		wellKnown := strings.TrimSuffix(s.cfg.Issuer, "/") + "/.well-known/openid-configuration"
		resp, err := http.Get(wellKnown)
		if err != nil {
			s.upstreamInitErr = fmt.Errorf("fetching OIDC discovery: %w", err)
			return
		}
		defer resp.Body.Close()
		var meta oidcMeta
		if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
			s.upstreamInitErr = fmt.Errorf("decoding OIDC discovery: %w", err)
			return
		}
		s.upstreamMeta = &meta
	})
	return s.upstreamInitErr
}

func (s *OAuthServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleMetadata)
	mux.HandleFunc("POST /oauth/register", s.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("GET /oauth/callback", s.handleCallback)
	mux.HandleFunc("POST /oauth/token", s.handleToken)
}

func (s *OAuthServer) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	base := strings.TrimSuffix(s.cfg.ResourceURL, "/")
	meta := map[string]any{
		"issuer":                             base,
		"authorization_endpoint":             base + "/oauth/authorize",
		"token_endpoint":                     base + "/oauth/token",
		"registration_endpoint":              base + "/oauth/register",
		"response_types_supported":           []string{"code"},
		"grant_types_supported":              []string{"authorization_code"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":   []string{"S256"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

func (s *OAuthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		TokenMethod  string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	if len(req.RedirectURIs) == 0 {
		http.Error(w, `{"error":"invalid_request","error_description":"redirect_uris required"}`, http.StatusBadRequest)
		return
	}

	clientID := randomHex(20)
	reg := clientRegistration{
		ClientID:     clientID,
		RedirectURIs: req.RedirectURIs,
		ClientName:   req.ClientName,
		TokenMethod:  "none",
	}
	s.clients.Store(clientID, &reg)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(reg)
}

func (s *OAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := s.fetchUpstreamMeta(); err != nil {
		http.Error(w, fmt.Sprintf("upstream error: %v", err), http.StatusBadGateway)
		return
	}

	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	codeChallenge := r.URL.Query().Get("code_challenge")
	codeChallengeMethod := r.URL.Query().Get("code_challenge_method")

	if clientID == "" || redirectURI == "" {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	regVal, ok := s.clients.Load(clientID)
	if !ok {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	reg := regVal.(*clientRegistration)
	if !containsStr(reg.RedirectURIs, redirectURI) {
		http.Error(w, `{"error":"invalid_redirect_uri"}`, http.StatusBadRequest)
		return
	}

	internalState := randomHex(32)
	code := &authCode{
		Code:                internalState,
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(10 * time.Minute),
	}
	s.authCodes.Store(internalState, code)

	callbackURL := strings.TrimSuffix(s.cfg.ResourceURL, "/") + "/oauth/callback"
	upstreamState := internalState + "|" + state

	u, _ := url.Parse(s.upstreamMeta.AuthorizationEndpoint)
	q := u.Query()
	q.Set("client_id", s.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", callbackURL)
	q.Set("state", upstreamState)
	q.Set("scope", "openid email profile")
	u.RawQuery = q.Encode()

	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	upstreamCode := r.URL.Query().Get("code")
	upstreamState := r.URL.Query().Get("state")

	parts := strings.SplitN(upstreamState, "|", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	internalState, clientState := parts[0], parts[1]

	codeVal, ok := s.authCodes.Load(internalState)
	if !ok {
		http.Error(w, "unknown state", http.StatusBadRequest)
		return
	}
	ac := codeVal.(*authCode)

	if time.Now().After(ac.ExpiresAt) {
		s.authCodes.Delete(internalState)
		http.Error(w, "expired", http.StatusBadRequest)
		return
	}

	ourCode := randomHex(32)
	ac.UpstreamCode = upstreamCode
	ac.Code = ourCode
	s.authCodes.Delete(internalState)
	s.authCodes.Store(ourCode, ac)

	u, _ := url.Parse(ac.RedirectURI)
	q := u.Query()
	q.Set("code", ourCode)
	if clientState != "" {
		q.Set("state", clientState)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *OAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := s.fetchUpstreamMeta(); err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")
	codeVal, ok := s.authCodes.Load(code)
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	ac := codeVal.(*authCode)
	s.authCodes.Delete(code)

	if time.Now().After(ac.ExpiresAt) {
		http.Error(w, `{"error":"invalid_grant","error_description":"code expired"}`, http.StatusBadRequest)
		return
	}

	if ac.CodeChallenge != "" {
		verifier := r.FormValue("code_verifier")
		if verifier == "" {
			http.Error(w, `{"error":"invalid_grant","error_description":"code_verifier required"}`, http.StatusBadRequest)
			return
		}
		h := sha256.Sum256([]byte(verifier))
		computed := base64.RawURLEncoding.EncodeToString(h[:])
		if computed != ac.CodeChallenge {
			http.Error(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`, http.StatusBadRequest)
			return
		}
	}

	callbackURL := strings.TrimSuffix(s.cfg.ResourceURL, "/") + "/oauth/callback"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {ac.UpstreamCode},
		"redirect_uri":  {callbackURL},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
	}
	resp, err := http.PostForm(s.upstreamMeta.TokenEndpoint, form)
	if err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	var upstream struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}

	ourToken := randomHex(32)
	s.tokens.Store(ourToken, &accessToken{
		Token:     ourToken,
		ClientID:  ac.ClientID,
		ExpiresAt: time.Now().Add(time.Duration(upstream.ExpiresIn) * time.Second),
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": ourToken,
		"token_type":   "Bearer",
		"expires_in":   upstream.ExpiresIn,
	})
}

func (s *OAuthServer) ValidateToken(token string) bool {
	val, ok := s.tokens.Load(token)
	if !ok {
		return false
	}
	t := val.(*accessToken)
	if time.Now().After(t.ExpiresAt) {
		s.tokens.Delete(token)
		return false
	}
	return true
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
