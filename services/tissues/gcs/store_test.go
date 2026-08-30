package gcs

import (
	"errors"
	"testing"

	"github.com/tedla-brandsema/tissues/services/tissues"
)

func TestObjectNameSeparatesProjectIssueAndFilename(t *testing.T) {
	tenantA := tissues.TenantID("7womw3jzkek74oggxj6f42xak4")
	tenantB := tissues.TenantID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	tests := []struct {
		tenant tissues.TenantID
		key    tissues.AssetKey
		want   string
	}{
		{tenantA, tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 17, Name: "photo.jpg"}, "tenants/7womw3jzkek74oggxj6f42xak4/issues/ALPHA/17/photo.jpg"},
		{tenantB, tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 17, Name: "photo.jpg"}, "tenants/aaaaaaaaaaaaaaaaaaaaaaaaaa/issues/ALPHA/17/photo.jpg"},
		{tenantA, tissues.AssetKey{ProjectKey: "BRAVO", IssueNumber: 17, Name: "photo.jpg"}, "tenants/7womw3jzkek74oggxj6f42xak4/issues/BRAVO/17/photo.jpg"},
		{tenantA, tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 18, Name: "photo.jpg"}, "tenants/7womw3jzkek74oggxj6f42xak4/issues/ALPHA/18/photo.jpg"},
		{tenantA, tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 17, Name: "diagram.png"}, "tenants/7womw3jzkek74oggxj6f42xak4/issues/ALPHA/17/diagram.png"},
	}
	for _, test := range tests {
		got, err := ObjectName(test.tenant, test.key)
		if err != nil || got != test.want {
			t.Errorf("ObjectName(%+v) = %q, %v; want %q", test.key, got, err, test.want)
		}
	}
}

func TestForTenantRejectsInvalidID(t *testing.T) {
	if _, err := (&Store{}).ForTenant("default"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("ForTenant error = %v", err)
	}
}

func TestIssuePrefixIsTenantBound(t *testing.T) {
	ref := tissues.IssueRef{ProjectKey: "FLUENT", Number: 1}
	a, err := issuePrefix("7womw3jzkek74oggxj6f42xak4", ref)
	if err != nil {
		t.Fatal(err)
	}
	b, err := issuePrefix("aaaaaaaaaaaaaaaaaaaaaaaaaa", ref)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a != "tenants/7womw3jzkek74oggxj6f42xak4/issues/FLUENT/1/" || b != "tenants/aaaaaaaaaaaaaaaaaaaaaaaaaa/issues/FLUENT/1/" {
		t.Fatalf("prefixes = %q / %q", a, b)
	}
}

func TestObjectNameRejectsUnsafeOrNoncanonicalIdentity(t *testing.T) {
	tenantID := tissues.TenantID("7womw3jzkek74oggxj6f42xak4")
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
		if _, err := ObjectName(tenantID, key); err == nil {
			t.Errorf("ObjectName(%+v) succeeded", key)
		}
	}
	if _, err := ObjectName("default", tissues.AssetKey{ProjectKey: "ALPHA", IssueNumber: 1, Name: "photo.jpg"}); err == nil {
		t.Fatal("invalid tenant accepted")
	}
}
