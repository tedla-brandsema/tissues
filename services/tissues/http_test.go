package tissues

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

func TestAuthDisabledServesBootstrap(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?view=open&selected=abc", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "🤧 tissues") {
		t.Fatalf("response=%d %q", rec.Code, rec.Body.String())
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
	cfg := Config{Enabled: true, Message: "tissues", Storage: StorageConfig{ProjectID: "example", Namespace: "test"}, Auth: AuthConfig{Enabled: true, BrokerURL: brokerURL, ClientID: "tissues", ClientSecret: "client-secret", RedirectURI: "http://tissues.example.test/tissues/auth/callback", SessionSecret: "01234567890123456789012345678901", InsecureCookie: true}}
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
}

func TestServiceProfileReloadDoesNotMutateOuterServerConfig(t *testing.T) {
	ctx := context.Background()
	store := config.NewMemoryStore()
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true,"message":"one","storage":{"project_id":"example","namespace":"test"}}`)})
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
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true,"message":"two","storage":{"project_id":"example","namespace":"test"}}`)})
	result, err := manager.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.Revision != 2 || outerPort != 8080 {
		t.Fatalf("reload=%#v outerPort=%d", result, outerPort)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "two") {
		t.Fatalf("body=%q", rec.Body.String())
	}
	store.Put("demo", config.Document{Format: "json", Data: []byte(`{"enabled":true,"message":"bad"}`)})
	if _, err := manager.Reload(ctx); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if manager.Current().Revision != 2 {
		t.Fatalf("revision=%d", manager.Current().Revision)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
