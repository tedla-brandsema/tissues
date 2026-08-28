package auth

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/service"
)

type Config struct {
	service.Contribution
	Enabled           bool   `cfg:"bool,default=false,restart=true"`
	SigningSecret     string `cfg:"string,secret=true,restart=true"`
	ClientID          string `cfg:"string,default=tissues,restart=true"`
	ClientSecret      string `cfg:"string,secret=true,restart=true"`
	ClientRedirectURI string `cfg:"string,restart=true"`
	ProjectID         string `cfg:"string,restart=true"`
	DatastoreNS       string `cfg:"string,default=tissues-auth,restart=true"`
	DatastoreKind     string `cfg:"string,default=tissues_auth_code,restart=true"`
	IdentityAPIKey    string `cfg:"string,secret=true,restart=true"`
	IdentityTenantID  string `cfg:"string,restart=true"`
	Entitlements      string `cfg:"string"`
	InsecureCookie    bool   `cfg:"bool,default=false,restart=true"`
}

var _ service.Configuration = Config{}

func (cfg Config) ValidateConfig() error {
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.SigningSecret) < 32 {
		return fmt.Errorf("SigningSecret must be at least 32 bytes")
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
