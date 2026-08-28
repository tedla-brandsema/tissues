package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/server"
	"github.com/tedla-brandsema/tissues/lib/service"
	authservice "github.com/tedla-brandsema/tissues/services/auth"
	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestComposeCanRunWithServicesInactive(t *testing.T) {
	cfg := appConfig{Server: server.Config{Host: "127.0.0.1", Port: 0, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 1024}}
	profile := coreconfig.Profile[appConfig]{Name: "test", Config: cfg}
	srv, closers, err := compose(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if len(closers) != 0 {
		t.Fatalf("closers=%d,want 0", len(closers))
	}
}

func TestConcreteServicesShareSDK(t *testing.T) {
	active := []service.Service{(*authservice.Service)(nil), (*tissues.Service)(nil)}
	if len(active) != 2 {
		t.Fatalf("active services = %d, want 2", len(active))
	}
}

type routeService struct {
	name string
	err  error
}

func (s routeService) Name() string                        { return s.name }
func (s routeService) RegisterRoutes(*http.ServeMux) error { return s.err }

func TestRegisterServicesUsesUniformContractAndNamesErrors(t *testing.T) {
	want := errors.New("broken")
	err := registerServices(http.NewServeMux(), []service.Service{routeService{name: "first"}, routeService{name: "second", err: want}})
	if !errors.Is(err, want) || err.Error() != "register second routes: broken" {
		t.Fatalf("error = %v", err)
	}
}
