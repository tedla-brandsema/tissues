package auth

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	"github.com/tedla-brandsema/tissues/lib/service"
)

type Config struct {
	service.Contribution
	Enabled               bool   `cfg:"bool,default=false,restart=true"`
	IssuerURL             string `cfg:"string,restart=true"`
	MCPResourceURL        string `cfg:"string,restart=true"`
	SigningSecret         string `cfg:"string,secret=true,restart=true"`
	ClientID              string `cfg:"string,default=tissues,restart=true"`
	ClientSecret          string `cfg:"string,secret=true,restart=true"`
	ClientRedirectURI     string `cfg:"string,restart=true"`
	ClientMetadataURLList string `cfg:"string,restart=true"`
	ProjectID             string `cfg:"string,restart=true"`
	DatastoreNS           string `cfg:"string,default=tissues-auth,restart=true"`
	DatastoreKind         string `cfg:"string,default=tissues_auth_code,restart=true"`
	IdentityAPIKey        string `cfg:"string,secret=true,restart=true"`
	IdentityTenantID      string `cfg:"string,restart=true"`
	Entitlements          string `cfg:"string"`
	InsecureCookie        bool   `cfg:"bool,default=false,restart=true"`
}

var _ service.Configuration = Config{}

func (cfg Config) ValidateConfig() error {
	if _, err := parseClientMetadataURLs(cfg.ClientMetadataURLList); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.SigningSecret) < 32 {
		return fmt.Errorf("SigningSecret must be at least 32 bytes")
	}
	if err := validateIssuerURL(cfg.IssuerURL); err != nil {
		return err
	}
	if err := validateMCPResourceURL(cfg.MCPResourceURL); err != nil {
		return err
	}
	for path, value := range map[string]string{"ClientSecret": cfg.ClientSecret, "ProjectID": cfg.ProjectID, "IdentityAPIKey": cfg.IdentityAPIKey} {
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

func parseClientMetadataURLs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var clientIDs []string
	seen := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ";") {
		clientID := strings.TrimSpace(entry)
		if err := broker.ValidateClientMetadataURL(clientID); err != nil {
			return nil, fmt.Errorf("ClientMetadataURLList: %w", err)
		}
		if _, exists := seen[clientID]; exists {
			return nil, fmt.Errorf("ClientMetadataURLList contains duplicate client ID %q", clientID)
		}
		seen[clientID] = struct{}{}
		clientIDs = append(clientIDs, clientID)
	}
	return clientIDs, nil
}

func validateIssuerURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("IssuerURL must be an absolute HTTP or HTTPS origin without path, query, fragment, or user information")
	}
	return nil
}

func validateMCPResourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "/mcp" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("MCPResourceURL must be an absolute HTTP or HTTPS URL with path /mcp and no query, fragment, or user information")
	}
	return nil
}
