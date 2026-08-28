package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ErrHelp reports that generated CLI help was requested and already written.
var ErrHelp = errors.New("configuration help requested")

// LoadOptions describes ordered configuration sources.
type LoadOptions struct {
	Name        string
	Prefix      string
	Store       Store
	Environment Environment
	Args        []string
	FlagOutput  io.Writer
}

// FieldError identifies the failed field and source without retaining its raw
// input, so secret conversion failures are safe to report.
type FieldError struct {
	Path   string
	Source Source
	Err    error
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("configure %s from %s: %v", e.Path, e.Source, e.Err)
}

func (e *FieldError) Unwrap() error { return e.Err }

// Load resolves defaults, optional profile data, present environment entries,
// and explicitly provided CLI arguments, then validates the complete result.
func Load[T any](ctx context.Context, options LoadOptions) (Profile[T], error) {
	candidate, fields, err := defaults[T]()
	if err != nil {
		return Profile[T]{}, err
	}
	if options.Name == "" {
		options.Name = "default"
	}
	if options.Environment == nil {
		options.Environment = MapEnvironment{}
	}
	fields = resolvedNames(fields, options.Prefix)
	sources := make(map[string]Source, len(fields))
	for _, field := range fields {
		if field.hasDefault {
			sources[field.path] = SourceDefault
		} else {
			sources[field.path] = SourceUnset
		}
	}

	if options.Store != nil {
		document, loadErr := options.Store.Load(ctx, options.Name)
		switch {
		case loadErr == nil:
			values, decodeErr := decodeDocument(document)
			if decodeErr != nil {
				return Profile[T]{}, fmt.Errorf("load profile %q: %w", options.Name, decodeErr)
			}
			if applyErr := applyProfile(reflect.ValueOf(&candidate).Elem(), fields, values, sources); applyErr != nil {
				return Profile[T]{}, applyErr
			}
		case errors.Is(loadErr, ErrProfileNotFound):
		default:
			return Profile[T]{}, loadErr
		}
	}

	root := reflect.ValueOf(&candidate).Elem()
	for _, field := range fields {
		if raw, ok := options.Environment.Lookup(field.envName); ok {
			if err := setText(valueAt(root, field.index), raw); err != nil {
				return Profile[T]{}, &FieldError{Path: field.path, Source: SourceEnvironment, Err: err}
			}
			sources[field.path] = SourceEnvironment
		}
	}
	if err := applyCLI(root, fields, options.Args, options.FlagOutput, sources); err != nil {
		return Profile[T]{}, err
	}
	for _, field := range fields {
		if field.required && sources[field.path] == SourceUnset {
			return Profile[T]{}, &FieldError{Path: field.path, Source: SourceUnset, Err: errors.New("required field was not supplied")}
		}
	}
	if err := validate(&candidate); err != nil {
		return Profile[T]{}, err
	}
	return profileFrom(options.Name, 1, candidate, provenanceFor(candidate, fields, sources), true), nil
}

func resolvedNames(fields []fieldSchema, prefix string) []fieldSchema {
	prefix = strings.ToUpper(strings.Trim(strings.TrimSpace(prefix), "_"))
	out := append([]fieldSchema(nil), fields...)
	for i := range out {
		if out[i].envName == "" {
			name := strings.ToUpper(strings.ReplaceAll(out[i].fileName, ".", "_"))
			if prefix != "" {
				name = prefix + "_" + name
			}
			out[i].envName = name
		}
	}
	return out
}

func decodeDocument(document Document) (map[string]any, error) {
	extension, err := normalizeFormat(document.Format)
	if err != nil {
		return nil, err
	}
	values := make(map[string]any)
	switch extension {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(document.Data))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, errors.New("decode JSON: multiple documents are not supported")
			}
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
	default:
		decoder := yaml.NewDecoder(bytes.NewReader(document.Data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&values); err != nil {
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, errors.New("decode YAML: multiple documents are not supported")
			}
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
	}
	return values, nil
}

func applyProfile(root reflect.Value, fields []fieldSchema, values map[string]any, sources map[string]Source) error {
	exact := make(map[string]bool, len(fields))
	prefixes := make(map[string]bool)
	for _, field := range fields {
		exact[field.fileName] = true
		parts := strings.Split(field.fileName, ".")
		for i := 1; i < len(parts); i++ {
			prefixes[strings.Join(parts[:i], ".")] = true
		}
	}
	if err := validateProfileShape("", values, exact, prefixes); err != nil {
		return err
	}
	flat := make(map[string]any)
	flattenValues("", values, flat)
	byName := make(map[string]fieldSchema, len(fields))
	for _, field := range fields {
		byName[field.fileName] = field
	}
	for path, raw := range flat {
		field, ok := byName[path]
		if !ok {
			return fmt.Errorf("unknown configuration field %q", path)
		}
		if err := setValue(valueAt(root, field.index), raw); err != nil {
			return &FieldError{Path: field.path, Source: SourceProfile, Err: err}
		}
		sources[field.path] = SourceProfile
	}
	return nil
}

func validateProfileShape(prefix string, values map[string]any, exact, prefixes map[string]bool) error {
	for name, value := range values {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if nested, ok := value.(map[string]any); ok {
			if !prefixes[path] {
				return fmt.Errorf("unknown configuration field %q", path)
			}
			if err := validateProfileShape(path, nested, exact, prefixes); err != nil {
				return err
			}
			continue
		}
		if !exact[path] {
			return fmt.Errorf("unknown configuration field %q", path)
		}
	}
	return nil
}

