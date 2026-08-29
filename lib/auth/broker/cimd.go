package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	maxClientMetadataBody         = 5 * 1024
	maxClientNameRunes            = 256
	maxClientMetadataRedirectURIs = 32
	clientMetadataFetchTimeout    = 5 * time.Second
)

// ClientResolver resolves OAuth clients that are not statically registered.
type ClientResolver interface {
	ResolveClient(context.Context, string) (Client, error)
}

// ClientMetadataFetcher retrieves one admitted Client ID Metadata Document.
type ClientMetadataFetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

// CIMDResolver resolves only explicitly admitted Client ID Metadata Documents.
type CIMDResolver struct {
	admitted map[string]struct{}
	fetcher  ClientMetadataFetcher
}

func NewCIMDResolver(clientIDs []string, fetcher ClientMetadataFetcher) (*CIMDResolver, error) {
	admitted := make(map[string]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		if err := ValidateClientMetadataURL(clientID); err != nil {
			return nil, err
		}
		if _, exists := admitted[clientID]; exists {
			return nil, fmt.Errorf("duplicate Client ID Metadata Document URL")
		}
		admitted[clientID] = struct{}{}
	}
	if fetcher == nil {
		fetcher = newSafeClientMetadataFetcher()
	}
	return &CIMDResolver{admitted: admitted, fetcher: fetcher}, nil
}

func (r *CIMDResolver) ResolveClient(ctx context.Context, clientID string) (Client, error) {
	if _, ok := r.admitted[clientID]; !ok {
		return Client{}, errors.New("client is not admitted")
	}
	body, err := r.fetcher.Fetch(ctx, clientID)
	if err != nil {
		return Client{}, errors.New("client metadata fetch failed")
	}
	return parseClientMetadata(body, clientID)
}

// ValidateClientMetadataURL validates a CIMD identifier without rewriting it.
func ValidateClientMetadataURL(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return errors.New("Client ID Metadata Document URL must be exact and nonempty")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("Client ID Metadata Document URL must be HTTPS with a host and without user information, query, or fragment")
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" || escapedPath == "/" {
		return errors.New("Client ID Metadata Document URL must have a non-root path")
	}
	for _, escapedSegment := range strings.Split(escapedPath, "/") {
		segment, err := url.PathUnescape(escapedSegment)
		if err != nil || segment == "." || segment == ".." {
			return errors.New("Client ID Metadata Document URL must not contain dot path segments")
		}
	}
	return nil
}

type clientMetadata struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	ResponseTypes           []string `json:"response_types"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func parseClientMetadata(body []byte, requestedClientID string) (Client, error) {
	var metadata clientMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return Client{}, errors.New("invalid client metadata JSON")
	}
	if metadata.ClientID == "" || metadata.ClientID != requestedClientID {
		return Client{}, errors.New("client metadata client_id mismatch")
	}
	if strings.TrimSpace(metadata.ClientName) == "" || len([]rune(metadata.ClientName)) > maxClientNameRunes {
		return Client{}, errors.New("invalid client_name")
	}
	for _, r := range metadata.ClientName {
		if unicode.IsControl(r) {
			return Client{}, errors.New("invalid client_name")
		}
	}
	if len(metadata.RedirectURIs) == 0 || len(metadata.RedirectURIs) > maxClientMetadataRedirectURIs {
		return Client{}, errors.New("invalid redirect_uris")
	}
	seenRedirects := make(map[string]struct{}, len(metadata.RedirectURIs))
	for _, redirectURI := range metadata.RedirectURIs {
		if err := validateMetadataRedirectURI(redirectURI); err != nil {
			return Client{}, errors.New("invalid redirect_uris")
		}
		if _, exists := seenRedirects[redirectURI]; exists {
			return Client{}, errors.New("duplicate redirect_uri")
		}
		seenRedirects[redirectURI] = struct{}{}
	}
	if metadata.TokenEndpointAuthMethod != string(TokenEndpointAuthMethodNone) {
		return Client{}, errors.New("unsupported token endpoint authentication method")
	}
	if len(metadata.ResponseTypes) > 0 && !containsExact(metadata.ResponseTypes, "code") {
		return Client{}, errors.New("incompatible response_types")
	}
	if len(metadata.GrantTypes) > 0 && !containsExact(metadata.GrantTypes, "authorization_code") {
		return Client{}, errors.New("incompatible grant_types")
	}
	return Client{ID: requestedClientID, RedirectURIs: append([]string(nil), metadata.RedirectURIs...), TokenEndpointAuthMethod: TokenEndpointAuthMethodNone, ExactRedirectURIs: true}, nil
}

func validateMetadataRedirectURI(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return errors.New("empty redirect URI")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme == "" || parsed.Fragment != "" || parsed.User != nil {
		return errors.New("invalid redirect URI")
	}
	if parsed.Hostname() == "" || strings.HasSuffix(parsed.Host, ":") {
		return errors.New("invalid redirect URI host")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		hostname := parsed.Hostname()
		if hostname == "localhost" {
			return nil
		}
		if address, err := netip.ParseAddr(hostname); err == nil && address.Unmap().IsLoopback() {
			return nil
		}
	}
	return errors.New("redirect URI must use HTTPS or local HTTP")
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type clientMetadataHTTPFetcher struct {
	client *http.Client
}

func newSafeClientMetadataFetcher() ClientMetadataFetcher {
	dialer := &validatedDialer{resolver: net.DefaultResolver, dialer: &net.Dialer{Timeout: clientMetadataFetchTimeout}}
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: dialer.DialContext,
	}
	return &clientMetadataHTTPFetcher{client: &http.Client{
		Transport: transport,
		Timeout:   clientMetadataFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("client metadata redirects are disabled")
		},
	}}
}

func (f *clientMetadataHTTPFetcher) Fetch(ctx context.Context, clientID string) ([]byte, error) {
	if err := ValidateClientMetadataURL(clientID); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, errors.New("create metadata request")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, errors.New("fetch client metadata")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("client metadata response is not successful")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !isJSONMediaType(mediaType) {
		return nil, errors.New("client metadata response is not JSON")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientMetadataBody+1))
	if err != nil {
		return nil, errors.New("read client metadata")
	}
	if len(body) > maxClientMetadataBody {
		return nil, errors.New("client metadata response is too large")
	}
	return body, nil
}

func isJSONMediaType(mediaType string) bool {
	if mediaType == "application/json" {
		return true
	}
	typeParts := strings.SplitN(mediaType, "/", 2)
	return len(typeParts) == 2 && typeParts[0] == "application" && len(typeParts[1]) > len("+json") && strings.HasSuffix(typeParts[1], "+json")
}

type netIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type validatedDialer struct {
	resolver netIPResolver
	dialer   contextDialer
}

func (d *validatedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid metadata destination")
	}
	addresses, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	return d.dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}

func (d *validatedDialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		if !isPublicClientMetadataIP(literal.Unmap()) {
			return nil, errors.New("client metadata destination is not public")
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("resolve client metadata destination")
	}
	for _, address := range addresses {
		if !isPublicClientMetadataIP(address.Unmap()) {
			return nil, errors.New("client metadata destination is not public")
		}
	}
	return addresses, nil
}

var nonPublicClientMetadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func isPublicClientMetadataIP(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range nonPublicClientMetadataPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
