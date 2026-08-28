package tissues

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tedla-brandsema/tissues/lib/core/config"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

func TestAuthDisabledServesGeneratedWorkspaceAndAssets(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?view=open&selected=abc", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("response=%d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="tissues-auth-enabled" content="false"`) || !strings.Contains(rec.Body.String(), `name="tissues-author" content=""`) {
		t.Fatalf("no-auth bootstrap metadata missing: %q", rec.Body.String())
	}
	for header, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s=%q want=%q", header, got, want)
		}
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") || strings.Contains(got, "unsafe-inline") {
		t.Errorf("CSP=%q", got)
	}
	assetStart := strings.Index(rec.Body.String(), `/tissues/assets/`)
	if assetStart < 0 {
		t.Fatal("generated asset reference missing")
	}
	assetEnd := strings.IndexAny(rec.Body.String()[assetStart:], `"'`)
	assetPath := rec.Body.String()[assetStart : assetStart+assetEnd]
	asset := httptest.NewRecorder()
	mux.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if asset.Code != http.StatusOK || asset.Body.Len() == 0 {
		t.Fatalf("asset %s response=%d bytes=%d", assetPath, asset.Code, asset.Body.Len())
	}
}

func TestAuthEnabledPreservesExactOriginalRequestURI(t *testing.T) {
	brokerURL := "https://auth.example.test"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch r.URL.Path {
		case "/token":
			raw, _ := json.Marshal(map[string]string{"access_token": "token"})
			body = string(raw)
		case "/userinfo":
			raw, _ := json.Marshal(map[string]string{"sub": "subject-1", "email": "person@example.test"})
			body = string(raw)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	cfg := Config{Enabled: true, Storage: StorageConfig{ProjectID: "example", Namespace: "test"}, Auth: AuthConfig{Enabled: true, BrokerURL: brokerURL, ClientID: "tissues", ClientSecret: "client-secret", RedirectURI: "http://tissues.example.test/tissues/auth/callback", SessionSecret: "01234567890123456789012345678901", InsecureCookie: true}}
	profile, err := config.NewServiceProfile("test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(slot, newMemoryRepository(), WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	requestURI := "/?view=open&selected=abc"
	unauth := httptest.NewRecorder()
	mux.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, requestURI, nil))
	if unauth.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", unauth.Code)
	}
	loginLocation := unauth.Header().Get("Location")
	parsed, _ := url.Parse(loginLocation)
	if parsed.Path != rpLoginPath || parsed.Query().Get("next") != requestURI {
		t.Fatalf("login=%q", loginLocation)
	}
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, loginLocation, nil))
	brokerLocation, _ := url.Parse(login.Header().Get("Location"))
	callbackReq := httptest.NewRequest(http.MethodGet, rpCallbackPath+"?code=code&state="+url.QueryEscape(brokerLocation.Query().Get("state")), nil)
	callbackReq.AddCookie(login.Result().Cookies()[0])
	callback := httptest.NewRecorder()
	mux.ServeHTTP(callback, callbackReq)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != requestURI {
		t.Fatalf("callback=%d %q", callback.Code, callback.Header().Get("Location"))
	}
	pageRequest := httptest.NewRequest(http.MethodGet, requestURI, nil)
	for _, cookie := range callback.Result().Cookies() {
		pageRequest.AddCookie(cookie)
	}
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, pageRequest)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="tissues-auth-enabled" content="true"`) || !strings.Contains(page.Body.String(), `name="tissues-author" content="person@example.test"`) {
		t.Fatalf("authenticated bootstrap=%d %q", page.Code, page.Body.String())
	}
	api := httptest.NewRecorder()
	mux.ServeHTTP(api, httptest.NewRequest(http.MethodGet, apiBasePath+"/issues", nil))
	if api.Code != http.StatusUnauthorized || !strings.Contains(api.Header().Get("Content-Type"), "application/json") || !strings.Contains(api.Body.String(), `"kind":"unauthorized"`) {
		t.Fatalf("unauthenticated API=%d %q %q", api.Code, api.Header().Get("Content-Type"), api.Body.String())
	}
}

func TestAuthenticatedBootstrapFallsBackToEscapedSubject(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	frontend, err := fs.Sub(frontendFiles, "frontend/generated")
	if err != nil {
		t.Fatal(err)
	}
	handler := svc.browserRootHandler(frontend, true)
	subject := `subject"><script>alert('x')</script>`
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(gcpauth.WithSubject(request.Context(), subject))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `name="tissues-auth-enabled" content="true"`) || !strings.Contains(body, `name="tissues-author" content="subject&#34;&gt;&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt;"`) {
		t.Fatalf("escaped subject metadata missing: %q", body)
	}
	if strings.Contains(body, subject) || strings.Contains(body, `<script>alert`) {
		t.Fatalf("subject was not safely escaped: %q", body)
	}
}

func TestAuthenticatedBootstrapPrefersEmail(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	frontend, err := fs.Sub(frontendFiles, "frontend/generated")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := gcpauth.WithEmail(gcpauth.WithSubject(request.Context(), "subject-1"), "preferred@example.test")
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	svc.browserRootHandler(frontend, true).ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), `name="tissues-author" content="preferred@example.test"`) || strings.Contains(recorder.Body.String(), `content="subject-1"`) {
		t.Fatalf("email preference missing: %q", recorder.Body.String())
	}
}

func TestServiceProfileReloadDoesNotMutateOuterServerConfig(t *testing.T) {
	ctx := context.Background()
	store := config.NewMemoryStore()
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true,"storage":{"project_id":"example","namespace":"one"}}`)})
	manager, err := config.NewManager[Config](ctx, config.LoadOptions{Name: "demo", Prefix: "TISSUES_SERVICE", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	outerPort := 8080
	svc, err := New(manager, newMemoryRepository())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true,"storage":{"project_id":"example","namespace":"two"}}`)})
	result, err := manager.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.Revision != 2 || result.Profile.Config.Storage.Namespace != "two" || outerPort != 8080 {
		t.Fatalf("reload=%#v outerPort=%d", result, outerPort)
	}
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true}`)})
	if _, err := manager.Reload(ctx); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if manager.Current().Revision != 2 {
		t.Fatalf("revision=%d", manager.Current().Revision)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
