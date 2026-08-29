package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const (
	appSessionCookieName = "tissues_app_session"
	stateCookieName      = "tissues_auth_state"
)

type RPConfig struct {
	BrokerURL    string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	LoginPath    string
	Secret       []byte
	// InsecureCookie must be explicitly enabled for local HTTP development.
	// The relying-party cookies are secure by default.
	InsecureCookie bool
	HTTPClient     *http.Client
}

type RP struct {
	cfg RPConfig
}

type rpSession struct {
	Subject string `json:"sub"`
	Email   string `json:"email,omitempty"`
	Expires int64  `json:"exp"`
}

type rpState struct {
	Nonce   string `json:"nonce"`
	Next    string `json:"next"`
	Expires int64  `json:"exp"`
}

func NewRP(cfg RPConfig) *RP {
	cfg.BrokerURL = strings.TrimRight(strings.TrimSpace(cfg.BrokerURL), "/")
	if cfg.LoginPath == "" {
		cfg.LoginPath = "/auth/login"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &RP{cfg: cfg}
}

func (rp *RP) Middleware(next http.Handler) http.Handler {
	return rp.SessionMiddleware(next, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, withNext(rp.cfg.LoginPath, r), http.StatusSeeOther)
	}))
}

// SessionMiddleware validates the relying-party session, adds its trusted
// identity to the request context, and delegates missing or expired sessions
// to unauthenticated. This lets browser pages redirect while JSON APIs return
// an appropriate non-HTML response.
func (rp *RP) SessionMiddleware(next, unauthenticated http.Handler) http.Handler {
	if unauthenticated == nil {
		unauthenticated = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := rp.readSession(r)
		if err != nil {
			unauthenticated.ServeHTTP(w, r)
			return
		}
		ctx := gcpauth.WithSubject(r.Context(), sess.Subject)
		if sess.Email != "" {
			ctx = gcpauth.WithEmail(ctx, sess.Email)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (rp *RP) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target, ok := rp.canonicalLoginTarget(r); ok {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}

		next := strings.TrimSpace(r.URL.Query().Get("next"))
		if next == "" || !isSafeRedirect(next) {
			next = "/"
		}

		nonce, err := randomToken(24)
		if err != nil {
			http.Error(w, "failed to create auth state", http.StatusInternalServerError)
			return
		}
		statePayload := rpState{
			Nonce:   nonce,
			Next:    next,
			Expires: time.Now().Add(5 * time.Minute).Unix(),
		}
		stateRaw, err := encodeSigned(statePayload, rp.cfg.Secret)
		if err != nil {
			http.Error(w, "failed to create auth state", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    stateRaw,
			Path:     "/",
			HttpOnly: true,
			Secure:   !rp.cfg.InsecureCookie,
			SameSite: http.SameSiteLaxMode,
		})

		u, _ := url.Parse(rp.cfg.BrokerURL + "/authorize")
		q := u.Query()
		q.Set("client_id", rp.cfg.ClientID)
		q.Set("redirect_uri", rp.cfg.RedirectURI)
		q.Set("state", nonce)
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
}

func (rp *RP) canonicalLoginTarget(r *http.Request) (string, bool) {
	redirectURI, err := url.Parse(strings.TrimSpace(rp.cfg.RedirectURI))
	if err != nil || redirectURI.Host == "" || (redirectURI.Scheme != "http" && redirectURI.Scheme != "https") {
		return "", false
	}
	if strings.EqualFold(r.Host, redirectURI.Host) {
		return "", false
	}
	return (&url.URL{
		Scheme:   redirectURI.Scheme,
		Host:     redirectURI.Host,
		Path:     rp.cfg.LoginPath,
		RawQuery: r.URL.RawQuery,
	}).String(), true
}

func (rp *RP) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authErr := strings.TrimSpace(r.URL.Query().Get("error")); authErr != "" {
			rp.clearStateCookie(w)
			http.Error(w, "authorization failed: "+authErr, http.StatusForbidden)
			return
		}

		code := strings.TrimSpace(r.URL.Query().Get("code"))
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		next, err := rp.validateState(r, state)
		if err != nil {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		rp.clearStateCookie(w)

		token, err := rp.exchangeCode(r.Context(), code)
		if err != nil {
			http.Error(w, "token exchange failed", http.StatusUnauthorized)
			return
		}

		sub, email, err := rp.fetchUserinfo(r.Context(), token)
		if err != nil {
			http.Error(w, "userinfo failed", http.StatusUnauthorized)
			return
		}

		val, exp, err := rp.mintSession(sub, email, 24*time.Hour)
		if err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     appSessionCookieName,
			Value:    val,
			Path:     "/",
			Expires:  exp,
			HttpOnly: true,
			Secure:   !rp.cfg.InsecureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, next, http.StatusSeeOther)
	})
}

func (rp *RP) LogoutHandler(loginPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     appSessionCookieName,
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   !rp.cfg.InsecureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
	}
}

func (rp *RP) validateState(r *http.Request, state string) (string, error) {
	c, err := r.Cookie(stateCookieName)
	if err != nil {
		return "", err
	}
	var st rpState
	if err := decodeSigned(c.Value, rp.cfg.Secret, &st); err != nil {
		return "", err
	}
	if st.Nonce == "" || st.Nonce != state {
		return "", errors.New("state mismatch")
	}
	if time.Now().Unix() > st.Expires {
		return "", errors.New("state expired")
	}
	if st.Next == "" || !isSafeRedirect(st.Next) {
		return "/", nil
	}
	return st.Next, nil
}

func (rp *RP) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !rp.cfg.InsecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (rp *RP) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", rp.cfg.ClientID)
	form.Set("client_secret", rp.cfg.ClientSecret)
	form.Set("redirect_uri", rp.cfg.RedirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rp.cfg.BrokerURL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := rp.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("token exchange returned non-200")
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", errors.New("empty access_token")
	}
	return out.AccessToken, nil
}

func (rp *RP) fetchUserinfo(ctx context.Context, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rp.cfg.BrokerURL+"/userinfo", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := rp.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", errors.New("userinfo returned non-200")
	}

	var out struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(out.Subject) == "" {
		return "", "", errors.New("missing subject")
	}
	return out.Subject, out.Email, nil
}

func (rp *RP) mintSession(sub, email string, ttl time.Duration) (string, time.Time, error) {
	exp := time.Now().Add(ttl)
	val, err := encodeSigned(rpSession{
		Subject: sub,
		Email:   email,
		Expires: exp.Unix(),
	}, rp.cfg.Secret)
	return val, exp, err
}

func (rp *RP) readSession(r *http.Request) (rpSession, error) {
	var sess rpSession
	c, err := r.Cookie(appSessionCookieName)
	if err != nil {
		return sess, err
	}
	if err := decodeSigned(c.Value, rp.cfg.Secret, &sess); err != nil {
		return sess, err
	}
	if time.Now().Unix() > sess.Expires || strings.TrimSpace(sess.Subject) == "" {
		return sess, errors.New("invalid session")
	}
	return sess, nil
}

func withNext(loginPath string, r *http.Request) string {
	if strings.TrimSpace(loginPath) == "" {
		loginPath = "/"
	}
	u, err := url.Parse(loginPath)
	if err != nil {
		return loginPath
	}
	q := u.Query()
	q.Set("next", r.URL.RequestURI())
	u.RawQuery = q.Encode()
	return u.String()
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
