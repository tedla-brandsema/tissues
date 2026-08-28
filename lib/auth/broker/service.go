package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

type Client struct {
	ID          string
	Secret      string
	RedirectURI string
}

type ServiceConfig struct {
	SigningSecret []byte
	Clients       map[string]Client
	Entitlements  map[string]map[string]struct{}
	CodeStore     CodeStore
	CodeTTL       time.Duration
	TokenTTL      time.Duration
}

type Service struct {
	cfg   ServiceConfig
	mu    sync.Mutex
	codes map[string]authCode
}

type authCode struct {
	Subject     string
	Email       string
	ClientID    string
	RedirectURI string
	ExpiresAt   time.Time
}

type accessToken struct {
	Subject   string `json:"sub"`
	Email     string `json:"email,omitempty"`
	ClientID  string `json:"client_id"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

type CodeStore interface {
	SaveCode(ctx context.Context, code string, val authCode) error
	ConsumeCode(ctx context.Context, code, clientID, redirectURI string) (authCode, error)
}

func NewService(cfg ServiceConfig) *Service {
	if cfg.CodeTTL <= 0 {
		cfg.CodeTTL = 5 * time.Minute
	}
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 15 * time.Minute
	}
	return &Service{
		cfg:   cfg,
		codes: make(map[string]authCode),
	}
}

func (s *Service) AuthorizeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
		redirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		client, err := s.validateClient(clientID, redirectURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		subject, ok := gcpauth.SubjectFromContext(r.Context())
		if !ok || subject == "" {
			http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
			return
		}
		email, _ := gcpauth.EmailFromContext(r.Context())

		if !s.isEntitled(subject, email, client.ID) {
			slog.Warn("auth broker access denied", "subject", subject, "email", email, "client_id", client.ID)
			redirectWithError(w, r, redirectURI, state, "access_denied")
			return
		}

		code, err := s.issueCode(r.Context(), subject, email, client.ID, redirectURI)
		if err != nil {
			http.Error(w, "failed to issue code", http.StatusInternalServerError)
			return
		}

		u, _ := url.Parse(redirectURI)
		q := u.Query()
		q.Set("code", code)
		if state != "" {
			q.Set("state", state)
		}
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
}

func (s *Service) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(r.FormValue("grant_type")) != "authorization_code" {
			http.Error(w, "unsupported grant_type", http.StatusBadRequest)
			return
		}

		clientID := strings.TrimSpace(r.FormValue("client_id"))
		clientSecret := strings.TrimSpace(r.FormValue("client_secret"))
		redirectURI := strings.TrimSpace(r.FormValue("redirect_uri"))
		codeVal := strings.TrimSpace(r.FormValue("code"))

		client, err := s.validateClient(clientID, redirectURI)
		if err != nil {
			http.Error(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		if clientSecret == "" || clientSecret != client.Secret {
			http.Error(w, "invalid_client", http.StatusUnauthorized)
			return
		}

		code, err := s.consumeCode(r.Context(), codeVal, clientID, redirectURI)
		if err != nil {
			http.Error(w, "invalid_grant", http.StatusBadRequest)
			return
		}

		now := time.Now()
		tok := accessToken{
			Subject:   code.Subject,
			Email:     code.Email,
			ClientID:  clientID,
			IssuedAt:  now.Unix(),
			ExpiresAt: now.Add(s.cfg.TokenTTL).Unix(),
		}
		accessTokenRaw, err := encodeSigned(tok, s.cfg.SigningSecret)
		if err != nil {
			http.Error(w, "server_error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": accessTokenRaw,
			"token_type":   "Bearer",
			"expires_in":   int64(s.cfg.TokenTTL / time.Second),
		})
	})
}

func (s *Service) UserinfoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		var claims accessToken
		if err := decodeSigned(token, s.cfg.SigningSecret, &claims); err != nil {
			http.Error(w, "invalid_token", http.StatusUnauthorized)
			return
		}
		if time.Now().Unix() > claims.ExpiresAt {
			http.Error(w, "expired_token", http.StatusUnauthorized)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"sub":       claims.Subject,
			"email":     claims.Email,
			"client_id": claims.ClientID,
			"exp":       claims.ExpiresAt,
		})
	})
}

func (s *Service) issueCode(ctx context.Context, subject, email, clientID, redirectURI string) (string, error) {
	codeVal, err := randomToken(32)
	if err != nil {
		return "", err
	}
	val := authCode{
		Subject:     subject,
		Email:       email,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		ExpiresAt:   time.Now().Add(s.cfg.CodeTTL),
	}
	if s.cfg.CodeStore != nil {
		if err := s.cfg.CodeStore.SaveCode(ctx, codeVal, val); err != nil {
			return "", err
		}
		return codeVal, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[codeVal] = val
	return codeVal, nil
}

func (s *Service) consumeCode(ctx context.Context, codeVal, clientID, redirectURI string) (authCode, error) {
	if s.cfg.CodeStore != nil {
		return s.cfg.CodeStore.ConsumeCode(ctx, codeVal, clientID, redirectURI)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	code, ok := s.codes[codeVal]
	if !ok {
		return authCode{}, errors.New("missing code")
	}
	delete(s.codes, codeVal)
	if time.Now().After(code.ExpiresAt) {
		return authCode{}, errors.New("expired code")
	}
	if code.ClientID != clientID || code.RedirectURI != redirectURI {
		return authCode{}, errors.New("code mismatch")
	}
	return code, nil
}

func (s *Service) validateClient(clientID, redirectURI string) (Client, error) {
	if clientID == "" || redirectURI == "" {
		return Client{}, errors.New("client_id and redirect_uri are required")
	}
	client, ok := s.cfg.Clients[clientID]
	if !ok {
		return Client{}, fmt.Errorf("unknown client_id")
	}
	if strings.TrimSpace(client.RedirectURI) != redirectURI {
		return Client{}, fmt.Errorf("redirect_uri mismatch")
	}
	return client, nil
}

func (s *Service) isEntitled(subject, email, clientID string) bool {
	if len(s.cfg.Entitlements) == 0 {
		return true
	}
	keys := []string{
		"sub:" + strings.TrimSpace(subject),
		"email:" + strings.ToLower(strings.TrimSpace(email)),
		"*",
	}
	for _, key := range keys {
		apps, ok := s.cfg.Entitlements[key]
		if !ok {
			continue
		}
		if _, ok := apps["*"]; ok {
			return true
		}
		if _, ok := apps[clientID]; ok {
			return true
		}
	}
	return false
}

func bearerToken(header string) (string, bool) {
	raw := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(raw[len("Bearer "):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, code string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
