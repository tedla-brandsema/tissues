package tissues

import (
	"context"
	"github.com/tedla-brandsema/tissues/lib/core/config"
	"testing"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	profile, err := config.Load[Config](context.Background(), config.LoadOptions{Environment: config.MapEnvironment{"TISSUES_STORAGE_PROJECT_ID": "example"}, Prefix: "TISSUES"})
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Config.Enabled || profile.Config.Storage.Namespace != "tissues" {
		t.Fatalf("config=%#v", profile.Config)
	}
}
func TestInactiveConfigNeedsNoProject(t *testing.T) {
	if err := (Config{}).ValidateConfig(); err != nil {
		t.Fatal(err)
	}
}
