package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func publicAuthorizationValues() url.Values {
	return url.Values{
		"client_id":             {"public"},
		"redirect_uri":          {publicRedirect},
		"response_type":         {"code"},
		"resource":              {testResource},
		"scope":                 {"tissues:write"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"opaque"},
	}
}

func authorizePublic(t *testing.T, svc *Service, change func(url.Values)) *httptest.ResponseRecorder {
	t.Helper()
	values := publicAuthorizationValues()
	if change != nil {
		change(values)
	}
	return authorizeRequest(t, svc, values, false)
}

func exchangePublic(t *testing.T, svc *Service, code string, change func(url.Values)) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {"public"},
		"redirect_uri":  {publicRedirect},
		"resource":      {testResource},
		"code_verifier": {testVerifier},
	}
	if change != nil {
		change(form)
	}
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.TokenHandler().ServeHTTP(rec, req)
	return rec
}

func TestPublicClientAuthorizationRequiresResourceScopeAndS256PKCE(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorizePublic(t, svc, nil))
	stored := svc.codes[code]
	if stored.ClientID != "public" || stored.RedirectURI != publicRedirect || stored.Resource != testResource || stored.CodeChallenge != testChallenge || stored.CodeChallengeMethod != "S256" || strings.Join(stored.Scopes, " ") != "tissues:read tissues:write" {
		t.Fatalf("public code binding = %#v", stored)
	}

	tests := map[string]struct {
		change    func(url.Values)
		wantError string
	}{
		"missing resource":    {func(v url.Values) { v.Del("resource") }, "invalid_target"},
		"repeated resource":   {func(v url.Values) { v["resource"] = []string{testResource, testResource} }, "invalid_target"},
		"missing scope":       {func(v url.Values) { v.Del("scope") }, "invalid_scope"},
		"empty scope":         {func(v url.Values) { v.Set("scope", "") }, "invalid_scope"},
		"unsupported scope":   {func(v url.Values) { v.Set("scope", "other") }, "invalid_scope"},
		"repeated scope":      {func(v url.Values) { v["scope"] = []string{"tissues:read", "tissues:write"} }, "invalid_scope"},
		"missing challenge":   {func(v url.Values) { v.Del("code_challenge") }, "invalid_request"},
		"malformed challenge": {func(v url.Values) { v.Set("code_challenge", strings.Repeat("a", 42)) }, "invalid_request"},
		"padded challenge":    {func(v url.Values) { v.Set("code_challenge", testChallenge+"=") }, "invalid_request"},
		"repeated challenge":  {func(v url.Values) { v["code_challenge"] = []string{testChallenge, testChallenge} }, "invalid_request"},
		"missing method":      {func(v url.Values) { v.Del("code_challenge_method") }, "invalid_request"},
		"plain method":        {func(v url.Values) { v.Set("code_challenge_method", "plain") }, "invalid_request"},
		"repeated method":     {func(v url.Values) { v["code_challenge_method"] = []string{"S256", "S256"} }, "invalid_request"},
		"repeated state":      {func(v url.Values) { v["state"] = []string{"one", "two"} }, "invalid_request"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := authorizePublic(t, newTestService(), tc.change)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			location, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			wantState := "opaque"
			if name == "repeated state" {
				wantState = ""
			}
			if location.Query().Get("error") != tc.wantError || location.Query().Get("iss") != testIssuer || location.Query().Get("state") != wantState {
				t.Fatalf("error redirect = %q", location.String())
			}
		})
	}
}

