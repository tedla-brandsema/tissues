package auth

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

const authFrontendCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type frontendHandler struct {
	basePath string
	files    fs.FS
	index    []byte
}

func newFrontendHandler(source fs.FS, basePath string) (http.Handler, error) {
	files, err := fs.Sub(source, "frontend/generated")
	if err != nil {
		return nil, fmt.Errorf("open auth frontend: %w", err)
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read auth frontend index: %w", err)
	}
	return &frontendHandler{basePath: strings.TrimRight(basePath, "/"), files: files, index: index}, nil
}

func (h *frontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", authFrontendCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == h.basePath || r.URL.Path == h.basePath+"/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(string(h.index)))
		return
	}
	if !strings.HasPrefix(r.URL.Path, h.basePath+"/") {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, h.basePath+"/")
	info, err := fs.Stat(h.files, name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, h.files, name)
}
