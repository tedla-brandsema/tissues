package store

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// goldenIssue is the exact model that testdata/issue.md represents.
func goldenIssue() *model.Issue {
	return &model.Issue{
		ID:          "abcdefghijklmnopqrstuvwxyz",
		Title:       "Support nested issues",
		State:       model.StateOpen,
		Created:     ts("2026-08-23T13:20:11Z"),
		Updated:     ts("2026-08-23T14:02:44Z"),
		Description: "Issues should be able to contain child issues, represented by\nfilesystem containment.",
	}
}

// goldenComment is the exact model that testdata/comment.md represents.
func goldenComment() *model.Comment {
	return &model.Comment{
		ID:      "zyxwvutsrqponmlkjihgfedcba",
		Author:  "agent@example",
		Created: ts("2026-08-23T13:41:02Z"),
		Updated: ts("2026-08-23T13:41:02Z"),
		Body:    "Agreed. Containment is the whole point.",
	}
}

func checkIssue(t *testing.T, got, want *model.Issue) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Title != want.Title {
		t.Errorf("Title = %q, want %q", got.Title, want.Title)
	}
	if got.State != want.State {
		t.Errorf("State = %q, want %q", got.State, want.State)
	}
	if !got.Created.Equal(want.Created) {
		t.Errorf("Created = %v, want %v", got.Created, want.Created)
	}
	if !got.Updated.Equal(want.Updated) {
		t.Errorf("Updated = %v, want %v", got.Updated, want.Updated)
	}
	if got.Description != want.Description {
		t.Errorf("Description = %q, want %q", got.Description, want.Description)
	}
}

func checkComment(t *testing.T, got, want *model.Comment) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Author != want.Author {
		t.Errorf("Author = %q, want %q", got.Author, want.Author)
	}
	if !got.Created.Equal(want.Created) {
		t.Errorf("Created = %v, want %v", got.Created, want.Created)
	}
	if !got.Updated.Equal(want.Updated) {
		t.Errorf("Updated = %v, want %v", got.Updated, want.Updated)
	}
	if got.Body != want.Body {
		t.Errorf("Body = %q, want %q", got.Body, want.Body)
	}
}

func TestIssueGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/issue.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := RenderIssue(goldenIssue())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("canonical issue render changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	parsed, err := ParseIssue(want)
	if err != nil {
		t.Fatal(err)
	}
	checkIssue(t, parsed, goldenIssue())
}

func TestCommentGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/comment.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := RenderComment(goldenComment())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("canonical comment render changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	parsed, err := ParseComment(want)
	if err != nil {
		t.Fatal(err)
	}
	checkComment(t, parsed, goldenComment())
}

func TestIssueRoundTrip(t *testing.T) {
	base := goldenIssue()
	cases := map[string]*model.Issue{
		"ordinary": base,
		"empty description": {
			ID: base.ID, Title: "No description", State: model.StateOpen,
			Created: base.Created, Updated: base.Created, Description: "",
		},
		"multiline markdown": {
			ID: base.ID, Title: "Lists and code", State: model.StateClosed,
			Created: base.Created, Updated: base.Updated,
			Description: "- one\n- two\n\n```go\nfunc main() {}\n```\n\nTrailing paragraph.",
		},
		"unicode title": {
			ID: base.ID, Title: "Ondersteun geneste kwesties — ✅ 日本語", State: model.StateOpen,
			Created: base.Created, Updated: base.Updated,
			Description: "Ünïcode in de beschrijving ook. 🎉",
		},
		"description containing a rule": {
			ID: base.ID, Title: "Horizontal rules", State: model.StateOpen,
			Created: base.Created, Updated: base.Updated,
			Description: "before\n\n---\n\nafter\n\n---",
		},
		"description with trailing blank line": {
			ID: base.ID, Title: "Trailing newline", State: model.StateOpen,
			Created: base.Created, Updated: base.Updated,
			Description: "body\n",
		},
		"nanosecond timestamps": {
			ID: base.ID, Title: "Sub-second precision", State: model.StateOpen,
			Created:     ts("2026-08-23T13:20:11.123456789Z"),
			Updated:     ts("2026-08-23T13:20:11.2Z"),
			Description: "Fractional seconds round-trip exactly.",
		},
		"description that looks like metadata": {
			ID: base.ID, Title: "Metadata lookalike", State: model.StateOpen,
			Created: base.Created, Updated: base.Updated,
			Description: "- **ID:** `deadbeefdeadbeefdeadbeefde`\n- **State:** closed",
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := RenderIssue(want)
			if err != nil {
				t.Fatalf("RenderIssue: %v", err)
			}
			got, err := ParseIssue(data)
			if err != nil {
				t.Fatalf("ParseIssue: %v\ndocument:\n%s", err, data)
			}
			checkIssue(t, got, want)

			again, err := RenderIssue(got)
			if err != nil {
				t.Fatalf("RenderIssue (second): %v", err)
			}
			if string(again) != string(data) {
				t.Errorf("render is not stable across a round trip")
			}
		})
	}
}

