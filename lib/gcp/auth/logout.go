package gcp

import "net/http"

func LogoutHandler(loginPath string, insecureCookie bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   !insecureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
	}
}
