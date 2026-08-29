package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const (
	testIssuer   = "https://auth.example.test"
	testResource = "https://auth.example.test/mcp"
	testRedirect = "https://app.example.test/callback"
)

func newTestService() *Service {
	return NewService(ServiceConfig{
		SigningSecret: []byte("01234567890123456789012345678901"),
		Issuer:        testIssuer, Resource: testResource,
		Scopes:            []string{"tissues:read", "tissues:write"},
		ScopeImplications: map[string][]string{"tissues:write": {"tissues:read"}},
		Clients:           map[string]Client{"tissues": {ID: "tissues", Secret: "secret", RedirectURI: testRedirect}},
	})
}

func authorize(t *testing.T, svc *Service, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return authorizeRequest(t, svc, values, true)
}

func authorizeRequest(t *testing.T, svc *Service, values url.Values, defaultResponseType bool) *httptest.ResponseRecorder {
	t.Helper()
	if values.Get("client_id") == "" {
		values.Set("client_id", "tissues")
	}
	if values.Get("redirect_uri") == "" {
		values.Set("redirect_uri", testRedirect)
	}
	if defaultResponseType && !values.Has("response_type") {
		values.Set("response_type", "code")
	}
	req := httptest.NewRequest(http.MethodGet, "/authorize?"+values.Encode(), nil)
	ctx := gcpauth.WithSubject(req.Context(), "subject-1")
	ctx = gcpauth.WithEmail(ctx, "person@example.test")
	rec := httptest.NewRecorder()
	svc.AuthorizeHandler().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestAuthorizationRequiresExactlyOneCodeResponseType(t *testing.T) {
	tests := map[string]struct {
		values              url.Values
		defaultResponseType bool
		wantError           string
	}{
		"missing":  {url.Values{"state": {"opaque"}}, false, "invalid_request"},
		"empty":    {url.Values{"response_type": {""}, "state": {"opaque"}}, true, "invalid_request"},
		"token":    {url.Values{"response_type": {"token"}, "state": {"opaque"}}, true, "unsupported_response_type"},
		"unknown":  {url.Values{"response_type": {"unknown"}, "state": {"opaque"}}, true, "unsupported_response_type"},
		"repeated": {url.Values{"response_type": {"code", "code"}, "state": {"opaque"}}, true, "invalid_request"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := authorizeRequest(t, newTestService(), tc.values, tc.defaultResponseType)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			location, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if location.Query().Get("error") != tc.wantError || location.Query().Get("state") != "opaque" || location.Query().Get("iss") != testIssuer {
				t.Fatalf("error redirect = %q", location.String())
			}
		})
	}
}

func TestAuthorizationDoesNotRedirectResponseTypeErrorsToInvalidRedirect(t *testing.T) {
	values := url.Values{"client_id": {"tissues"}, "redirect_uri": {"https://evil.example/callback"}}
	rec := authorizeRequest(t, newTestService(), values, false)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Fatalf("invalid redirect response = %d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
}

func authorizationCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, body=%q", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := location.Query().Get("iss"); got != testIssuer {
		t.Fatalf("iss = %q, want %q", got, testIssuer)
	}
	return location.Query().Get("code")
}

func exchange(t *testing.T, svc *Service, code, resource string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {"tissues"},
		"client_secret": {"secret"}, "redirect_uri": {testRedirect},
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.TokenHandler().ServeHTTP(rec, req)
	return rec
}

