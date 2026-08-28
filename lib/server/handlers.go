package server

import (
	"net/http"
)

const (
	// https://vimeo.com/173610242
	// https://pkg.go.dev/expvar
	// https://www.reddit.com/r/golang/comments/x5im1n/how_do_you_write_service_health_checking_api/
	// https://github.com/mozilla-services/Dockerflow?tab=readme-ov-file#containerized-app-requirements
	LbHeartbeat = "/__lbheartbeat__"
)

func HeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	// w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(http.StatusText(http.StatusOK)))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}
