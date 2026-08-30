package broker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const cimdRedirect = "https://client.example.test/callback"

func metadataFor(clientID, redirectURI string) []byte {
	return []byte(`{"client_id":"` + clientID + `","client_name":"Test Client","redirect_uris":["` + redirectURI + `"],"response_types":["code"],"grant_types":["authorization_code"],"token_endpoint_auth_method":"none"}`)
}

func newCIMDTestService(t *testing.T, fetcher ClientMetadataFetcher, entitlements map[string]map[string]struct{}) *Service {
	t.Helper()
	resolver, err := NewCIMDResolver([]string{testMetadataClientID}, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(ServiceConfig{
		SigningSecret: []byte("01234567890123456789012345678901"),
		Issuer:        testIssuer,
		Resource:      testResource,
		Scopes:        []string{"tissues:read", "tissues:write"},
		ScopeImplications: map[string][]string{
			"tissues:write": {"tissues:read"},
		},
		Clients:        map[string]Client{"tissues": {ID: "tissues", Secret: "secret", RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: TokenEndpointAuthMethodClientSecretPost}},
		ClientResolver: resolver,
		Entitlements:   entitlements,
	})
}

func cimdAuthorizationValues() url.Values {
	return url.Values{
		"client_id":             {testMetadataClientID},
		"redirect_uri":          {cimdRedirect},
		"response_type":         {"code"},
		"resource":              {testResource},
		"scope":                 {"tissues:read"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"opaque"},
	}
}

func authorizeCIMD(t *testing.T, svc *Service, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/authorize?"+values.Encode(), nil)
	ctx := gcpauth.WithSubject(req.Context(), "subject-1")
	ctx = gcpauth.WithEmail(ctx, "person@example.test")
	rec := httptest.NewRecorder()
	svc.AuthorizeHandler().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func exchangeCIMD(svc *Service, clientID, code, redirectURI, resource, verifier string) *httptest.ResponseRecorder {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {verifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.TokenHandler().ServeHTTP(rec, req)
	return rec
}

func TestCIMDAuthorizationResolutionAndPublicRequirements(t *testing.T) {
	for name, mutate := range map[string]func(url.Values){
		"valid":            func(url.Values) {},
		"missing PKCE":     func(v url.Values) { v.Del("code_challenge") },
		"missing resource": func(v url.Values) { v.Del("resource") },
		"missing scope":    func(v url.Values) { v.Del("scope") },
	} {
		t.Run(name, func(t *testing.T) {
			fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, cimdRedirect)}
			svc := newCIMDTestService(t, fetcher, nil)
			values := cimdAuthorizationValues()
			mutate(values)
			rec := authorizeCIMD(t, svc, values)
			if rec.Code != http.StatusFound {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			location, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if location.Query().Get("iss") != testIssuer || location.Query().Get("state") != "opaque" {
				t.Fatalf("redirect = %q", location.String())
			}
			if name == "valid" {
				if location.Query().Get("code") == "" || location.Query().Get("error") != "" {
					t.Fatalf("authorization redirect = %q", location.String())
				}
			} else if location.Query().Get("error") == "" || location.Query().Get("code") != "" {
				t.Fatalf("error redirect = %q", location.String())
			}
		})
	}
}

func TestCIMDAuthorizationNeverRedirectsBeforeMetadataAndRedirectValidation(t *testing.T) {
	t.Run("unadmitted URL is not fetched", func(t *testing.T) {
		fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, cimdRedirect)}
		svc := newCIMDTestService(t, fetcher, nil)
		values := cimdAuthorizationValues()
		values.Set("client_id", "https://unknown.example.test/client.json")
		rec := authorizeCIMD(t, svc, values)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" || len(fetcher.calls) != 0 {
			t.Fatalf("status=%d location=%q fetches=%v", rec.Code, rec.Header().Get("Location"), fetcher.calls)
		}
	})
	t.Run("invalid metadata", func(t *testing.T) {
		fetcher := &stubMetadataFetcher{body: []byte(`{"client_id":"` + testMetadataClientID + `"}`)}
		svc := newCIMDTestService(t, fetcher, nil)
		rec := authorizeCIMD(t, svc, cimdAuthorizationValues())
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("unregistered redirect", func(t *testing.T) {
		fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, cimdRedirect)}
		svc := newCIMDTestService(t, fetcher, nil)
		values := cimdAuthorizationValues()
		values.Set("redirect_uri", "https://evil.example.test/callback")
		rec := authorizeCIMD(t, svc, values)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
		}
	})
}

