package datastore

import (
	"context"
	"errors"
	"testing"
	"time"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestProjectIssueCommentKeyAncestry(t *testing.T) {
	s := &Store{namespace: "example"}
	project := s.projectKey("FLUENT")
	if project.Kind != ProjectKind || project.Name != "FLUENT" || project.Parent != nil || project.Namespace != "example" {
		t.Fatalf("project = %#v", project)
	}
	ref := tissues.IssueRef{ProjectKey: "FLUENT", Number: 17}
	issue := s.issueKey(ref)
	if issue.Kind != IssueKind || issue.Name != "FLUENT-17" || issue.Parent == nil || issue.Parent.Name != "FLUENT" || issue.Parent.Kind != ProjectKind {
		t.Fatalf("issue = %#v", issue)
	}
	comment := s.commentKey(ref, "bbbbbbbbbbbbbbbbbbbbbbbbbb")
	if comment.Kind != CommentKind || comment.Parent == nil || comment.Parent.Name != issue.Name || comment.Parent.Parent == nil || comment.Parent.Parent.Name != "FLUENT" {
		t.Fatalf("comment = %#v", comment)
	}
}

func TestEntityMappingAndNoIndex(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 123, time.UTC)
	project := &tissues.Project{Key: "FLUENT", Created: now, NextIssueNumber: 18}
	props, err := gcds.SaveStruct(encodeProject(project))
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, props, map[string]bool{"Created": false, "NextIssueNumber": false})
	if decoded := decodeProject(project.Key, encodeProject(project)); decoded.Key != "FLUENT" || decoded.NextIssueNumber != 18 || !decoded.Created.Equal(now) {
		t.Fatalf("project = %#v", decoded)
	}

	issue := &tissues.Issue{ProjectKey: "FLUENT", Number: 17, Ref: "FLUENT-17", Title: "title", State: tissues.StateOpen, Created: now, Updated: now, Description: "**markdown**", ParentRef: "FLUENT-3", Children: []*tissues.Issue{{Ref: "derived"}}, Comments: []*tissues.Comment{{ID: "derived"}}}
	props, err = gcds.SaveStruct(encodeIssue(issue))
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, props, map[string]bool{"Number": false, "Title": false, "State": false, "Created": false, "Updated": false, "Description": true, "ParentRef": false})
	decodedIssue := decodeIssue(tissues.IssueRef{ProjectKey: "FLUENT", Number: 17}, encodeIssue(issue))
	if decodedIssue.Number != 17 || decodedIssue.Ref != "FLUENT-17" || decodedIssue.ParentRef != "FLUENT-3" || len(decodedIssue.Children) != 0 {
		t.Fatalf("issue = %#v", decodedIssue)
	}
	comment := &tissues.Comment{ID: "cccccccccccccccccccccccccc", Author: "agent", Created: now, Updated: now, Body: "body"}
	props, err = gcds.SaveStruct(encodeComment(comment))
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, props, map[string]bool{"Author": false, "Created": false, "Updated": false, "Body": true})
}

func TestErrorTranslation(t *testing.T) {
	if !errors.Is(translate(gcds.ErrNoSuchEntity), tissues.ErrNotFound) {
		t.Fatal("no-such-entity not translated")
	}
	if !errors.Is(translate(gcds.ErrConcurrentTransaction), tissues.ErrConflict) {
		t.Fatal("concurrent transaction not translated")
	}
	if !errors.Is(translate(errors.New("provider detail")), tissues.ErrInternal) {
		t.Fatal("provider error not translated")
	}
}

func TestPagedQueriesRejectInvalidDatastoreCursors(t *testing.T) {
	store := &Store{namespace: "example"}
	request := tissues.PageRequest{Size: 25, Cursor: "%"}
	if _, err := store.ListProjectsPage(context.Background(), request); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("Project cursor = %v", err)
	}
	if _, err := store.ListIssueOverviewsPage(context.Background(), request); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("Issue cursor = %v", err)
	}
}

func assertProperties(t *testing.T, props []gcds.Property, want map[string]bool) {
	t.Helper()
	if len(props) != len(want) {
		t.Fatalf("properties=%#v", props)
	}
	for _, prop := range props {
		noindex, ok := want[prop.Name]
		if !ok || prop.NoIndex != noindex {
			t.Fatalf("property %#v, want noindex=%v", prop, noindex)
		}
	}
}
