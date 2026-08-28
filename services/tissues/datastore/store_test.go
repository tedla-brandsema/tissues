package datastore

import (
	"errors"
	"testing"
	"time"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestKeyShapesAndNamespace(t *testing.T) {
	s := &Store{namespace: "example"}
	issue := s.issueKey("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	if issue.Kind != IssueKind || issue.Name != "aaaaaaaaaaaaaaaaaaaaaaaaaa" || issue.Parent != nil || issue.Namespace != "example" {
		t.Fatalf("issue key=%#v", issue)
	}
	comment := s.commentKey(issue.Name, "bbbbbbbbbbbbbbbbbbbbbbbbbb")
	if comment.Kind != CommentKind || comment.Name != "bbbbbbbbbbbbbbbbbbbbbbbbbb" || comment.Parent == nil || comment.Parent.Kind != IssueKind || comment.Parent.Name != issue.Name || comment.Namespace != "example" {
		t.Fatalf("comment key=%#v", comment)
	}
}
func TestMappingPersistsCanonicalFieldsAndNoIndex(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	issue := &tissues.Issue{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "title", State: tissues.StateOpen, Created: now, Updated: now, Description: "**markdown**", ParentID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Children: []*tissues.Issue{{ID: "derived"}}, Comments: []*tissues.Comment{{ID: "derived"}}}
	props, err := gcds.SaveStruct(encodeIssue(issue))
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, props, map[string]bool{"Title": false, "State": false, "Created": false, "Updated": false, "Description": true, "ParentID": false})
	decoded := decodeIssue(issue.ID, encodeIssue(issue))
	if decoded.ID != issue.ID || decoded.Description != issue.Description || len(decoded.Children) != 0 || len(decoded.Comments) != 0 {
		t.Fatalf("decoded issue=%#v", decoded)
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
func assertProperties(t *testing.T, props []gcds.Property, want map[string]bool) {
	t.Helper()
	if len(props) != len(want) {
		t.Fatalf("properties=%#v", props)
	}
	for _, prop := range props {
		noindex, ok := want[prop.Name]
		if !ok || prop.NoIndex != noindex {
			t.Fatalf("property %#v,want noindex=%v", prop, noindex)
		}
	}
}
