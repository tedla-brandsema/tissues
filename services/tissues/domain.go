package tissues

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
)

func (s State) Valid() bool { return s == StateOpen || s == StateClosed }

// Project is the sole domain layer above Issue. NextIssueNumber is internal
// persistent allocator state and is intentionally omitted from API DTOs.
type Project struct {
	Key             string
	Created         time.Time
	NextIssueNumber int64
}

func CanonicalProjectKey(value string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(value))
	if len(key) < 1 || len(key) > 16 || key[0] < 'A' || key[0] > 'Z' {
		return "", fmt.Errorf("project key must match [A-Z][A-Z0-9]{0,15}")
	}
	for _, char := range key[1:] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return "", fmt.Errorf("project key must match [A-Z][A-Z0-9]{0,15}")
		}
	}
	return key, nil
}

func (p *Project) Validate() error {
	key, err := CanonicalProjectKey(p.Key)
	if err != nil || key != p.Key {
		return fmt.Errorf("project: invalid canonical key %q", p.Key)
	}
	if p.Created.IsZero() || p.Created.Location() != time.UTC {
		return fmt.Errorf("project %s: created timestamp must be non-zero UTC", p.Key)
	}
	if p.NextIssueNumber <= 0 {
		return fmt.Errorf("project %s: next issue number must be positive", p.Key)
	}
	return nil
}

// IssueRef is the canonical human issue reference.
type IssueRef struct {
	ProjectKey string
	Number     int64
}

func ParseIssueRef(value string) (IssueRef, error) {
	input := strings.TrimSpace(value)
	if input != strings.TrimSpace(input) {
		return IssueRef{}, fmt.Errorf("issue reference must not contain whitespace around its key or number")
	}
	if strings.Count(input, "-") != 1 {
		return IssueRef{}, fmt.Errorf("issue reference must be PROJECT-NUMBER")
	}
	parts := strings.SplitN(input, "-", 2)
	if parts[0] != strings.TrimSpace(parts[0]) {
		return IssueRef{}, fmt.Errorf("issue reference project key must not contain whitespace")
	}
	key, err := CanonicalProjectKey(parts[0])
	if err != nil {
		return IssueRef{}, fmt.Errorf("issue reference: %w", err)
	}
	if parts[1] == "" || (len(parts[1]) > 1 && parts[1][0] == '0') {
		return IssueRef{}, fmt.Errorf("issue reference number must be positive base-10 without leading zeros")
	}
	for _, char := range parts[1] {
		if char < '0' || char > '9' {
			return IssueRef{}, fmt.Errorf("issue reference number must be positive base-10")
		}
	}
	number, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || number <= 0 {
		return IssueRef{}, fmt.Errorf("issue reference number must be positive base-10")
	}
	return IssueRef{ProjectKey: key, Number: number}, nil
}

func (r IssueRef) String() string {
	if r.ProjectKey == "" || r.Number <= 0 {
		return ""
	}
	return r.ProjectKey + "-" + strconv.FormatInt(r.Number, 10)
}

func (r IssueRef) Validate() error {
	parsed, err := ParseIssueRef(r.String())
	if err != nil || parsed != r {
		return fmt.Errorf("invalid issue reference %q", r.String())
	}
	return nil
}

// Issue is the one and only issue type. ParentID is canonical relationship
// data; Ref and ParentRef are derived views; Children and Comments are derived
// repository views.
type Issue struct {
	ID          string
	ProjectKey  string
	Number      int64
	Ref         string
	Title       string
	State       State
	Created     time.Time
	Updated     time.Time
	Description string
	ParentID    string
	ParentRef   string
	Children    []*Issue
	Comments    []*Comment
}

type Comment struct {
	ID      string
	Author  string
	Created time.Time
	Updated time.Time
	Body    string
}

func Timestamp(t time.Time) time.Time { return t.UTC() }

func (i *Issue) Validate() error {
	if !ValidID(i.ID) {
		return fmt.Errorf("issue: malformed ID %q", i.ID)
	}
	key, err := CanonicalProjectKey(i.ProjectKey)
	if err != nil || key != i.ProjectKey {
		return fmt.Errorf("issue %s: invalid project key %q", i.ID, i.ProjectKey)
	}
	if i.Number <= 0 {
		return fmt.Errorf("issue %s: number must be positive", i.ID)
	}
	wantRef := (IssueRef{ProjectKey: i.ProjectKey, Number: i.Number}).String()
	if i.Ref != wantRef {
		return fmt.Errorf("issue %s: reference %q disagrees with %q", i.ID, i.Ref, wantRef)
	}
	if err := validLine("issue "+i.ID+": title", i.Title); err != nil {
		return err
	}
	if !i.State.Valid() {
		return fmt.Errorf("issue %s: invalid state %q", i.ID, i.State)
	}
	if i.ParentID != "" && !ValidID(i.ParentID) {
		return fmt.Errorf("issue %s: malformed parent ID %q", i.ID, i.ParentID)
	}
	if i.ParentRef != "" {
		parentRef, err := ParseIssueRef(i.ParentRef)
		if err != nil || parentRef.ProjectKey != i.ProjectKey {
			return fmt.Errorf("issue %s: invalid parent reference %q", i.ID, i.ParentRef)
		}
	}
	return validTimes("issue "+i.ID, i.Created, i.Updated)
}

func (c *Comment) Validate() error {
	if !ValidID(c.ID) {
		return fmt.Errorf("comment: malformed ID %q", c.ID)
	}
	if err := validLine("comment "+c.ID+": author", c.Author); err != nil {
		return err
	}
	if strings.TrimSpace(c.Body) == "" {
		return fmt.Errorf("comment %s: empty body", c.ID)
	}
	return validTimes("comment "+c.ID, c.Created, c.Updated)
}

func SortProjects(projects []*Project) {
	slices.SortFunc(projects, func(a, b *Project) int { return strings.Compare(a.Key, b.Key) })
}

func SortComments(cs []*Comment) {
	slices.SortFunc(cs, func(a, b *Comment) int {
		if !a.Created.Equal(b.Created) {
			return a.Created.Compare(b.Created)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func sortIssues(issues []*Issue) {
	slices.SortFunc(issues, func(a, b *Issue) int {
		if a.Number < b.Number {
			return -1
		}
		if a.Number > b.Number {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, issue := range issues {
		sortIssues(issue.Children)
		SortComments(issue.Comments)
	}
}

func validLine(what, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: empty", what)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s: must be a single line", what)
	}
	return nil
}

func validTimes(what string, created, updated time.Time) error {
	if created.IsZero() || created.Location() != time.UTC {
		return fmt.Errorf("%s: created timestamp must be non-zero UTC", what)
	}
	if updated.IsZero() || updated.Location() != time.UTC {
		return fmt.Errorf("%s: updated timestamp must be non-zero UTC", what)
	}
	if updated.Before(created) {
		return fmt.Errorf("%s: updated is before created", what)
	}
	return nil
}
