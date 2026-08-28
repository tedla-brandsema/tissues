package gcp

import (
	"net/http"
	"time"
)

// Middleware enforces that a valid GCP auth session cookie exists.
func Middleware(secret []byte, loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				redirectToLogin(w, r, loginPath, secret)
				return
			}
			payload, err := decodeCookie(cookie.Value, secret)
			if err != nil {
				redirectToLogin(w, r, loginPath, secret)
				return
			}
			ctx := WithSubject(r.Context(), payload.Subject)
			ctx = WithEmail(ctx, payload.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func redirectToLogin(w http.ResponseWriter, r *http.Request, loginPath string, secret []byte) {
	redirectTo, err := withSignedNext(loginPath, r.URL.RequestURI(), secret, time.Now())
	if err != nil {
		redirectTo = loginPath
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}
