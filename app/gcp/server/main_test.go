package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/server"
	"github.com/tedla-brandsema/tissues/lib/service"
	authservice "github.com/tedla-brandsema/tissues/services/auth"
	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestComposeCanRunWithServicesInactive(t *testing.T) {
	cfg := appConfig{Server: server.Config{Host: "127.0.0.1", Port: 0, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 1024}}
	profile := coreconfig.Profile[appConfig]{Name: "test", Config: cfg}
	srv, closers, err := compose(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if len(closers) != 0 {
		t.Fatalf("closers=%d,want 0", len(closers))
	}
}

func TestConcreteServicesShareSDK(t *testing.T) {
	active := []service.Service{(*authservice.Service)(nil), (*tissues.Service)(nil)}
	if len(active) != 2 {
		t.Fatalf("active services = %d, want 2", len(active))
	}
}

func TestTissuesMCPAuthWiresCanonicalIdentityAndVerifier(t *testing.T) {
	wantErr := errors.New("rejected")
	called := ""
	cfg := authservice.Config{IssuerURL: "https://auth.example.test", MCPResourceURL: "https://tissues.example.test/mcp"}
	bridge := tissuesMCPAuth(cfg, func(token string) (authservice.VerifiedAccessToken, error) {
		called = token
		if token == "bad" {
			return authservice.VerifiedAccessToken{}, wantErr
		}
		return authservice.VerifiedAccessToken{
			Subject: "subject-1", Email: "person@example.test", ClientID: "client-1",
			Scopes: []string{authservice.ScopeRead}, ExpiresAt: time.Unix(1234, 0),
		}, nil
	})
	if bridge.Issuer != cfg.IssuerURL || bridge.Resource != cfg.MCPResourceURL || bridge.Verify == nil {
		t.Fatalf("bridge = %#v", bridge)
	}
	verified, err := bridge.Verify(context.Background(), "good")
	if err != nil || called != "good" || verified.Subject != "subject-1" || verified.Email != "person@example.test" || verified.ClientID != "client-1" || !reflect.DeepEqual(verified.Scopes, []string{authservice.ScopeRead}) || !verified.ExpiresAt.Equal(time.Unix(1234, 0)) {
		t.Fatalf("verified=%#v called=%q err=%v", verified, called, err)
	}
	verified.Scopes[0] = "mutated"
	again, err := bridge.Verify(context.Background(), "good")
	if err != nil || again.Scopes[0] != authservice.ScopeRead {
		t.Fatalf("bridge did not copy scopes: %#v err=%v", again, err)
	}
	if _, err := bridge.Verify(context.Background(), "bad"); !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
}

func TestProductionAssetAndTimeoutEnvironmentLoads(t *testing.T) {
	profile, err := coreconfig.Load[appConfig](context.Background(), coreconfig.LoadOptions{
		Prefix: "TISSUES",
		Environment: coreconfig.MapEnvironment{
			"TISSUES_SERVER_READ_TIMEOUT":           "60s",
			"TISSUES_SERVER_WRITE_TIMEOUT":          "60s",
			"TISSUES_TISSUES_BOOTSTRAP_TENANT_ID":   "7womw3jzkek74oggxj6f42xak4",
			"TISSUES_TISSUES_STORAGE_PROJECT_ID":    "tissues-dev",
			"TISSUES_TISSUES_STORAGE_NAMESPACE":     "tissues",
			"TISSUES_TISSUES_ASSETS_BUCKET":         "tissues-dev-tissues-assets-production",
			"TISSUES_AUTH_ISSUER_URL":               "https://tissues.example.test",
			"TISSUES_AUTH_MCP_RESOURCE_URL":         "https://tissues.example.test/mcp",
			"TISSUES_AUTH_CLIENT_METADATA_URL_LIST": "https://client.example.test/oauth/client.json",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Config.Server.ReadTimeout != 60*time.Second || profile.Config.Server.WriteTimeout != 60*time.Second {
		t.Fatalf("timeouts = %s/%s", profile.Config.Server.ReadTimeout, profile.Config.Server.WriteTimeout)
	}
	if got := profile.Config.Tissues.Assets.Bucket; got != "tissues-dev-tissues-assets-production" {
		t.Fatalf("asset bucket = %q", got)
	}
	if got := profile.Config.Tissues.BootstrapTenantID; got != "7womw3jzkek74oggxj6f42xak4" {
		t.Fatalf("bootstrap tenant = %q", got)
	}
	if profile.Config.Auth.IssuerURL != "https://tissues.example.test" || profile.Config.Auth.MCPResourceURL != "https://tissues.example.test/mcp" {
		t.Fatalf("auth canonical URLs = %q / %q", profile.Config.Auth.IssuerURL, profile.Config.Auth.MCPResourceURL)
	}
	if profile.Config.Auth.ClientMetadataURLList != "https://client.example.test/oauth/client.json" {
		t.Fatalf("auth Client Metadata URL list = %q", profile.Config.Auth.ClientMetadataURLList)
	}
}

type routeService struct {
	name string
	err  error
}

func (s routeService) Name() string                        { return s.name }
func (s routeService) RegisterRoutes(*http.ServeMux) error { return s.err }

func TestRegisterServicesUsesUniformContractAndNamesErrors(t *testing.T) {
	want := errors.New("broken")
	err := registerServices(http.NewServeMux(), []service.Service{routeService{name: "first"}, routeService{name: "second", err: want}})
	if !errors.Is(err, want) || err.Error() != "register second routes: broken" {
		t.Fatalf("error = %v", err)
	}
}
