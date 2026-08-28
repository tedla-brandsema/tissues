package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
	"github.com/tedla-brandsema/tissues/lib/service"
)

const serviceName = "tissues-auth"

//go:embed frontend
var frontendFiles embed.FS

type appConfig struct {
	Server  service.Config
	Service authServiceConfig
}

func (cfg appConfig) ValidateConfig() error {
	if err := cfg.Server.ValidateConfig(); err != nil {
		return err
	}
	return cfg.Service.ValidateConfig()
}

type authServiceConfig struct {
	coreconfig.Contribution
	SigningSecret     string `cfg:"string,required=true,secret=true,restart=true"`
	ClientID          string `cfg:"string,default=tissues,restart=true"`
	ClientSecret      string `cfg:"string,required=true,secret=true,restart=true"`
	ClientRedirectURI string `cfg:"string,required=true,restart=true"`
	ProjectID         string `cfg:"string,required=true,restart=true"`
	DatastoreNS       string `cfg:"string,default=tissues-auth,restart=true"`
	DatastoreKind     string `cfg:"string,default=tissues_auth_code,restart=true"`
	IdentityAPIKey    string `cfg:"string,required=true,secret=true,restart=true"`
	IdentityTenantID  string `cfg:"string,restart=true"`
	Entitlements      string `cfg:"string"`
	InsecureCookie    bool   `cfg:"bool,default=false,restart=true"`
}

var _ coreconfig.ServiceContribution = authServiceConfig{}

func (cfg authServiceConfig) ValidateConfig() error {
	if len(cfg.SigningSecret) < 32 {
		return fmt.Errorf("SigningSecret must be at least 32 bytes")
	}
	for path, value := range map[string]string{
		"ClientSecret": cfg.ClientSecret, "ProjectID": cfg.ProjectID, "IdentityAPIKey": cfg.IdentityAPIKey,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", path)
		}
	}
	parsed, err := url.Parse(cfg.ClientRedirectURI)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("ClientRedirectURI must be an absolute HTTP or HTTPS URL")
	}
	return nil
}

func main() {
	if err := run(context.Background(), os.Args[1:], coreconfig.SystemEnvironment()); err != nil {
		if errors.Is(err, coreconfig.ErrHelp) {
			return
		}
		slog.Error("auth service failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, environment coreconfig.Environment) error {
	selection, err := coreconfig.Bootstrap("TISSUES_AUTH", environment, args)
	if err != nil {
		return err
	}
	profile, err := coreconfig.Load[appConfig](ctx, coreconfig.LoadOptions{
		Name: selection.Name, Prefix: "TISSUES_AUTH", Store: coreconfig.NewFileStore(selection.Directory),
		Environment: environment, Args: selection.Args, FlagOutput: os.Stderr,
	})
	if err != nil {
		return err
	}
	serviceProfile, err := coreconfig.NewServiceProfile(profile.Name, profile.Config.Service)
	if err != nil {
		return err
	}
	serviceSlot, err := coreconfig.NewSlot(serviceProfile)
	if err != nil {
		return err
	}
	cfg := serviceSlot.Current().Config

	dsClient, err := gcds.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("create Datastore client: %w", err)
	}
	defer dsClient.Close()

	svc, err := service.New(ctx, serviceName, profile.Config.Server)
	if err != nil {
		return err
	}
	brokerSvc := broker.NewService(broker.ServiceConfig{
		SigningSecret: []byte(cfg.SigningSecret),
		Clients: map[string]broker.Client{cfg.ClientID: {
			ID: cfg.ClientID, Secret: cfg.ClientSecret, RedirectURI: cfg.ClientRedirectURI,
		}},
		Entitlements: parseEntitlements(cfg.Entitlements),
		CodeStore:    broker.NewDatastoreCodeStore(dsClient, cfg.DatastoreNS, cfg.DatastoreKind),
	})
	frontend, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		return err
	}
	loginHandler, err := gcpauth.New(gcpauth.Config{
		BasePath: "/auth/login", Secret: []byte(cfg.SigningSecret), LoginRedirect: "/authorize",
		InsecureCookie: cfg.InsecureCookie, APIKey: cfg.IdentityAPIKey, TenantID: cfg.IdentityTenantID,
	}, gcpauth.Frontend{Static: frontend, Templates: frontend})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /authorize", gcpauth.Middleware([]byte(cfg.SigningSecret), "/auth/login")(brokerSvc.AuthorizeHandler()))
	mux.Handle("POST /token", brokerSvc.TokenHandler())
	mux.Handle("GET /userinfo", brokerSvc.UserinfoHandler())
	mux.Handle("/auth/login", loginHandler)
	mux.Handle("/auth/login/", loginHandler)
	mux.Handle("GET /auth/logout", gcpauth.LogoutHandler("/auth/login", !cfg.InsecureCookie))
	svc.SetMux(mux)
	return svc.StartAndWait()
}

// Entitlements use the form "sub:uid=tissues;email:user@example.com=tissues".
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
