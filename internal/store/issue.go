package store

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
)

// IssueFile is the canonical filename of an issue document.
const IssueFile = "issue.md"

const issueMarker = "<!-- tissues:issue:v0 -->"

var issueTmpl = template.Must(template.New("issue").Parse(
	"# {{.Title}}\n" +
		"\n" +
		issueMarker + "\n" +
		"- **ID:** `{{.ID}}`\n" +
		"- **State:** {{.State}}\n" +
		"- **Created:** {{.Created}}\n" +
		"- **Updated:** {{.Updated}}\n" +
		"\n" +
		"---\n" +
		"\n" +
		"{{.Description}}\n"))

// RenderIssue writes the canonical issue.md document for i.
func RenderIssue(i *model.Issue) ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	if !ValidID(i.ID) {
		return nil, fmt.Errorf("issue %q: malformed ID", i.ID)
	}
	var buf bytes.Buffer
	err := issueTmpl.Execute(&buf, struct {
		Title, ID, State, Created, Updated, Description string
	}{
		Title:       i.Title,
		ID:          i.ID,
		State:       string(i.State),
		Created:     formatTime(i.Created),
		Updated:     formatTime(i.Updated),
		Description: i.Description,
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseIssue parses a canonical issue.md document. Parsing is strict and
// positional: anything unexpected is an error, never a repair.
//
// ParentID, Children and Comments are not part of the document; they are
// reconstructed from filesystem containment by Load.
func ParseIssue(data []byte) (*model.Issue, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 11 {
		return nil, fmt.Errorf("issue document too short: %d lines", len(lines))
	}
	title, ok := strings.CutPrefix(lines[0], "# ")
	if !ok {
		return nil, fmt.Errorf("line 1: expected %q heading", "# ")
	}
	if err := expectBlank(lines[1], 2); err != nil {
		return nil, err
	}
	if lines[2] != issueMarker {
		return nil, fmt.Errorf("line 3: expected marker %q, got %q", issueMarker, lines[2])
	}
	raw, err := metaValue(lines[3], 4, "ID")
	if err != nil {
		return nil, err
	}
	id, hasOpen := strings.CutPrefix(raw, "`")
	id, hasClose := strings.CutSuffix(id, "`")
	if !hasOpen || !hasClose || !ValidID(id) {
		return nil, fmt.Errorf("line 4: malformed ID %q", raw)
	}
	state, err := metaValue(lines[4], 5, "State")
	if err != nil {
		return nil, err
	}
	created, err := metaTime(lines[5], 6, "Created")
	if err != nil {
		return nil, err
	}
	updated, err := metaTime(lines[6], 7, "Updated")
	if err != nil {
		return nil, err
	}
	if err := expectBlank(lines[7], 8); err != nil {
		return nil, err
	}
	if lines[8] != "---" {
		return nil, fmt.Errorf("line 9: expected %q, got %q", "---", lines[8])
	}
	if err := expectBlank(lines[9], 10); err != nil {
		return nil, err
	}

	i := &model.Issue{
		ID:          id,
		Title:       title,
		State:       model.State(state),
		Created:     created,
		Updated:     updated,
		Description: strings.TrimSuffix(strings.Join(lines[10:], "\n"), "\n"),
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

func expectBlank(line string, n int) error {
	if line != "" {
		return fmt.Errorf("line %d: expected blank line, got %q", n, line)
	}
	return nil
}

// metaValue extracts the value of a canonical metadata line. The key is
// positional: a reordered document fails here.
func metaValue(line string, n int, key string) (string, error) {
	v, ok := strings.CutPrefix(line, "- **"+key+":** ")
	if !ok || v == "" {
		return "", fmt.Errorf("line %d: expected %q metadata, got %q", n, key, line)
	}
	return v, nil
}

func metaTime(line string, n int, key string) (time.Time, error) {
	v, err := metaValue(line, n, key)
	if err != nil {
		return time.Time{}, err
	}
	t, err := parseTime(v)
	if err != nil {
		return time.Time{}, fmt.Errorf("line %d: %s: %w", n, key, err)
	}
	return t, nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseTime accepts only RFC3339 UTC at second precision, i.e. exactly what
// formatTime produces. Offsets and sub-second precision are rejected.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("malformed timestamp %q", s)
	}
	t = t.UTC()
	if t.Format(time.RFC3339) != s {
		return time.Time{}, fmt.Errorf("timestamp %q is not RFC3339 UTC at second precision", s)
	}
	return t, nil
}
