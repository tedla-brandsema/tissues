package main

import (
	"context"
	"strings"
	"testing"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
)

func TestTypedConfigNamesMissingRequiredField(t *testing.T) {
	_, err := coreconfig.Load[appConfig](context.Background(), coreconfig.LoadOptions{Prefix: "TISSUES_AUTH"})
	if err == nil || !strings.Contains(err.Error(), "Service.SigningSecret") {
		t.Fatalf("Load() error = %v, want typed missing field path", err)
	}
	if strings.Contains(err.Error(), "TISSUES_AUTH_SECRET") {
		t.Fatalf("Load() retained obsolete parser contract: %v", err)
	}
}

func TestTypedAuthConfigSourcesAndDefaults(t *testing.T) {
	environment := coreconfig.MapEnvironment{
		"PORT":                                     "9091",
		"TISSUES_AUTH_SERVICE_SIGNING_SECRET":      strings.Repeat("s", 32),
		"TISSUES_AUTH_SERVICE_CLIENT_SECRET":       strings.Repeat("c", 32),
		"TISSUES_AUTH_SERVICE_CLIENT_REDIRECT_URI": "http://localhost:9090/auth/callback",
		"TISSUES_AUTH_SERVICE_IDENTITY_API_KEY":    "test-api-key",
		"TISSUES_AUTH_SERVICE_PROJECT_ID":          "test-project",
		"TISSUES_AUTH_SERVICE_INSECURE_COOKIE":     "true",
		"TISSUES_AUTH_SERVICE_IDENTITY_TENANT_ID":  "test-tenant",
		"TISSUES_AUTH_SERVICE_ENTITLEMENTS":        "*=tissues",
	}
	profile, err := coreconfig.Load[appConfig](context.Background(), coreconfig.LoadOptions{Prefix: "TISSUES_AUTH", Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Config.Server.Port != 9091 || profile.Config.Service.ClientID != "tissues" || !profile.Config.Service.InsecureCookie {
		t.Fatalf("unexpected config: %+v", profile.Config)
	}
	secret, ok := profile.Field("Service.SigningSecret")
	if !ok || secret.Value != "[redacted]" || secret.Source != coreconfig.SourceEnvironment {
		t.Fatalf("secret provenance = %#v, %v", secret, ok)
	}
}

func TestParseEntitlements(t *testing.T) {
	got := parseEntitlements("sub:one=tissues,other;email:a@example.test=tissues")
	if _, ok := got["sub:one"]["tissues"]; !ok {
		t.Fatalf("parseEntitlements() = %#v", got)
	}
}

func TestAuthServiceContributionUsesProfileSlot(t *testing.T) {
	cfg := authServiceConfig{
		SigningSecret: strings.Repeat("s", 32), ClientID: "tissues",
		ClientSecret: "client-secret", ClientRedirectURI: "https://tissues.example.test/auth/callback",
		ProjectID: "test-project", IdentityAPIKey: "test-api-key",
	}
	profile, err := coreconfig.NewServiceProfile("auth-test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := coreconfig.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	current := slot.Current()
	if current.Name != "auth-test" || current.Revision != 1 || current.Config.ClientID != "tissues" {
		t.Fatalf("current service profile = %#v", current)
	}
}
