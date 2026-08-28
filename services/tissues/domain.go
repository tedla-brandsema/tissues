package tissues

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
)

func (s State) Valid() bool { return s == StateOpen || s == StateClosed }

// Issue is the one and only issue type. ParentID is canonical relationship
// data; Children and Comments are derived repository views.
type Issue struct {
	ID          string
	Title       string
	State       State
	Created     time.Time
	Updated     time.Time
	Description string
	ParentID    string
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
	if err := validLine("issue "+i.ID+": title", i.Title); err != nil {
		return err
	}
	if !i.State.Valid() {
		return fmt.Errorf("issue %s: invalid state %q", i.ID, i.State)
	}
	if i.ParentID != "" && !ValidID(i.ParentID) {
		return fmt.Errorf("issue %s: malformed parent ID %q", i.ID, i.ParentID)
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

func SortComments(cs []*Comment) {
	slices.SortFunc(cs, func(a, b *Comment) int {
		if !a.Created.Equal(b.Created) {
			return a.Created.Compare(b.Created)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func sortIssues(issues []*Issue) {
	slices.SortFunc(issues, func(a, b *Issue) int { return strings.Compare(a.ID, b.ID) })
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
