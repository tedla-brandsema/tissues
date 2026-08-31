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
	ID                      string
	Secret                  string
	RedirectURIs            []string
	TokenEndpointAuthMethod TokenEndpointAuthMethod
	ExactRedirectURIs       bool
}

type TokenEndpointAuthMethod string

const (
	TokenEndpointAuthMethodNone             TokenEndpointAuthMethod = "none"
	TokenEndpointAuthMethodClientSecretPost TokenEndpointAuthMethod = "client_secret_post"
)

type ServiceConfig struct {
	SigningSecret     []byte
	Issuer            string
	Resource          string
	Scopes            []string
	ScopeImplications map[string][]string
	Clients           map[string]Client
	ClientResolver    ClientResolver
	Entitlements      map[string]map[string]struct{}
	CodeStore         CodeStore
	CodeTTL           time.Duration
	TokenTTL          time.Duration
}

type Service struct {
	cfg   ServiceConfig
	mu    sync.Mutex
	codes map[string]authCode
}

type authCode struct {
	Subject             string
	Email               string
	ClientID            string
	RedirectURI         string
	Resource            string
	Scopes              []string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

type accessToken struct {
	Issuer    string `json:"iss"`
	Resource  string `json:"aud,omitempty"`
	Subject   string `json:"sub"`
	Email     string `json:"email,omitempty"`
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope,omitempty"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
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

		query := r.URL.Query()
		clientIDs, hasClientID := query["client_id"]
		redirectURIs, hasRedirectURI := query["redirect_uri"]
		if !hasClientID || len(clientIDs) != 1 || strings.TrimSpace(clientIDs[0]) == "" || !hasRedirectURI || len(redirectURIs) != 1 || strings.TrimSpace(redirectURIs[0]) == "" {
			http.Error(w, "client_id and redirect_uri are required", http.StatusBadRequest)
			return
		}
		client, redirectURI, err := s.validateClient(r.Context(), clientIDs[0], redirectURIs[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		states, hasState := query["state"]
		if hasState && len(states) != 1 {
			redirectWithError(w, r, redirectURI, "", s.cfg.Issuer, "invalid_request")
			return
		}
		state := strings.TrimSpace(query.Get("state"))
		responseTypes, hasResponseType := query["response_type"]
		if !hasResponseType || len(responseTypes) != 1 || responseTypes[0] == "" {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "invalid_request")
			return
		}
		if responseTypes[0] != "code" {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "unsupported_response_type")
			return
		}
		if client.TokenEndpointAuthMethod != TokenEndpointAuthMethodNone && client.TokenEndpointAuthMethod != TokenEndpointAuthMethodClientSecretPost {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "unauthorized_client")
			return
		}
		publicClient := client.TokenEndpointAuthMethod == TokenEndpointAuthMethodNone
		resources, hasResource := query["resource"]
		resource := query.Get("resource")
		if (publicClient && (!hasResource || len(resources) != 1 || resource == "" || resource != s.cfg.Resource)) || (!publicClient && hasResource && (len(resources) != 1 || resource == "" || resource != s.cfg.Resource)) {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "invalid_target")
			return
		}
		scopesRaw, hasScope := query["scope"]
		if hasScope && len(scopesRaw) != 1 {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "invalid_scope")
			return
		}
		scopes, err := canonicalScopes(query.Get("scope"), s.cfg.Scopes, s.cfg.ScopeImplications)
		if err != nil || (hasScope && len(scopes) == 0) || (publicClient && (!hasScope || len(scopes) == 0)) {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "invalid_scope")
			return
		}
		codeChallenge, codeChallengeMethod, err := authorizationPKCE(query, publicClient)
		if err != nil {
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "invalid_request")
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
			redirectWithError(w, r, redirectURI, state, s.cfg.Issuer, "access_denied")
			return
		}

		code, err := s.issueCode(r.Context(), subject, email, client.ID, redirectURI, resource, scopes, codeChallenge, codeChallengeMethod)
		if err != nil {
			http.Error(w, "failed to issue code", http.StatusInternalServerError)
			return
		}

		u, _ := url.Parse(redirectURI)
		q := u.Query()
		q.Set("code", code)
		q.Set("iss", s.cfg.Issuer)
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
			writeTokenError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		grantType, hasGrantType := singlePostFormValue(r, "grant_type")
		if !hasGrantType || strings.TrimSpace(grantType) != "authorization_code" {
			writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type")
			return
		}

		clientIDRaw, hasClientID := singlePostFormValue(r, "client_id")
		if !hasClientID {
			writeTokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
		clientID := clientIDRaw
		redirectURIRaw, hasRedirectURI := singlePostFormValue(r, "redirect_uri")
		codeValRaw, hasCode := singlePostFormValue(r, "code")
		if !hasRedirectURI || !hasCode {
			writeTokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		redirectURI := redirectURIRaw
		codeVal := strings.TrimSpace(codeValRaw)
		resources, hasResource := r.PostForm["resource"]
		resource := ""
		if len(resources) == 1 {
			resource = resources[0]
		}
		if len(r.Form["resource"]) != len(resources) || (hasResource && (len(resources) != 1 || resource == "")) {
			writeTokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}

		client, err := s.resolveClient(r.Context(), clientID)
		if err != nil {
			writeTokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
		clientID = client.ID
		if !client.ExactRedirectURIs {
			redirectURI = strings.TrimSpace(redirectURI)
		}
		if !matchesRedirectURI(client.RedirectURIs, redirectURI, allowsLoopbackPort(client)) {
			writeTokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		codeVerifier := ""
		switch client.TokenEndpointAuthMethod {
		case TokenEndpointAuthMethodClientSecretPost:
			clientSecrets, ok := r.PostForm["client_secret"]
			if !ok || len(clientSecrets) != 1 || len(r.Form["client_secret"]) != 1 || clientSecrets[0] == "" || clientSecrets[0] != client.Secret {
				writeTokenError(w, http.StatusUnauthorized, "invalid_client")
				return
			}
			if _, ok := r.Form["code_verifier"]; ok {
				writeTokenError(w, http.StatusBadRequest, "invalid_grant")
				return
			}
		case TokenEndpointAuthMethodNone:
			if _, ok := r.Form["client_secret"]; ok {
				writeTokenError(w, http.StatusUnauthorized, "invalid_client")
				return
			}
			verifiers, ok := r.PostForm["code_verifier"]
			if !ok || len(verifiers) != 1 || len(r.Form["code_verifier"]) != 1 || !validPKCEValue(verifiers[0]) {
				writeTokenError(w, http.StatusBadRequest, "invalid_grant")
				return
			}
			codeVerifier = verifiers[0]
		default:
			writeTokenError(w, http.StatusUnauthorized, "invalid_client")
			return
		}

		code, err := s.consumeCode(r.Context(), codeVal, clientID, redirectURI, resource, codeVerifier)
		if err != nil {
			writeTokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}

		now := time.Now()
		tok := accessToken{
			Issuer:    s.cfg.Issuer,
			Resource:  code.Resource,
			Subject:   code.Subject,
			Email:     code.Email,
			ClientID:  clientID,
			Scope:     strings.Join(code.Scopes, " "),
			IssuedAt:  now.Unix(),
			ExpiresAt: now.Add(s.cfg.TokenTTL).Unix(),
		}
		accessTokenRaw, err := encodeSigned(tok, s.cfg.SigningSecret)
		if err != nil {
			writeTokenError(w, http.StatusInternalServerError, "server_error")
			return
		}

		response := map[string]any{
			"access_token": accessTokenRaw,
			"token_type":   "Bearer",
			"expires_in":   int64(s.cfg.TokenTTL / time.Second),
		}
		if tok.Scope != "" {
			response["scope"] = tok.Scope
		}
		writeTokenJSON(w, http.StatusOK, response)
	})
}

func singlePostFormValue(r *http.Request, key string) (string, bool) {
	values, ok := r.PostForm[key]
	if !ok || len(values) != 1 || len(r.Form[key]) != 1 {
		return "", false
	}
	return values[0], true
}

func writeTokenError(w http.ResponseWriter, status int, code string) {
	writeTokenJSON(w, status, map[string]string{"error": code})
}

func writeTokenJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
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

		claims, err := s.VerifyAccessToken(token, s.cfg.Issuer, "")
		if err != nil {
			http.Error(w, "invalid_token", http.StatusUnauthorized)
			return
		}
		if time.Now().After(claims.ExpiresAt) {
			http.Error(w, "expired_token", http.StatusUnauthorized)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"sub":       claims.Subject,
			"email":     claims.Email,
			"client_id": claims.ClientID,
			"exp":       claims.ExpiresAt.Unix(),
		})
	})
}

