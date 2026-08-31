package tissues

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/service"
)

// Config is the complete typed contribution for one tissues Service.
type Config struct {
	service.Contribution
	Enabled           bool   `cfg:"bool,default=true,restart=true"`
	BootstrapTenantID string `cfg:"string,restart=true"`
	Assets            AssetsConfig
	Auth              AuthConfig
}

var _ service.Configuration = Config{}

type AssetsConfig struct {
	Bucket string `cfg:"string,restart=true"`
}

// AuthConfig controls relying-service enforcement independently of whether
// the auth Service is active in the hosting Server.
type AuthConfig struct {
	Enabled        bool   `cfg:"bool,default=false,restart=true"`
	BrokerURL      string `cfg:"string,restart=true"`
	ClientID       string `cfg:"string,restart=true"`
	ClientSecret   string `cfg:"string,secret=true,restart=true"`
	RedirectURI    string `cfg:"string,restart=true"`
	SessionSecret  string `cfg:"string,secret=true,restart=true"`
	InsecureCookie bool   `cfg:"bool,default=false,restart=true"`
}

func (cfg Config) ValidateConfig() error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.Assets.Bucket) == "" {
		return fmt.Errorf("Assets.Bucket is required when Enabled is true")
	}
	if _, err := ParseTenantID(cfg.BootstrapTenantID); err != nil {
		return fmt.Errorf("BootstrapTenantID must be a valid Tissues ID")
	}
	if !cfg.Auth.Enabled {
		return nil
	}
	for path, value := range map[string]string{
		"Auth.BrokerURL": cfg.Auth.BrokerURL, "Auth.ClientID": cfg.Auth.ClientID,
		"Auth.ClientSecret": cfg.Auth.ClientSecret, "Auth.RedirectURI": cfg.Auth.RedirectURI,
		"Auth.SessionSecret": cfg.Auth.SessionSecret,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required when Auth.Enabled is true", path)
		}
	}
	if len(cfg.Auth.SessionSecret) < 32 {
		return fmt.Errorf("Auth.SessionSecret must be at least 32 bytes")
	}
	for path, raw := range map[string]string{"Auth.BrokerURL": cfg.Auth.BrokerURL, "Auth.RedirectURI": cfg.Auth.RedirectURI} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute HTTP or HTTPS URL", path)
		}
	}
	return nil
}
