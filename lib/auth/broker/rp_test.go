package broker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

func TestRelyingPartyCookiesAreSecureByDefault(t *testing.T) {
	rp := NewRP(RPConfig{
		BrokerURL:   "https://auth.example.test",
		ClientID:    "tissues",
		RedirectURI: "https://tissues.example.test/auth/callback",
		Secret:      []byte("test-only-secret"),
	})
	recorder := httptest.NewRecorder()
	rp.LoginHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	if cookies[0].Name != stateCookieName {
		t.Fatalf("cookie name = %q, want %q", cookies[0].Name, stateCookieName)
	}
	if !cookies[0].Secure {
		t.Fatal("state cookie Secure = false, want true")
	}
}

func TestRelyingPartyCallbackPreservesExactURLAndIdentity(t *testing.T) {
	client := &http.Client{Transport: rpRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := map[string]string{"access_token": "token"}
		if r.URL.Path == "/userinfo" {
			body = map[string]string{"sub": "subject-1", "email": "person@example.test"}
		}
		encoded, _ := json.Marshal(body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})}
	rp := NewRP(RPConfig{
		BrokerURL: "https://auth.example.test", ClientID: "tissues", ClientSecret: "client-secret",
		RedirectURI: "https://tissues.example.test/auth/callback", Secret: []byte("01234567890123456789012345678901"),
		InsecureCookie: true, HTTPClient: client,
	})
	next := "/?view=open&selected=abc"
	loginRecorder := httptest.NewRecorder()
	rp.LoginHandler().ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/auth/login?next="+url.QueryEscape(next), nil))
	brokerLocation, _ := url.Parse(loginRecorder.Header().Get("Location"))
	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state="+url.QueryEscape(brokerLocation.Query().Get("state")), nil)
	callbackRequest.AddCookie(loginRecorder.Result().Cookies()[0])
	callbackRecorder := httptest.NewRecorder()
	rp.CallbackHandler().ServeHTTP(callbackRecorder, callbackRequest)
	if callbackRecorder.Code != http.StatusSeeOther || callbackRecorder.Header().Get("Location") != next {
		t.Fatalf("callback = %d %q, want 303 %q", callbackRecorder.Code, callbackRecorder.Header().Get("Location"), next)
	}
	var session *http.Cookie
	for _, cookie := range callbackRecorder.Result().Cookies() {
		if cookie.Name == appSessionCookieName {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("callback did not mint session")
	}
	identity := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(session)
	rp.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, subjectOK := gcpauth.SubjectFromContext(r.Context())
		email, emailOK := gcpauth.EmailFromContext(r.Context())
		if !subjectOK || !emailOK || subject != "subject-1" || email != "person@example.test" {
			t.Errorf("identity = %q/%v %q/%v", subject, subjectOK, email, emailOK)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(identity, request)
	if identity.Code != http.StatusNoContent {
		t.Fatalf("authenticated status = %d", identity.Code)
	}
}

func TestRelyingPartyRejectsUnsafeNext(t *testing.T) {
	rp := NewRP(RPConfig{BrokerURL: "https://auth.example.test", ClientID: "tissues", RedirectURI: "https://tissues.example.test/auth/callback", Secret: []byte("secret")})
	for _, target := range []string{"https://evil.example/", "//evil.example/"} {
		recorder := httptest.NewRecorder()
		rp.LoginHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/login?next="+url.QueryEscape(target), nil))
		var state rpState
		if err := decodeSigned(recorder.Result().Cookies()[0].Value, rp.cfg.Secret, &state); err != nil {
			t.Fatal(err)
		}
		if state.Next != "/" {
			t.Fatalf("unsafe %q became %q, want /", target, state.Next)
		}
	}
}

type rpRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRelyingPartyInsecureCookieRequiresExplicitOptIn(t *testing.T) {
	rp := NewRP(RPConfig{
		BrokerURL:      "http://127.0.0.1:8081",
		ClientID:       "tissues",
		RedirectURI:    "http://127.0.0.1:8080/auth/callback",
		Secret:         []byte("test-only-secret"),
		InsecureCookie: true,
	})
	recorder := httptest.NewRecorder()
	rp.LoginHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("state cookie Secure = true, want false for explicit local opt-in")
	}
}
