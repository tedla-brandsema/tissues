package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	identitytoolkit "google.golang.org/api/identitytoolkit/v1"
	"google.golang.org/api/option"
)

type Identity struct {
	Subject  string
	Email    string
	TenantID string
}

type Verifier struct {
	apiKey        string
	httpClient    *http.Client
	useHTTPClient bool
}

func NewVerifier(apiKey string, httpClient *http.Client) *Verifier {
	useHTTPClient := httpClient != nil
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Verifier{
		apiKey:        strings.TrimSpace(apiKey),
		httpClient:    httpClient,
		useHTTPClient: useHTTPClient,
	}
}

type SignInResult struct {
	Identity
	IDToken    string
	RefreshTok string
	ExpiresIn  time.Duration
}

func (v *Verifier) SignInWithEmailPassword(ctx context.Context, email, password, tenantID string) (SignInResult, error) {
	if v == nil || v.apiKey == "" {
		return SignInResult{}, fmt.Errorf("identity platform api key is required")
	}
	if strings.TrimSpace(email) == "" {
		return SignInResult{}, fmt.Errorf("email is required")
	}
	if password == "" {
		return SignInResult{}, fmt.Errorf("password is required")
	}

	opts := []option.ClientOption{option.WithAPIKey(v.apiKey)}
	// WithHTTPClient overrides auth options; only use it when explicitly provided
	// by caller, so API key remains active for default behavior.
	if v.useHTTPClient && v.httpClient != nil {
		opts = append(opts, option.WithHTTPClient(v.httpClient))
	}
	svc, err := identitytoolkit.NewService(ctx, opts...)
	if err != nil {
		return SignInResult{}, err
	}
	req := &identitytoolkit.GoogleCloudIdentitytoolkitV1SignInWithPasswordRequest{
		Email:             strings.TrimSpace(email),
		Password:          password,
		ReturnSecureToken: true,
		TenantId:          strings.TrimSpace(tenantID),
	}
	out, err := svc.Accounts.SignInWithPassword(req).Context(ctx).Do()
	if err != nil {
		return SignInResult{}, fmt.Errorf("identity platform signInWithPassword failed: %w", err)
	}
	if strings.TrimSpace(out.LocalId) == "" {
		return SignInResult{}, fmt.Errorf("identity platform signInWithPassword returned empty subject")
	}
	if strings.TrimSpace(out.IdToken) == "" {
		return SignInResult{}, fmt.Errorf("identity platform signInWithPassword returned empty id token")
	}
	if out.ExpiresIn < 0 {
		return SignInResult{}, fmt.Errorf("identity platform signInWithPassword returned invalid expiresIn")
	}

	return SignInResult{
		Identity: Identity{
			Subject:  out.LocalId,
			Email:    out.Email,
			TenantID: strings.TrimSpace(tenantID),
		},
		IDToken:    out.IdToken,
		RefreshTok: out.RefreshToken,
		ExpiresIn:  time.Duration(out.ExpiresIn) * time.Second,
	}, nil
}
