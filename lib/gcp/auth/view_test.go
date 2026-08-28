package gcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestViewDelegatesLoginGETAndAssets(t *testing.T) {
	var paths []string
	h := newTestView(t, Config{BasePath: "/auth/login", Secret: []byte("secret")}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, "generated auth frontend")
	}))

	for _, path := range []string{"/auth/login", "/auth/login/", "/auth/login/assets/app.js"} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "generated auth frontend" {
			t.Fatalf("GET %s = %d %q", path, recorder.Code, recorder.Body.String())
		}
	}
	if strings.Join(paths, ",") != "/auth/login,/auth/login/,/auth/login/assets/app.js" {
		t.Fatalf("delegated paths = %v", paths)
	}
}

func TestViewPOSTInvokesIdentityVerifierAndPreservesSuccessFlow(t *testing.T) {
	secret := []byte("test-only-signing-secret")
	next := "/authorize?client_id=tissues&state=abc"
	signed, err := withSignedNext("/auth/login", next, secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	signedURL, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	verifierCalled := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		verifierCalled = true
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"email":"person@example.test"`) || !strings.Contains(string(body), `"password":"password"`) {
			t.Fatalf("verifier body = %s", body)
		}
		return jsonResponse(http.StatusOK, `{"localId":"subject-1","email":"person@example.test","idToken":"id-token","refreshToken":"refresh-token","expiresIn":"3600"}`), nil
	})}
	h := newTestView(t, Config{BasePath: "/auth/login", Secret: secret, APIKey: "test-api-key", HTTPClient: client, InsecureCookie: true}, http.NotFoundHandler())

	form := url.Values{"email": {"person@example.test"}, "password": {"password"}}
	for _, name := range []string{nextParamName, nextExpParamName, nextSigParamName} {
		form.Set(name, signedURL.Query().Get(name))
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(recorder, request)

	if !verifierCalled {
		t.Fatal("Identity Platform verifier was not called")
	}
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != next {
		t.Fatalf("login response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if !strings.Contains(recorder.Header().Get("Set-Cookie"), sessionCookieName+"=") {
		t.Fatalf("missing auth session cookie: %q", recorder.Header().Get("Set-Cookie"))
	}
}

func TestViewFailedLoginReturnsToGeneratedUIWithSignedState(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, `{"error":{"message":"INVALID_PASSWORD"}}`), nil
	})}
	h := newTestView(t, Config{BasePath: "/auth/login", Secret: []byte("secret"), APIKey: "test-api-key", HTTPClient: client}, http.NotFoundHandler())
	form := url.Values{
		"email": {"person@example.test"}, "password": {"wrong"},
		nextParamName: {"/authorize?client_id=tissues"}, nextExpParamName: {"123"}, nextSigParamName: {"signed"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if location.Path != "/auth/login" || query.Get("error") != "invalid_credentials" || query.Get(nextParamName) != form.Get(nextParamName) || query.Get(nextExpParamName) != form.Get(nextExpParamName) || query.Get(nextSigParamName) != form.Get(nextSigParamName) {
		t.Fatalf("failure location = %q", location.String())
	}
}

func TestViewRejectsMethodAndOutsidePath(t *testing.T) {
	h := newTestView(t, Config{BasePath: "/auth/login", Secret: []byte("secret")}, http.NotFoundHandler())

	for _, test := range []struct {
		method string
		path   string
		status int
	}{{http.MethodPut, "/auth/login", http.StatusMethodNotAllowed}, {http.MethodPost, "/auth/login/assets/app.js", http.StatusMethodNotAllowed}, {http.MethodGet, "/other", http.StatusNotFound}} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s = %d, want %d", test.method, test.path, recorder.Code, test.status)
		}
	}
}

func newTestView(t *testing.T, cfg Config, frontend http.Handler) http.Handler {
	t.Helper()
	handler, err := New(cfg, Frontend{GET: frontend})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
