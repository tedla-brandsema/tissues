package main

import (
	"bytes"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"
)

func TestRunRejectsBadCommands(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":      {},
		"unknown command": {"frobnicate"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(args, &out); err == nil {
				t.Fatal("run returned nil, want a non-zero outcome")
			}
			if !strings.Contains(out.String(), "tissues serve") {
				t.Errorf("usage was not printed:\n%s", out.String())
			}
		})
	}
}

// The loopback default and remote-sync default are both load-bearing: v0 has
// no authentication, and remote synchronization is the normal mode.
func TestServeFlagDefaults(t *testing.T) {
	cfg, err := parseServe(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q, want 127.0.0.1:8080 (loopback)", cfg.addr)
	}
	if !strings.HasPrefix(cfg.addr, "127.0.0.1:") {
		t.Errorf("default addr %q is not loopback-only", cfg.addr)
	}
	if !cfg.remoteSync {
		t.Error("default remote-sync = false, want true")
	}
	if cfg.repo != "." {
		t.Errorf("default repo = %q, want .", cfg.repo)
	}
}

func TestServeFlagOverrides(t *testing.T) {
	cfg, err := parseServe([]string{"-repo", "/tmp/x", "-addr", "127.0.0.1:9999", "-remote-sync=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.repo != "/tmp/x" || cfg.addr != "127.0.0.1:9999" || cfg.remoteSync {
		t.Errorf("parsed config = %+v", cfg)
	}

	var out bytes.Buffer
	if _, err := parseServe([]string{"-nope"}, &out); err == nil {
		t.Error("an unknown flag should fail")
	}
	if _, err := parseServe([]string{"extra"}, &out); err == nil {
		t.Error("an unexpected positional argument should fail")
	}
}

// serve must fail before it ever listens if the repository is unusable.
func TestServeRejectsInvalidRepositoryBeforeListening(t *testing.T) {
	// Port 0 would succeed at binding, so if serve listened at all this would
	// block rather than return.
	cfg := serveConfig{repo: t.TempDir(), addr: "127.0.0.1:0", remoteSync: false}
	err := serve(cfg, &bytes.Buffer{})
	if err == nil {
		t.Fatal("serve accepted a directory that is not a Git repository")
	}
	if !strings.Contains(err.Error(), "not a Git repository") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// An empty but valid repository must start: no issues/ directory is normal.
func TestServeAcceptsEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "tissues"},
		{"config", "user.email", "tissues@example"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Hold a real listener on an ephemeral port and hand serve the same
	// address. serve then gets past startup validation and fails at
	// ListenAndServe with address-already-in-use — deterministic, and with no
	// dependence on privileged ports or a fixed port number.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	cfg := serveConfig{repo: dir, addr: held.Addr().String(), remoteSync: false}
	err = serve(cfg, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a listen failure on an address that is already bound")
	}
	if strings.Contains(err.Error(), "not a Git repository") || strings.Contains(err.Error(), "repository unusable") {
		t.Fatalf("startup validation rejected a valid empty repository: %v", err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "listen" {
		t.Errorf("error = %v, want a listen failure (startup validation should have passed)", err)
	}
}
