package firestore

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	gcfirestore "cloud.google.com/go/firestore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	tenantA = tissues.TenantID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	tenantB = tissues.TenantID("bbbbbbbbbbbbbbbbbbbbbbbbbb")
	refA    = tissues.IssueRef{ProjectKey: "ALPHA", Number: 17}
	base    = time.Date(2026, 8, 30, 12, 0, 0, 123, time.UTC)
)

func TestTenantPathsAndDocumentIDs(t *testing.T) {
	client, err := gcfirestore.NewClientWithDatabase(context.Background(), "test-project", "test-native", option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	root, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	boundValue, err := root.ForTenant(tenantA)
	if err != nil {
		t.Fatal(err)
	}
	bound := boundValue.(*TenantStore)
	commentID := "cccccccccccccccccccccccccc"
	want := map[string]string{
		"tenant":  "tenants/" + tenantA.String(),
		"project": "tenants/" + tenantA.String() + "/projects/ALPHA",
		"issue":   "tenants/" + tenantA.String() + "/issues/ALPHA-17",
		"comment": "tenants/" + tenantA.String() + "/comments/ALPHA-17~" + commentID,
	}
	got := map[string]string{
		"tenant":  relativePath(bound.tenant.Path),
		"project": relativePath(bound.projectRef("ALPHA").Path),
		"issue":   relativePath(bound.issueRef(refA).Path),
		"comment": relativePath(bound.commentRef(refA, commentID).Path),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	if _, err := root.ForTenant("default"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("invalid tenant = %v", err)
	}
}

func relativePath(path string) string {
	const marker = "/documents/"
	for i := 0; i+len(marker) <= len(path); i++ {
		if path[i:i+len(marker)] == marker {
			return path[i+len(marker):]
		}
	}
	return path
}

func TestEntityRoundTripsPreserveNanosecondsAndRevision(t *testing.T) {
	project := &tissues.Project{Key: "ALPHA", Created: base, NextIssueNumber: 18}
	projectRoundTrip, err := decodeProject("ALPHA", encodeProject(project))
	if err != nil || !reflect.DeepEqual(projectRoundTrip, project) {
		t.Fatalf("Project round trip = %#v, %v", projectRoundTrip, err)
	}
	issue := &tissues.Issue{ProjectKey: "ALPHA", Number: 17, Ref: "ALPHA-17", Title: "Nanoseconds", State: tissues.StateOpen, Created: base, Updated: base.Add(time.Nanosecond), Description: "body", ParentRef: "ALPHA-16"}
	issueRoundTrip, revision, err := decodeIssue("ALPHA-17", encodeIssue(issue, 41))
	if err != nil || revision != 41 || !reflect.DeepEqual(issueRoundTrip, issue) {
		t.Fatalf("Issue round trip = %#v, revision %d, %v", issueRoundTrip, revision, err)
	}
	comment := &tissues.Comment{ID: "cccccccccccccccccccccccccc", Author: "Ada", Created: base.Add(time.Nanosecond), Updated: base.Add(2 * time.Nanosecond), Body: "exact"}
	gotRef, commentRoundTrip, err := decodeComment(commentDocumentID(refA, comment.ID), encodeComment(refA, comment))
	if err != nil || gotRef != refA || !reflect.DeepEqual(commentRoundTrip, comment) {
		t.Fatalf("Comment round trip = %s %#v, %v", gotRef, commentRoundTrip, err)
	}
	if commentRoundTrip.Created.UnixNano() != base.UnixNano()+1 || commentRoundTrip.Updated.UnixNano() != base.UnixNano()+2 {
		t.Fatalf("nanoseconds truncated: %#v", commentRoundTrip)
	}
}

func TestCommentDocumentID(t *testing.T) {
	id := "cccccccccccccccccccccccccc"
	encoded := commentDocumentID(refA, id)
	if encoded != "ALPHA-17~"+id {
		t.Fatalf("encoded = %q", encoded)
	}
	ref, decodedID, err := parseCommentDocumentID(encoded)
	if err != nil || ref != refA || decodedID != id {
		t.Fatalf("decoded = %v %q, %v", ref, decodedID, err)
	}
	for _, malformed := range []string{"", "ALPHA-17", "ALPHA-17~bad", "ALPHA-17~" + id + "~extra"} {
		if _, _, err := parseCommentDocumentID(malformed); err == nil {
			t.Fatalf("accepted %q", malformed)
		}
	}
}

func TestCursorBindingsAndResumeValues(t *testing.T) {
	projectCursor := encodeCursor(cursorPayload{Version: cursorVersion, TenantID: tenantA.String(), Kind: projectCursorKind, ProjectKey: "ALPHA"})
	project, err := decodeCursor(tenantA, projectCursorKind, "", projectCursor)
	if err != nil || project.ProjectKey != "ALPHA" {
		t.Fatalf("Project cursor = %#v, %v", project, err)
	}
	issueCursor := encodeCursor(cursorPayload{Version: cursorVersion, TenantID: tenantA.String(), Kind: issueCursorKind, Filter: "ALPHA", UpdatedNS: base.UnixNano(), ProjectKey: "ALPHA", Number: 17})
	issue, err := decodeCursor(tenantA, issueCursorKind, "ALPHA", issueCursor)
	if err != nil || issue.UpdatedNS != base.UnixNano() || issue.ProjectKey != "ALPHA" || issue.Number != 17 {
		t.Fatalf("Issue cursor = %#v, %v", issue, err)
	}
	for name, test := range map[string]struct {
		tenant tissues.TenantID
		kind   string
		filter string
		cursor string
	}{
		"cross tenant":  {tenantB, issueCursorKind, "ALPHA", issueCursor},
		"wrong query":   {tenantA, projectCursorKind, "", issueCursor},
		"no filter":     {tenantA, issueCursorKind, "", issueCursor},
		"other filter":  {tenantA, issueCursorKind, "BRAVO", issueCursor},
		"malformed":     {tenantA, issueCursorKind, "ALPHA", "not-base64!"},
		"old version":   {tenantA, issueCursorKind, "ALPHA", encodeCursor(cursorPayload{Version: 0, TenantID: tenantA.String(), Kind: issueCursorKind, Filter: "ALPHA", ProjectKey: "ALPHA", Number: 17})},
		"unknown field": {tenantA, issueCursorKind, "ALPHA", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"tenant":"aaaaaaaaaaaaaaaaaaaaaaaaaa","query":"issue-overviews","filter":"ALPHA","project_key":"ALPHA","number":17,"extra":true}`))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCursor(test.tenant, test.kind, test.filter, test.cursor); !errors.Is(err, tissues.ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestStoredIdentityAndRevisionCorruption(t *testing.T) {
	project := projectEntity{Key: "ALPHA", CreatedNS: base.UnixNano(), NextIssueNumber: 1}
	if _, err := decodeProject("BRAVO", project); !errors.Is(err, tissues.ErrInternal) {
		t.Fatalf("Project mismatch = %v", err)
	}
	validIssue := issueEntity{ProjectKey: "ALPHA", Number: 17, Ref: "ALPHA-17", Title: "Issue", State: string(tissues.StateOpen), CreatedNS: base.UnixNano(), UpdatedNS: base.UnixNano(), Description: "body"}
	issueCases := map[string]issueEntity{
		"wrong document ID": validIssue,
		"wrong ref":         withIssue(validIssue, func(e *issueEntity) { e.Ref = "ALPHA-18" }),
		"wrong project":     withIssue(validIssue, func(e *issueEntity) { e.ProjectKey = "BRAVO" }),
		"wrong number":      withIssue(validIssue, func(e *issueEntity) { e.Number = 18 }),
		"bad parent":        withIssue(validIssue, func(e *issueEntity) { e.ParentRef = "alpha-1" }),
		"negative revision": withIssue(validIssue, func(e *issueEntity) { e.CommentOrderRevision = -1 }),
	}
	for name, entity := range issueCases {
		docID := "ALPHA-17"
		if name == "wrong document ID" {
			docID = "ALPHA-18"
		}
		if _, _, err := decodeIssue(docID, entity); !errors.Is(err, tissues.ErrInternal) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	validComment := commentEntity{ProjectKey: "ALPHA", IssueRef: "ALPHA-17", CommentID: "cccccccccccccccccccccccccc", Author: "Ada", CreatedNS: base.UnixNano(), UpdatedNS: base.UnixNano(), Body: "body"}
	commentCases := map[string]commentEntity{
		"wrong issue":   withComment(validComment, func(e *commentEntity) { e.IssueRef = "ALPHA-18" }),
		"wrong project": withComment(validComment, func(e *commentEntity) { e.ProjectKey = "BRAVO" }),
		"wrong ID":      withComment(validComment, func(e *commentEntity) { e.CommentID = "dddddddddddddddddddddddddd" }),
	}
	for name, entity := range commentCases {
		if _, _, err := decodeComment("ALPHA-17~cccccccccccccccccccccccccc", entity); !errors.Is(err, tissues.ErrInternal) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	if _, _, err := decodeComment("ALPHA-17~dddddddddddddddddddddddddd", validComment); !errors.Is(err, tissues.ErrInternal) {
		t.Fatalf("wrong Comment document ID = %v", err)
	}
	if _, err := checkedRevision(math.MaxInt64); !errors.Is(err, tissues.ErrInternal) {
		t.Fatalf("overflow = %v", err)
	}
	if revision, err := issueWriteRevision(issueReadState{}); err != nil || revision != 0 {
		t.Fatalf("new Issue revision = %d, %v", revision, err)
	}
	if revision, err := issueWriteRevision(issueReadState{exists: true, revision: 41}); err != nil || revision != 42 {
		t.Fatalf("existing Issue revision = %d, %v", revision, err)
	}
}

func TestIssueOverviewRelationshipValidation(t *testing.T) {
	project := &tissues.Project{Key: "ALPHA", Created: base, NextIssueNumber: 18}
	parent := &tissues.Issue{ProjectKey: "ALPHA", Number: 16, Ref: "ALPHA-16", Title: "Parent", State: tissues.StateOpen, Created: base, Updated: base}
	child := &tissues.Issue{ProjectKey: "ALPHA", Number: 17, Ref: "ALPHA-17", Title: "Child", State: tissues.StateOpen, Created: base, Updated: base, ParentRef: parent.Ref}
	withoutParent := *child
	withoutParent.ParentRef = ""

	for _, test := range []struct {
		name     string
		issues   []*tissues.Issue
		projects map[string]*tissues.Project
		parents  map[string]*tissues.Issue
		wantErr  bool
	}{
		{name: "valid Project without parent", issues: []*tissues.Issue{&withoutParent}, projects: map[string]*tissues.Project{"ALPHA": project}},
		{name: "valid Project and parent", issues: []*tissues.Issue{child}, projects: map[string]*tissues.Project{"ALPHA": project}, parents: map[string]*tissues.Issue{"ALPHA-16": parent}},
		{name: "missing Project", issues: []*tissues.Issue{child}, parents: map[string]*tissues.Issue{"ALPHA-16": parent}, wantErr: true},
		{name: "corrupt Project", issues: []*tissues.Issue{child}, projects: map[string]*tissues.Project{"ALPHA": {Key: "ALPHA"}}, parents: map[string]*tissues.Issue{"ALPHA-16": parent}, wantErr: true},
		{name: "missing parent", issues: []*tissues.Issue{child}, projects: map[string]*tissues.Project{"ALPHA": project}, wantErr: true},
		{name: "corrupt parent identity", issues: []*tissues.Issue{child}, projects: map[string]*tissues.Project{"ALPHA": project}, parents: map[string]*tissues.Issue{"ALPHA-16": {ProjectKey: "ALPHA", Number: 15, Ref: "ALPHA-16", Title: "Parent", State: tissues.StateOpen, Created: base, Updated: base}}, wantErr: true},
		{name: "parent from wrong Project", issues: []*tissues.Issue{child}, projects: map[string]*tissues.Project{"ALPHA": project}, parents: map[string]*tissues.Issue{"ALPHA-16": {ProjectKey: "BRAVO", Number: 16, Ref: "BRAVO-16", Title: "Parent", State: tissues.StateOpen, Created: base, Updated: base}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateIssueOverviewRelationships(test.issues, test.projects, test.parents)
			if test.wantErr && !errors.Is(err, tissues.ErrInternal) {
				t.Fatalf("error = %v, want ErrInternal", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestIssueOverviewRelationshipKeysDeduplicate(t *testing.T) {
	issues := []*tissues.Issue{
		{ProjectKey: "BRAVO", ParentRef: "BRAVO-2"},
		{ProjectKey: "ALPHA", ParentRef: "ALPHA-1"},
		{ProjectKey: "ALPHA", ParentRef: "ALPHA-1"},
	}
	projectKeys, parentRefs := overviewRelationshipKeys(issues)
	if !reflect.DeepEqual(projectKeys, []string{"ALPHA", "BRAVO"}) {
		t.Fatalf("Project keys = %#v", projectKeys)
	}
	if !reflect.DeepEqual(parentRefs, []string{"ALPHA-1", "BRAVO-2"}) {
		t.Fatalf("Parent refs = %#v", parentRefs)
	}
}

func TestHierarchyCorruption(t *testing.T) {
	missing := map[string]*tissues.Issue{"ALPHA-1": {Ref: "ALPHA-1", ParentRef: "ALPHA-2"}}
	if err := validateAcyclic(missing); !errors.Is(err, tissues.ErrInternal) {
		t.Fatalf("missing parent = %v", err)
	}
	cycle := map[string]*tissues.Issue{
		"ALPHA-1": {Ref: "ALPHA-1", ParentRef: "ALPHA-2"},
		"ALPHA-2": {Ref: "ALPHA-2", ParentRef: "ALPHA-1"},
	}
	if err := validateAcyclic(cycle); !errors.Is(err, tissues.ErrInternal) {
		t.Fatalf("cycle = %v", err)
	}
}

func TestProviderErrorTranslation(t *testing.T) {
	for _, test := range []struct {
		provider error
		want     error
	}{
		{status.Error(codes.NotFound, "provider detail"), tissues.ErrNotFound},
		{status.Error(codes.Aborted, "provider detail"), tissues.ErrConflict},
		{status.Error(codes.AlreadyExists, "provider detail"), tissues.ErrConflict},
		{status.Error(codes.FailedPrecondition, "missing index"), tissues.ErrInternal},
		{errors.New("provider detail"), tissues.ErrInternal},
	} {
		if got := translate(test.provider); got != test.want {
			t.Fatalf("translate(%v) = %v, want %v", test.provider, got, test.want)
		}
	}
}

func withIssue(entity issueEntity, mutate func(*issueEntity)) issueEntity {
	mutate(&entity)
	return entity
}

func withComment(entity commentEntity, mutate func(*commentEntity)) commentEntity {
	mutate(&entity)
	return entity
}
