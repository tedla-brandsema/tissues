package broker

import (
	"net/url"
	"strings"
	"time"
)

func matchesRedirectURI(registered []string, candidate string, allowLoopbackPort bool) bool {
	for _, registeredURI := range registered {
		registeredURI = strings.TrimSpace(registeredURI)
		if registeredURI == candidate {
			return true
		}
		if allowLoopbackPort && matchesLoopbackRedirectURI(registeredURI, candidate) {
			return true
		}
	}
	return false
}

func matchesLoopbackRedirectURI(registered, candidate string) bool {
	registeredURL, err := url.Parse(registered)
	if err != nil {
		return false
	}
	candidateURL, err := url.Parse(candidate)
	if err != nil {
		return false
	}
	hostname := registeredURL.Hostname()
	if registeredURL.Scheme != "http" || (hostname != "127.0.0.1" && hostname != "::1") {
		return false
	}
	if candidateURL.Scheme != registeredURL.Scheme || candidateURL.Hostname() != hostname {
		return false
	}
	return registeredURL.User.String() == candidateURL.User.String() &&
		registeredURL.Path == candidateURL.Path &&
		registeredURL.RawPath == candidateURL.RawPath &&
		registeredURL.ForceQuery == candidateURL.ForceQuery &&
		registeredURL.RawQuery == candidateURL.RawQuery &&
		registeredURL.Fragment == candidateURL.Fragment
}

func validateCodeBinding(code authCode, clientID, redirectURI, resource, codeVerifier string, now time.Time) error {
	if now.After(code.ExpiresAt) {
		return ErrCodeExpired
	}
	if code.ClientID != clientID || code.RedirectURI != redirectURI || code.Resource != resource {
		return ErrCodeMismatch
	}
	if !matchesPKCE(code.CodeChallenge, code.CodeChallengeMethod, codeVerifier) {
		return ErrCodeMismatch
	}
	return nil
}