func TestCommentRoundTrip(t *testing.T) {
	base := goldenComment()
	cases := map[string]*model.Comment{
		"ordinary": base,
		"multiline markdown": {
			ID: base.ID, Author: "t.brandsema@gmail.com",
			Created: base.Created, Updated: base.Updated,
			Body: "Two points:\n\n1. one\n2. two\n\n> quoted",
		},
		"unicode": {
			ID: base.ID, Author: "agent-één@example",
			Created: base.Created, Updated: base.Updated,
			Body: "Eens. 🐙 日本語",
		},
		"body containing a rule": {
			ID: base.ID, Author: base.Author,
			Created: base.Created, Updated: base.Updated,
			Body: "before\n\n---\n\nafter",
		},
		"nanosecond timestamps": {
			ID: base.ID, Author: base.Author,
			Created: ts("2026-08-23T13:41:02.000000001Z"),
			Updated: ts("2026-08-23T13:41:02.5Z"),
			Body:    "One nanosecond after the second.",
		},
		"edited later": {
			ID: base.ID, Author: base.Author,
			Created: base.Created, Updated: ts("2026-09-01T09:00:00Z"),
			Body: "Edited body.",
		},
		"body with trailing blank line": {
			ID: base.ID, Author: base.Author,
			Created: base.Created, Updated: base.Updated,
			Body: "body\n",
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := RenderComment(want)
			if err != nil {
				t.Fatalf("RenderComment: %v", err)
			}
			got, err := ParseComment(data)
			if err != nil {
				t.Fatalf("ParseComment: %v\ndocument:\n%s", err, data)
			}
			checkComment(t, got, want)

			again, err := RenderComment(got)
			if err != nil {
				t.Fatalf("RenderComment (second): %v", err)
			}
			if string(again) != string(data) {
				t.Errorf("render is not stable across a round trip")
			}
		})
	}
}

const validIssueDoc = "# Title\n" +
	"\n" +
	"<!-- tissues:issue:v0 -->\n" +
	"- **ID:** `abcdefghijklmnopqrstuvwxyz`\n" +
	"- **State:** open\n" +
	"- **Created:** 2026-08-23T13:20:11Z\n" +
	"- **Updated:** 2026-08-23T14:02:44Z\n" +
	"\n---\n\nBody.\n"

const validCommentDoc = "<!-- tissues:comment:v0 -->\n" +
	"- **Author:** agent@example\n" +
	"- **ID:** `zyxwvutsrqponmlkjihgfedcba`\n" +
	"- **Created:** 2026-08-23T13:41:02Z\n" +
	"- **Updated:** 2026-08-23T13:41:02Z\n" +
	"\n---\n\nBody.\n"

func TestParseIssueRejects(t *testing.T) {
	if _, err := ParseIssue([]byte(validIssueDoc)); err != nil {
		t.Fatalf("baseline document must parse: %v", err)
	}
	cases := map[string]string{
		"wrong marker":                    strings.Replace(validIssueDoc, "tissues:issue:v0", "tissues:issue:v1", 1),
		"missing marker":                  strings.Replace(validIssueDoc, "<!-- tissues:issue:v0 -->\n", "", 1),
		"comment marker":                  strings.Replace(validIssueDoc, "tissues:issue:v0", "tissues:comment:v0", 1),
		"reordered metadata":              strings.Replace(validIssueDoc, "- **ID:** `abcdefghijklmnopqrstuvwxyz`\n- **State:** open\n", "- **State:** open\n- **ID:** `abcdefghijklmnopqrstuvwxyz`\n", 1),
		"extra metadata":                  strings.Replace(validIssueDoc, "- **Updated:**", "- **Parent:** `abcdefghijklmnopqrstuvwxyz`\n- **Updated:**", 1),
		"invalid state":                   strings.Replace(validIssueDoc, "- **State:** open", "- **State:** in-progress", 1),
		"empty state":                     strings.Replace(validIssueDoc, "- **State:** open", "- **State:** ", 1),
		"malformed timestamp":             strings.Replace(validIssueDoc, "2026-08-23T13:20:11Z", "23-08-2026 13:20", 1),
		"non-utc timestamp":               strings.Replace(validIssueDoc, "2026-08-23T13:20:11Z", "2026-08-23T15:20:11+02:00", 1),
		"trailing zeros in fraction":      strings.Replace(validIssueDoc, "2026-08-23T13:20:11Z", "2026-08-23T13:20:11.500Z", 1),
		"zero fraction on a whole second": strings.Replace(validIssueDoc, "2026-08-23T13:20:11Z", "2026-08-23T13:20:11.0Z", 1),
		"updated before created":          strings.Replace(validIssueDoc, "- **Updated:** 2026-08-23T14:02:44Z", "- **Updated:** 2026-08-23T13:20:10Z", 1),
		"empty title":                     strings.Replace(validIssueDoc, "# Title\n", "# \n", 1),
		"blank title":                     strings.Replace(validIssueDoc, "# Title\n", "#    \n", 1),
		"no title heading":                strings.Replace(validIssueDoc, "# Title\n", "Title\n", 1),
		"malformed id":                    strings.Replace(validIssueDoc, "`abcdefghijklmnopqrstuvwxyz`", "`not-an-id`", 1),
		"unquoted id":                     strings.Replace(validIssueDoc, "`abcdefghijklmnopqrstuvwxyz`", "abcdefghijklmnopqrstuvwxyz", 1),
		"uppercase id":                    strings.Replace(validIssueDoc, "`abcdefghijklmnopqrstuvwxyz`", "`ABCDEFGHIJKLMNOPQRSTUVWXYZ`", 1),
		"missing blank line":              strings.Replace(validIssueDoc, "# Title\n\n<!--", "# Title\n<!--", 1),
		"missing rule":                    strings.Replace(validIssueDoc, "\n---\n", "\n\n", 1),
		"truncated":                       "# Title\n\n<!-- tissues:issue:v0 -->\n",
		"empty":                           "",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseIssue([]byte(doc)); err == nil {
				t.Errorf("ParseIssue accepted invalid document, got %+v\n%s", got, doc)
			}
		})
	}
}

