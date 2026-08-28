package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServerServesAndStops(t *testing.T) {
	svc, err := New(context.Background(), "test", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	svc.Mux().HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "tissues")
	})

	done := make(chan error, 1)
	go func() { done <- svc.StartAndWait() }()

	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + svc.ListenerAddr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "tissues" {
		t.Fatalf("body = %q, want %q", got, "tissues")
	}

	if err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop")
	}
}

func TestServerReportsListenerConflict(t *testing.T) {
	first, err := New(context.Background(), "first", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer first.ln.Close()

	port := first.ListenerAddr().(*net.TCPAddr).Port
	cfg := testConfig()
	cfg.Port = port
	if _, err := New(context.Background(), "second", cfg); err == nil {
		t.Fatal("New() error = nil, want address-in-use error")
	}
}

func testConfig() Config {
	return Config{Host: "127.0.0.1", Port: 0, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 1024}
}
