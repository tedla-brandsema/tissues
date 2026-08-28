package config

import (
	"fmt"
	"os"
	"strings"
)

// Environment supplies explicitly present environment values.
type Environment interface {
	Lookup(string) (string, bool)
}

type EnvironmentFunc func(string) (string, bool)

func (f EnvironmentFunc) Lookup(name string) (string, bool) { return f(name) }

// MapEnvironment is a deterministic Environment useful in tests.
type MapEnvironment map[string]string

func (e MapEnvironment) Lookup(name string) (string, bool) {
	value, ok := e[name]
	return value, ok
}

// SystemEnvironment is the process environment source adapter.
func SystemEnvironment() Environment { return EnvironmentFunc(os.LookupEnv) }

// Selection identifies a local profile and leaves non-selector CLI arguments
// for schema-generated parsing.
type Selection struct {
	Name      string
	Directory string
	Args      []string
}

// Bootstrap resolves only --profile and --profiles. CLI values override their
// derived environment equivalents.
func Bootstrap(prefix string, environment Environment, args []string) (Selection, error) {
	selection := Selection{Name: "default", Directory: "./profiles"}
	if environment == nil {
		environment = MapEnvironment{}
	}
	envPrefix := strings.ToUpper(strings.TrimSuffix(prefix, "_"))
	if envPrefix != "" {
		if value, ok := environment.Lookup(envPrefix + "_PROFILE"); ok {
			selection.Name = value
		}
		if value, ok := environment.Lookup(envPrefix + "_PROFILES"); ok {
			selection.Directory = value
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--profile", arg == "--profiles":
			if i+1 >= len(args) {
				return Selection{}, fmt.Errorf("%s requires a value", arg)
			}
			i++
			if arg == "--profile" {
				selection.Name = args[i]
			} else {
				selection.Directory = args[i]
			}
		case strings.HasPrefix(arg, "--profile="):
			selection.Name = strings.TrimPrefix(arg, "--profile=")
		case strings.HasPrefix(arg, "--profiles="):
			selection.Directory = strings.TrimPrefix(arg, "--profiles=")
		default:
			selection.Args = append(selection.Args, arg)
		}
	}
	if !validProfileName(selection.Name) {
		return Selection{}, fmt.Errorf("invalid profile name %q", selection.Name)
	}
	if selection.Directory == "" {
		return Selection{}, fmt.Errorf("profile directory must not be empty")
	}
	return selection, nil
}