func TestParseCommentRejects(t *testing.T) {
	if _, err := ParseComment([]byte(validCommentDoc)); err != nil {
		t.Fatalf("baseline document must parse: %v", err)
	}
	cases := map[string]string{
		"wrong marker":           strings.Replace(validCommentDoc, "tissues:comment:v0", "tissues:comment:v1", 1),
		"missing marker":         strings.Replace(validCommentDoc, "<!-- tissues:comment:v0 -->\n", "", 1),
		"issue marker":           strings.Replace(validCommentDoc, "tissues:comment:v0", "tissues:issue:v0", 1),
		"reordered metadata":     strings.Replace(validCommentDoc, "- **Author:** agent@example\n- **ID:** `zyxwvutsrqponmlkjihgfedcba`\n", "- **ID:** `zyxwvutsrqponmlkjihgfedcba`\n- **Author:** agent@example\n", 1),
		"empty author":           strings.Replace(validCommentDoc, "- **Author:** agent@example", "- **Author:** ", 1),
		"blank author":           strings.Replace(validCommentDoc, "- **Author:** agent@example", "- **Author:**    ", 1),
		"empty body":             strings.Replace(validCommentDoc, "\n---\n\nBody.\n", "\n---\n\n\n", 1),
		"blank body":             strings.Replace(validCommentDoc, "\n---\n\nBody.\n", "\n---\n\n   \n", 1),
		"malformed timestamp":    strings.Replace(validCommentDoc, "2026-08-23T13:41:02Z", "yesterday", 1),
		"non-utc timestamp":      strings.Replace(validCommentDoc, "- **Created:** 2026-08-23T13:41:02Z", "- **Created:** 2026-08-23T15:41:02+02:00", 1),
		"updated before created": strings.Replace(validCommentDoc, "- **Updated:** 2026-08-23T13:41:02Z", "- **Updated:** 2026-08-23T13:41:01Z", 1),
		"malformed id":           strings.Replace(validCommentDoc, "`zyxwvutsrqponmlkjihgfedcba`", "`0001`", 1),
		"missing rule":           strings.Replace(validCommentDoc, "\n---\n", "\n\n", 1),
		"truncated":              "<!-- tissues:comment:v0 -->\n",
		"empty":                  "",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseComment([]byte(doc)); err == nil {
				t.Errorf("ParseComment accepted invalid document, got %+v\n%s", got, doc)
			}
		})
	}
}

// The document never carries parentage: containment is the only source.
func TestIssueDocumentHasNoParentField(t *testing.T) {
	iss := goldenIssue()
	iss.ParentID = "deadbeefdeadbeefdeadbeefde"
	iss.Children = []*model.Issue{goldenIssue()}
	iss.Comments = []*model.Comment{goldenComment()}
	data, err := RenderIssue(iss)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), iss.ParentID) {
		t.Errorf("issue.md leaks parent ID:\n%s", data)
	}
	got, err := ParseIssue(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "" || got.Children != nil || got.Comments != nil {
		t.Errorf("parsed issue carries derived data: parent=%q children=%d comments=%d",
			got.ParentID, len(got.Children), len(got.Comments))
	}
}
