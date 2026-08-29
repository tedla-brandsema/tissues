package broker

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
)

func authorizationPKCE(query url.Values, required bool) (string, string, error) {
	challenges, hasChallenge := query["code_challenge"]
	methods, hasMethod := query["code_challenge_method"]
	if !required {
		if hasChallenge || hasMethod {
			return "", "", errors.New("PKCE is not enabled for this client")
		}
		return "", "", nil
	}
	if !hasChallenge || len(challenges) != 1 || !validPKCEValue(challenges[0]) {
		return "", "", errors.New("invalid code challenge")
	}
	if !hasMethod || len(methods) != 1 || methods[0] != "S256" {
		return "", "", errors.New("invalid code challenge method")
	}
	return challenges[0], methods[0], nil
}

func validPKCEValue(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, c := range []byte(value) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			continue
		}
		return false
	}
	return true
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func matchesPKCE(challenge, method, verifier string) bool {
	if challenge == "" || method == "" {
		return challenge == "" && method == "" && verifier == ""
	}
	if method != "S256" || !validPKCEValue(challenge) || !validPKCEValue(verifier) {
		return false
	}
	computed := s256Challenge(verifier)
	return subtle.ConstantTimeCompare([]byte(challenge), []byte(computed)) == 1
}
