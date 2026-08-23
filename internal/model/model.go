// Package model defines the tissues domain: issues and comments.
//
// The model is independent of how issues are stored. Markdown serialization
// and filesystem layout live in internal/store.
package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// State is the lifecycle state of an issue. There are exactly two.
type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
)

func (s State) Valid() bool { return s == StateOpen || s == StateClosed }

// Timestamp normalizes t to the domain timestamp precision: UTC, whole
// seconds. Domain timestamps are facts recorded by tissues; they are never
// derived from file modification times, commit times, filenames or IDs.
func Timestamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// Issue is a single tissues issue.
//
// ParentID, Children and Comments are reconstructed from filesystem
// containment when the tree is loaded. They are never serialized into
// issue.md.
type Issue struct {
	ID          string
	Title       string
	State       State
	Created     time.Time
	Updated     time.Time
	Description string

	ParentID string
	Children []*Issue
	Comments []*Comment
}

// Comment is a single comment on an issue.
type Comment struct {
	ID      string
	Author  string
	Created time.Time
	Updated time.Time
	Body    string
}

func (i *Issue) Validate() error {
	if i.ID == "" {
		return errors.New("issue: empty ID")
	}
	if err := validLine("issue "+i.ID+": title", i.Title); err != nil {
		return err
	}
	if !i.State.Valid() {
		return fmt.Errorf("issue %s: invalid state %q", i.ID, i.State)
	}
	return validTimes("issue "+i.ID, i.Created, i.Updated)
}

func (c *Comment) Validate() error {
	if c.ID == "" {
		return errors.New("comment: empty ID")
	}
	if err := validLine("comment "+c.ID+": author", c.Author); err != nil {
		return err
	}
	if strings.TrimSpace(c.Body) == "" {
		return fmt.Errorf("comment %s: empty body", c.ID)
	}
	return validTimes("comment "+c.ID, c.Created, c.Updated)
}

// SortComments orders comments for presentation: Created ASC, then ID ASC.
// Updated is deliberately not consulted, so editing a comment never moves it.
func SortComments(cs []*Comment) {
	slices.SortFunc(cs, func(a, b *Comment) int {
		if !a.Created.Equal(b.Created) {
			return a.Created.Compare(b.Created)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func validLine(what, s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s: empty", what)
	}
	if strings.ContainsAny(s, "\n\r") {
		return fmt.Errorf("%s: must be a single line", what)
	}
	return nil
}

func validTimes(what string, created, updated time.Time) error {
	for _, ts := range []struct {
		name string
		t    time.Time
	}{{"created", created}, {"updated", updated}} {
		if ts.t.IsZero() {
			return fmt.Errorf("%s: zero %s timestamp", what, ts.name)
		}
		if ts.t.Location() != time.UTC || !ts.t.Equal(ts.t.Truncate(time.Second)) {
			return fmt.Errorf("%s: %s timestamp must be UTC at second precision", what, ts.name)
		}
	}
	if updated.Before(created) {
		return fmt.Errorf("%s: updated is before created", what)
	}
	return nil
}
