package gcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMiddlewareRedirectIncludesSignedNextParams(t *testing.T) {
	h := Middleware([]byte("secret"), "/auth/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/authorize?client_id=tissues&state=abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/auth/login?") {
		t.Fatalf("expected redirect to login with query, got %q", loc)
	}

	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	q := u.Query()
	next := q.Get(nextParamName)
	exp := q.Get(nextExpParamName)
	sig := q.Get(nextSigParamName)
	if next == "" || exp == "" || sig == "" {
		t.Fatalf("expected next/exp/sig query params, got next=%q exp=%q sig=%q", next, exp, sig)
	}
	if err := validateSignedNext(next, exp, sig, []byte("secret"), time.Now()); err != nil {
		t.Fatalf("validateSignedNext() error = %v", err)
	}
}

func TestResolveLoginRedirectRejectsTamperedNext(t *testing.T) {
	secret := []byte("secret")
	signedURL, err := withSignedNext("/auth/login", "/authorize?client_id=tissues", secret, time.Now())
	if err != nil {
		t.Fatalf("withSignedNext() error = %v", err)
	}
	u, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse signedURL: %v", err)
	}
	q := u.Query()
	q.Set(nextParamName, "/authorize?client_id=evil")

	form := url.Values{}
	form.Set(nextParamName, q.Get(nextParamName))
	form.Set(nextExpParamName, q.Get(nextExpParamName))
	form.Set(nextSigParamName, q.Get(nextSigParamName))

	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	got := resolveLoginRedirect(req, "/authorize", secret)
	if got != "/authorize" {
		t.Fatalf("resolveLoginRedirect() = %q, want fallback", got)
	}
}
