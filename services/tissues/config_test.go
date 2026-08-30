package tissues

import (
	"context"
	"testing"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

const testTenantID = TenantID("7womw3jzkek74oggxj6f42xak4")

func TestConfigDefaultsAndValidation(t *testing.T) {
	profile, err := config.Load[Config](context.Background(), config.LoadOptions{Environment: config.MapEnvironment{"TISSUES_BOOTSTRAP_TENANT_ID": testTenantID.String(), "TISSUES_STORAGE_PROJECT_ID": "example", "TISSUES_ASSETS_BUCKET": "assets"}, Prefix: "TISSUES"})
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Config.Enabled || profile.Config.BootstrapTenantID != testTenantID.String() || profile.Config.Storage.Namespace != "tissues" || profile.Config.Assets.Bucket != "assets" {
		t.Fatalf("config=%#v", profile.Config)
	}
}

func TestEnabledConfigRejectsInvalidBootstrapTenant(t *testing.T) {
	base := Config{Enabled: true, Storage: StorageConfig{ProjectID: "example", Namespace: "tissues"}, Assets: AssetsConfig{Bucket: "assets"}}
	for _, tenant := range []string{"", "default", "not-a-tissues-id"} {
		base.BootstrapTenantID = tenant
		if err := base.ValidateConfig(); err == nil {
			t.Errorf("BootstrapTenantID %q accepted", tenant)
		}
	}
}
func TestInactiveConfigNeedsNoProject(t *testing.T) {
	if err := (Config{}).ValidateConfig(); err != nil {
		t.Fatal(err)
	}
}
