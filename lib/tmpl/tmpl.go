// Package tmpl provides functionality for managing and rendering HTML templates
// sourced from an fs.FS interface, with support for reloading and replacing templates.
package tmpl

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"

	"github.com/tedla-brandsema/tissues/lib/fio"
)

// FSTemplate represents a collection of HTML templates loaded from an fs.FS source.
// It supports reloading and dynamic replacement of individual templates.
// FSTemplate is not concurrency-safe; callers must synchronize reloads.
type FSTemplate struct {
	fs       fs.FS
	patterns []string
	cache    *template.Template
}

// Reload reloads the templates from the filesystem based on the configured patterns.
// This is useful for refreshing templates during runtime without restarting the application.
func (t *FSTemplate) Reload() {
	t.cache = template.Must(template.ParseFS(t.fs, t.patterns...))
}

// Replace replaces a named template within the FSTemplate's cache with a new template.
// If the template does not exist in the cache or the provided template is nil, an error is returned.
// Parameters:
//   - name: The name of the template to replace.
//   - tmpl: The new template to replace the existing one.
//
// Returns an error if the replacement fails.
func (t *FSTemplate) Replace(name string, tmpl *template.Template) error {
	if tmpl == nil {
		return errors.New("tmpl cannot be nil")
	}

	if lookup := t.cache.Lookup(name); lookup == nil {
		return fmt.Errorf("no template found with name %q", name)
	}

	tmpl, err := template.New(name).AddParseTree(name, tmpl.Tree.Copy())
	if err != nil {
		return err
	}

	t.Reload()
	t.cache = template.Must(Merge(t.cache, tmpl))

	return nil
}

// Tmpl returns the current parsed template cache. If the cache is nil, it reloads
// the templates from the filesystem before returning the cache.
// Returns:
//   - *template.Template: The current template cache.
func (t *FSTemplate) Tmpl() *template.Template {
	if t.cache == nil {
		t.Reload()
	}
	return t.cache
}

// FromExtension creates an FSTemplate by collecting all files in the filesystem
// with the specified file extension, parsing them as templates.
// Parameters:
//   - fsys: The filesystem to load templates from.
//   - ext: The file extension to filter template files.
//
// Returns:
//   - *FSTemplate: A new FSTemplate instance.
//   - error: An error if template parsing fails.
func FromExtension(fsys fs.FS, ext string) (*FSTemplate, error) {
	files, err := fio.CollectFiles(fsys, ".", ext)
	if err != nil {
		return nil, err
	}
	return FromPatterns(fsys, files...)
}

// FromPatterns creates an FSTemplate by matching and parsing files in the filesystem
// based on the provided patterns.
// Parameters:
//   - fsys: The filesystem to load templates from.
//   - patterns: Patterns to match template files.
//
// Returns:
//   - *FSTemplate: A new FSTemplate instance.
//   - error: An error if template parsing fails.
func FromPatterns(fsys fs.FS, patterns ...string) (*FSTemplate, error) {
	var err error

	t := &FSTemplate{
		fs:       fsys,
		patterns: patterns,
	}

	t.cache, err = template.ParseFS(t.fs, t.patterns...)
	if err != nil {
		return nil, err
	}

	return t, nil
}

// Render executes a template with the given name and data, writing the output as HTML.
// Parameters:
//   - tmpl: The template to render.
//   - name: The name of the template to execute.
//   - data: The data to pass to the template during execution.
//
// Returns:
//   - template.HTML: The rendered HTML as a safe HTML string.
func Render(tmpl *template.Template, name string, data any) template.HTML {
	if tmpl == nil {
		return template.HTML(fmt.Sprintf("template %q is nil", name))
	}
	var buffer bytes.Buffer
	err := tmpl.ExecuteTemplate(&buffer, name, data)
	if err != nil {
		return template.HTML(fmt.Sprintf("render execution err in template %q: %s", name, err.Error()))
	}
	return template.HTML(buffer.String())
}

// Merge combines multiple templates into a single template, preserving their individual parse trees.
// Parameters:
//   - tmpl: A variadic slice of templates to merge.
//
// Returns:
//   - *template.Template: A new template containing all merged templates.
//   - error: An error if any of the templates cannot be cloned.
func Merge(tmpl ...*template.Template) (*template.Template, error) {
	var tpl *template.Template
	for _, t := range tmpl {
		clone, err := t.Clone()
		if err != nil {
			return nil, err
		}

		if tpl == nil {
			tpl = clone
			continue
		}

		err = AddTmpl(tpl, clone)
		if err != nil {
			return nil, err
		}
	}
	return tpl, nil
}

func AddTmpl(dst, tpl *template.Template) error {
	for _, t := range tpl.Templates() {
		_, err := dst.AddParseTree(t.Name(), t.Tree.Copy())
		if err != nil {
			return err
		}
	}
	return nil
}

func TemplateAs(tpl *template.Template, as string) (*template.Template, error) {
	if tpl == nil {
		return nil, errors.New("template is nil")
	}
	return template.New(as).AddParseTree(as, tpl.Tree.Copy())
}

func FatalLookup(t *template.Template, name string) *template.Template {
	if t == nil {
		panic(errors.New("template is nil"))
	}
	lookup := t.Lookup(name)
	if lookup == nil {
		panic(fmt.Errorf("no template found with name %q", name))
	}
	return lookup
}
