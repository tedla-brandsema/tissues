package auth

import (
	"embed"
	"fmt"
	"net/http"
	"strings"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
	"github.com/tedla-brandsema/tissues/lib/service"
)

// Service is an in-process component; it owns auth behavior, routes, and its
// frontend, but no listener, PORT, signals, or deployment lifecycle.
type Service struct {
	profile service.Profile[Config]
	broker  *broker.Service
	login   http.Handler
}

var _ service.Service = (*Service)(nil)

//go:embed frontend/generated
var frontendFiles embed.FS

func New(profile service.Profile[Config], client *gcds.Client) (*Service, error) {
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
	if client == nil {
		return nil, fmt.Errorf("auth Datastore client is required")
	}
	brokerService := broker.NewService(broker.ServiceConfig{SigningSecret: []byte(cfg.SigningSecret), Clients: map[string]broker.Client{cfg.ClientID: {ID: cfg.ClientID, Secret: cfg.ClientSecret, RedirectURI: cfg.ClientRedirectURI}}, Entitlements: parseEntitlements(cfg.Entitlements), CodeStore: broker.NewDatastoreCodeStore(client, cfg.DatastoreNS, cfg.DatastoreKind)})
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
	mux.Handle("/auth/login", s.login)
	mux.Handle("/auth/login/", s.login)
	mux.Handle("GET /auth/logout", gcpauth.LogoutHandler("/auth/login", cfg.InsecureCookie))
	return nil
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
