package fio

import (
	"io/fs"
	"path/filepath"
)

func CollectFiles(fsys fs.FS, path string, ext string) ([]string, error) {
	var filenames []string

	if path == "" {
		path = "."
	}

	err := fs.WalkDir(fsys, path, func(s string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if filepath.Ext(s) == ext {
			filenames = append(filenames, s)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return filenames, nil
}
