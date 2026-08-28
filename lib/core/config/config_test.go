package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type testNested struct {
	BrokerURL string `cfg:"string,default=https://default.example,restart=true"`
	Enabled   bool   `cfg:"bool,default=true"`
}

type testConfig struct {
	Name     string        `cfg:"string,default=base"`
	Count    int           `cfg:"int,default=7" val:"rangeint,min=0,max=100"`
	Empty    string        `cfg:"string"`
	Delay    time.Duration `cfg:"duration,default=1s" val:"posduration"`
	Required string        `cfg:"string,required=true"`
	Secret   string        `cfg:"string,secret=true"`
	Port     int           `cfg:"int,default=8080,restart=true,env=PORT" val:"rangeint,min=0,max=65535"`
	Auth     testNested
}

func TestLoadCompletePrecedenceAndProvenance(t *testing.T) {
	store := NewMemoryStore()
	store.Put("demo", Document{Format: "yaml", Data: []byte("name: profile\ncount: 10\nrequired: from-profile\nauth:\n  enabled: true\n")})
	profile, err := Load[testConfig](context.Background(), LoadOptions{
		Name: "demo", Prefix: "TISSUES", Store: store,
		Environment: MapEnvironment{"TISSUES_NAME": "environment", "TISSUES_COUNT": "11", "TISSUES_AUTH_ENABLED": "true"},
		Args:        []string{"--name=CLI", "--count=12", "--auth-enabled=false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Config.Name != "CLI" || profile.Config.Count != 12 || profile.Config.Auth.Enabled {
		t.Fatalf("resolved config = %#v", profile.Config)
	}
	for path, want := range map[string]Source{"Name": SourceCLI, "Count": SourceCLI, "Required": SourceProfile, "Delay": SourceDefault, "Empty": SourceUnset, "Auth.Enabled": SourceCLI} {
		field, ok := profile.Field(path)
		if !ok || field.Source != want {
			t.Fatalf("%s provenance = %#v, %v; want %s", path, field, ok, want)
		}
	}
}

func TestOnlyPresentSourcesOverrideAndExplicitZerosCount(t *testing.T) {
	store := NewMemoryStore()
	store.Put("demo", Document{Format: "json", Data: []byte(`{"name":"profile","count":0,"empty":"","required":""}`)})
	profile, err := Load[testConfig](context.Background(), LoadOptions{Name: "demo", Prefix: "TISSUES", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Config.Count != 0 || profile.Config.Empty != "" || profile.Config.Required != "" || !profile.Config.Auth.Enabled {
		t.Fatalf("resolved explicit zeros = %#v", profile.Config)
	}
	if field, _ := profile.Field("Required"); field.Source != SourceProfile {
		t.Fatalf("required explicit empty source = %s", field.Source)
	}

	profile, err = Load[testConfig](context.Background(), LoadOptions{
		Prefix: "TISSUES", Environment: MapEnvironment{"TISSUES_REQUIRED": "env", "TISSUES_AUTH_ENABLED": "true"},
		Args: []string{"--required=", "--count=0", "--auth-enabled=false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Config.Required != "" || profile.Config.Count != 0 || profile.Config.Auth.Enabled {
		t.Fatalf("CLI explicit zeros = %#v", profile.Config)
	}
}

func TestRequiredUsesPresenceAndInvalidSchemaFails(t *testing.T) {
	type requiredConfig struct {
		Value string `cfg:"string,required=true"`
	}
	for name, options := range map[string]LoadOptions{
		"profile":     {Name: "p", Store: memoryWith("p", `{"value":""}`)},
		"environment": {Prefix: "APP", Environment: MapEnvironment{"APP_VALUE": ""}},
		"CLI":         {Args: []string{"--value="}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load[requiredConfig](context.Background(), options); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := Load[requiredConfig](context.Background(), LoadOptions{}); err == nil {
		t.Fatal("missing required field accepted")
	}
	type badSchema struct {
		Value string `cfg:"string,default=x,required=true"`
	}
	if _, err := Load[badSchema](context.Background(), LoadOptions{}); err == nil || !strings.Contains(err.Error(), "required and default") {
		t.Fatalf("bad schema error = %v", err)
	}
}

func TestSecretRedactionAndStructuredConversionError(t *testing.T) {
	profile, err := Load[testConfig](context.Background(), LoadOptions{
		Prefix: "TISSUES", Environment: MapEnvironment{"TISSUES_REQUIRED": "ok", "TISSUES_SECRET": "do-not-print"},
	})
	if err != nil {
		t.Fatal(err)
	}
	field, _ := profile.Field("Secret")
	if field.Value != redacted || strings.Contains(fmt.Sprintf("%+v", profile.Provenance()), "do-not-print") {
		t.Fatalf("secret provenance leaked: %#v", field)
	}
	_, err = Load[testConfig](context.Background(), LoadOptions{
		Prefix: "TISSUES", Environment: MapEnvironment{"TISSUES_REQUIRED": "ok", "TISSUES_COUNT": "sensitive-bad-value"},
	})
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) || fieldErr.Path != "Count" || fieldErr.Source != SourceEnvironment || strings.Contains(err.Error(), "sensitive-bad-value") {
		t.Fatalf("conversion error = %#v / %v", fieldErr, err)
	}
	type secretValidation struct {
		Secret string `cfg:"string,secret=true" val:"min,size=20"`
	}
	_, err = Load[secretValidation](context.Background(), LoadOptions{Environment: MapEnvironment{"SECRET": "leaky-secret"}})
	if err == nil || strings.Contains(err.Error(), "leaky-secret") || !strings.Contains(err.Error(), redacted) {
		t.Fatalf("secret validation error was not redacted: %v", err)
	}
	var output strings.Builder
	_, err = Load[secretValidation](context.Background(), LoadOptions{Args: []string{"--secret=cli-leaky"}, FlagOutput: &output})
	if err == nil || strings.Contains(err.Error()+output.String(), "cli-leaky") {
		t.Fatalf("secret CLI error leaked: %v / %q", err, output.String())
	}
}

func TestDerivedNamesAndOverride(t *testing.T) {
	profile, err := Load[testConfig](context.Background(), LoadOptions{Prefix: "TISSUES", Environment: MapEnvironment{"TISSUES_REQUIRED": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	field, _ := profile.Field("Auth.BrokerURL")
	if field.FileName != "auth.broker_url" || field.Environment != "TISSUES_AUTH_BROKER_URL" || field.Flag != "--auth-broker-url" {
		t.Fatalf("derived names = %#v", field)
	}
	port, _ := profile.Field("Port")
	if port.Environment != "PORT" {
		t.Fatalf("source override = %#v", port)
	}
}

func TestGeneratedHelpIsDerivedAndSecretSafe(t *testing.T) {
	var output strings.Builder
	_, err := Load[testConfig](context.Background(), LoadOptions{
		Prefix: "TISSUES", Environment: MapEnvironment{"TISSUES_SECRET": "do-not-print"},
		Args: []string{"--help"}, FlagOutput: &output,
	})
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	help := output.String()
	for _, want := range []string{"auth-broker-url", "TISSUES_AUTH_BROKER_URL", "default: https://default.example"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "do-not-print") {
		t.Fatal("help exposed secret value")
	}
}

func TestBootstrapSelectorsCLIOverrideEnvironment(t *testing.T) {
	selection, err := Bootstrap("TISSUES", MapEnvironment{
		"TISSUES_PROFILE": "environment", "TISSUES_PROFILES": "/environment",
	}, []string{"--profile=CLI", "--profiles", "/cli", "--name=value"})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Name != "CLI" || selection.Directory != "/cli" || len(selection.Args) != 1 || selection.Args[0] != "--name=value" {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestStrictDocumentsAndValidation(t *testing.T) {
	for name, doc := range map[string]Document{
		"JSON unknown":         {Format: "json", Data: []byte(`{"required":"ok","unknown":1}`)},
		"YAML unknown":         {Format: "yaml", Data: []byte("required: ok\nunknown: 1\n")},
		"empty unknown object": {Format: "json", Data: []byte(`{"required":"ok","unknown":{}}`)},
		"JSON multiple":        {Format: "json", Data: []byte(`{"required":"ok"} {}`)},
		"YAML multiple":        {Format: "yaml", Data: []byte("required: ok\n---\nrequired: again\n")},
		"type mismatch":        {Format: "yaml", Data: []byte("required: ok\ncount: nope\n")},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			store.Put("bad", doc)
			if _, err := Load[testConfig](context.Background(), LoadOptions{Name: "bad", Store: store}); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	if _, err := Load[testConfig](context.Background(), LoadOptions{Environment: MapEnvironment{"REQUIRED": "ok", "COUNT": "101"}}); err == nil {
		t.Fatal("Valex-invalid candidate accepted")
	}
	type crossConfig struct {
		Enabled bool   `cfg:"bool"`
		Value   string `cfg:"string"`
	}
	// A local named type supplies cross-field validation.
	_ = crossConfig{}
}

type validatedConfig struct {
	Enabled bool   `cfg:"bool"`
	Value   string `cfg:"string"`
}

func (cfg validatedConfig) ValidateConfig() error {
	if cfg.Enabled && cfg.Value == "" {
		return errors.New("Value is required when Enabled")
	}
	return nil
}

func TestStructValidation(t *testing.T) {
	if _, err := Load[validatedConfig](context.Background(), LoadOptions{Args: []string{"--enabled=true"}}); err == nil {
		t.Fatal("cross-field-invalid candidate accepted")
	}
}

func TestManagerReloadNoopSuccessFailureAndConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	store := memoryWith("demo", `{"required":"ok","name":"one"}`)
	manager, err := NewManager[testConfig](ctx, LoadOptions{Name: "demo", Prefix: "APP", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	noop, err := manager.Reload(ctx)
	if err != nil || noop.Changed || noop.Profile.Revision != 1 {
		t.Fatalf("no-op = %#v, %v", noop, err)
	}

	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				_ = manager.Current().Config.Name
			}
		}()
	}
	store.Put("demo", Document{Format: "json", Data: []byte(`{"required":"ok","name":"two","port":9090}`)})
	changed, err := manager.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if changed.Profile.Revision != 2 || len(changed.LiveChanges) != 1 || len(changed.RestartRequired) != 1 {
		t.Fatalf("changed = %#v", changed)
	}

	store.Put("demo", Document{Format: "json", Data: []byte(`{"required":"ok","count":101}`)})
	if rejected, err := manager.Reload(ctx); err == nil || rejected.Profile.Revision != 2 {
		t.Fatalf("invalid reload = %#v, %v", rejected, err)
	}
	if current := manager.Current(); current.Revision != 2 || current.Config.Name != "two" {
		t.Fatalf("active profile changed = %#v", current)
	}
}

func memoryWith(name, data string) *MemoryStore {
	store := NewMemoryStore()
	store.Put(name, Document{Format: "json", Data: []byte(data)})
	return store
}
