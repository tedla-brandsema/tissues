package tissues

import (
	"reflect"
	"testing"
	"time"
)

func TestCanonicalProjectKey(t *testing.T) {
	valid := map[string]string{"f": "F", " fluent ": "FLUENT", "abc123": "ABC123", "TISSUES": "TISSUES", "ABCDEFGHIJKLMNOP": "ABCDEFGHIJKLMNOP"}
	for input, want := range valid {
		got, err := CanonicalProjectKey(input)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "1ABC", "A B", "A_B", "A-B", "A/B", "ABCDEFGHIJKLMNOPQ", "Ä"} {
		if got, err := CanonicalProjectKey(input); err == nil {
			t.Errorf("%q accepted as %q", input, got)
		}
	}
}

func TestParseIssueRef(t *testing.T) {
	for _, input := range []string{"FLUENT-17", " fluent-17 "} {
		ref, err := ParseIssueRef(input)
		if err != nil || ref.ProjectKey != "FLUENT" || ref.Number != 17 || ref.String() != "FLUENT-17" {
			t.Errorf("%q = %#v, %v", input, ref, err)
		}
	}
	for _, input := range []string{"", "#", "#FLUENT-17", " #fluent-17 ", "FLUENT", "-1", "FLUENT-", "FLUENT-0", "FLUENT-01", "FLUENT--1", "FLUENT-1-more", "BAD_KEY-1", "1FLUENT-1", "##FLUENT-1", "# FLUENT-1", "FLUENT -1", "FLUENT-+1"} {
		if ref, err := ParseIssueRef(input); err == nil {
			t.Errorf("%q accepted as %s", input, ref)
		}
	}
}

func TestIssueTreesSortByNumber(t *testing.T) {
	issues := []*Issue{
		{Number: 10, Children: []*Issue{{Number: 4}, {Number: 3}}},
		{Number: 2},
	}
	sortIssues(issues)
	if issues[0].Number != 2 || issues[1].Number != 10 || issues[1].Children[0].Number != 3 || issues[1].Children[1].Number != 4 {
		t.Fatalf("numeric sort failed: %#v", issues)
	}
}

func TestIssueIdentityIsCanonicalRefOnly(t *testing.T) {
	typeOfIssue := reflect.TypeOf(Issue{})
	for _, removed := range []string{"ID", "ParentID"} {
		if _, exists := typeOfIssue.FieldByName(removed); exists {
			t.Fatalf("Issue still exposes removed field %s", removed)
		}
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	valid := Issue{ProjectKey: "FLUENT", Number: 17, Ref: "FLUENT-17", Title: "Issue", State: StateOpen, Created: now, Updated: now, Description: "body", ParentRef: "FLUENT-3"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []Issue{
		{ProjectKey: "fluent", Number: 17, Ref: "FLUENT-17", Title: "Issue", State: StateOpen, Created: now, Updated: now},
		{ProjectKey: "FLUENT", Number: 0, Ref: "FLUENT-0", Title: "Issue", State: StateOpen, Created: now, Updated: now},
		{ProjectKey: "FLUENT", Number: 17, Ref: "FLUENT-18", Title: "Issue", State: StateOpen, Created: now, Updated: now},
		{ProjectKey: "FLUENT", Number: 17, Ref: "FLUENT-17", Title: "Issue", State: StateOpen, Created: now, Updated: now, ParentRef: "fluent-3"},
		{ProjectKey: "FLUENT", Number: 17, Ref: "FLUENT-17", Title: "Issue", State: StateOpen, Created: now, Updated: now, ParentRef: "TISSUES-3"},
	}
	for i := range invalid {
		if err := invalid[i].Validate(); err == nil {
			t.Fatalf("invalid Issue %d accepted: %#v", i, invalid[i])
		}
	}
}

func TestCommentIdentityStillRequiresValidID(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	valid := Comment{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Author: "Ada", Created: now, Updated: now, Body: "body"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.ID = "FLUENT-1"
	if err := valid.Validate(); err == nil {
		t.Fatal("non-random Comment ID accepted")
	}
}
