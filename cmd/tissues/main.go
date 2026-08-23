// Command tissues serves the tissues REST API over a Git repository holding
// canonical Markdown issue data.
//
// v0 has no authentication. See the security note in README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tedla-brandsema/tissues/internal/rest"
	"github.com/tedla-brandsema/tissues/internal/service"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "tissues: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		cfg, err := parseServe(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil // help was requested and printed
		}
		if err != nil {
			return err
		}
		return serve(cfg, out)
	default:
		usage(out)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(out io.Writer) {
	fmt.Fprint(out, `tissues — a Git-backed Markdown issue tracker for humans and agents

usage:
  tissues serve [flags]

Run "tissues serve -h" for the serve flags.
`)
}

type serveConfig struct {
	repo       string
	addr       string
	remoteSync bool
}

func parseServe(args []string, out io.Writer) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	var cfg serveConfig
	fs.StringVar(&cfg.repo, "repo", ".", "Git repository holding the tissues data")
	// Loopback by default: v0 has no authentication, and the process may hold
	// Git push credentials.
	fs.StringVar(&cfg.addr, "addr", "127.0.0.1:8080", "address to listen on (loopback by default; v0 has no authentication)")
	fs.BoolVar(&cfg.remoteSync, "remote-sync", true, "pull before and push after each change using ordinary Git credentials")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return cfg, nil
}

func serve(cfg serveConfig, out io.Writer) error {
	logger := log.New(out, "", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, err := service.New(ctx, cfg.repo, cfg.remoteSync)
	if err != nil {
		return err
	}
	// Read the whole tree once before listening: a repository that is not
	// valid Git, or that holds invalid tissues data, must fail at startup
	// rather than on the first request. An empty repository is valid.
	if _, err := svc.ListIssues(ctx); err != nil {
		return err
	}

	srv := &http.Server{Addr: cfg.addr, Handler: rest.New(svc)}
	logger.Printf("tissues: repository %s, remote-sync=%v", cfg.repo, cfg.remoteSync)
	logger.Printf("tissues: listening on http://%s", cfg.addr)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop()
		logger.Print("tissues: shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
