package datastore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/services/tissues"
	tissuesds "github.com/tedla-brandsema/tissues/services/tissues/datastore"
)

type unusedAssetStore struct{}

func (unusedAssetStore) ForTenant(id tissues.TenantID) (tissues.TenantAssetStore, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return unusedAssetStore{}, nil
}

func (unusedAssetStore) Put(context.Context, tissues.AssetKey, tissues.AssetWrite) (*tissues.Asset, error) {
	return nil, errors.New("unused")
}
func (unusedAssetStore) Open(context.Context, tissues.AssetKey) (*tissues.AssetContent, error) {
	return nil, errors.New("unused")
}
func (unusedAssetStore) List(context.Context, tissues.IssueRef) ([]*tissues.Asset, error) {
	return nil, errors.New("unused")
}

func TestRealDatastoreProjectsReferencesAndConcurrentAllocation(t *testing.T) {
	if os.Getenv("TISSUES_GCP_INTEGRATION") != "1" {
		t.Skip("set TISSUES_GCP_INTEGRATION=1 for real Datastore test")
	}
	projectID := strings.TrimSpace(os.Getenv("TISSUES_GCP_TEST_PROJECT"))
	if projectID == "" {
		t.Fatal("TISSUES_GCP_TEST_PROJECT is required")
	}
	ctx := context.Background()
	client, err := gcds.NewClient(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	suffix, err := tissues.NewID()
	if err != nil {
		t.Fatal(err)
	}
	namespace := "tissues-projects-it-" + suffix[:12]
	t.Logf("integration namespace: %s", namespace)
	defer cleanupNamespace(t, ctx, client, namespace)
	root, err := tissuesds.New(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	tenantID := tissues.TenantID("7womw3jzkek74oggxj6f42xak4")
	bound, err := root.ForTenant(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	repo := bound.(*tissuesds.TenantStore)
	profile, err := config.NewServiceProfile("integration", tissues.Config{Enabled: true, BootstrapTenantID: tenantID.String(), Storage: tissues.StorageConfig{ProjectID: projectID, Namespace: namespace}, Assets: tissues.AssetsConfig{Bucket: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := tissues.New(slot, root, unusedAssetStore{})
	if err != nil {
		t.Fatal(err)
	}
	tenantB := tissues.TenantID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	profileB, err := config.NewServiceProfile("integration-b", tissues.Config{Enabled: true, BootstrapTenantID: tenantB.String(), Storage: tissues.StorageConfig{ProjectID: projectID, Namespace: namespace}, Assets: tissues.AssetsConfig{Bucket: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	slotB, err := config.NewSlot(profileB)
	if err != nil {
		t.Fatal(err)
	}
	svcB, err := tissues.New(slotB, root, unusedAssetStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcB.CreateProject(ctx, "FLUENT"); err != nil {
		t.Fatal(err)
	}
	issueB, err := svcB.CreateIssue(ctx, "FLUENT", tissues.CreateIssueRequest{Title: "Tenant B", Description: "isolated"})
	if err != nil || issueB.Ref != "FLUENT-1" {
		t.Fatalf("Tenant B Issue = %#v, %v", issueB, err)
	}

	for _, key := range []string{"FLUENT", "TISSUES"} {
		if _, err := svc.CreateProject(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	fluent := make([]*tissues.Issue, 3)
	for i := range fluent {
		fluent[i], err = svc.CreateIssue(ctx, "FLUENT", tissues.CreateIssueRequest{Title: fmt.Sprintf("F%d", i+1), Description: "body"})
		if err != nil {
			t.Fatal(err)
		}
		if fluent[i].Ref != fmt.Sprintf("FLUENT-%d", i+1) {
			t.Fatalf("FLUENT ref = %s", fluent[i].Ref)
		}
	}
	if issueA, err := svc.GetIssue(ctx, "FLUENT-1"); err != nil || issueA.Title != "F1" {
		t.Fatalf("Tenant A point read = %#v, %v", issueA, err)
	}
	if issueB, err := svcB.GetIssue(ctx, "FLUENT-1"); err != nil || issueB.Title != "Tenant B" {
		t.Fatalf("Tenant B point read = %#v, %v", issueB, err)
	}
	for i := 1; i <= 2; i++ {
		issue, err := svc.CreateIssue(ctx, "TISSUES", tissues.CreateIssueRequest{Title: "T", Description: "body"})
		if err != nil || issue.Ref != fmt.Sprintf("TISSUES-%d", i) {
			t.Fatalf("TISSUES issue = %#v, %v", issue, err)
		}
	}
	originalNumber, originalRef := fluent[1].Number, fluent[1].Ref
	for _, parent := range []string{"FLUENT-1", "FLUENT-3", "", "FLUENT-1"} {
		moved, err := svc.MoveIssue(ctx, fluent[1].Ref, parent)
		if err != nil {
			t.Fatalf("move to %q: %v", parent, err)
		}
		if moved.Number != originalNumber || moved.Ref != originalRef {
			t.Fatal("move changed immutable identity")
		}
	}
	if _, err := svc.MoveIssue(ctx, "FLUENT-2", "FLUENT-999999"); !errors.Is(err, tissues.ErrNotFound) {
		t.Fatalf("ghost ref = %v", err)
	}
	if _, err := svc.MoveIssue(ctx, "FLUENT-2", "TISSUES-1"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("cross-project = %v", err)
	}
	if _, err := svc.MoveIssue(ctx, "FLUENT-2", "FLUENT-2"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("self = %v", err)
	}
	if _, err := svc.MoveIssue(ctx, "FLUENT-1", "FLUENT-2"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("cycle = %v", err)
	}
	if _, err := svc.GetIssue(ctx, "#FLUENT-2"); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("hash-prefixed ID = %v", err)
	}
	read, err := svc.GetIssue(ctx, "FLUENT-2")
	if err != nil || read.ParentRef != "FLUENT-1" {
		t.Fatalf("hierarchy read = %#v, %v", read, err)
	}
	commentRef := tissues.IssueRef{ProjectKey: "FLUENT", Number: 2}
	futureCommentTime := time.Date(2098, 1, 1, 0, 0, 0, 123, time.UTC)
	seedComment := &tissues.Comment{ID: "cccccccccccccccccccccccccc", Author: "seed", Created: futureCommentTime, Updated: futureCommentTime, Body: "seed"}
	if err := repo.RunInTransaction(ctx, func(tx tissues.Transaction) error {
		issue, err := tx.GetIssue(ctx, commentRef)
		if err != nil {
			return err
		}
		if err := tx.PutComment(ctx, commentRef, seedComment); err != nil {
			return err
		}
		return tx.PutIssue(ctx, issue)
	}); err != nil {
		t.Fatal(err)
	}
	nextComment, err := svc.AddComment(ctx, commentRef.String(), "Ada", "next")
	if err != nil || !nextComment.Created.Equal(futureCommentTime.Add(time.Nanosecond)) {
		t.Fatalf("transactional latest Comment = %#v, %v", nextComment, err)
	}
	tenantKey := gcds.NameKey(tissuesds.TenantKind, tenantID.String(), nil)
	tenantKey.Namespace = namespace
	projectKey := gcds.NameKey(tissuesds.ProjectKind, "FLUENT", tenantKey)
	projectKey.Namespace = namespace
	issueKeyForComments := gcds.NameKey(tissuesds.IssueKind, commentRef.String(), projectKey)
	issueKeyForComments.Namespace = namespace
	commentKeys, err := client.GetAll(ctx, gcds.NewQuery(tissuesds.CommentKind).Namespace(namespace).Ancestor(issueKeyForComments).KeysOnly(), nil)
	if err != nil || len(commentKeys) != 2 {
		t.Fatalf("canonical Comment ancestry = %d, %v", len(commentKeys), err)
	}

	const concurrent = 6
	created := make([]*tissues.Issue, concurrent)
	errs := make([]error, concurrent)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			created[index], errs[index] = svc.CreateIssue(ctx, "FLUENT", tissues.CreateIssueRequest{Title: fmt.Sprintf("Concurrent %d", index), Description: "body"})
		}(i)
	}
	wg.Wait()
	numbers := make([]int, 0, concurrent)
	seenRefs := map[string]bool{}
	for i, issue := range created {
		if errs[i] != nil {
			t.Fatalf("concurrent create %d: %v", i, errs[i])
		}
		if seenRefs[issue.Ref] {
			t.Fatalf("duplicate concurrent identity: %#v", issue)
		}
		seenRefs[issue.Ref] = true
		numbers = append(numbers, int(issue.Number))
		resolved, err := repo.GetIssue(ctx, tissues.IssueRef{ProjectKey: "FLUENT", Number: issue.Number})
		if err != nil || resolved.Ref != issue.Ref {
			t.Fatalf("canonical lookup %s = %#v, %v", issue.Ref, resolved, err)
		}
	}
	sort.Ints(numbers)
	for i, number := range numbers {
		if number != i+4 {
			t.Fatalf("concurrent numbers = %v", numbers)
		}
	}
	project, err := repo.GetProject(ctx, "FLUENT")
	if err != nil || project.NextIssueNumber != 10 {
		t.Fatalf("allocator = %#v, %v", project, err)
	}
	issueKeys, err := client.GetAll(ctx, gcds.NewQuery(tissuesds.IssueKind).Namespace(namespace).Ancestor(projectKey).KeysOnly(), nil)
	if err != nil || len(issueKeys) != 9 {
		t.Fatalf("canonical Issue keys = %d, %v", len(issueKeys), err)
	}
	for _, key := range issueKeys {
		ref, parseErr := tissues.ParseIssueRef(key.Name)
		if parseErr != nil || ref.ProjectKey != "FLUENT" {
			t.Fatalf("noncanonical Issue key %#v", key)
		}
	}
	t.Logf("canonical Issue keys verified; concurrent committed numbers=%v; allocator next=%d", numbers, project.NextIssueNumber)

	if _, err := svc.CreateProject(ctx, "TELONAUTICS"); err != nil {
		t.Fatal(err)
	}
	projectPage1, err := svc.ListProjectsPage(ctx, 2, "")
	if err != nil || len(projectPage1.Projects) != 2 || projectPage1.Projects[0].Key != "FLUENT" || projectPage1.Projects[1].Key != "TELONAUTICS" || projectPage1.NextCursor == "" {
		t.Fatalf("Project page 1 = %#v, %v", projectPage1, err)
	}
	projectPage2, err := svc.ListProjectsPage(ctx, 2, projectPage1.NextCursor)
	if err != nil || len(projectPage2.Projects) != 1 || projectPage2.Projects[0].Key != "TISSUES" || projectPage2.NextCursor != "" {
		t.Fatalf("Project page 2 = %#v, %v", projectPage2, err)
	}

	seenOverview := map[string]bool{}
	var previousUpdated time.Time
	cursor := ""
	pages := 0
	for {
		page, err := svc.ListIssueOverviewsPage(ctx, 2, cursor, "")
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, overview := range page.Issues {
			if seenOverview[overview.Ref] {
				t.Fatalf("duplicate paged Issue %s", overview.Ref)
			}
			seenOverview[overview.Ref] = true
			if !previousUpdated.IsZero() && overview.Updated.After(previousUpdated) {
				t.Fatalf("global Issue ordering increased from %s to %s", previousUpdated, overview.Updated)
			}
			previousUpdated = overview.Updated
			if overview.Ref == "FLUENT-2" && overview.ParentRef != "FLUENT-1" {
				t.Fatalf("FLUENT-2 parent ref = %q", overview.ParentRef)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seenOverview) != 11 || pages < 2 {
		t.Fatalf("paged global Issues = %d across %d pages", len(seenOverview), pages)
	}
	pageA, err := svc.ListProjectsPage(ctx, 1, "")
	if err != nil || pageA.NextCursor == "" {
		t.Fatalf("Tenant A cursor = %#v, %v", pageA, err)
	}
	if _, err := svcB.ListProjectsPage(ctx, 1, pageA.NextCursor); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("cross-tenant cursor = %v", err)
	}
	seenFiltered := map[string]bool{}
	previousUpdated = time.Time{}
	cursor = ""
	for {
		page, err := svc.ListIssueOverviewsPage(ctx, 2, cursor, "FLUENT")
		if err != nil {
			t.Fatal(err)
		}
		for _, overview := range page.Issues {
			if overview.ProjectKey != "FLUENT" || seenFiltered[overview.Ref] {
				t.Fatalf("filtered Issue = %#v", overview)
			}
			if !previousUpdated.IsZero() && overview.Updated.After(previousUpdated) {
				t.Fatalf("filtered Issue ordering increased")
			}
			seenFiltered[overview.Ref] = true
			previousUpdated = overview.Updated
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seenFiltered) != 9 {
		t.Fatalf("filtered FLUENT Issues = %d", len(seenFiltered))
	}
	t.Logf("cursor pagination verified: Projects 2+1; global Issues=%d across %d pages", len(seenOverview), pages)
	assertOverviewCorruptionDetection(t, ctx, client, repo, namespace, tenantID)
}

func assertOverviewCorruptionDetection(t *testing.T, ctx context.Context, client *gcds.Client, repo *tissuesds.TenantStore, namespace string, tenantID tissues.TenantID) {
	t.Helper()
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	issueProperties := func(number int64, parentRef string) gcds.PropertyList {
		return gcds.PropertyList{
			{Name: "Number", Value: number}, {Name: "Title", Value: "corrupt"}, {Name: "State", Value: "open"},
			{Name: "Created", Value: future}, {Name: "Updated", Value: future}, {Name: "Description", Value: "body", NoIndex: true}, {Name: "ParentRef", Value: parentRef},
		}
	}
	tenantKey := gcds.NameKey(tissuesds.TenantKind, tenantID.String(), nil)
	tenantKey.Namespace = namespace
	projectKey := gcds.NameKey(tissuesds.ProjectKind, "FLUENT", tenantKey)
	projectKey.Namespace = namespace
	check := func(name string, issueKey *gcds.Key) {
		t.Helper()
		properties := issueProperties(map[string]int64{"bad-key": 999, "key-mismatch": 998, "missing-parent": 997, "bad-ancestry": 996}[name], map[string]string{"missing-parent": "FLUENT-123456"}[name])
		if _, err := client.Put(ctx, issueKey, &properties); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ListIssueOverviewsPage(ctx, tissues.PageRequest{Size: 1}); !errors.Is(err, tissues.ErrInternal) {
			t.Fatalf("%s corruption = %v", name, err)
		}
		if err := client.Delete(ctx, issueKey); err != nil {
			t.Fatal(err)
		}
	}
	badKeyIssue := gcds.NameKey(tissuesds.IssueKind, "not-a-ref", projectKey)
	badKeyIssue.Namespace = namespace
	check("bad-key", badKeyIssue)
	keyMismatchIssue := gcds.NameKey(tissuesds.IssueKind, "FLUENT-999", projectKey)
	keyMismatchIssue.Namespace = namespace
	check("key-mismatch", keyMismatchIssue)
	missingParentIssue := gcds.NameKey(tissuesds.IssueKind, "FLUENT-997", projectKey)
	missingParentIssue.Namespace = namespace
	check("missing-parent", missingParentIssue)
	t.Log("tenant Issue overview rejects malformed canonical keys, mismatched numbers, and missing parents")
}

func countIssues(issues []*tissues.Issue) int {
	total := 0
	for _, issue := range issues {
		total += 1 + countIssues(issue.Children)
	}
	return total
}

func cleanupNamespace(t *testing.T, ctx context.Context, client *gcds.Client, namespace string) {
	t.Helper()
	kinds := []string{tissuesds.CommentKind, tissuesds.IssueKind, tissuesds.ProjectKind, tissuesds.TenantKind}
	for _, kind := range kinds {
		keys, err := client.GetAll(ctx, gcds.NewQuery(kind).Namespace(namespace).KeysOnly(), nil)
		if err != nil {
			t.Errorf("cleanup query %s: %v", kind, err)
			continue
		}
		if len(keys) > 0 {
			if err := client.DeleteMulti(ctx, keys); err != nil {
				t.Errorf("cleanup delete %s: %v", kind, err)
			}
		}
	}
	for _, kind := range kinds {
		keys, err := client.GetAll(ctx, gcds.NewQuery(kind).Namespace(namespace).KeysOnly(), nil)
		if err != nil || len(keys) != 0 {
			t.Errorf("cleanup residual kind=%s keys=%d error=%v", kind, len(keys), err)
		}
	}
	t.Logf("cleanup verified zero residual entities for all tissues Project-era kinds in namespace %s", namespace)
}
