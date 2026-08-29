package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
)

func TestInactiveAuthNeedsNoCredentials(t *testing.T) {
	if err := (Config{}).ValidateConfig(); err != nil {
		t.Fatal(err)
	}
}

func validConfig() Config {
	return Config{
		Enabled: true, IssuerURL: "http://127.0.0.1:18080", MCPResourceURL: "http://127.0.0.1:18080/mcp",
		SigningSecret: "01234567890123456789012345678901", ClientID: "tissues", ClientSecret: "secret",
		ClientRedirectURI: "http://127.0.0.1:18080/tissues/auth/callback", ProjectID: "project", IdentityAPIKey: "api-key",
	}
}

func TestEnabledAuthRequiresCanonicalIssuerAndMCPResource(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Config)
		want   string
	}{
		"issuer required":       {func(c *Config) { c.IssuerURL = "" }, "IssuerURL"},
		"issuer absolute":       {func(c *Config) { c.IssuerURL = "/auth" }, "IssuerURL"},
		"issuer scheme":         {func(c *Config) { c.IssuerURL = "ftp://auth.example" }, "IssuerURL"},
		"issuer host":           {func(c *Config) { c.IssuerURL = "https:///auth" }, "IssuerURL"},
		"issuer path":           {func(c *Config) { c.IssuerURL = "https://auth.example/path" }, "IssuerURL"},
		"issuer trailing slash": {func(c *Config) { c.IssuerURL = "https://auth.example/" }, "IssuerURL"},
		"issuer query":          {func(c *Config) { c.IssuerURL = "https://auth.example?x=1" }, "IssuerURL"},
		"issuer fragment":       {func(c *Config) { c.IssuerURL = "https://auth.example#x" }, "IssuerURL"},
		"resource required":     {func(c *Config) { c.MCPResourceURL = "" }, "MCPResourceURL"},
		"resource absolute":     {func(c *Config) { c.MCPResourceURL = "/mcp" }, "MCPResourceURL"},
		"resource scheme":       {func(c *Config) { c.MCPResourceURL = "ftp://auth.example/mcp" }, "MCPResourceURL"},
		"resource host":         {func(c *Config) { c.MCPResourceURL = "https:///mcp" }, "MCPResourceURL"},
		"resource path":         {func(c *Config) { c.MCPResourceURL = "https://auth.example/other" }, "MCPResourceURL"},
		"resource query":        {func(c *Config) { c.MCPResourceURL = "https://auth.example/mcp?x=1" }, "MCPResourceURL"},
		"resource fragment":     {func(c *Config) { c.MCPResourceURL = "https://auth.example/mcp#x" }, "MCPResourceURL"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tc.change(&cfg)
			if err := cfg.ValidateConfig(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s validation error", err, tc.want)
			}
		})
	}
}

func TestValidLocalAndProductionAuthConfiguration(t *testing.T) {
	local := validConfig()
	if err := local.ValidateConfig(); err != nil {
		t.Fatalf("local config: %v", err)
	}
	production := validConfig()
	production.IssuerURL = "https://tissues-abc.europe-west4.run.app"
	production.MCPResourceURL = production.IssuerURL + "/mcp"
	production.ClientRedirectURI = production.IssuerURL + "/tissues/auth/callback"
	if err := production.ValidateConfig(); err != nil {
		t.Fatalf("production config: %v", err)
	}
}

func TestAuthorizationServerMetadataIsTruthfulAndCanonical(t *testing.T) {
	cfg := validConfig()
	rec := httptest.NewRecorder()
	authorizationServerMetadataHandler(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("metadata response = %d headers=%v", rec.Code, rec.Header())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["issuer"] != cfg.IssuerURL || got["authorization_endpoint"] != cfg.IssuerURL+"/authorize" || got["token_endpoint"] != cfg.IssuerURL+"/token" || got["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("metadata = %#v", got)
	}
	if !reflect.DeepEqual(got["response_types_supported"], []any{"code"}) || !reflect.DeepEqual(got["grant_types_supported"], []any{"authorization_code"}) || !reflect.DeepEqual(got["token_endpoint_auth_methods_supported"], []any{"none", "client_secret_post"}) || !reflect.DeepEqual(got["code_challenge_methods_supported"], []any{"S256"}) || !reflect.DeepEqual(got["scopes_supported"], []any{ScopeRead, ScopeWrite}) {
		t.Fatalf("metadata capabilities = %#v", got)
	}
	for _, forbidden := range []string{"registration_endpoint", "client_id_metadata_document_supported", "refresh_token", "offline_access"} {
		if _, ok := got[forbidden]; ok || strings.Contains(rec.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("metadata advertises %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestAuthorizationServerMetadataRouteIsRegisteredAndGETOnly(t *testing.T) {
	cfg := validConfig()
	profile, err := coreconfig.NewServiceProfile("test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := coreconfig.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{profile: slot, broker: broker.NewService(broker.ServiceConfig{}), login: http.NotFoundHandler()}
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", get.Code, get.Body.String())
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/.well-known/oauth-authorization-server", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.Code)
	}
}

func TestParseEntitlements(t *testing.T) {
	got := parseEntitlements("sub:one=tissues,other;email:a@example.test=tissues")
	if _, ok := got["sub:one"]["other"]; !ok {
		t.Fatalf("entitlements=%#v", got)
	}
}