func TestBrowserAuthorizationAndTokenExchangeRemainResourceAndScopeLess(t *testing.T) {
	svc := newTestService()
	rec := authorize(t, svc, url.Values{"state": {"opaque"}})
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location.Query().Get("state") != "opaque" || location.Query().Get("iss") != testIssuer {
		t.Fatalf("authorization redirect = %q", location.String())
	}
	code := authorizationCode(t, rec)
	if stored := svc.codes[code]; stored.Resource != "" || len(stored.Scopes) != 0 {
		t.Fatalf("browser code binding = resource %q scopes %v", stored.Resource, stored.Scopes)
	}
	tokenRec := exchange(t, svc, code, "")
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token status = %d body=%q", tokenRec.Code, tokenRec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(tokenRec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["scope"]; ok {
		t.Fatalf("browser response unexpectedly contains scope: %v", response)
	}
	if _, ok := response["refresh_token"]; ok {
		t.Fatalf("browser response unexpectedly contains refresh_token: %v", response)
	}
	verified, err := svc.VerifyAccessToken(response["access_token"].(string), testIssuer, "")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Resource != "" || len(verified.Scopes) != 0 || verified.Subject != "subject-1" || verified.Email != "person@example.test" || verified.ClientID != "tissues" {
		t.Fatalf("verified browser token = %#v", verified)
	}
	userinfoReq := httptest.NewRequest(http.MethodGet, "/userinfo", nil)
	userinfoReq.Header.Set("Authorization", "Bearer "+response["access_token"].(string))
	userinfoRec := httptest.NewRecorder()
	svc.UserinfoHandler().ServeHTTP(userinfoRec, userinfoReq)
	if userinfoRec.Code != http.StatusOK {
		t.Fatalf("userinfo status = %d body=%q", userinfoRec.Code, userinfoRec.Body.String())
	}
	var userinfo map[string]any
	if err := json.Unmarshal(userinfoRec.Body.Bytes(), &userinfo); err != nil {
		t.Fatal(err)
	}
	if userinfo["sub"] != "subject-1" || userinfo["email"] != "person@example.test" || userinfo["client_id"] != "tissues" || userinfo["exp"] == nil {
		t.Fatalf("userinfo = %#v", userinfo)
	}
}

func TestAuthorizationValidatesResourceScopesAndCanonicalizesWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		values    url.Values
		wantError string
	}{
		"unknown resource": {url.Values{"resource": {"https://other.example/mcp"}}, "invalid_target"},
		"empty resource":   {url.Values{"resource": {""}}, "invalid_target"},
		"spaced resource":  {url.Values{"resource": {" " + testResource}}, "invalid_target"},
		"many resources":   {url.Values{"resource": {testResource, testResource}}, "invalid_target"},
		"unknown scope":    {url.Values{"scope": {"other"}}, "invalid_scope"},
		"empty scope":      {url.Values{"scope": {""}}, "invalid_scope"},
		"many scopes":      {url.Values{"scope": {"tissues:read", "tissues:write"}}, "invalid_scope"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := authorize(t, newTestService(), tc.values)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			location, _ := url.Parse(rec.Header().Get("Location"))
			if location.Query().Get("error") != tc.wantError || location.Query().Get("iss") != testIssuer {
				t.Fatalf("error redirect = %q", location.String())
			}
		})
	}

	for name, tc := range map[string]struct {
		raw  string
		want []string
	}{
		"read":       {"tissues:read", []string{"tissues:read"}},
		"write":      {"tissues:write", []string{"tissues:read", "tissues:write"}},
		"duplicates": {"tissues:write tissues:read tissues:write", []string{"tissues:read", "tissues:write"}},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService()
			code := authorizationCode(t, authorize(t, svc, url.Values{"resource": {testResource}, "scope": {tc.raw}}))
			if got := svc.codes[code]; got.Resource != testResource || !reflect.DeepEqual(got.Scopes, tc.want) {
				t.Fatalf("code binding = %q %v, want %q %v", got.Resource, got.Scopes, testResource, tc.want)
			}
		})
	}
}

