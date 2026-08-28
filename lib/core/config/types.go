// Package config resolves typed Go configuration schemas into immutable,
// revisioned profiles.
package config

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// Source identifies the highest-precedence source that supplied a field.
type Source string

const (
	SourceUnset       Source = "unset"
	SourceDefault     Source = "default"
	SourceProfile     Source = "profile"
	SourceEnvironment Source = "environment"
	SourceCLI         Source = "CLI"
	redacted                 = "[redacted]"
)

// Revision is a monotonically increasing profile revision.
type Revision uint64

// FieldProvenance describes one resolved field without exposing secret values.
type FieldProvenance struct {
	Path        string
	FileName    string
	Environment string
	Flag        string
	Source      Source
	Secret      bool
	Restart     bool
	Value       string
}

// Profile is a named, validated configuration snapshot.
type Profile[T any] struct {
	Name     string
	Revision Revision
	Config   T

	provenance map[string]FieldProvenance
	validated  bool
}

// Provenance returns a stable copy of the field provenance.
func (p Profile[T]) Provenance() []FieldProvenance {
	paths := make([]string, 0, len(p.provenance))
	for path := range p.provenance {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]FieldProvenance, 0, len(paths))
	for _, path := range paths {
		out = append(out, p.provenance[path])
	}
	return out
}

// Field returns provenance for one Go field path.
func (p Profile[T]) Field(path string) (FieldProvenance, bool) {
	field, ok := p.provenance[path]
	return field, ok
}

// NewProfile validates an already resolved configuration and gives it an
// initial revision. It is useful when composing a service sub-profile from an
// outer application profile.
func NewProfile[T any](name string, value T) (Profile[T], error) {
	fields, err := schemaFor[T]()
	if err != nil {
		return Profile[T]{}, err
	}
	if err := validate(&value); err != nil {
		return Profile[T]{}, err
	}
	return profileFrom(name, 1, value, provenanceFor(value, fields, nil), true), nil
}

func profileFrom[T any](name string, revision Revision, value T, provenance map[string]FieldProvenance, validated bool) Profile[T] {
	if name == "" {
		name = "default"
	}
	return Profile[T]{
		Name:       name,
		Revision:   revision,
		Config:     clone(value),
		provenance: cloneProvenance(provenance),
		validated:  validated,
	}
}

func cloneProfile[T any](profile Profile[T]) Profile[T] {
	return profileFrom(profile.Name, profile.Revision, profile.Config, profile.provenance, profile.validated)
}

func cloneProvenance(in map[string]FieldProvenance) map[string]FieldProvenance {
	out := make(map[string]FieldProvenance, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// ReplaceResult classifies one atomic profile replacement.
type ReplaceResult[T any] struct {
	Changed         bool
	Profile         Profile[T]
	LiveChanges     []string
	RestartRequired []string
}

// Slot owns the currently active immutable profile snapshot.
type Slot[T any] struct {
	mu      sync.RWMutex
	current Profile[T]
}

// NewSlot creates a slot from a validated profile.
func NewSlot[T any](initial Profile[T]) (*Slot[T], error) {
	if !initial.validated {
		return nil, fmt.Errorf("profile %q has not been validated", initial.Name)
	}
	return &Slot[T]{current: cloneProfile(initial)}, nil
}

// Current returns an isolated copy of the active profile.
func (s *Slot[T]) Current() Profile[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneProfile(s.current)
}

// Replace atomically publishes a complete validated candidate. Equal effective
// configuration is a no-op and does not advance the revision.
func (s *Slot[T]) Replace(candidate Profile[T]) (ReplaceResult[T], error) {
	if !candidate.validated {
		return ReplaceResult[T]{}, fmt.Errorf("profile %q has not been validated", candidate.Name)
	}
	fields, err := schemaFor[T]()
	if err != nil {
		return ReplaceResult[T]{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if reflect.DeepEqual(s.current.Config, candidate.Config) {
		return ReplaceResult[T]{Profile: cloneProfile(s.current)}, nil
	}

	live, restart := changedFields(s.current.Config, candidate.Config, fields)
	candidate.Revision = s.current.Revision + 1
	s.current = cloneProfile(candidate)
	return ReplaceResult[T]{
		Changed:         true,
		Profile:         cloneProfile(s.current),
		LiveChanges:     live,
		RestartRequired: restart,
	}, nil
}

func changedFields[T any](before, after T, fields []fieldSchema) ([]string, []string) {
	left := reflect.ValueOf(before)
	right := reflect.ValueOf(after)
	var live, restart []string
	for _, field := range fields {
		if reflect.DeepEqual(valueAt(left, field.index).Interface(), valueAt(right, field.index).Interface()) {
			continue
		}
		if field.restart {
			restart = append(restart, field.path)
		} else {
			live = append(live, field.path)
		}
	}
	return live, restart
}

func clone[T any](value T) T {
	return cloneReflect(reflect.ValueOf(value)).Interface().(T)
}

func cloneReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneReflect(value.Elem()))
		return out
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := cloneReflect(value.Elem())
		wrapped := reflect.New(value.Type()).Elem()
		wrapped.Set(out)
		return wrapped
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			out.Index(i).Set(cloneReflect(value.Index(i)))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneReflect(iter.Key()), cloneReflect(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		for i := range value.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneReflect(value.Field(i)))
			}
		}
		return out
	default:
		return value
	}
}
