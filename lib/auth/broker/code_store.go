package broker

import (
	"context"
	"errors"
)

var (
	ErrCodeNotFound = errors.New("authorization code not found")
	ErrCodeExpired  = errors.New("authorization code expired")
	ErrCodeMismatch = errors.New("authorization code mismatch")
)

type CodeStore interface {
	SaveCode(ctx context.Context, code string, val authCode) error
	ConsumeCode(ctx context.Context, code, clientID, redirectURI, resource, codeVerifier string) (authCode, error)
}
