package tmpl

import (
	"crypto"
	"encoding/hex"
	"html/template"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const bodiesTmplName = "bodies.tmpl"

var bodiesTmplLiteral = `
{{ define "hello" -}}
<body>
	<h1>Hello, world!</h1>
</body>
{{- end }}

{{ define "goodbye" -}}
<body>
	<h1>Goodbye, world!</h1>
</body>
{{- end }}
`

const baseTmplName = "base.tmpl"

var baseTmplLiteral = `
{{ define "head" -}}
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>Template Test</title>
</head>
{{- end }}

{{ define "body" -}}
<body>
	<h1>Placeholder</h1>
</body>
{{- end }}

{{ define "index" -}}
<!doctype html>
<html lang="en">
{{ template "head" }}
{{ template "body" }}
</html>
{{- end }}
`

var (
	tplFS         fstest.MapFS
	tmplNamesWant []string
	tmplCountWant int
)

func before() {
	tplFS = fstest.MapFS{
		baseTmplName:   {Data: []byte(baseTmplLiteral)},
		bodiesTmplName: {Data: []byte(bodiesTmplLiteral)},
	}

	tmplNamesWant = []string{
		"base.tmpl",
		"bodies.tmpl",
		"head",
		"body",
		"index",
		"hello",
		"goodbye",
	}
	tmplCountWant = len(tmplNamesWant)
}

func TestFromExtension(t *testing.T) {
	before()
	tmpl, err := FromExtension(tplFS, Ext)
	if err != nil {
		t.Fatalf("error parsing filesystem templates: %v", err)
	}
	verifyTmplNamesAndCount(t, tmpl.Tmpl())
}

func TestFromPattern(t *testing.T) {
	before()
	tmpl, err := FromPatterns(tplFS, "*.tmpl")
	if err != nil {
		t.Fatalf("error parsing filesystem templates: %v", err)
	}
	verifyTmplNamesAndCount(t, tmpl.Tmpl())
}

func verifyTmplNamesAndCount(t *testing.T, tmpl *template.Template) {
	if countGot := len(tmpl.Templates()); countGot != tmplCountWant {
		t.Fatalf("template len missmatch: got %d, want %d", countGot, tmplCountWant)
	}

	for _, tpl := range tmpl.Templates() {
		i := slices.Index(tmplNamesWant, tpl.Name())
		if i == -1 {
			t.Fatalf("template name %s is not contained in controll", tpl.Name())
		}
		tmplNamesWant = slices.Delete(tmplNamesWant, i, i+1)
	}

	if len(tmplNamesWant) != 0 {
		t.Fatalf("missing templates: %s", tmplNamesWant)
	}
}

func TestReplace(t *testing.T) {
	before()

	var err error

	type testCase struct {
		name          string
		replaceTarget string
		templateName  string
		expectedHash  string
	}

	templates, err := FromExtension(tplFS, Ext)
	if err != nil {
		t.Fatalf("failed to parse templates: %v", err)
	}

	testCases := []testCase{
		{
			name:          "Base FSTemplate",
			replaceTarget: "",
			templateName:  "index",
			expectedHash:  "470c11f20c03978fe82d46d0b91b40479716ab148e853d77cc387488c420754f",
		},
		{
			name:          "Hello FSTemplate",
			replaceTarget: "body",
			templateName:  "hello",
			expectedHash:  "aafbb714ee1c5da69c6ae03ae73b563ac4c331a37ecc0eae74b3e5eba9fd7f34",
		},
		{
			name:          "Goodbye FSTemplate",
			replaceTarget: "body",
			templateName:  "goodbye",
			expectedHash:  "4af828cbbbfda09bab074124f682bfe743bb71868943b281f907ab8b2e51b0b0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.replaceTarget != "" {
				err = templates.Replace(tc.replaceTarget, templates.Tmpl().Lookup(tc.templateName))
				if err != nil {
					t.Fatalf("failed to supplant template %s: %v", tc.templateName, err)
				}
			}

			html := Render(templates.Tmpl(), "index", nil)
			actualHash := hash([]byte(html))

			if actualHash != tc.expectedHash {
				t.Errorf("hash mismatch for %s: got %s, want %s", tc.name, actualHash, tc.expectedHash)
			}
		})
	}
}

func TestReload(t *testing.T) {
	before()

	tmpl, err := FromExtension(tplFS, Ext)
	if err != nil {
		t.Fatalf("failed to parse templates: %v", err)
	}

	html := Render(tmpl.Tmpl(), "index", nil)

	t.Log(html)

	f := tplFS["base.tmpl"]
	f.Data = []byte(strings.Replace(baseTmplLiteral, "Placeholder", "CHANGED", 1))

	tmpl.Reload()
	html = Render(tmpl.Tmpl(), "index", nil)

	t.Log(html)
}

func hash(b []byte) string {
	h := crypto.SHA256.New()
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
