// Package service defines the contract implemented by in-process Services.
package service

import (
	"net/http"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
)

// Service contributes HTTP routes to a hosting Server. Process lifecycle is
// owned by the Server runtime, not by a Service.
type Service interface {
	Name() string
	RegisterRoutes(*http.ServeMux) error
}

// Contribution marks a typed Service configuration contribution.
type Contribution = coreconfig.Contribution

// Configuration is the compile-time contract for Service configuration.
type Configuration interface {
	coreconfig.ServiceContribution
}

// Profile exposes the current immutable typed Service configuration snapshot.
type Profile[C Configuration] interface {
	Current() coreconfig.Profile[C]
}
