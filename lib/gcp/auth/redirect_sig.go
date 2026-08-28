package gcp

import (
	"crypto"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	corecrypto "github.com/tedla-brandsema/tissues/lib/core/crypto"
)

const (
	nextParamName    = "next"
	nextExpParamName = "next_exp"
	nextSigParamName = "next_sig"
)

var errInvalidSignedRedirect = errors.New("invalid signed redirect")

func withSignedNext(loginPath, next string, secret []byte, now time.Time) (string, error) {
	if strings.TrimSpace(loginPath) == "" {
		loginPath = "/"
	}
	next = strings.TrimSpace(next)
	if !isSafeRedirect(next) {
		return "", errInvalidSignedRedirect
	}
	if now.IsZero() {
		now = time.Now()
	}
	exp := now.Add(5 * time.Minute).Unix()
	sig, err := signNext(next, exp, secret)
	if err != nil {
		return "", err
	}

	u, err := url.Parse(loginPath)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(nextParamName, next)
	q.Set(nextExpParamName, strconv.FormatInt(exp, 10))
	q.Set(nextSigParamName, sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validateSignedNext(next, expRaw, sig string, secret []byte, now time.Time) error {
	next = strings.TrimSpace(next)
	sig = strings.TrimSpace(sig)
	if next == "" || sig == "" || !isSafeRedirect(next) {
		return errInvalidSignedRedirect
	}
	exp, err := strconv.ParseInt(strings.TrimSpace(expRaw), 10, 64)
	if err != nil {
		return errInvalidSignedRedirect
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Unix() > exp {
		return errInvalidSignedRedirect
	}
	expectedSig, err := signNext(next, exp, secret)
	if err != nil {
		return err
	}
	if sig != expectedSig {
		return errInvalidSignedRedirect
	}
	return nil
}

func signNext(next string, exp int64, secret []byte) (string, error) {
	payload := next + "\n" + strconv.FormatInt(exp, 10)
	mac, err := corecrypto.Hmac([]byte(payload), crypto.SHA256, secret)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(mac), nil
}