func TestAuthorizationPreservesExactRedirectAndEntitlements(t *testing.T) {
	svc := newTestService()
	rec := authorize(t, svc, url.Values{"redirect_uri": {testRedirect + "/wrong"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "redirect_uri mismatch") {
		t.Fatalf("redirect mismatch = %d %q", rec.Code, rec.Body.String())
	}
	svc.cfg.Entitlements = map[string]map[string]struct{}{"sub:someone-else": {"tissues": {}}}
	rec = authorize(t, svc, url.Values{"state": {"opaque"}})
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location.Query().Get("error") != "access_denied" || location.Query().Get("state") != "opaque" || location.Query().Get("iss") != testIssuer {
		t.Fatalf("denial redirect = %q", location.String())
	}
}

func TestResourceBindingIsAtomicAndCodeRemainsSingleUse(t *testing.T) {
	for _, resource := range []string{"", "https://other.example/mcp"} {
		svc := newTestService()
		code := authorizationCode(t, authorize(t, svc, url.Values{"resource": {testResource}, "scope": {"tissues:read"}}))
		rec := exchange(t, svc, code, resource)
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != "invalid_grant" {
			t.Fatalf("resource %q exchange = %d %q", resource, rec.Code, rec.Body.String())
		}
		if correct := exchange(t, svc, code, testResource); correct.Code != http.StatusOK {
			t.Fatalf("resource %q mismatch consumed code: correct exchange = %d %q", resource, correct.Code, correct.Body.String())
		}
		if replay := exchange(t, svc, code, testResource); replay.Code != http.StatusBadRequest || strings.TrimSpace(replay.Body.String()) != "invalid_grant" {
			t.Fatalf("resource %q successful exchange replay = %d %q", resource, replay.Code, replay.Body.String())
		}
	}
}

func TestInMemoryClientAndRedirectMismatchesDoNotConsumeCode(t *testing.T) {
	for name, mismatch := range map[string]func(*Service, string) error{
		"client": func(svc *Service, code string) error {
			_, err := svc.consumeCode(context.Background(), code, "other", testRedirect, "")
			return err
		},
		"redirect": func(svc *Service, code string) error {
			_, err := svc.consumeCode(context.Background(), code, "tissues", testRedirect+"/wrong", "")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService()
			code := authorizationCode(t, authorize(t, svc, url.Values{}))
			if err := mismatch(svc, code); err == nil {
				t.Fatal("mismatch unexpectedly succeeded")
			}
			if _, err := svc.consumeCode(context.Background(), code, "tissues", testRedirect, ""); err != nil {
				t.Fatalf("correct redemption after mismatch: %v", err)
			}
			if _, err := svc.consumeCode(context.Background(), code, "tissues", testRedirect, ""); err == nil {
				t.Fatal("successful redemption replay unexpectedly succeeded")
			}
		})
	}
}

func TestScopedTokenClaimsResponseAndVerification(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorize(t, svc, url.Values{"resource": {testResource}, "scope": {"tissues:write"}}))
	rec := exchange(t, svc, code, testResource)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["scope"] != "tissues:read tissues:write" || response["token_type"] != "Bearer" || response["expires_in"] != float64(900) {
		t.Fatalf("token response = %#v", response)
	}
	if _, ok := response["refresh_token"]; ok {
		t.Fatal("refresh token emitted")
	}
	verified, err := svc.VerifyAccessToken(response["access_token"].(string), testIssuer, testResource)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Issuer != testIssuer || verified.Resource != testResource || !reflect.DeepEqual(verified.Scopes, []string{"tissues:read", "tissues:write"}) || verified.IssuedAt.IsZero() || verified.ExpiresAt.Sub(verified.IssuedAt) != 15*time.Minute {
		t.Fatalf("verified token = %#v", verified)
	}
	var claims map[string]any
	if err := decodeSigned(response["access_token"].(string), svc.cfg.SigningSecret, &claims); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"iss": testIssuer, "aud": testResource, "sub": "subject-1", "email": "person@example.test", "client_id": "tissues", "scope": "tissues:read tissues:write"} {
		if claims[key] != want {
			t.Fatalf("claim %s = %#v, want %#v", key, claims[key], want)
		}
	}
	if claims["iat"] == nil || claims["exp"] == nil {
		t.Fatalf("timestamp claims = %#v", claims)
	}
}

func TestTokenExchangeRequiresConfidentialClientSecret(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorize(t, svc, url.Values{}))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {"tissues"}, "redirect_uri": {testRedirect}}
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.TokenHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != "invalid_client" {
		t.Fatalf("exchange = %d %q", rec.Code, rec.Body.String())
	}
}

func TestCodeExpiryAndReplayRemainEnforced(t *testing.T) {
	svc := newTestService()
	code := authorizationCode(t, authorize(t, svc, url.Values{}))
	stored := svc.codes[code]
	stored.ExpiresAt = time.Now().Add(-time.Second)
	svc.codes[code] = stored
	if rec := exchange(t, svc, code, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired exchange status = %d", rec.Code)
	}
	if _, ok := svc.codes[code]; !ok {
		t.Fatal("expired exchange consumed code")
	}
	if rec := exchange(t, svc, code, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("replay status = %d", rec.Code)
	}
}