func TestPublicAuthorizationRejectsAmbiguousClientAndRedirectWithoutRedirecting(t *testing.T) {
	for name, change := range map[string]func(url.Values){
		"repeated client":   func(v url.Values) { v["client_id"] = []string{"public", "public"} },
		"repeated redirect": func(v url.Values) { v["redirect_uri"] = []string{publicRedirect, publicRedirect} },
	} {
		t.Run(name, func(t *testing.T) {
			rec := authorizePublic(t, newTestService(), change)
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
				t.Fatalf("response = %d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
		})
	}
}

func TestPublicClientAuthorizationRejectsUnknownClientAndPreservesEntitlements(t *testing.T) {
	rec := authorizePublic(t, newTestService(), func(v url.Values) { v.Set("client_id", "unknown") })
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Fatalf("unknown client = %d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	svc := newTestService()
	svc.cfg.Entitlements = map[string]map[string]struct{}{"sub:subject-1": {"tissues": {}}}
	rec = authorizePublic(t, svc, nil)
	location, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || location.Query().Get("error") != "access_denied" || location.Query().Get("iss") != testIssuer {
		t.Fatalf("public entitlement denial = %d %q", rec.Code, location.String())
	}
}

func TestPublicTokenExchangeUsesNoClientSecretAndConsumesOnlyOnExactBinding(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorizePublic(t, svc, nil))
	rec := exchangePublic(t, svc, code, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %q", rec.Code, rec.Body.String())
	}
	requireTokenResponseHeaders(t, rec)
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["scope"] != "tissues:read tissues:write" || response["access_token"] == nil {
		t.Fatalf("response = %#v", response)
	}
	requireTokenError(t, exchangePublic(t, svc, code, nil), http.StatusBadRequest, "invalid_grant")

	tests := map[string]func(url.Values){
		"wrong verifier":     func(v url.Values) { v.Set("code_verifier", strings.Repeat("z", 43)) },
		"missing verifier":   func(v url.Values) { v.Del("code_verifier") },
		"malformed verifier": func(v url.Values) { v.Set("code_verifier", "short") },
		"repeated verifier":  func(v url.Values) { v["code_verifier"] = []string{testVerifier, testVerifier} },
		"wrong resource":     func(v url.Values) { v.Set("resource", "https://other.example/mcp") },
		"missing resource":   func(v url.Values) { v.Del("resource") },
		"repeated resource":  func(v url.Values) { v["resource"] = []string{testResource, testResource} },
		"wrong redirect":     func(v url.Values) { v.Set("redirect_uri", publicRedirect+"/wrong") },
		"repeated redirect":  func(v url.Values) { v["redirect_uri"] = []string{publicRedirect, publicRedirect} },
		"repeated code": func(v url.Values) {
			value := v.Get("code")
			v["code"] = []string{value, value}
		},
		"wrong client": func(v url.Values) { v.Set("client_id", "public-b") },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			svc := newTestService()
			code := authorizationCode(t, authorizePublic(t, svc, nil))
			mismatch := exchangePublic(t, svc, code, change)
			requireTokenError(t, mismatch, http.StatusBadRequest, "invalid_grant")
			if correct := exchangePublic(t, svc, code, nil); correct.Code != http.StatusOK {
				t.Fatalf("mismatch consumed code: %d %q", correct.Code, correct.Body.String())
			}
		})
	}

	svc = newTestService()
	code = authorizationCode(t, authorizePublic(t, svc, nil))
	repeatedClient := exchangePublic(t, svc, code, func(v url.Values) { v["client_id"] = []string{"public", "public"} })
	requireTokenError(t, repeatedClient, http.StatusUnauthorized, "invalid_client")
	if correct := exchangePublic(t, svc, code, nil); correct.Code != http.StatusOK {
		t.Fatalf("repeated client consumed code: %d %q", correct.Code, correct.Body.String())
	}
}

func TestPublicPKCEBindingFlowsThroughCodeStore(t *testing.T) {
	store := &recordingCodeStore{}
	svc := newTestService()
	svc.cfg.CodeStore = store
	code := authorizationCode(t, authorizePublic(t, svc, nil))
	if store.code != code || store.value.CodeChallenge != testChallenge || store.value.CodeChallengeMethod != "S256" {
		t.Fatalf("saved public code = %q %#v", store.code, store.value)
	}
	if rec := exchangePublic(t, svc, code, nil); rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %q", rec.Code, rec.Body.String())
	}
	if store.codeVerifier != testVerifier || !store.consumed {
		t.Fatalf("store verifier/consume = %q %t", store.codeVerifier, store.consumed)
	}
}

