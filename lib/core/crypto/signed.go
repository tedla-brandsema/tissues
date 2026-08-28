package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

var ErrInvalidSignedPayload = errors.New("invalid signed payload")

// EncodeSigned serializes payload as JSON and appends an HMAC-SHA256 signature.
func EncodeSigned(payload any, secret []byte) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := sign(raw, secret)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// DecodeSigned validates signature and decodes payload into dst.
func DecodeSigned(token string, secret []byte, dst any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return ErrInvalidSignedPayload
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrInvalidSignedPayload
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrInvalidSignedPayload
	}
	if !hmac.Equal(sig, sign(raw, secret)) {
		return ErrInvalidSignedPayload
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return ErrInvalidSignedPayload
	}
	return nil
}

func sign(data, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
