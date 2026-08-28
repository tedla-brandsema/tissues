package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/service"
)

func TestAuthDisabledServesExistingRoute(t *testing.T) {
	mux, err := newMux(testSlot(t, tissuesConfig{Message: "🤧 tissues"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?view=open&selected=abc", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "🤧 tissues") {
		t.Fatalf("response = %d %q, want tissues bootstrap", recorder.Code, recorder.Body.String())
	}
}

func TestAuthEnabledPreservesOriginalRequestURI(t *testing.T) {
	brokerURL := "https://auth.example.test"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch r.URL.Path {
		case "/token":
			encoded, _ := json.Marshal(map[string]string{"access_token": "token"})
			body = string(encoded)
		case "/userinfo":
			encoded, _ := json.Marshal(map[string]string{"sub": "subject-1", "email": "person@example.test"})
			body = string(encoded)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	cfg := tissuesConfig{Message: "tissues", Auth: authConfig{
		Enabled: true, BrokerURL: brokerURL, ClientID: "tissues", ClientSecret: "client-secret",
		RedirectURI:   "http://tissues.example.test/auth/callback",
		SessionSecret: "01234567890123456789012345678901", InsecureCookie: true,
	}}
	mux, err := newMux(testSlot(t, cfg), client)
	if err != nil {
		t.Fatal(err)
	}
	requestURI := "/?view=open&selected=abc"
	unauthenticated := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, requestURI, nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated status = %d, want 303", unauthenticated.Code)
	}
	loginLocation := unauthenticated.Header().Get("Location")
	parsedLogin, _ := url.Parse(loginLocation)
	if got := parsedLogin.Query().Get("next"); got != requestURI {
		t.Fatalf("login next = %q, want exact %q", got, requestURI)
	}

	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, loginLocation, nil))
	brokerLocation, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state="+url.QueryEscape(brokerLocation.Query().Get("state")), nil)
	callbackRequest.AddCookie(login.Result().Cookies()[0])
	callback := httptest.NewRecorder()
	mux.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != requestURI {
		t.Fatalf("callback = %d %q, want 303 exact %q", callback.Code, callback.Header().Get("Location"), requestURI)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestServiceProfileReloadIsIndependentAndAtomic(t *testing.T) {
	ctx := context.Background()
	store := coreconfig.NewMemoryStore()
	store.Put("demo", coreconfig.Document{Format: "json", Data: []byte(`{"message":"one"}`)})
	manager, err := coreconfig.NewManager[tissuesConfig](ctx, coreconfig.LoadOptions{Name: "demo", Prefix: "TISSUES_SERVICE", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	outer := service.Config{Host: "127.0.0.1", Port: 8080}
	mux, err := newMux(manager, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Put("demo", coreconfig.Document{Format: "json", Data: []byte(`{"message":"two"}`)})
	result, err := manager.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.Revision != 2 || result.Profile.Config.Message != "two" || len(result.LiveChanges) != 1 || outer.Port != 8080 {
		t.Fatalf("live reload = %#v; outer = %#v", result, outer)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), "two") {
		t.Fatalf("live consumer did not observe revision 2: %q", recorder.Body.String())
	}

	store.Put("demo", coreconfig.Document{Format: "json", Data: []byte(`{"message":"bad","auth":{"enabled":true}}`)})
	if _, err := manager.Reload(ctx); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if current := manager.Current(); current.Revision != 2 || current.Config.Message != "two" {
		t.Fatalf("invalid reload changed current = %#v", current)
	}

	store.Put("demo", coreconfig.Document{Format: "json", Data: []byte(`{"message":"two","auth":{"enabled":true,"broker_url":"https://auth.example.test","client_id":"tissues","client_secret":"secret","redirect_uri":"https://tissues.example.test/auth/callback","session_secret":"01234567890123456789012345678901"}}`)})
	result, err = manager.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.Revision != 3 || len(result.RestartRequired) == 0 {
		t.Fatalf("restart reload = %#v", result)
	}
}

func testSlot(t *testing.T, cfg tissuesConfig) *coreconfig.Slot[tissuesConfig] {
	t.Helper()
	profile, err := coreconfig.NewServiceProfile("test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := coreconfig.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	return slot
}