func TestClientAuthenticationMethodPreventsCrossClientConfusion(t *testing.T) {
	svc := newTestService()
	publicCode := authorizationCode(t, authorizePublic(t, svc, nil))
	withSecret := exchangePublic(t, svc, publicCode, func(v url.Values) { v.Set("client_secret", "secret") })
	requireTokenError(t, withSecret, http.StatusUnauthorized, "invalid_client")
	if correct := exchangePublic(t, svc, publicCode, nil); correct.Code != http.StatusOK {
		t.Fatalf("public secret attempt consumed code: %d %q", correct.Code, correct.Body.String())
	}

	confidentialCode := authorizationCode(t, authorize(t, svc, url.Values{}))
	publicAttempt := exchangePublic(t, svc, confidentialCode, nil)
	requireTokenError(t, publicAttempt, http.StatusBadRequest, "invalid_grant")
	if correct := exchange(t, svc, confidentialCode, ""); correct.Code != http.StatusOK {
		t.Fatalf("public attempt consumed confidential code: %d %q", correct.Code, correct.Body.String())
	}

	svc = newTestService()
	publicCode = authorizationCode(t, authorizePublic(t, svc, nil))
	confidentialAttempt := exchange(t, svc, publicCode, "")
	requireTokenError(t, confidentialAttempt, http.StatusBadRequest, "invalid_grant")
	if correct := exchangePublic(t, svc, publicCode, nil); correct.Code != http.StatusOK {
		t.Fatalf("confidential attempt consumed public code: %d %q", correct.Code, correct.Body.String())
	}
}

func TestConfidentialClientRejectsVerifierWithoutConsumingCode(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorize(t, svc, url.Values{}))
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {"tissues"},
		"client_secret": {"secret"},
		"redirect_uri":  {testRedirect},
		"code_verifier": {testVerifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.TokenHandler().ServeHTTP(rec, req)
	requireTokenError(t, rec, http.StatusBadRequest, "invalid_grant")
	if correct := exchange(t, svc, code, ""); correct.Code != http.StatusOK {
		t.Fatalf("verifier attempt consumed confidential code: %d %q", correct.Code, correct.Body.String())
	}
}

func TestPKCES256KnownVector(t *testing.T) {
	if got := s256Challenge(testVerifier); got != testChallenge {
		t.Fatalf("S256 challenge = %q, want RFC 7636 value %q", got, testChallenge)
	}
}

func TestRedirectURIRegistrationIsExactExceptNativeLoopbackPort(t *testing.T) {
	tests := map[string]struct {
		registered        []string
		candidate         string
		allowLoopbackPort bool
		want              bool
	}{
		"ordinary exact":                    {[]string{"https://client.example/callback?x=1"}, "https://client.example/callback?x=1", true, true},
		"ordinary port differs":             {[]string{"https://client.example:443/callback"}, "https://client.example:8443/callback", true, false},
		"ipv4 loopback port varies":         {[]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:49152/callback", true, true},
		"ipv6 loopback port varies":         {[]string{"http://[::1]:8080/callback"}, "http://[::1]:49152/callback", true, true},
		"confidential loopback stays exact": {[]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:49152/callback", false, false},
		"loopback path differs":             {[]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:49152/other", true, false},
		"loopback query differs":            {[]string{"http://127.0.0.1/callback?x=1"}, "http://127.0.0.1:49152/callback?x=2", true, false},
		"loopback IP family differs":        {[]string{"http://127.0.0.1/callback"}, "http://[::1]:49152/callback", true, false},
		"localhost not wildcarded":          {[]string{"http://localhost/callback"}, "http://localhost:49152/callback", true, false},
		"nonloopback not wildcarded":        {[]string{"http://192.168.1.2/callback"}, "http://192.168.1.2:49152/callback", true, false},
		"scheme differs":                    {[]string{"http://127.0.0.1/callback"}, "https://127.0.0.1:49152/callback", true, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := matchesRedirectURI(tc.registered, tc.candidate, tc.allowLoopbackPort); got != tc.want {
				t.Fatalf("matchesRedirectURI(%q, %q) = %t, want %t", tc.registered, tc.candidate, got, tc.want)
			}
		})
	}
}