func flattenValues(prefix string, values map[string]any, out map[string]any) {
	for name, value := range values {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if nested, ok := value.(map[string]any); ok {
			flattenValues(path, nested, out)
			continue
		}
		out[path] = value
	}
}

func setValue(target reflect.Value, raw any) error {
	if target.Type() == reflect.TypeFor[time.Duration]() {
		text, ok := raw.(string)
		if !ok {
			return errors.New("expected duration string")
		}
		return setText(target, text)
	}
	switch target.Kind() {
	case reflect.String:
		value, ok := raw.(string)
		if !ok {
			return errors.New("expected string")
		}
		target.SetString(value)
	case reflect.Bool:
		value, ok := raw.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		target.SetBool(value)
	case reflect.Int:
		switch value := raw.(type) {
		case json.Number:
			parsed, err := value.Int64()
			if err != nil {
				return errors.New("expected integer")
			}
			target.SetInt(parsed)
		case int:
			target.SetInt(int64(value))
		case int64:
			target.SetInt(value)
		default:
			return errors.New("expected integer")
		}
	default:
		return fmt.Errorf("unsupported field type %s", target.Type())
	}
	return nil
}

func setText(target reflect.Value, raw string) error {
	if target.Type() == reflect.TypeFor[time.Duration]() {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return errors.New("expected duration")
		}
		target.SetInt(int64(value))
		return nil
	}
	switch target.Kind() {
	case reflect.String:
		target.SetString(raw)
	case reflect.Int:
		value, err := strconv.ParseInt(raw, 10, target.Type().Bits())
		if err != nil {
			return errors.New("expected integer")
		}
		target.SetInt(value)
	case reflect.Bool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.New("expected boolean")
		}
		target.SetBool(value)
	default:
		return fmt.Errorf("unsupported field type %s", target.Type())
	}
	return nil
}

type trackedFlag struct {
	target reflect.Value
	set    bool
}

func (f *trackedFlag) String() string {
	if !f.target.IsValid() {
		return ""
	}
	if f.target.Type() == reflect.TypeFor[time.Duration]() {
		return time.Duration(f.target.Int()).String()
	}
	return fmt.Sprint(f.target.Interface())
}

func (f *trackedFlag) Set(raw string) error {
	if err := setText(f.target, raw); err != nil {
		return err
	}
	f.set = true
	return nil
}

func (f *trackedFlag) IsBoolFlag() bool { return f.target.Kind() == reflect.Bool }

func applyCLI(root reflect.Value, fields []fieldSchema, args []string, output io.Writer, sources map[string]Source) error {
	set := flag.NewFlagSet("config", flag.ContinueOnError)
	if output == nil {
		output = io.Discard
	}
	var flagOutput bytes.Buffer
	set.SetOutput(&flagOutput)
	tracked := make(map[string]*trackedFlag, len(fields))
	for _, field := range fields {
		value := &trackedFlag{target: valueAt(root, field.index)}
		tracked[field.path] = value
		help := "environment: " + field.envName
		if field.hasDefault && !field.secret {
			help += "; default: " + field.defaultText
		}
		set.Var(value, field.flagName, help)
		set.Lookup(field.flagName).DefValue = ""
	}
	if err := set.Parse(args); err != nil {
		message := sanitizeCLIText(flagOutput.String(), fields, args)
		if message != "" {
			_, _ = io.WriteString(output, message)
		}
		if errors.Is(err, flag.ErrHelp) {
			return ErrHelp
		}
		return fmt.Errorf("parse configuration CLI: %s", sanitizeCLIText(err.Error(), fields, args))
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(set.Args(), " "))
	}
	for _, field := range fields {
		if tracked[field.path].set {
			sources[field.path] = SourceCLI
		}
	}
	return nil
}

func sanitizeCLIText(message string, fields []fieldSchema, args []string) string {
	secretFlags := make(map[string]bool)
	for _, field := range fields {
		if field.secret {
			secretFlags["--"+field.flagName] = true
		}
	}
	for i, arg := range args {
		name, value, hasValue := strings.Cut(arg, "=")
		if !secretFlags[name] {
			continue
		}
		if hasValue && value != "" {
			message = strings.ReplaceAll(message, value, redacted)
		} else if !hasValue && i+1 < len(args) && args[i+1] != "" {
			message = strings.ReplaceAll(message, args[i+1], redacted)
		}
	}
	return message
}

func provenanceFor[T any](candidate T, fields []fieldSchema, sources map[string]Source) map[string]FieldProvenance {
	root := reflect.ValueOf(candidate)
	out := make(map[string]FieldProvenance, len(fields))
	for _, field := range fields {
		source := SourceUnset
		if sources != nil {
			source = sources[field.path]
		}
		value := redacted
		if !field.secret {
			resolved := valueAt(root, field.index)
			if resolved.Type() == reflect.TypeFor[time.Duration]() {
				value = time.Duration(resolved.Int()).String()
			} else {
				value = fmt.Sprint(resolved.Interface())
			}
		}
		out[field.path] = FieldProvenance{
			Path: field.path, FileName: field.fileName, Environment: field.envName,
			Flag: "--" + field.flagName, Source: source, Secret: field.secret,
			Restart: field.restart, Value: value,
		}
	}
	return out
}
