package gcp

import (
	"fmt"
	"net/http"
	"strings"
)

type Config struct {
	BasePath      string
	Secret        []byte
	LoginRedirect string
	// InsecureCookie must be explicitly enabled for local HTTP development.
	// Identity cookies are secure by default.
	InsecureCookie bool
	APIKey         string
	TenantID       string
	HTTPClient     *http.Client
}

// Frontend is supplied by the internal auth Service that owns its UI.
type Frontend struct {
	GET http.Handler
}

type authView struct {
	cfg          Config
	loginHandler http.HandlerFunc
	frontend     http.Handler
}

func New(cfg Config, frontend Frontend) (http.Handler, error) {
	if frontend.GET == nil {
		return nil, fmt.Errorf("auth frontend GET handler is required")
	}
	cfg.BasePath = cleanBasePath(cfg.BasePath)
	if cfg.LoginRedirect == "" {
		cfg.LoginRedirect = "/"
	}

	return &authView{
		cfg:          cfg,
		loginHandler: loginHandler(NewVerifier(cfg.APIKey, cfg.HTTPClient), cfg),
		frontend:     frontend.GET,
	}, nil
}

func (a *authView) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, a.cfg.BasePath) {
		http.NotFound(w, r)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, a.cfg.BasePath)
	if rest == "" || rest == "/" {
		switch r.Method {
		case http.MethodGet:
			a.frontend.ServeHTTP(w, r)
		case http.MethodPost:
			a.loginHandler(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(rest, "/") {
		http.NotFound(w, r)
		return
	}
	a.frontend.ServeHTTP(w, r)
}

func cleanBasePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}
