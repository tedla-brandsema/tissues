package gcp

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func loginHandler(v *Verifier, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.FormValue("email"))
		password := r.FormValue("password")
		if email == "" || password == "" {
			slog.Warn("gcp auth login rejected: missing credentials", "has_email", email != "", "has_password", password != "")
			http.Error(w, "email and password are required", http.StatusBadRequest)
			return
		}
		slog.Info(
			"gcp auth login attempt",
			"email", email,
			"configured_tenant_id", cfg.TenantID,
		)

		result, err := v.SignInWithEmailPassword(r.Context(), email, password, cfg.TenantID)
		if err != nil {
			slog.Warn("gcp auth sign-in failed", "email", email, "error", err)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		slog.Info(
			"gcp auth sign-in success",
			"subject", result.Subject,
			"email", result.Email,
			"token_tenant_id", result.TenantID,
			"id_token_len", len(result.IDToken),
			"id_token_ttl_seconds", int64(result.ExpiresIn/time.Second),
		)
		if cfg.TenantID != "" && result.TenantID != cfg.TenantID {
			slog.Warn(
				"gcp auth token tenant mismatch",
				"expected_tenant_id", cfg.TenantID,
				"token_tenant_id", result.TenantID,
			)
			http.Error(w, "token tenant mismatch", http.StatusUnauthorized)
			return
		}

		val, exp, err := mintAuthCookie(result.Identity, 24*time.Hour, cfg.Secret)
		if err != nil {
			slog.Error("gcp auth cookie mint failed", "error", err)
			http.Error(w, "auth error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    val,
			Path:     "/",
			Expires:  exp,
			HttpOnly: true,
			Secure:   !cfg.InsecureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		slog.Info(
			"gcp auth login success",
			"subject", result.Subject,
			"email", result.Email,
			"tenant_id", result.TenantID,
			"cookie_expires_unix", exp.Unix(),
			"cookie_len", len(val),
		)

		redirectTo := resolveLoginRedirect(r, cfg.LoginRedirect, cfg.Secret)
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	}
}

func resolveLoginRedirect(r *http.Request, fallback string, secret []byte) string {
	next := strings.TrimSpace(r.FormValue(nextParamName))
	if next != "" {
		exp := strings.TrimSpace(r.FormValue(nextExpParamName))
		sig := strings.TrimSpace(r.FormValue(nextSigParamName))
		if err := validateSignedNext(next, exp, sig, secret, time.Now()); err == nil {
			return next
		}
		slog.Warn(
			"gcp auth rejected signed redirect target",
			"next", next,
			"has_exp", exp != "",
			"has_sig", sig != "",
		)
	}
	if fallback == "" {
		return "/"
	}
	return fallback
}

func isSafeRedirect(target string) bool {
	if !strings.HasPrefix(target, "/") {
		return false
	}
	if strings.HasPrefix(target, "//") {
		return false
	}
	return true
}
