package server

import (
	"context"
	"testing"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

func TestConfigUsesSchemaDefaultsAndBarePortOverride(t *testing.T) {
	profile, err := config.Load[Config](context.Background(), config.LoadOptions{
		Prefix: "TISSUES", Environment: config.MapEnvironment{"PORT": "9090"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.Config.Address(); got != ":9090" {
		t.Fatalf("Address() = %q, want :9090", got)
	}
	field, ok := profile.Field("Port")
	if !ok || field.Environment != "PORT" || field.Source != config.SourceEnvironment {
		t.Fatalf("Port provenance = %#v, %v", field, ok)
	}
	if profile.Config.ReadTimeout == 0 || profile.Config.MaxHeaderBytes == 0 {
		t.Fatal("schema defaults were not applied")
	}
}

func TestConfigRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"nope", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			_, err := config.Load[Config](context.Background(), config.LoadOptions{Environment: config.MapEnvironment{"PORT": port}})
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}
