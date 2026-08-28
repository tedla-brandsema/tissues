package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const shutdownTimeout = 10 * time.Second

// Server is the process boundary. It owns one HTTP listener and its
// graceful-shutdown lifecycle and hosts in-process Services through Handler.
type Server struct {
	*http.Server
	Name    string
	ln      net.Listener
	started time.Time
}

// New binds the configured listener so startup failures are reported to the
// caller before the Server enters its blocking serve loop.
func New(ctx context.Context, name string, cfg Config) (*Server, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("server name is required")
	}
	if err := cfg.ValidateConfig(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	address := cfg.Address()
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", address, err)
	}

	srv := &http.Server{
		Addr:              ln.Addr().String(),
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	return &Server{Server: srv, Name: name, ln: ln}, nil
}

// ListenerAddr is the actual bound address. This is useful when Addr used an
// ephemeral port in tests.
func (s *Server) ListenerAddr() net.Addr {
	return s.ln.Addr()
}

// Mux returns the configured ServeMux, creating one when necessary.
func (s *Server) Mux() *http.ServeMux {
	if mux, ok := s.Handler.(*http.ServeMux); ok {
		return mux
	}
	mux := http.NewServeMux()
	s.SetMux(mux)
	return mux
}

// SetMux installs mux as the HTTP handler.
func (s *Server) SetMux(mux *http.ServeMux) {
	s.Handler = mux
}

func (s *Server) String() string {
	return fmt.Sprintf("%s@%s", s.Name, s.ListenerAddr())
}

// StartAndWait serves until shutdown or a listener error. SIGINT and SIGTERM
// trigger graceful shutdown.
func (s *Server) StartAndWait() error {
	s.Mux().HandleFunc("GET "+LbHeartbeat, HeartbeatHandler)

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	serveDone := make(chan struct{})
	go func() {
		select {
		case <-signalCtx.Done():
			slog.Info("received shutdown signal", "server", s.Name)
			if err := s.Stop(); err != nil {
				slog.Error("graceful shutdown failed", "server", s.Name, "error", err)
			}
		case <-serveDone:
		}
	}()

	s.started = time.Now()
	slog.Info("starting server", "server", s.Name, "address", s.ListenerAddr().String())
	err := s.Serve(s.ln)
	close(serveDone)
	stopSignals()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve %s: %w", s.Name, err)
	}
	return nil
}

// Stop gracefully shuts down the Server.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down %s: %w", s.Name, err)
	}
	slog.Info("server stopped", "server", s.Name)
	return nil
}
