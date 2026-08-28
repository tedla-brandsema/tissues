package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tedla-brandsema/tagex"
	"github.com/tedla-brandsema/valex"
	"github.com/tedla-brandsema/valex/validators"
)

const cfgTagKey = "cfg"

type fieldSchema struct {
	path        string
	fileName    string
	envName     string
	flagName    string
	index       []int
	typeOf      reflect.Type
	hasDefault  bool
	defaultText string
	required    bool
	secret      bool
	restart     bool
}

type stringDirective struct {
	Default  string `param:"default, required=false"`
	Required bool   `param:"required, required=false"`
	Secret   bool   `param:"secret, required=false"`
	Restart  bool   `param:"restart, required=false"`
	Env      string `param:"env, required=false"`
}

func (*stringDirective) Name() string                    { return "string" }
func (*stringDirective) Mode() tagex.DirectiveMode       { return tagex.MutMode }
func (d *stringDirective) Handle(string) (string, error) { return d.Default, nil }

type intDirective struct {
	Default  int    `param:"default, required=false"`
	Required bool   `param:"required, required=false"`
	Secret   bool   `param:"secret, required=false"`
	Restart  bool   `param:"restart, required=false"`
	Env      string `param:"env, required=false"`
}

func (*intDirective) Name() string              { return "int" }
func (*intDirective) Mode() tagex.DirectiveMode { return tagex.MutMode }
func (d *intDirective) Handle(int) (int, error) { return d.Default, nil }

type boolDirective struct {
	Default  bool   `param:"default, required=false"`
	Required bool   `param:"required, required=false"`
	Secret   bool   `param:"secret, required=false"`
	Restart  bool   `param:"restart, required=false"`
	Env      string `param:"env, required=false"`
}

func (*boolDirective) Name() string                { return "bool" }
func (*boolDirective) Mode() tagex.DirectiveMode   { return tagex.MutMode }
func (d *boolDirective) Handle(bool) (bool, error) { return d.Default, nil }

type durationDirective struct {
	Default  string `param:"default, required=false"`
	Required bool   `param:"required, required=false"`
	Secret   bool   `param:"secret, required=false"`
	Restart  bool   `param:"restart, required=false"`
	Env      string `param:"env, required=false"`
}

func (*durationDirective) Name() string              { return "duration" }
func (*durationDirective) Mode() tagex.DirectiveMode { return tagex.MutMode }
func (d *durationDirective) Handle(time.Duration) (time.Duration, error) {
	if d.Default == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(d.Default)
	if err != nil {
		return 0, fmt.Errorf("invalid duration default")
	}
	return value, nil
}

var (
	cfgTag = newConfigTag()
	vals   = newValidationRegistry()

	schemaCache sync.Map
)

func newConfigTag() *tagex.Tag {
	tag := tagex.NewTag(cfgTagKey)
	tagex.MustRegisterDirective(tag, &stringDirective{})
	tagex.MustRegisterDirective(tag, &intDirective{})
	tagex.MustRegisterDirective(tag, &boolDirective{})
	tagex.MustRegisterDirective(tag, &durationDirective{})
	return tag
}

func newValidationRegistry() *valex.Registry {
	registry := valex.NewRegistry()
	valex.MustRegisterDirectiveTo(registry, &validators.IntRangeValidator{})
	valex.MustRegisterDirectiveTo(registry, &validators.PositiveDurationValidator{})
	valex.MustRegisterDirectiveTo(registry, &validators.MinLengthValidator{})
	return registry
}

// StructValidator handles cross-field invariants after the single Valex pass.
type StructValidator interface {
	ValidateConfig() error
}

func validate[T any](candidate *T) error {
	if err := vals.ValidateStruct(candidate); err != nil {
		return fmt.Errorf("validate config: %s", sanitizeValidationError(*candidate, err))
	}
	if validator, ok := any(candidate).(StructValidator); ok {
		if err := validator.ValidateConfig(); err != nil {
			return fmt.Errorf("validate config: %s", sanitizeValidationError(*candidate, err))
		}
		return nil
	}
	if validator, ok := any(*candidate).(StructValidator); ok {
		if err := validator.ValidateConfig(); err != nil {
			return fmt.Errorf("validate config: %s", sanitizeValidationError(*candidate, err))
		}
	}
	return nil
}

func sanitizeValidationError[T any](candidate T, validationErr error) string {
	message := validationErr.Error()
	fields, err := schemaFor[T]()
	if err != nil {
		return "candidate is invalid"
	}
	root := reflect.ValueOf(candidate)
	for _, field := range fields {
		if !field.secret {
			continue
		}
		value := fmt.Sprint(valueAt(root, field.index).Interface())
		if value != "" {
			message = strings.ReplaceAll(message, value, redacted)
		}
	}
	return message
}

func defaults[T any]() (T, []fieldSchema, error) {
	var candidate T
	fields, err := schemaFor[T]()
	if err != nil {
		return candidate, nil, err
	}
	if err := cfgTag.ProcessStruct(&candidate); err != nil {
		return candidate, nil, fmt.Errorf("apply cfg defaults: %w", err)
	}
	return candidate, fields, nil
}

