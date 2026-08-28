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

//go:embed frontend/generated
var frontendFiles embed.FS

const (
	rpLoginPath           = "/tissues/auth/login"
	rpCallbackPath        = "/tissues/auth/callback"
	rpLogoutPath          = "/tissues/auth/logout"
	assetPath             = "/tissues/assets/"
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

// RegisterRoutes mounts tissues routes into the hosting Server mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) error {
	frontend, err := fs.Sub(frontendFiles, "frontend/generated")
	if err != nil {
		return err
	}
	cfg := s.profile.Current().Config.Auth
	root := secureBrowserHandler(s.browserRootHandler(frontend, cfg.Enabled))
	assets := secureBrowserHandler(http.StripPrefix("/tissues/", http.FileServer(http.FS(frontend))))
	mux.Handle("GET "+assetPath, assets)

	api := s.apiHandler()
	if !cfg.Enabled {
		mux.Handle(apiBasePath+"/", api)
		mux.Handle("GET /{$}", root)
		return nil
	}
	rp := broker.NewRP(broker.RPConfig{BrokerURL: cfg.BrokerURL, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURI: cfg.RedirectURI, LoginPath: rpLoginPath, Secret: []byte(cfg.SessionSecret), InsecureCookie: cfg.InsecureCookie, HTTPClient: s.httpClient})
	mux.Handle("GET "+rpLoginPath, rp.LoginHandler())
	mux.Handle("GET "+rpCallbackPath, rp.CallbackHandler())
	mux.Handle("GET "+rpLogoutPath, rp.LogoutHandler(rpLoginPath))
	mux.Handle(apiBasePath+"/", rp.SessionMiddleware(api, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
	})))
	mux.Handle("GET /{$}", rp.Middleware(root))
	return nil
}

func (s *Service) browserRootHandler(frontend fs.FS, authEnabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := fs.ReadFile(frontend, "index.html")
		if readErr != nil {
			http.Error(w, "frontend unavailable", http.StatusInternalServerError)
			return
		}
		author := ""
		if authEnabled {
			author = trustedAuthor(r)
		}
		replacer := strings.NewReplacer(
			"__TISSUES_MESSAGE__", html.EscapeString(s.profile.Current().Config.Message),
			"__TISSUES_AUTH_ENABLED__", fmt.Sprintf("%t", authEnabled),
			"__TISSUES_AUTHOR__", html.EscapeString(author),
		)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, replacer.Replace(string(body)))
	})
}

func secureBrowserHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
