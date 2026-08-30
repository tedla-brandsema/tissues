package auth

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
	"github.com/tedla-brandsema/tissues/lib/service"
)

const (
	ScopeRead  = "tissues:read"
	ScopeWrite = "tissues:write"
)

var supportedScopes = []string{ScopeRead, ScopeWrite}

// Service is an in-process component; it owns auth behavior, routes, and its
// frontend, but no listener, PORT, signals, or deployment lifecycle.
type Service struct {
	profile service.Profile[Config]
	broker  *broker.Service
	login   http.Handler
}

// VerifiedAccessToken is the broker-neutral access-token identity exposed to
// relying Services. Transport-specific authentication types stay at the
// relying Service boundary.
type VerifiedAccessToken struct {
	Subject   string
	Email     string
	ClientID  string
	Scopes    []string
	ExpiresAt time.Time
}

// ErrInvalidAccessToken identifies a token that cannot be used for this
// Service's configured issuer and MCP resource.
var ErrInvalidAccessToken = errors.New("invalid access token")

// VerifyAccessToken verifies a bearer token against the active canonical
// issuer and MCP resource.
func (s *Service) VerifyAccessToken(token string) (VerifiedAccessToken, error) {
	cfg := s.profile.Current().Config
	verified, err := s.broker.VerifyAccessToken(token, cfg.IssuerURL, cfg.MCPResourceURL)
	if err != nil {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	return VerifiedAccessToken{
		Subject: verified.Subject, Email: verified.Email, ClientID: verified.ClientID,
		Scopes: append([]string(nil), verified.Scopes...), ExpiresAt: verified.ExpiresAt,
	}, nil
}

var _ service.Service = (*Service)(nil)

//go:embed frontend/generated
var frontendFiles embed.FS

func New(profile service.Profile[Config], codeStore broker.CodeStore) (*Service, error) {
	if profile == nil {
		return nil, fmt.Errorf("auth profile is required")
	}
	cfg := profile.Current().Config
	if err := cfg.ValidateConfig(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("auth Service is inactive")
	}
	if codeStore == nil {
		return nil, fmt.Errorf("auth CodeStore is required")
	}
	clientMetadataURLs, err := parseClientMetadataURLs(cfg.ClientMetadataURLList)
	if err != nil {
		return nil, err
	}
	clientResolver, err := broker.NewCIMDResolver(clientMetadataURLs, nil)
	if err != nil {
		return nil, err
	}
	brokerService := broker.NewService(broker.ServiceConfig{SigningSecret: []byte(cfg.SigningSecret), Issuer: cfg.IssuerURL, Resource: cfg.MCPResourceURL, Scopes: supportedScopes, ScopeImplications: map[string][]string{ScopeWrite: {ScopeRead}}, Clients: map[string]broker.Client{cfg.ClientID: {ID: cfg.ClientID, Secret: cfg.ClientSecret, RedirectURIs: []string{cfg.ClientRedirectURI}, TokenEndpointAuthMethod: broker.TokenEndpointAuthMethodClientSecretPost}}, ClientResolver: clientResolver, Entitlements: parseEntitlements(cfg.Entitlements), CodeStore: codeStore})
	frontend, err := newFrontendHandler(frontendFiles, "/auth/login")
	if err != nil {
		return nil, err
	}
	login, err := gcpauth.New(gcpauth.Config{BasePath: "/auth/login", Secret: []byte(cfg.SigningSecret), LoginRedirect: "/authorize", InsecureCookie: cfg.InsecureCookie, APIKey: cfg.IdentityAPIKey, TenantID: cfg.IdentityTenantID}, gcpauth.Frontend{GET: frontend})
	if err != nil {
		return nil, err
	}
	return &Service{profile: profile, broker: brokerService, login: login}, nil
}

func (*Service) Name() string { return "auth" }

func (s *Service) RegisterRoutes(mux *http.ServeMux) error {
	cfg := s.profile.Current().Config
	mux.Handle("GET /authorize", gcpauth.Middleware([]byte(cfg.SigningSecret), "/auth/login")(s.broker.AuthorizeHandler()))
	mux.Handle("POST /token", s.broker.TokenHandler())
	mux.Handle("GET /userinfo", s.broker.UserinfoHandler())
	mux.Handle("GET /.well-known/oauth-authorization-server", authorizationServerMetadataHandler(cfg))
	mux.Handle("/auth/login", s.login)
	mux.Handle("/auth/login/", s.login)
	mux.Handle("GET /auth/logout", gcpauth.LogoutHandler("/auth/login", cfg.InsecureCookie))
	return nil
}

type authorizationServerMetadata struct {
	Issuer                                      string   `json:"issuer"`
	AuthorizationEndpoint                       string   `json:"authorization_endpoint"`
	TokenEndpoint                               string   `json:"token_endpoint"`
	ResponseTypesSupported                      []string `json:"response_types_supported"`
	GrantTypesSupported                         []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported           []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported               []string `json:"code_challenge_methods_supported"`
	ScopesSupported                             []string `json:"scopes_supported"`
	AuthorizationResponseIssuerParameterSupport bool     `json:"authorization_response_iss_parameter_supported"`
	ClientIDMetadataDocumentSupported           bool     `json:"client_id_metadata_document_supported"`
}

func authorizationServerMetadataHandler(cfg Config) http.Handler {
	metadata := authorizationServerMetadata{
		Issuer:                                      cfg.IssuerURL,
		AuthorizationEndpoint:                       cfg.IssuerURL + "/authorize",
		TokenEndpoint:                               cfg.IssuerURL + "/token",
		ResponseTypesSupported:                      []string{"code"},
		GrantTypesSupported:                         []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported:           []string{"none", "client_secret_post"},
		CodeChallengeMethodsSupported:               []string{"S256"},
		ScopesSupported:                             append([]string(nil), supportedScopes...),
		AuthorizationResponseIssuerParameterSupport: true,
		ClientIDMetadataDocumentSupported:           true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	})
}

func parseEntitlements(raw string) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, entry := range strings.Split(strings.TrimSpace(raw), ";") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		clients := make(map[string]struct{})
		for _, client := range strings.Split(parts[1], ",") {
			if client = strings.TrimSpace(client); client != "" {
				clients[client] = struct{}{}
			}
		}
		if len(clients) > 0 {
			out[strings.TrimSpace(parts[0])] = clients
		}
	}
	return out
}
