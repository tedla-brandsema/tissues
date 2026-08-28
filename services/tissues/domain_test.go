package tissues

import "testing"

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