func schemaFor[T any]() ([]fieldSchema, error) {
	typeOf := reflect.TypeFor[T]()
	if cached, ok := schemaCache.Load(typeOf); ok {
		result := cached.([]fieldSchema)
		return append([]fieldSchema(nil), result...), nil
	}
	if typeOf.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config schema must be a struct, got %s", typeOf)
	}
	fields, err := scanSchema(typeOf, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	schemaCache.Store(typeOf, fields)
	return append([]fieldSchema(nil), fields...), nil
}

func scanSchema(typeOf reflect.Type, goPath, filePath []string, index []int) ([]fieldSchema, error) {
	var out []fieldSchema
	for i := range typeOf.NumField() {
		field := typeOf.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldIndex := append(append([]int(nil), index...), i)
		fieldGoPath := append(append([]string(nil), goPath...), field.Name)
		fieldFilePath := append(append([]string(nil), filePath...), snakeCase(field.Name))
		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeFor[time.Duration]() {
			nested, err := scanSchema(field.Type, fieldGoPath, fieldFilePath, fieldIndex)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			continue
		}

		directive, args, err := parseConfigTag(field.Tag.Get(cfgTagKey))
		if err != nil {
			return nil, fmt.Errorf("config schema %s: %w", strings.Join(fieldGoPath, "."), err)
		}
		expectedDirective := directiveFor(field.Type)
		if expectedDirective == "" {
			return nil, fmt.Errorf("config schema %s: unsupported field type %s", strings.Join(fieldGoPath, "."), field.Type)
		}
		if directive == "" {
			directive = expectedDirective
		}
		if directive != expectedDirective {
			return nil, fmt.Errorf("config schema %s: directive %q does not match %s", strings.Join(fieldGoPath, "."), directive, field.Type)
		}
		hasDefault := false
		if _, ok := args["default"]; ok {
			hasDefault = true
		}
		required, err := boolArg(args, "required")
		if err != nil {
			return nil, fmt.Errorf("config schema %s: %w", strings.Join(fieldGoPath, "."), err)
		}
		secret, err := boolArg(args, "secret")
		if err != nil {
			return nil, fmt.Errorf("config schema %s: %w", strings.Join(fieldGoPath, "."), err)
		}
		restart, err := boolArg(args, "restart")
		if err != nil {
			return nil, fmt.Errorf("config schema %s: %w", strings.Join(fieldGoPath, "."), err)
		}
		if required && hasDefault {
			return nil, fmt.Errorf("config schema %s: required and default cannot be combined", strings.Join(fieldGoPath, "."))
		}
		if secret && hasDefault {
			return nil, fmt.Errorf("config schema %s: secret fields cannot define defaults", strings.Join(fieldGoPath, "."))
		}

		envName := ""
		if value := strings.TrimSpace(args["env"]); value != "" {
			envName = value
		}
		out = append(out, fieldSchema{
			path:        strings.Join(fieldGoPath, "."),
			fileName:    strings.Join(fieldFilePath, "."),
			envName:     envName,
			flagName:    strings.Join(kebabParts(fieldGoPath), "-"),
			index:       fieldIndex,
			typeOf:      field.Type,
			hasDefault:  hasDefault,
			defaultText: args["default"],
			required:    required,
			secret:      secret,
			restart:     restart,
		})
	}
	return out, nil
}

func parseConfigTag(raw string) (string, map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", map[string]string{}, nil
	}
	if strings.Contains(raw, ";") {
		return "", nil, fmt.Errorf("cfg supports one typed directive per field")
	}
	parts := strings.Split(raw, ",")
	directive := strings.TrimSpace(parts[0])
	args := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 || strings.TrimSpace(pair[0]) == "" {
			return "", nil, fmt.Errorf("malformed cfg parameter")
		}
		value := strings.TrimSpace(pair[1])
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
		key := strings.TrimSpace(pair[0])
		if _, exists := args[key]; exists {
			return "", nil, fmt.Errorf("duplicate cfg parameter %q", key)
		}
		args[key] = value
	}
	for key := range args {
		switch key {
		case "default", "required", "secret", "restart", "env":
		default:
			return "", nil, fmt.Errorf("unknown cfg parameter %q", key)
		}
	}
	return directive, args, nil
}

func boolArg(args map[string]string, name string) (bool, error) {
	raw, ok := args[name]
	if !ok {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

func directiveFor(typeOf reflect.Type) string {
	if typeOf == reflect.TypeFor[time.Duration]() {
		return "duration"
	}
	switch typeOf.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int:
		return "int"
	case reflect.Bool:
		return "bool"
	default:
		return ""
	}
}

func snakeCase(value string) string {
	words := splitWords(value)
	for i := range words {
		words[i] = strings.ToLower(words[i])
	}
	return strings.Join(words, "_")
}

func kebabParts(parts []string) []string {
	var out []string
	for _, part := range parts {
		words := splitWords(part)
		for _, word := range words {
			out = append(out, strings.ToLower(word))
		}
	}
	return out
}

func splitWords(value string) []string {
	runes := []rune(value)
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		boundary := unicode.IsUpper(runes[i]) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]))
		acronymEnd := unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if boundary || acronymEnd {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	if start < len(runes) {
		words = append(words, string(runes[start:]))
	}
	return words
}

func valueAt(value reflect.Value, index []int) reflect.Value {
	for _, part := range index {
		value = value.Field(part)
	}
	return value
}