func (s *Service) issueCode(ctx context.Context, subject, email, clientID, redirectURI, resource string, scopes []string, codeChallenge, codeChallengeMethod string) (string, error) {
	codeVal, err := randomToken(32)
	if err != nil {
		return "", err
	}
	val := authCode{
		Subject:             subject,
		Email:               email,
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Resource:            resource,
		Scopes:              append([]string(nil), scopes...),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(s.cfg.CodeTTL),
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

func (s *Service) consumeCode(ctx context.Context, codeVal, clientID, redirectURI, resource, codeVerifier string) (authCode, error) {
	if s.cfg.CodeStore != nil {
		return s.cfg.CodeStore.ConsumeCode(ctx, codeVal, clientID, redirectURI, resource, codeVerifier)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	code, ok := s.codes[codeVal]
	if !ok {
		return authCode{}, errors.New("missing code")
	}
	if err := validateCodeBinding(code, clientID, redirectURI, resource, codeVerifier, time.Now()); err != nil {
		return authCode{}, err
	}
	delete(s.codes, codeVal)
	return code, nil
}

func (s *Service) validateClient(ctx context.Context, clientID, redirectURI string) (Client, string, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(redirectURI) == "" {
		return Client{}, "", errors.New("client_id and redirect_uri are required")
	}
	client, err := s.resolveClient(ctx, clientID)
	if err != nil {
		return Client{}, "", fmt.Errorf("unknown client_id")
	}
	if !client.ExactRedirectURIs {
		redirectURI = strings.TrimSpace(redirectURI)
	}
	if !matchesRedirectURI(client.RedirectURIs, redirectURI, allowsLoopbackPort(client)) {
		return Client{}, "", fmt.Errorf("redirect_uri mismatch")
	}
	return client, redirectURI, nil
}

func (s *Service) resolveClient(ctx context.Context, clientID string) (Client, error) {
	staticClientID := strings.TrimSpace(clientID)
	if client, ok := s.cfg.Clients[staticClientID]; ok && client.ID == staticClientID {
		return client, nil
	}
	if s.cfg.ClientResolver == nil {
		return Client{}, errors.New("unknown client")
	}
	client, err := s.cfg.ClientResolver.ResolveClient(ctx, clientID)
	if err != nil || client.ID != clientID {
		return Client{}, errors.New("unknown client")
	}
	return client, nil
}

func allowsLoopbackPort(client Client) bool {
	return client.TokenEndpointAuthMethod == TokenEndpointAuthMethodNone
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

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, issuer, code string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("iss", issuer)
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
