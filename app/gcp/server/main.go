package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	gcfirestore "cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/tedla-brandsema/tissues/lib/auth/broker"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/lib/server"
	"github.com/tedla-brandsema/tissues/lib/service"
	authservice "github.com/tedla-brandsema/tissues/services/auth"
	"github.com/tedla-brandsema/tissues/services/tissues"
	tissuesfirestore "github.com/tedla-brandsema/tissues/services/tissues/firestore"
	tissuesgcs "github.com/tedla-brandsema/tissues/services/tissues/gcs"
)

const serverName = "tissues"

type appConfig struct {
	Server    server.Config
	Firestore firestoreConfig
	Auth      authservice.Config
	Tissues   tissues.Config
}

type firestoreConfig struct {
	ProjectID  string `cfg:"string,restart=true"`
	DatabaseID string `cfg:"string,restart=true"`
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
	if err := cfg.Firestore.validate(cfg.Auth.Enabled || cfg.Tissues.Enabled); err != nil {
		return fmt.Errorf("Firestore: %w", err)
	}
	return nil
}

func (cfg firestoreConfig) validate(required bool) error {
	if !required && cfg.ProjectID == "" && cfg.DatabaseID == "" {
		return nil
	}
	for _, field := range []struct {
		path  string
		value string
	}{{"ProjectID", cfg.ProjectID}, {"DatabaseID", cfg.DatabaseID}} {
		trimmed := strings.TrimSpace(field.value)
		if trimmed == "" {
			return fmt.Errorf("%s must not be empty", field.path)
		}
		if field.value != trimmed {
			return fmt.Errorf("%s must not contain leading or trailing whitespace", field.path)
		}
	}
	if cfg.DatabaseID == "(default)" {
		return fmt.Errorf("DatabaseID must name a non-default Firestore database")
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
	var firestoreClient *gcfirestore.Client
	if profile.Config.Auth.Enabled || profile.Config.Tissues.Enabled {
		firestoreClient, err = gcfirestore.NewClientWithDatabase(ctx, profile.Config.Firestore.ProjectID, profile.Config.Firestore.DatabaseID)
		if err != nil {
			return nil, nil, fmt.Errorf("create shared Firestore client: %w", err)
		}
		closers = append(closers, firestoreClient.Close)
	}
	if profile.Config.Auth.Enabled {
		codeStore, storeErr := broker.NewFirestoreCodeStore(firestoreClient)
		if storeErr != nil {
			closeOnError()
			return nil, nil, fmt.Errorf("create auth Firestore CodeStore: %w", storeErr)
		}
		authService, serviceErr = authservice.New(authSlot, codeStore)
		if serviceErr != nil {
			closeOnError()
			return nil, nil, serviceErr
		}
		active = append(active, authService)
	}
	if profile.Config.Tissues.Enabled {
		repository, repoErr := tissuesfirestore.New(firestoreClient)
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
