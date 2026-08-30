package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testMetadataClientID = "https://client.example.test/oauth/client.json"

const chatGPTMetadataClientID = "https://chatgpt.com/oauth/client.json"

type stubMetadataFetcher struct {
	body  []byte
	err   error
	calls []string
}

func (f *stubMetadataFetcher) Fetch(_ context.Context, clientID string) ([]byte, error) {
	f.calls = append(f.calls, clientID)
	return append([]byte(nil), f.body...), f.err
}

func validClientMetadata() string {
	return `{"client_id":"` + testMetadataClientID + `","client_name":"Test Client","redirect_uris":["https://client.example.test/callback"],"response_types":["code"],"grant_types":["authorization_code"],"token_endpoint_auth_method":"none"}`
}

func currentChatGPTClientMetadata() string {
	return `{"client_id":"` + chatGPTMetadataClientID + `","client_name":"ChatGPT","redirect_uris":["https://chatgpt.com/connector_platform_oauth_redirect"],"response_types":["code"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"private_key_jwt","token_endpoint_auth_methods_supported":["none","private_key_jwt"]}`
}

func TestClientMetadataURLValidation(t *testing.T) {
	valid := []string{
		testMetadataClientID,
		"https://client.example.test:8443/a/b",
		"https://EXAMPLE.test/oauth/%63lient.json",
	}
	for _, raw := range valid {
		if err := ValidateClientMetadataURL(raw); err != nil {
			t.Errorf("ValidateClientMetadataURL(%q) = %v", raw, err)
		}
	}
	invalid := []string{
		"http://client.example.test/oauth/client.json",
		"https://client.example.test",
		"https://client.example.test/",
		"https://user@client.example.test/oauth/client.json",
		"https://client.example.test/oauth/client.json#fragment",
		"https://client.example.test/oauth/client.json?version=1",
		"https://client.example.test/oauth/./client.json",
		"https://client.example.test/oauth/../client.json",
		"https://client.example.test/oauth/%2e%2e/client.json",
	}
	for _, raw := range invalid {
		if err := ValidateClientMetadataURL(raw); err == nil {
			t.Errorf("ValidateClientMetadataURL(%q) succeeded", raw)
		}
	}
}

