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
	request := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	request.Host = "tissues.example.test"
	rp.LoginHandler().ServeHTTP(recorder, request)

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

func TestRelyingPartyLoginRequestsExactlyOneAuthorizationCodeResponseType(t *testing.T) {
	rp := NewRP(RPConfig{
		BrokerURL:   "https://auth.example.test",
		ClientID:    "tissues",
		RedirectURI: "https://tissues.example.test/auth/callback",
		Secret:      []byte("test-only-secret"),
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://tissues.example.test/auth/login?next=%2F", nil)
	rp.LoginHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := location.Query()["response_type"]; len(got) != 1 || got[0] != "code" {
		t.Fatalf("response_type = %v, want exactly [code]", got)
	}
	if location.Query().Get("client_id") != "tissues" || location.Query().Get("redirect_uri") != "https://tissues.example.test/auth/callback" || location.Query().Get("state") == "" {
		t.Fatalf("authorization query = %v", location.Query())
	}
	if location.Query().Has("resource") || location.Query().Has("scope") {
		t.Fatalf("browser authorization unexpectedly requests resource/scope: %v", location.Query())
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
	loginRequest := httptest.NewRequest(http.MethodGet, "/auth/login?next="+url.QueryEscape(next), nil)
	loginRequest.Host = "tissues.example.test"
	rp.LoginHandler().ServeHTTP(loginRecorder, loginRequest)
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
		request := httptest.NewRequest(http.MethodGet, "/auth/login?next="+url.QueryEscape(target), nil)
		request.Host = "tissues.example.test"
		rp.LoginHandler().ServeHTTP(recorder, request)
		var state rpState
		if err := decodeSigned(recorder.Result().Cookies()[0].Value, rp.cfg.Secret, &state); err != nil {
			t.Fatal(err)
		}
		if state.Next != "/" {
			t.Fatalf("unsafe %q became %q, want /", target, state.Next)
		}
	}
}

func TestSessionMiddlewareDelegatesUnauthenticatedRequests(t *testing.T) {
	rp := NewRP(RPConfig{Secret: []byte("test-only-secret")})
	recorder := httptest.NewRecorder()
	rp.SessionMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler called")
	}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
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
	request := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	request.Host = "127.0.0.1:8080"
	rp.LoginHandler().ServeHTTP(recorder, request)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("state cookie Secure = true, want false for explicit local opt-in")
	}
}

func TestRelyingPartyCanonicalizesLoginHostBeforeCreatingState(t *testing.T) {
	rp := NewRP(RPConfig{
		BrokerURL:      "http://127.0.0.1:18080",
		ClientID:       "tissues",
		RedirectURI:    "http://127.0.0.1:18080/tissues/auth/callback",
		LoginPath:      "/tissues/auth/login",
		Secret:         []byte("test-only-secret"),
		InsecureCookie: true,
	})
	const rawQuery = "next=%2F%3Fview%3Dissue%26issue%3DFLUENT-1"
	noncanonical := httptest.NewRequest(http.MethodGet, "http://localhost:18080/tissues/auth/login?"+rawQuery, nil)
	noncanonicalRecorder := httptest.NewRecorder()
	rp.LoginHandler().ServeHTTP(noncanonicalRecorder, noncanonical)

	const canonicalLogin = "http://127.0.0.1:18080/tissues/auth/login?" + rawQuery
	if noncanonicalRecorder.Code != http.StatusFound || noncanonicalRecorder.Header().Get("Location") != canonicalLogin {
		t.Fatalf("noncanonical login = %d %q, want %d %q", noncanonicalRecorder.Code, noncanonicalRecorder.Header().Get("Location"), http.StatusFound, canonicalLogin)
	}
	if cookies := noncanonicalRecorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("noncanonical login set %d cookies, want none", len(cookies))
	}

	canonical := httptest.NewRequest(http.MethodGet, canonicalLogin, nil)
	canonicalRecorder := httptest.NewRecorder()
	rp.LoginHandler().ServeHTTP(canonicalRecorder, canonical)
	if canonicalRecorder.Code != http.StatusFound {
		t.Fatalf("canonical login status = %d, want %d", canonicalRecorder.Code, http.StatusFound)
	}
	brokerLocation, err := url.Parse(canonicalRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if brokerLocation.Scheme != "http" || brokerLocation.Host != "127.0.0.1:18080" || brokerLocation.Path != "/authorize" {
		t.Fatalf("canonical login location = %q, want normal broker authorize URL", brokerLocation.String())
	}
	cookies := canonicalRecorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stateCookieName {
		t.Fatalf("canonical login cookies = %#v, want one %s cookie", cookies, stateCookieName)
	}
	if cookies[0].Domain != "" {
		t.Fatalf("state cookie Domain = %q, want host-only", cookies[0].Domain)
	}
	var state rpState
	if err := decodeSigned(cookies[0].Value, rp.cfg.Secret, &state); err != nil {
		t.Fatal(err)
	}
	if state.Nonce == "" || state.Nonce != brokerLocation.Query().Get("state") {
		t.Fatalf("cookie nonce %q does not match authorize state %q", state.Nonce, brokerLocation.Query().Get("state"))
	}
}

func TestRelyingPartyInvalidRedirectURIDoesNotPanic(t *testing.T) {
	for _, redirectURI := range []string{"://invalid", "/tissues/auth/callback", "mailto:user@example.test"} {
		t.Run(redirectURI, func(t *testing.T) {
			rp := NewRP(RPConfig{BrokerURL: "https://auth.example.test", ClientID: "tissues", RedirectURI: redirectURI, Secret: []byte("test-only-secret")})
			recorder := httptest.NewRecorder()
			rp.LoginHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
			if recorder.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
			}
		})
	}
}
