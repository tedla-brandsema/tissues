package tissues

import (
	"embed"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/auth/broker"
)

//go:embed frontend
var frontendFiles embed.FS

const (
	rpLoginPath    = "/tissues/auth/login"
	rpCallbackPath = "/tissues/auth/callback"
	rpLogoutPath   = "/tissues/auth/logout"
)

// RegisterRoutes mounts tissues routes into the hosting Server mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) error {
	frontend, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		return err
	}
	root := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, readErr := fs.ReadFile(frontend, "index.html")
		if readErr != nil {
			http.Error(w, "frontend unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, strings.ReplaceAll(string(body), "{{MESSAGE}}", html.EscapeString(s.profile.Current().Config.Message)))
	})
	cfg := s.profile.Current().Config.Auth
	if !cfg.Enabled {
		mux.Handle("GET /{$}", root)
		return nil
	}
	rp := broker.NewRP(broker.RPConfig{BrokerURL: cfg.BrokerURL, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURI: cfg.RedirectURI, LoginPath: rpLoginPath, Secret: []byte(cfg.SessionSecret), InsecureCookie: cfg.InsecureCookie, HTTPClient: s.httpClient})
	mux.Handle("GET "+rpLoginPath, rp.LoginHandler())
	mux.Handle("GET "+rpCallbackPath, rp.CallbackHandler())
	mux.Handle("GET "+rpLogoutPath, rp.LogoutHandler(rpLoginPath))
	mux.Handle("GET /{$}", rp.Middleware(root))
	return nil
}
