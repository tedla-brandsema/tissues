package gcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestViewHandleRootRendersTemplate(t *testing.T) {
	h := newTestView(t, Config{
		BasePath: "/auth/login",
		Secret:   []byte("secret"),
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<tissues-auth-login base=\"/auth/login\">") {
		t.Fatalf("expected login component in body, got %q", body)
	}
	if !strings.Contains(body, "@fluentui/web-components") {
		t.Fatalf("expected fluent import map in body")
	}
}

func TestViewServesStaticAssets(t *testing.T) {
	h := newTestView(t, Config{
		BasePath: "/auth/login",
		Secret:   []byte("secret"),
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login/static/registry.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}
	if !strings.Contains(rec.Body.String(), "bootstrapGCPAuthUI") {
		t.Fatalf("expected registry content, got %q", rec.Body.String())
	}
}

func TestViewRejectsMethodOnRoot(t *testing.T) {
	h := newTestView(t, Config{
		BasePath: "/auth/login",
		Secret:   []byte("secret"),
	})

	req := httptest.NewRequest(http.MethodPut, "/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", res.StatusCode)
	}
}

func TestViewReturnsNotFoundOutsideBasePath(t *testing.T) {
	h := newTestView(t, Config{
		BasePath: "/auth/login",
		Secret:   []byte("secret"),
	})

	req := httptest.NewRequest(http.MethodGet, "/other", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", res.StatusCode)
	}
}

func newTestView(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	static := fstest.MapFS{
		"static/registry.js": {Data: []byte("export const bootstrapGCPAuthUI = true")},
	}
	templates := fstest.MapFS{
		"templates/login.tmpl": {Data: []byte(`{{define "login"}}<script type="importmap">@fluentui/web-components</script><tissues-auth-login base="{{.BasePath}}">{{end}}`)},
	}
	handler, err := New(cfg, Frontend{Static: static, Templates: templates})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