func TestVerifyAccessTokenRejectsInvalidTokensWithoutLeakingThem(t *testing.T) {
	svc := newTestService()
	now := time.Now().Unix()
	valid := accessToken{Issuer: testIssuer, Resource: testResource, Subject: "subject-1", Email: "person@example.test", ClientID: "tissues", Scope: "tissues:read", IssuedAt: now, ExpiresAt: now + 900}
	tests := map[string]accessToken{
		"wrong issuer": valid, "wrong resource": valid, "missing subject": valid,
		"missing client": valid, "unknown client": valid, "missing issued at": valid,
		"bad expiry": valid, "unknown scope": valid, "noncanonical scopes": valid,
	}
	tests["wrong issuer"] = withToken(valid, func(v *accessToken) { v.Issuer = "https://wrong.example" })
	tests["wrong resource"] = withToken(valid, func(v *accessToken) { v.Resource = "https://wrong.example/mcp" })
	tests["missing subject"] = withToken(valid, func(v *accessToken) { v.Subject = "" })
	tests["missing client"] = withToken(valid, func(v *accessToken) { v.ClientID = "" })
	tests["unknown client"] = withToken(valid, func(v *accessToken) { v.ClientID = "other" })
	tests["missing issued at"] = withToken(valid, func(v *accessToken) { v.IssuedAt = 0 })
	tests["bad expiry"] = withToken(valid, func(v *accessToken) { v.ExpiresAt = v.IssuedAt })
	tests["unknown scope"] = withToken(valid, func(v *accessToken) { v.Scope = "other" })
	tests["noncanonical scopes"] = withToken(valid, func(v *accessToken) { v.Scope = "tissues:write tissues:read" })
	for name, claims := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := encodeSigned(claims, svc.cfg.SigningSecret)
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.VerifyAccessToken(raw, testIssuer, testResource)
			if !errors.Is(err, ErrInvalidAccessToken) || strings.Contains(err.Error(), raw) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := svc.VerifyAccessToken("tampered-token", testIssuer, testResource); !errors.Is(err, ErrInvalidAccessToken) {
		t.Fatalf("tampered error = %v", err)
	}
	malformed, err := encodeSigned(map[string]any{"iss": testIssuer, "aud": testResource, "sub": "subject-1", "client_id": "tissues", "scope": "tissues:read", "iat": "not-a-timestamp", "exp": now + 900}, svc.cfg.SigningSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyAccessToken(malformed, testIssuer, testResource); !errors.Is(err, ErrInvalidAccessToken) {
		t.Fatalf("malformed claims error = %v", err)
	}
}

func TestVerifierParsesExpiredTokenButUserinfoRejectsIt(t *testing.T) {
	svc := newTestService()
	now := time.Now().Unix()
	raw, err := encodeSigned(accessToken{Issuer: testIssuer, Subject: "subject-1", ClientID: "tissues", IssuedAt: now - 1000, ExpiresAt: now - 100}, svc.cfg.SigningSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyAccessToken(raw, testIssuer, ""); err != nil {
		t.Fatalf("verifier compared expiration: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	svc.UserinfoHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != "expired_token" {
		t.Fatalf("userinfo = %d %q", rec.Code, rec.Body.String())
	}
}

func TestCodeStoreReceivesAndMatchesResourceAndScopes(t *testing.T) {
	store := &recordingCodeStore{}
	svc := newTestService()
	svc.cfg.CodeStore = store
	code := authorizationCode(t, authorize(t, svc, url.Values{"resource": {testResource}, "scope": {"tissues:write"}}))
	if store.code != code || store.value.Resource != testResource || !reflect.DeepEqual(store.value.Scopes, []string{"tissues:read", "tissues:write"}) {
		t.Fatalf("saved = %q %#v", store.code, store.value)
	}
	if rec := exchange(t, svc, code, testResource); rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %q", rec.Code, rec.Body.String())
	}
	if store.resource != testResource {
		t.Fatalf("consumed resource = %q", store.resource)
	}
}

type recordingCodeStore struct {
	code, resource string
	value          authCode
	consumed       bool
}

func (s *recordingCodeStore) SaveCode(_ context.Context, code string, value authCode) error {
	s.code, s.value = code, value
	return nil
}

func (s *recordingCodeStore) ConsumeCode(_ context.Context, code, clientID, redirectURI, resource string) (authCode, error) {
	if s.consumed || code != s.code {
		return authCode{}, ErrCodeNotFound
	}
	s.consumed = true
	s.resource = resource
	if s.value.ClientID != clientID || s.value.RedirectURI != redirectURI || s.value.Resource != resource {
		return authCode{}, ErrCodeMismatch
	}
	return s.value, nil
}

func withToken(value accessToken, change func(*accessToken)) accessToken {
	change(&value)
	return value
}
