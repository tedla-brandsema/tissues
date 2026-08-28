package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type emptyServiceConfig struct {
	Contribution
}

type contributedServiceConfig struct {
	Contribution
	Enabled bool `cfg:"bool,default=true"`
}

// These assignments are the positive compile-time proof. A type that does not
// embed Contribution cannot satisfy ServiceContribution or be passed to
// NewServiceProfile; the generic constraint is the negative contract.
var (
	_ ServiceContribution = emptyServiceConfig{}
	_ ServiceContribution = contributedServiceConfig{}
)

func TestEmptyServiceContribution(t *testing.T) {
	profile, err := NewServiceProfile("empty", emptyServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "empty" || profile.Revision != 1 {
		t.Fatalf("profile = %#v", profile)
	}
	if fields := profile.Provenance(); len(fields) != 0 {
		t.Fatalf("marker generated provenance: %#v", fields)
	}

	var help strings.Builder
	_, err = Load[emptyServiceConfig](context.Background(), LoadOptions{Args: []string{"--help"}, FlagOutput: &help})
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("Load(--help) error = %v", err)
	}
	if strings.Contains(help.String(), "contribution") {
		t.Fatalf("marker generated CLI help: %q", help.String())
	}

	for _, document := range []Document{{Format: "json", Data: []byte(`{}`)}, {Format: "yaml", Data: []byte("{}\n")}} {
		store := NewMemoryStore()
		store.Put("empty", document)
		if _, err := Load[emptyServiceConfig](context.Background(), LoadOptions{Name: "empty", Store: store}); err != nil {
			t.Fatalf("empty document was not accepted: %v", err)
		}
	}
	for _, document := range []Document{{Format: "json", Data: []byte(`{"contribution":{}}`)}, {Format: "yaml", Data: []byte("contribution: {}\n")}} {
		store := NewMemoryStore()
		store.Put("leaked", document)
		if _, err := Load[emptyServiceConfig](context.Background(), LoadOptions{Name: "leaked", Store: store}); err == nil {
			t.Fatal("marker unexpectedly became a JSON/YAML field")
		}
	}
}

func TestContributionExposesOnlyRealFields(t *testing.T) {
	profile, err := Load[contributedServiceConfig](context.Background(), LoadOptions{Prefix: "APP"})
	if err != nil {
		t.Fatal(err)
	}
	fields := profile.Provenance()
	if len(fields) != 1 || fields[0].Path != "Enabled" || fields[0].FileName != "enabled" || fields[0].Environment != "APP_ENABLED" || fields[0].Flag != "--enabled" {
		t.Fatalf("configuration surface = %#v", fields)
	}
	if _, err := NewServiceProfile("service", profile.Config); err != nil {
		t.Fatal(err)
	}
}