func TestCIMDResolverAdmissionIsExactAndPreventsUnknownFetch(t *testing.T) {
	fetcher := &stubMetadataFetcher{body: []byte(validClientMetadata())}
	resolver, err := NewCIMDResolver([]string{testMetadataClientID}, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	client, err := resolver.ResolveClient(context.Background(), testMetadataClientID)
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != testMetadataClientID || !client.ExactRedirectURIs {
		t.Fatalf("client = %#v", client)
	}
	for _, lookalike := range []string{"https://CLIENT.example.test/oauth/client.json", " " + testMetadataClientID} {
		if _, err := resolver.ResolveClient(context.Background(), lookalike); err == nil {
			t.Fatalf("non-exact identifier %q was admitted", lookalike)
		}
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != testMetadataClientID {
		t.Fatalf("fetches = %v", fetcher.calls)
	}
	if _, err := NewCIMDResolver([]string{testMetadataClientID, testMetadataClientID}, fetcher); err == nil {
		t.Fatal("duplicate admission succeeded")
	}
}

func TestClientMetadataValidation(t *testing.T) {
	tests := map[string]string{
		"valid":                   validClientMetadata(),
		"harmless unknown":        strings.TrimSuffix(validClientMetadata(), "}") + `,"logo_uri":"https://ignored.example/logo.png","unknown":{"nested":true}}`,
		"missing client id":       `{"client_name":"Client","redirect_uris":["https://client.example/cb"],"token_endpoint_auth_method":"none"}`,
		"client id mismatch":      strings.Replace(validClientMetadata(), testMetadataClientID, "https://other.example/client.json", 1),
		"missing client name":     `{"client_id":"` + testMetadataClientID + `","redirect_uris":["https://client.example/cb"],"token_endpoint_auth_method":"none"}`,
		"empty client name":       strings.Replace(validClientMetadata(), "Test Client", " ", 1),
		"control client name":     strings.Replace(validClientMetadata(), "Test Client", `Test\nClient`, 1),
		"oversized client name":   strings.Replace(validClientMetadata(), "Test Client", strings.Repeat("n", maxClientNameRunes+1), 1),
		"missing redirects":       `{"client_id":"` + testMetadataClientID + `","client_name":"Client","token_endpoint_auth_method":"none"}`,
		"empty redirects":         strings.Replace(validClientMetadata(), `["https://client.example.test/callback"]`, `[]`, 1),
		"duplicate redirects":     strings.Replace(validClientMetadata(), `["https://client.example.test/callback"]`, `["https://client.example.test/callback","https://client.example.test/callback"]`, 1),
		"too many redirects":      strings.Replace(validClientMetadata(), `["https://client.example.test/callback"]`, manyRedirectURIs(maxClientMetadataRedirectURIs+1), 1),
		"invalid redirect":        strings.Replace(validClientMetadata(), "https://client.example.test/callback", "relative/callback", 1),
		"redirect with fragment":  strings.Replace(validClientMetadata(), "https://client.example.test/callback", "https://client.example.test/callback#fragment", 1),
		"missing auth method":     strings.Replace(validClientMetadata(), `,"token_endpoint_auth_method":"none"`, "", 1),
		"shared secret method":    strings.Replace(validClientMetadata(), `"none"`, `"client_secret_post"`, 1),
		"unsupported auth method": strings.Replace(validClientMetadata(), `"none"`, `"private_key_jwt"`, 1),
		"incompatible response":   strings.Replace(validClientMetadata(), `["code"]`, `["token"]`, 1),
		"incompatible grant":      strings.Replace(validClientMetadata(), `["authorization_code"]`, `["client_credentials"]`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			client, err := parseClientMetadata([]byte(body), testMetadataClientID)
			if name == "valid" || name == "harmless unknown" {
				if err != nil || client.ID != testMetadataClientID || client.TokenEndpointAuthMethod != TokenEndpointAuthMethodNone || !client.ExactRedirectURIs {
					t.Fatalf("client=%#v err=%v", client, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid metadata accepted: %#v", client)
			}
		})
	}
}

func TestCurrentChatGPTClientMetadataResolvesAsPublic(t *testing.T) {
	fetcher := &stubMetadataFetcher{body: []byte(currentChatGPTClientMetadata())}
	resolver, err := NewCIMDResolver([]string{chatGPTMetadataClientID}, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	client, err := resolver.ResolveClient(context.Background(), chatGPTMetadataClientID)
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != chatGPTMetadataClientID || client.TokenEndpointAuthMethod != TokenEndpointAuthMethodNone || client.Secret != "" || !client.ExactRedirectURIs {
		t.Fatalf("client = %#v", client)
	}
	if !reflect.DeepEqual(client.RedirectURIs, []string{"https://chatgpt.com/connector_platform_oauth_redirect"}) {
		t.Fatalf("redirect URIs = %v", client.RedirectURIs)
	}
}

func TestClientMetadataTokenEndpointAuthMethodSelection(t *testing.T) {
	base := validClientMetadata()
	tests := map[string]struct {
		body string
		want bool
	}{
		"plural none": {
			body: strings.TrimSuffix(base, "}") + `,"token_endpoint_auth_methods_supported":["none"]}`,
			want: true,
		},
		"plural preference order irrelevant": {
			body: strings.Replace(strings.TrimSuffix(base, "}")+`,"token_endpoint_auth_methods_supported":["private_key_jwt","none"]}`, `"token_endpoint_auth_method":"none"`, `"token_endpoint_auth_method":"private_key_jwt"`, 1),
			want: true,
		},
		"plural unsupported": {
			body: strings.TrimSuffix(base, "}") + `,"token_endpoint_auth_methods_supported":["private_key_jwt"]}`,
		},
		"plural empty": {
			body: strings.TrimSuffix(base, "}") + `,"token_endpoint_auth_methods_supported":[]}`,
		},
		"singular none": {
			body: base,
			want: true,
		},
		"singular unsupported": {
			body: strings.Replace(base, `"token_endpoint_auth_method":"none"`, `"token_endpoint_auth_method":"private_key_jwt"`, 1),
		},
		"singular missing": {
			body: strings.Replace(base, `,"token_endpoint_auth_method":"none"`, "", 1),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client, err := parseClientMetadata([]byte(tc.body), testMetadataClientID)
			if tc.want {
				if err != nil || client.TokenEndpointAuthMethod != TokenEndpointAuthMethodNone || client.Secret != "" {
					t.Fatalf("client=%#v err=%v", client, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("unsupported metadata accepted: %#v", client)
			}
		})
	}
}

func manyRedirectURIs(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprintf("https://client.example.test/callback/%d", i)
	}
	return `["` + strings.Join(values, `","`) + `"]`
}

func TestClientMetadataCompatibilityDefaults(t *testing.T) {
	body := `{"client_id":"` + testMetadataClientID + `","client_name":"Client","redirect_uris":["https://client.example.test/oauth/callback"],"token_endpoint_auth_method":"none"}`
	if _, err := parseClientMetadata([]byte(body), testMetadataClientID); err != nil {
		t.Fatalf("RFC 7591 code-flow defaults rejected: %v", err)
	}
}

func TestMetadataRedirectURIPolicy(t *testing.T) {
	accepted := []string{
		"https://client.example.test/callback",
		"https://client.example.test:8443/callback",
		"http://127.0.0.1:41000/callback",
		"http://[::1]:41000/callback",
		"http://localhost:41000/callback",
	}
	for _, redirectURI := range accepted {
		if err := validateMetadataRedirectURI(redirectURI); err != nil {
			t.Errorf("validateMetadataRedirectURI(%q) = %v", redirectURI, err)
		}
	}

	rejected := []string{
		"http://client.example.test/callback",
		"http://10.0.0.1/callback",
		"http://192.168.1.10/callback",
		"http://localhost.example.test/callback",
		"http://foo.localhost/callback",
		"http://localhost.evil.example/callback",
		"com.example.app:/oauth/callback",
		"ftp://client.example.test/callback",
		"file:///tmp/callback",
		"ws://client.example.test/callback",
		"relative/callback",
		"",
		"https://user@client.example.test/callback",
		"https://client.example.test/callback#fragment",
		"https://[::1",
	}
	for _, redirectURI := range rejected {
		if err := validateMetadataRedirectURI(redirectURI); err == nil {
			t.Errorf("validateMetadataRedirectURI(%q) succeeded", redirectURI)
		}
	}
}

func TestClientMetadataDestinationPolicy(t *testing.T) {
	tests := map[string]bool{
		"10.0.0.1":             false,
		"127.0.0.1":            false,
		"169.254.1.1":          false,
		"100.64.0.1":           false,
		"0.0.0.0":              false,
		"224.0.0.1":            false,
		"::1":                  false,
		"fd00::1":              false,
		"fe80::1":              false,
		"::":                   false,
		"2001:db8::1":          false,
		"8.8.8.8":              true,
		"2606:4700:4700::1111": true,
	}
	for raw, want := range tests {
		address := netip.MustParseAddr(raw)
		if got := isPublicClientMetadataIP(address); got != want {
			t.Errorf("isPublicClientMetadataIP(%s) = %t, want %t", raw, got, want)
		}
	}
}

type stubResolver struct {
	addresses []netip.Addr
	err       error
	calls     int
}

func (r *stubResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.addresses, r.err
}

type recordingDialer struct {
	address string
}

func (d *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.address = address
	return nil, errors.New("dial stopped by test")
}

func TestValidatedDialerUsesValidatedAddressWithoutSecondDNSLookup(t *testing.T) {
	resolver := &stubResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	dialer := &recordingDialer{}
	validated := &validatedDialer{resolver: resolver, dialer: dialer}
	_, err := validated.DialContext(context.Background(), "tcp", "metadata.example.test:443")
	if err == nil {
		t.Fatal("test dial unexpectedly succeeded")
	}
	if resolver.calls != 1 || dialer.address != "8.8.8.8:443" {
		t.Fatalf("resolver calls=%d dial address=%q", resolver.calls, dialer.address)
	}
}

func TestValidatedDialerRejectsAnyDisallowedResolutionAndIPLiteral(t *testing.T) {
	resolver := &stubResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}}
	dialer := &recordingDialer{}
	validated := &validatedDialer{resolver: resolver, dialer: dialer}
	if _, err := validated.DialContext(context.Background(), "tcp", "metadata.example.test:443"); err == nil || dialer.address != "" {
		t.Fatalf("mixed resolution err=%v dial=%q", err, dialer.address)
	}
	if _, err := validated.DialContext(context.Background(), "tcp", "127.0.0.1:443"); err == nil || resolver.calls != 1 {
		t.Fatalf("literal destination err=%v resolver calls=%d", err, resolver.calls)
	}
}

func TestValidatedDialerRejectsDNSFailureWithoutDialing(t *testing.T) {
	resolver := &stubResolver{err: errors.New("DNS unavailable")}
	dialer := &recordingDialer{}
	validated := &validatedDialer{resolver: resolver, dialer: dialer}
	if _, err := validated.DialContext(context.Background(), "tcp", "metadata.example.test:443"); err == nil || resolver.calls != 1 || dialer.address != "" {
		t.Fatalf("err=%v resolver calls=%d dial=%q", err, resolver.calls, dialer.address)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func metadataResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClientMetadataHTTPResponsePolicy(t *testing.T) {
	tests := map[string]struct {
		response *http.Response
		err      error
		wantOK   bool
	}{
		"application json":  {metadataResponse(http.StatusOK, "application/json; charset=utf-8", validClientMetadata()), nil, true},
		"structured json":   {metadataResponse(http.StatusOK, "application/client-metadata+json", validClientMetadata()), nil, true},
		"redirect":          {metadataResponse(http.StatusFound, "application/json", `{}`), nil, false},
		"non 2xx":           {metadataResponse(http.StatusBadGateway, "application/json", `{}`), nil, false},
		"html":              {metadataResponse(http.StatusOK, "text/html", `{}`), nil, false},
		"oversized":         {metadataResponse(http.StatusOK, "application/json", strings.Repeat("x", maxClientMetadataBody+1)), nil, false},
		"transport failure": {nil, errors.New("network detail"), false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return tc.response, tc.err }), Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
			fetcher := &clientMetadataHTTPFetcher{client: client}
			_, err := fetcher.Fetch(context.Background(), testMetadataClientID)
			if (err == nil) != tc.wantOK {
				t.Fatalf("err=%v wantOK=%t", err, tc.wantOK)
			}
		})
	}
}

func TestClientMetadataHTTPFetcherDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			response := metadataResponse(http.StatusFound, "application/json", `{}`)
			response.Header.Set("Location", "https://other.example.test/client.json")
			response.Request = req
			return response, nil
		}),
		Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect denied")
		},
	}
	_, err := (&clientMetadataHTTPFetcher{client: client}).Fetch(context.Background(), testMetadataClientID)
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestClientMetadataHTTPFetcherHasFiniteTimeout(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
		Timeout: 5 * time.Millisecond,
	}
	started := time.Now()
	_, err := (&clientMetadataHTTPFetcher{client: client}).Fetch(context.Background(), testMetadataClientID)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestProductionClientMetadataFetcherDisablesProxyAndCookies(t *testing.T) {
	fetcher, ok := newSafeClientMetadataFetcher().(*clientMetadataHTTPFetcher)
	if !ok {
		t.Fatalf("fetcher type = %T", fetcher)
	}
	transport, ok := fetcher.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || fetcher.client.Jar != nil || fetcher.client.CheckRedirect == nil || fetcher.client.Timeout != clientMetadataFetchTimeout {
		t.Fatalf("client=%#v transport=%#v", fetcher.client, transport)
	}
}

func TestCIMDResolverSanitizesFetchAndJSONFailures(t *testing.T) {
	for name, fetcher := range map[string]*stubMetadataFetcher{
		"fetch": {err: errors.New("dial tcp 10.0.0.1: secret detail")},
		"json":  {body: []byte(`not-json`)},
	} {
		t.Run(name, func(t *testing.T) {
			resolver, err := NewCIMDResolver([]string{testMetadataClientID}, fetcher)
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ResolveClient(context.Background(), testMetadataClientID)
			if err == nil || strings.Contains(err.Error(), "10.0.0.1") || strings.Contains(err.Error(), "secret detail") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
