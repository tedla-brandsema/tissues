package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/service"
)

const serviceName = "tissues"

//go:embed frontend
var frontendFiles embed.FS

type appConfig struct {
	Server  service.Config
	Service tissuesConfig
}

func (cfg appConfig) ValidateConfig() error {
	if err := cfg.Server.ValidateConfig(); err != nil {
		return err
	}
	return cfg.Service.ValidateConfig()
}

type tissuesConfig struct {
	coreconfig.Contribution
	Message string `cfg:"string,default=🤧 tissues"`
	Auth    authConfig
}

var _ coreconfig.ServiceContribution = tissuesConfig{}

type authConfig struct {
	Enabled        bool   `cfg:"bool,default=false,restart=true"`
	BrokerURL      string `cfg:"string,restart=true"`
	ClientID       string `cfg:"string,restart=true"`
	ClientSecret   string `cfg:"string,secret=true,restart=true"`
	RedirectURI    string `cfg:"string,restart=true"`
	SessionSecret  string `cfg:"string,secret=true,restart=true"`
	InsecureCookie bool   `cfg:"bool,default=false,restart=true"`
}

func (cfg tissuesConfig) ValidateConfig() error {
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

func main() {
	if err := run(context.Background(), os.Args[1:], coreconfig.SystemEnvironment()); err != nil {
		if errors.Is(err, coreconfig.ErrHelp) {
			return
		}
		slog.Error("tissues service failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, environment coreconfig.Environment) error {
	selection, err := coreconfig.Bootstrap("TISSUES", environment, args)
	if err != nil {
		return err
	}
	profile, err := coreconfig.Load[appConfig](ctx, coreconfig.LoadOptions{
		Name: selection.Name, Prefix: "TISSUES", Store: coreconfig.NewFileStore(selection.Directory),
		Environment: environment, Args: selection.Args, FlagOutput: os.Stderr,
	})
	if err != nil {
		return err
	}
	serviceProfile, err := coreconfig.NewServiceProfile(profile.Name, profile.Config.Service)
	if err != nil {
		return err
	}
	slot, err := coreconfig.NewSlot(serviceProfile)
	if err != nil {
		return err
	}
	svc, err := service.New(ctx, serviceName, profile.Config.Server)
	if err != nil {
		return err
	}
	mux, err := newMux(slot, nil)
	if err != nil {
		return err
	}
	svc.SetMux(mux)
	return svc.StartAndWait()
}

type profileReader[T any] interface {
	Current() coreconfig.Profile[T]
}

func newMux(slot profileReader[tissuesConfig], client *http.Client) (*http.ServeMux, error) {
	current := slot.Current().Config
	frontend, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		return nil, err
	}
	root := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, readErr := fs.ReadFile(frontend, "index.html")
		if readErr != nil {
			http.Error(w, "frontend unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, strings.ReplaceAll(string(body), "{{MESSAGE}}", html.EscapeString(slot.Current().Config.Message)))
	})

	mux := http.NewServeMux()
	if !current.Auth.Enabled {
		mux.Handle("GET /{$}", root)
		return mux, nil
	}
	rp := broker.NewRP(broker.RPConfig{
		BrokerURL: current.Auth.BrokerURL, ClientID: current.Auth.ClientID,
		ClientSecret: current.Auth.ClientSecret, RedirectURI: current.Auth.RedirectURI,
		LoginPath: "/auth/login", Secret: []byte(current.Auth.SessionSecret),
		InsecureCookie: current.Auth.InsecureCookie, HTTPClient: client,
	})
	mux.Handle("GET /auth/login", rp.LoginHandler())
	mux.Handle("GET /auth/callback", rp.CallbackHandler())
	mux.Handle("GET /auth/logout", rp.LogoutHandler("/auth/login"))
	mux.Handle("GET /{$}", rp.Middleware(root))
	return mux, nil
}
