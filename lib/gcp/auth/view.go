package gcp

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/tedla-brandsema/tissues/lib/fio"
	"github.com/tedla-brandsema/tissues/lib/tmpl"
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

// Frontend is supplied by the deployable service that owns its UI.
type Frontend struct {
	Static    fs.FS
	Templates fs.FS
}

type authView struct {
	cfg          Config
	loginHandler http.HandlerFunc
	static       http.Handler
	templates    *template.Template
}

func New(cfg Config, frontend Frontend) (http.Handler, error) {
	if frontend.Static == nil || frontend.Templates == nil {
		return nil, fmt.Errorf("auth frontend static and templates are required")
	}
	templates, err := tmpl.CollectTemplates(frontend.Templates)
	if err != nil {
		return nil, fmt.Errorf("load auth frontend templates: %w", err)
	}
	cfg.BasePath = cleanBasePath(cfg.BasePath)
	if cfg.LoginRedirect == "" {
		cfg.LoginRedirect = "/"
	}

	return &authView{
		cfg:          cfg,
		loginHandler: loginHandler(NewVerifier(cfg.APIKey, cfg.HTTPClient), cfg),
		static:       http.StripPrefix(cfg.BasePath, fio.FsNoDirFileServer(frontend.Static)),
		templates:    templates,
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
			a.handleRoot(w, r)
		case http.MethodPost:
			a.loginHandler(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch {
	case strings.HasPrefix(rest, "/static/"):
		a.static.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *authView) handleRoot(w http.ResponseWriter, _ *http.Request) {
	data := struct {
		BasePath string
	}{
		BasePath: a.cfg.BasePath,
	}

	if err := a.templates.ExecuteTemplate(w, "login", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
