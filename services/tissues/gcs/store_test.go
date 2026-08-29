package gcs

import (
	"testing"

	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestObjectNameSeparatesProjectIssueAndFilename(t *testing.T) {
	tests := []struct {
		key  tissues.AssetKey
		want string
	}{
		{tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 17, Name: "photo.jpg"}, "issues/ALPHA/17/photo.jpg"},
		{tissues.AssetKey{ProjectKey: "BRAVO", IssueNumber: 17, Name: "photo.jpg"}, "issues/BRAVO/17/photo.jpg"},
		{tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 18, Name: "photo.jpg"}, "issues/ALPHA/18/photo.jpg"},
		{tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 17, Name: "diagram.png"}, "issues/ALPHA/17/diagram.png"},
	}
	for _, test := range tests {
		got, err := ObjectName(test.key)
		if err != nil || got != test.want {
			t.Errorf("ObjectName(%+v) = %q, %v; want %q", test.key, got, err, test.want)
		}
	}
}

func TestObjectNameRejectsUnsafeOrNoncanonicalIdentity(t *testing.T) {
	for _, key := range []tissues.AssetKey{
		{ProjectKey: "alpha", IssueNumber: 1, Name: "photo.jpg"},
		{ProjectKey: "ALPHA", IssueNumber: 0, Name: "photo.jpg"},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: "Photo.jpg"},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: "photo.jpeg"},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: "../photo.jpg"},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: `dir\photo.jpg`},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: "photo name.jpg"},
		{ProjectKey: "ALPHA", IssueNumber: 1, Name: "photo\n.jpg"},
	} {
		if _, err := ObjectName(key); err == nil {
			t.Errorf("ObjectName(%+v) succeeded", key)
		}
	}
}
