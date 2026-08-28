package tmpl

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/tedla-brandsema/tissues/lib/fio"
)

func TestLoadDiskTemplates(t *testing.T) {
	// Create a temporary directory structure with template files
	tempDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tempDir, "test1.tmpl"), []byte("{{define \"test1\"}}Hello, World{{end}}"), 0644)
	if err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "test2.tmpl"), []byte("{{define \"test2\"}}Hello, {{.Name}}{{end}}"), 0644)
	if err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}

	// Call LoadDiskTemplates
	tmpl, err := LoadDiskTemplates(tempDir)
	if err != nil {
		t.Fatalf("LoadDiskTemplates failed: %v", err)
	}

	// Verify the templates are loaded
	if tmpl.Lookup("test1") == nil {
		t.Error("Template 'test1' not found")
	}
	if tmpl.Lookup("test2") == nil {
		t.Error("Template 'test2' not found")
	}
}

func TestLoadDiskTemplatesWithErrors(t *testing.T) {
	// Call LoadDiskTemplates with a non-existent directory
	_, err := LoadDiskTemplates("nonexistent-dir")
	if err == nil {
		t.Fatal("Expected error for non-existent directory, got nil")
	}
}

func TestLoadFSTemplates(t *testing.T) {
	// Create an in-memory file system with templates
	fsys := fstest.MapFS{
		"test1.tmpl": {Data: []byte("{{define \"test1\"}}Content for test1{{end}}")},
		"test2.tmpl": {Data: []byte("{{define \"test2\"}}Content for test2{{end}}")},
	}

	// Test LoadFSTemplates
	tmpl, err := LoadFSTemplates(fsys)
	if err != nil {
		t.Fatalf("LoadFSTemplates failed: %v", err)
	}

	// Validate templates
	if tmpl.Lookup("test1") == nil {
		t.Errorf("Template 'test1' not found")
	}
	if tmpl.Lookup("test2") == nil {
		t.Errorf("Template 'test2' not found")
	}
}

func TestCollectTemplates(t *testing.T) {
	// Create an in-memory file system with templates
	fsys := fstest.MapFS{
		"template1.tmpl": {Data: []byte("{{define \"template1\"}}Template1 Content{{end}}")},
	}

	// Test CollectTemplates
	tmpl, err := CollectTemplates(fsys)
	if err != nil {
		t.Fatalf("CollectTemplates failed: %v", err)
	}

	// Validate templates
	if tmpl.Lookup("template1") == nil {
		t.Errorf("Template 'template1' not found")
	}
}

func TestCollectFiles(t *testing.T) {
	// Create an in-memory file system with mixed files
	fsys := fstest.MapFS{
		"file1.tmpl": {Data: []byte("Content for file1")},
		"file2.tmpl": {Data: []byte("Content for file2")},
		"file3.txt":  {Data: []byte("Content for file3")},
	}

	// Test CollectFiles
	files, err := fio.CollectFiles(fsys, ".", ".tmpl")
	if err != nil {
		t.Fatalf("CollectFiles failed: %v", err)
	}

	// Validate file collection
	expected := map[string]bool{
		"file1.tmpl": true,
		"file2.tmpl": true,
	}
	if len(files) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(files))
	}
	for _, file := range files {
		if !expected[file] {
			t.Errorf("Unexpected file collected: %s", file)
		}
	}
}

func TestLoadFSTemplatesWithErrors(t *testing.T) {
	// Create an in-memory file system with no valid templates
	fsys := fstest.MapFS{
		"invalid.txt": {Data: []byte("Not a template")},
	}

	// Test LoadFSTemplates
	_, err := LoadFSTemplates(fsys)
	if err == nil {
		t.Fatal("Expected error for fs.FS with no valid template files, got nil")
	}
}

func TestCollectFilesWithSubdirectories(t *testing.T) {
	// Create an in-memory file system with nested directories and files
	fsys := fstest.MapFS{
		"file1.tmpl":                 {Data: []byte("Content for file1")},
		"subdir/file2.tmpl":          {Data: []byte("Content for file2")},
		"subdir/nested/file3.tmpl":   {Data: []byte("Content for file3")},
		"subdir/nested/file4.txt":    {Data: []byte("Content for file4 (not a template)")},
		"subdir/nested2/deeper.tmpl": {Data: []byte("Content for deeper template")},
		"unrelated-file.txt":         {Data: []byte("Content for unrelated file")},
	}

	// Test CollectFiles
	files, err := fio.CollectFiles(fsys, ".", ".tmpl")
	if err != nil {
		t.Fatalf("CollectFiles failed: %v", err)
	}

	// Validate file collection
	expected := map[string]bool{
		"file1.tmpl":                 true,
		"subdir/file2.tmpl":          true,
		"subdir/nested/file3.tmpl":   true,
		"subdir/nested2/deeper.tmpl": true,
	}
	if len(files) != len(expected) {
		t.Fatalf("Expected %d files, got %d", len(expected), len(files))
	}
	for _, file := range files {
		if !expected[file] {
			t.Errorf("Unexpected file collected: %s", file)
		}
	}
}

func TestLoadFSTemplatesWithSubdirectories(t *testing.T) {
	// Create an in-memory file system with nested directories and templates
	fsys := fstest.MapFS{
		"root.tmpl":                 {Data: []byte("{{define \"root\"}}Root Content{{end}}")},
		"subdir/inner1.tmpl":        {Data: []byte("{{define \"inner1\"}}Inner1 Content{{end}}")},
		"subdir/nested/inner2.tmpl": {Data: []byte("{{define \"inner2\"}}Inner2 Content{{end}}")},
	}

	// Test LoadFSTemplates
	tmpl, err := LoadFSTemplates(fsys)
	if err != nil {
		t.Fatalf("LoadFSTemplates failed: %v", err)
	}

	// Validate templates
	if tmpl.Lookup("root") == nil {
		t.Errorf("Template 'root' not found")
	}
	if tmpl.Lookup("inner1") == nil {
		t.Errorf("Template 'inner1' not found")
	}
	if tmpl.Lookup("inner2") == nil {
		t.Errorf("Template 'inner2' not found")
	}
}
