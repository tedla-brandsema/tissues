package broker

import (
	"crypto/rand"
	"encoding/base64"
	"errors"

	corecrypto "github.com/tedla-brandsema/tissues/lib/core/crypto"
)

var (
	errInvalidToken = errors.New("invalid token")
)

func encodeSigned(payload any, secret []byte) (string, error) {
	return corecrypto.EncodeSigned(payload, secret)
}

func decodeSigned(token string, secret []byte, dst any) error {
	if err := corecrypto.DecodeSigned(token, secret, dst); err != nil {
		return errInvalidToken
	}
	return nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