func TestCIMDRedirectMatchingAllowsOnlyLoopbackIPPortVariation(t *testing.T) {
	tests := map[string]struct {
		registered string
		candidate  string
		want       bool
	}{
		"IPv4 registered without port":    {"http://127.0.0.1/callback", "http://127.0.0.1:42000/callback", true},
		"IPv4 registered with other port": {"http://127.0.0.1:41000/callback", "http://127.0.0.1:42000/callback", true},
		"IPv6 registered without port":    {"http://[::1]/callback", "http://[::1]:42000/callback", true},
		"loopback host differs":           {"http://127.0.0.1/callback", "http://[::1]:42000/callback", false},
		"path differs":                    {"http://127.0.0.1/callback", "http://127.0.0.1:42000/other", false},
		"query differs":                   {"http://127.0.0.1/callback?mode=one", "http://127.0.0.1:42000/callback?mode=two", false},
		"scheme differs":                  {"http://127.0.0.1/callback", "https://127.0.0.1:42000/callback", false},
		"localhost port differs":          {"http://localhost:41000/callback", "http://localhost:42000/callback", false},
		"HTTPS port differs":              {"https://client.example.test:41000/callback", "https://client.example.test:42000/callback", false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, tc.registered)}
			svc := newCIMDTestService(t, fetcher, nil)
			values := cimdAuthorizationValues()
			values.Set("redirect_uri", tc.candidate)
			rec := authorizeCIMD(t, svc, values)
			if tc.want {
				if rec.Code != http.StatusFound {
					t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
				}
				location, err := url.Parse(rec.Header().Get("Location"))
				if err != nil || location.Query().Get("code") == "" || location.Query().Get("error") != "" {
					t.Fatalf("location=%q err=%v", rec.Header().Get("Location"), err)
				}
				return
			}
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
				t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestCIMDLoopbackAuthorizationCodeBindsActualRuntimeRedirect(t *testing.T) {
	registered := "http://127.0.0.1/callback"
	runtimeRedirect := "http://127.0.0.1:42000/callback"
	fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, registered)}
	svc := newCIMDTestService(t, fetcher, nil)
	values := cimdAuthorizationValues()
	values.Set("redirect_uri", runtimeRedirect)
	code := authorizationCode(t, authorizeCIMD(t, svc, values))

	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, "http://127.0.0.1:43000/callback", testResource, testVerifier), http.StatusBadRequest, "invalid_grant")
	success := exchangeCIMD(svc, testMetadataClientID, code, runtimeRedirect, testResource, testVerifier)
	if success.Code != http.StatusOK {
		t.Fatalf("runtime redirect exchange status=%d body=%q", success.Code, success.Body.String())
	}
}

func TestCIMDInvalidRedirectSchemesNeverBecomeTrusted(t *testing.T) {
	for name, redirectURI := range map[string]string{
		"remote HTTP":   "http://client.example.test/callback",
		"custom scheme": "com.example.app:/oauth/callback",
	} {
		t.Run(name, func(t *testing.T) {
			fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, redirectURI)}
			svc := newCIMDTestService(t, fetcher, nil)
			values := cimdAuthorizationValues()
			values.Set("redirect_uri", redirectURI)
			rec := authorizeCIMD(t, svc, values)
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
				t.Fatalf("status=%d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
		})
	}
}

func TestStaticClientWinsBeforeCIMDResolver(t *testing.T) {
	fetcher := &stubMetadataFetcher{err: errors.New("must not fetch")}
	resolver, err := NewCIMDResolver([]string{testMetadataClientID}, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceConfig{
		SigningSecret: []byte("01234567890123456789012345678901"), Issuer: testIssuer, Resource: testResource,
		Scopes: []string{"tissues:read"}, Clients: map[string]Client{testMetadataClientID: {ID: testMetadataClientID, RedirectURIs: []string{cimdRedirect}, TokenEndpointAuthMethod: TokenEndpointAuthMethodNone}}, ClientResolver: resolver,
	})
	rec := authorizeCIMD(t, svc, cimdAuthorizationValues())
	if rec.Code != http.StatusFound || len(fetcher.calls) != 0 {
		t.Fatalf("status=%d fetches=%v body=%q", rec.Code, fetcher.calls, rec.Body.String())
	}
}

func TestCIMDEntitlementUsesExactURLClientIdentity(t *testing.T) {
	fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, cimdRedirect)}
	svc := newCIMDTestService(t, fetcher, map[string]map[string]struct{}{"sub:subject-1": {"other": {}}})
	rec := authorizeCIMD(t, svc, cimdAuthorizationValues())
	location, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || location.Query().Get("error") != "access_denied" || location.Query().Get("iss") != testIssuer {
		t.Fatalf("status=%d redirect=%q", rec.Code, location.String())
	}
}

func TestCIMDTokenResolutionFailuresAndBindingMismatchesDoNotConsumeCode(t *testing.T) {
	fetcher := &stubMetadataFetcher{body: metadataFor(testMetadataClientID, cimdRedirect)}
	svc := newCIMDTestService(t, fetcher, nil)
	code := authorizationCode(t, authorizeCIMD(t, svc, cimdAuthorizationValues()))

	fetcher.err = errors.New("temporary fetch failure")
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource, testVerifier), http.StatusUnauthorized, "invalid_client")
	fetcher.err = nil

	fetcher.body = metadataFor(testMetadataClientID, "https://changed.example.test/callback")
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource, testVerifier), http.StatusBadRequest, "invalid_grant")
	fetcher.body = metadataFor(testMetadataClientID, cimdRedirect)

	requireTokenError(t, exchangeCIMD(svc, "https://unknown.example.test/client.json", code, cimdRedirect, testResource, testVerifier), http.StatusUnauthorized, "invalid_client")
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, "https://other.example.test/callback", testResource, testVerifier), http.StatusBadRequest, "invalid_grant")
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource+"/other", testVerifier), http.StatusBadRequest, "invalid_grant")
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource, strings.Repeat("a", 43)), http.StatusBadRequest, "invalid_grant")

	success := exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource, testVerifier)
	if success.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%q", success.Code, success.Body.String())
	}
	requireTokenResponseHeaders(t, success)
	requireTokenError(t, exchangeCIMD(svc, testMetadataClientID, code, cimdRedirect, testResource, testVerifier), http.StatusBadRequest, "invalid_grant")
}

var _ ClientMetadataFetcher = (*stubMetadataFetcher)(nil)
