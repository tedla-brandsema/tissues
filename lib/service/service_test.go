package service

import (
	"net/http"
	"testing"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
)

type testConfig struct{ Contribution }

var _ Configuration = testConfig{}

type testService struct{}

func (testService) Name() string                        { return "test" }
func (testService) RegisterRoutes(*http.ServeMux) error { return nil }

var _ Service = testService{}

func TestCoreSlotImplementsProfile(t *testing.T) {
	profile, err := coreconfig.NewServiceProfile("test", testConfig{})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := coreconfig.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	var reader Profile[testConfig] = slot
	if got := reader.Current().Name; got != "test" {
		t.Fatalf("profile name = %q, want test", got)
	}
}
