package store

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/tedla-brandsema/tissues/internal/model"
)

const commentMarker = "<!-- tissues:comment:v0 -->"

var commentTmpl = template.Must(template.New("comment").Parse(
	commentMarker + "\n" +
		"- **Author:** {{.Author}}\n" +
		"- **ID:** `{{.ID}}`\n" +
		"- **Created:** {{.Created}}\n" +
		"- **Updated:** {{.Updated}}\n" +
		"\n" +
		"---\n" +
		"\n" +
		"{{.Body}}\n"))

// RenderComment writes the canonical document for a comment.
func RenderComment(c *model.Comment) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !ValidID(c.ID) {
		return nil, fmt.Errorf("comment %q: malformed ID", c.ID)
	}
	var buf bytes.Buffer
	err := commentTmpl.Execute(&buf, struct {
		Author, ID, Created, Updated, Body string
	}{
		Author:  c.Author,
		ID:      c.ID,
		Created: formatTime(c.Created),
		Updated: formatTime(c.Updated),
		Body:    c.Body,
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseComment parses a canonical comment document. Parsing is strict and
// positional.
func ParseComment(data []byte) (*model.Comment, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 9 {
		return nil, fmt.Errorf("comment document too short: %d lines", len(lines))
	}
	if lines[0] != commentMarker {
		return nil, fmt.Errorf("line 1: expected marker %q, got %q", commentMarker, lines[0])
	}
	author, err := metaValue(lines[1], 2, "Author")
	if err != nil {
		return nil, err
	}
	raw, err := metaValue(lines[2], 3, "ID")
	if err != nil {
		return nil, err
	}
	id, hasOpen := strings.CutPrefix(raw, "`")
	id, hasClose := strings.CutSuffix(id, "`")
	if !hasOpen || !hasClose || !ValidID(id) {
		return nil, fmt.Errorf("line 3: malformed ID %q", raw)
	}
	created, err := metaTime(lines[3], 4, "Created")
	if err != nil {
		return nil, err
	}
	updated, err := metaTime(lines[4], 5, "Updated")
	if err != nil {
		return nil, err
	}
	if err := expectBlank(lines[5], 6); err != nil {
		return nil, err
	}
	if lines[6] != "---" {
		return nil, fmt.Errorf("line 7: expected %q, got %q", "---", lines[6])
	}
	if err := expectBlank(lines[7], 8); err != nil {
		return nil, err
	}

	c := &model.Comment{
		ID:      id,
		Author:  author,
		Created: created,
		Updated: updated,
		Body:    strings.TrimSuffix(strings.Join(lines[8:], "\n"), "\n"),
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
