package gcp

import (
	"errors"
	"time"

	corecrypto "github.com/tedla-brandsema/tissues/lib/core/crypto"
)

var (
	errInvalidCookie = errors.New("invalid auth cookie")
	errExpiredCookie = errors.New("expired auth cookie")
)

const sessionCookieName = "tissues_auth"

type sessionPayload struct {
	Subject  string `json:"sub"`
	Email    string `json:"email,omitempty"`
	TenantID string `json:"tid,omitempty"`
	Expires  int64  `json:"exp"`
}

func encodeCookie(p sessionPayload, secret []byte) (string, error) {
	return corecrypto.EncodeSigned(p, secret)
}

func decodeCookie(val string, secret []byte) (sessionPayload, error) {
	var p sessionPayload
	if err := corecrypto.DecodeSigned(val, secret, &p); err != nil {
		return p, errInvalidCookie
	}
	if time.Now().Unix() > p.Expires {
		return p, errExpiredCookie
	}
	return p, nil
}

func mintAuthCookie(identity Identity, ttl time.Duration, secret []byte) (string, time.Time, error) {
	exp := time.Now().Add(ttl)
	val, err := encodeCookie(sessionPayload{
		Subject:  identity.Subject,
		Email:    identity.Email,
		TenantID: identity.TenantID,
		Expires:  exp.Unix(),
	}, secret)
	return val, exp, err
}
