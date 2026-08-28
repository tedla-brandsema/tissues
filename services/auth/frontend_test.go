package auth

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratedFrontendAndAssets(t *testing.T) {
	handler, err := newFrontendHandler(frontendFiles, "/auth/login")
	if err != nil {
		t.Fatal(err)
	}

	page := getFrontend(t, handler, "/auth/login")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("login page = %d %q", page.Code, page.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": authFrontendCSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
	} {
		if got := page.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	for _, forbidden := range []string{"@fluentui/web-components", "esm.sh", "importmap", "tissues-auth-login"} {
		if strings.Contains(page.Body.String(), forbidden) {
			t.Fatalf("generated page contains %q", forbidden)
		}
	}

	assets := regexp.MustCompile(`(?:src|href)="(/auth/login/(?:assets/[^"]+|favicon\.svg))"`).FindAllStringSubmatch(page.Body.String(), -1)
	if len(assets) < 3 {
		t.Fatalf("generated asset references = %v", assets)
	}
	for _, match := range assets {
		asset := getFrontend(t, handler, match[1])
		if asset.Code != http.StatusOK || asset.Body.Len() == 0 {
			t.Fatalf("GET %s = %d, %d bytes", match[1], asset.Code, asset.Body.Len())
		}
		for _, forbidden := range []string{"@fluentui/web-components", "esm.sh", "tissues-auth-login"} {
			if strings.Contains(asset.Body.String(), forbidden) {
				t.Fatalf("%s contains %q", match[1], forbidden)
			}
		}
	}
}

func getFrontend(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}
