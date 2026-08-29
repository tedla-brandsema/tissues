package broker

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ErrInvalidAccessToken identifies tokens that fail signature or claim validation.
var ErrInvalidAccessToken = errors.New("invalid access token")

// VerifiedAccessToken contains only validated broker access-token claims.
type VerifiedAccessToken struct {
	Subject   string
	Email     string
	ClientID  string
	Issuer    string
	Resource  string
	Scopes    []string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// VerifyAccessToken verifies a signed broker token and its expected issuer and resource.
// Expiration is parsed but deliberately not compared with the current time.
func (s *Service) VerifyAccessToken(token, expectedIssuer, expectedResource string) (VerifiedAccessToken, error) {
	var claims accessToken
	if err := decodeSigned(token, s.cfg.SigningSecret, &claims); err != nil {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	if !validClaimValue(claims.Subject) || !validClaimValue(claims.ClientID) {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	if _, ok := s.cfg.Clients[claims.ClientID]; !ok {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	if expectedIssuer != "" && claims.Issuer != expectedIssuer {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	if expectedResource != "" && claims.Resource != expectedResource {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	if claims.IssuedAt <= 0 || claims.ExpiresAt <= 0 || claims.ExpiresAt <= claims.IssuedAt {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	scopes, err := canonicalScopes(claims.Scope, s.cfg.Scopes, s.cfg.ScopeImplications)
	if err != nil || strings.Join(scopes, " ") != claims.Scope {
		return VerifiedAccessToken{}, ErrInvalidAccessToken
	}
	return VerifiedAccessToken{
		Subject: claims.Subject, Email: claims.Email, ClientID: claims.ClientID,
		Issuer: claims.Issuer, Resource: claims.Resource, Scopes: scopes,
		IssuedAt: time.Unix(claims.IssuedAt, 0), ExpiresAt: time.Unix(claims.ExpiresAt, 0),
	}, nil
}

func validClaimValue(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func canonicalScopes(raw string, supported []string, implications map[string][]string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	allowed := make(map[string]int, len(supported))
	for i, scope := range supported {
		allowed[scope] = i
	}
	wanted := make(map[string]bool, len(supported))
	for _, scope := range strings.Fields(raw) {
		if _, ok := allowed[scope]; !ok {
			return nil, fmt.Errorf("unsupported scope")
		}
		wanted[scope] = true
	}
	for scope := range wanted {
		for _, implied := range implications[scope] {
			if _, ok := allowed[implied]; !ok {
				return nil, fmt.Errorf("unsupported implied scope")
			}
			wanted[implied] = true
		}
	}
	out := make([]string, 0, len(wanted))
	for _, scope := range supported {
		if wanted[scope] {
			out = append(out, scope)
		}
	}
	return out, nil
}
