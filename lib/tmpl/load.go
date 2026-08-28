package tmpl

import (
	"html/template"
	"io/fs"
	"os"

	"github.com/tedla-brandsema/tissues/lib/fio"
)

var (
	Ext = ".tmpl"
)

func LoadDiskTemplates(dirs ...string) (*template.Template, error) {
	var fileSys []fs.FS
	for _, dir := range dirs {
		fileSys = append(fileSys, os.DirFS(dir))
	}
	return LoadFSTemplates(fileSys...)
}

func LoadFSTemplates(tmplFS ...fs.FS) (*template.Template, error) {
	var err error
	var tmpl *template.Template
	for _, fsFS := range tmplFS {
		if tmpl == nil {
			tmpl, err = CollectTemplates(fsFS)
			if err != nil {
				return nil, err
			}
			continue
		}
		// Else: add and override
		var coll *template.Template
		coll, err = CollectTemplates(fsFS)
		if err != nil {
			return nil, err
		}
		for _, view := range coll.Templates() {
			_, err = tmpl.AddParseTree(view.Name(), view.Tree)
			if err != nil {
				return nil, err
			}
		}
	}
	return tmpl, nil
}

func CollectTemplates(fsys fs.FS) (*template.Template, error) {
	files, err := fio.CollectFiles(fsys, ".", Ext)
	if err != nil {
		return nil, err
	}
	return template.ParseFS(fsys, files...)
}
