package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	gcds "cloud.google.com/go/datastore"
	"cloud.google.com/go/storage"
	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/server"
	"github.com/tedla-brandsema/tissues/lib/service"
	authservice "github.com/tedla-brandsema/tissues/services/auth"
	"github.com/tedla-brandsema/tissues/services/tissues"
	tissuesds "github.com/tedla-brandsema/tissues/services/tissues/datastore"
	tissuesgcs "github.com/tedla-brandsema/tissues/services/tissues/gcs"
)

const serverName = "tissues"

type appConfig struct {
	Server  server.Config
	Auth    authservice.Config
	Tissues tissues.Config
}

func (cfg appConfig) ValidateConfig() error {
	if err := cfg.Server.ValidateConfig(); err != nil {
		return err
	}
	if err := cfg.Auth.ValidateConfig(); err != nil {
		return fmt.Errorf("Auth: %w", err)
	}
	if err := cfg.Tissues.ValidateConfig(); err != nil {
		return fmt.Errorf("Tissues: %w", err)
	}
	return nil
}

func main() {
	if err := run(context.Background(), os.Args[1:], coreconfig.SystemEnvironment()); err != nil {
		if errors.Is(err, coreconfig.ErrHelp) {
			return
		}
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, environment coreconfig.Environment) error {
	selection, err := coreconfig.Bootstrap("TISSUES", environment, args)
	if err != nil {
		return err
	}
	profile, err := coreconfig.Load[appConfig](ctx, coreconfig.LoadOptions{Name: selection.Name, Prefix: "TISSUES", Store: coreconfig.NewFileStore(selection.Directory), Environment: environment, Args: selection.Args, FlagOutput: os.Stderr})
	if err != nil {
		return err
	}
	srv, closers, err := compose(ctx, profile)
	if err != nil {
		return err
	}
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			if closeErr := closers[i](); closeErr != nil {
				slog.Error("close dependency", "error", closeErr)
			}
		}
	}()
	return srv.StartAndWait()
}

type closeFunc func() error

func compose(ctx context.Context, profile coreconfig.Profile[appConfig]) (*server.Server, []closeFunc, error) {
	authProfile, err := coreconfig.NewServiceProfile(profile.Name, profile.Config.Auth)
	if err != nil {
		return nil, nil, err
	}
	authSlot, err := coreconfig.NewSlot(authProfile)
	if err != nil {
		return nil, nil, err
	}
	tissuesProfile, err := coreconfig.NewServiceProfile(profile.Name, profile.Config.Tissues)
	if err != nil {
		return nil, nil, err
	}
	tissuesSlot, err := coreconfig.NewSlot(tissuesProfile)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	var closers []closeFunc
	var active []service.Service
	var authService *authservice.Service
	var serviceErr error
	closeOnError := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i]()
		}
	}
	if profile.Config.Auth.Enabled {
		client, clientErr := gcds.NewClient(ctx, profile.Config.Auth.ProjectID)
		if clientErr != nil {
			return nil, nil, fmt.Errorf("create auth Datastore client: %w", clientErr)
		}
		closers = append(closers, client.Close)
		codeStore := broker.NewDatastoreCodeStore(client, profile.Config.Auth.DatastoreNS, profile.Config.Auth.DatastoreKind)
		authService, serviceErr = authservice.New(authSlot, codeStore)
		if serviceErr != nil {
			closeOnError()
			return nil, nil, serviceErr
		}
		active = append(active, authService)
	}
	if profile.Config.Tissues.Enabled {
		client, clientErr := gcds.NewClient(ctx, profile.Config.Tissues.Storage.ProjectID)
		if clientErr != nil {
			closeOnError()
			return nil, nil, fmt.Errorf("create tissues Datastore client: %w", clientErr)
		}
		closers = append(closers, client.Close)
		repository, repoErr := tissuesds.New(client, profile.Config.Tissues.Storage.Namespace)
		if repoErr != nil {
			closeOnError()
			return nil, nil, repoErr
		}
		assetClient, assetClientErr := storage.NewClient(ctx)
		if assetClientErr != nil {
			closeOnError()
			return nil, nil, fmt.Errorf("create tissues GCS client: %w", assetClientErr)
		}
		closers = append(closers, assetClient.Close)
		assetStore, assetStoreErr := tissuesgcs.New(assetClient, profile.Config.Tissues.Assets.Bucket)
		if assetStoreErr != nil {
			closeOnError()
			return nil, nil, assetStoreErr
		}
		var tissuesOptions []tissues.Option
		if authService != nil {
			tissuesOptions = append(tissuesOptions, tissues.WithMCPAuth(tissuesMCPAuth(profile.Config.Auth, authService.VerifyAccessToken)))
		}
		tissuesService, serviceErr := tissues.New(tissuesSlot, repository, assetStore, tissuesOptions...)
		if serviceErr != nil {
			closeOnError()
			return nil, nil, serviceErr
		}
		active = append(active, tissuesService)
	}
	if err := registerServices(mux, active); err != nil {
		closeOnError()
		return nil, nil, err
	}
	srv, err := server.New(ctx, serverName, profile.Config.Server)
	if err != nil {
		closeOnError()
		return nil, nil, err
	}
	srv.SetMux(mux)
	return srv, closers, nil
}

func tissuesMCPAuth(cfg authservice.Config, verify func(string) (authservice.VerifiedAccessToken, error)) tissues.MCPAuth {
	return tissues.MCPAuth{
		Issuer: cfg.IssuerURL, Resource: cfg.MCPResourceURL,
		Verify: func(_ context.Context, token string) (tissues.MCPVerifiedToken, error) {
			verified, err := verify(token)
			if err != nil {
				return tissues.MCPVerifiedToken{}, err
			}
			return tissues.MCPVerifiedToken{
				Subject: verified.Subject, Email: verified.Email, ClientID: verified.ClientID,
				Scopes: append([]string(nil), verified.Scopes...), ExpiresAt: verified.ExpiresAt,
			}, nil
		},
	}
}

func registerServices(mux *http.ServeMux, active []service.Service) error {
	for _, svc := range active {
		if err := svc.RegisterRoutes(mux); err != nil {
			return fmt.Errorf("register %s routes: %w", svc.Name(), err)
		}
	}
	return nil
}
