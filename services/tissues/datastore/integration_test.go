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
	repo, err := tissuesds.New(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewServiceProfile("integration", tissues.Config{Enabled: true, Storage: tissues.StorageConfig{ProjectID: projectID, Namespace: namespace}, Assets: tissues.AssetsConfig{Bucket: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := tissues.New(slot, repo, unusedAssetStore{})
	if err != nil {
		t.Fatal(err)
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
	for i := 1; i <= 2; i++ {
		issue, err := svc.CreateIssue(ctx, "TISSUES", tissues.CreateIssueRequest{Title: "T", Description: "body"})
		if err != nil || issue.Ref != fmt.Sprintf("TISSUES-%d", i) {
			t.Fatalf("TISSUES issue = %#v, %v", issue, err)
		}
	}
	originalID, originalNumber, originalRef := fluent[1].ID, fluent[1].Number, fluent[1].Ref
	for _, parent := range []string{"FLUENT-1", "FLUENT-3", "", "FLUENT-1"} {
		moved, err := svc.MoveIssue(ctx, fluent[1].Ref, parent)
		if err != nil {
			t.Fatalf("move to %q: %v", parent, err)
		}
		if moved.ID != originalID || moved.Number != originalNumber || moved.Ref != originalRef {
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
	seenRefs, seenIDs := map[string]bool{}, map[string]bool{}
	for i, issue := range created {
		if errs[i] != nil {
			t.Fatalf("concurrent create %d: %v", i, errs[i])
		}
		if seenRefs[issue.Ref] || seenIDs[issue.ID] {
			t.Fatalf("duplicate concurrent identity: %#v", issue)
		}
		seenRefs[issue.Ref], seenIDs[issue.ID] = true, true
		numbers = append(numbers, int(issue.Number))
		resolved, err := repo.ResolveIssue(ctx, tissues.IssueRef{ProjectKey: "FLUENT", Number: issue.Number})
		if err != nil || resolved.ID != issue.ID {
			t.Fatalf("index %s = %#v, %v", issue.Ref, resolved, err)
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
	projectKey := gcds.NameKey(tissuesds.ProjectKind, "FLUENT", nil)
	projectKey.Namespace = namespace
	refKeys, err := client.GetAll(ctx, gcds.NewQuery(tissuesds.IssueRefKind).Namespace(namespace).Ancestor(projectKey).KeysOnly(), nil)
	if err != nil || len(refKeys) != 9 {
		t.Fatalf("reference indexes = %d, %v", len(refKeys), err)
	}
	t.Logf("per-project refs verified; concurrent committed numbers=%v; allocator next=%d", numbers, project.NextIssueNumber)

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
	assertOverviewCorruptionDetection(t, ctx, client, repo, namespace)
}

func assertOverviewCorruptionDetection(t *testing.T, ctx context.Context, client *gcds.Client, repo *tissuesds.Store, namespace string) {
	t.Helper()
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	issueProperties := func(number int64, parentID string) gcds.PropertyList {
		return gcds.PropertyList{
			{Name: "Number", Value: number}, {Name: "Title", Value: "corrupt"}, {Name: "State", Value: "open"},
			{Name: "Created", Value: future}, {Name: "Updated", Value: future}, {Name: "Description", Value: "body", NoIndex: true}, {Name: "ParentID", Value: parentID},
		}
	}
	projectKey := gcds.NameKey(tissuesds.ProjectKind, "FLUENT", nil)
	projectKey.Namespace = namespace
	check := func(name string, issueKey, refKey *gcds.Key, refIssueID string) {
		t.Helper()
		properties := issueProperties(map[string]int64{"missing-ref": 999, "bad-index": 998, "missing-parent": 997, "bad-ancestry": 996}[name], map[string]string{"missing-parent": "yyyyyyyyyyyyyyyyyyyyyyyyyy"}[name])
		if _, err := client.Put(ctx, issueKey, &properties); err != nil {
			t.Fatal(err)
		}
		if refKey != nil {
			if _, err := client.Put(ctx, refKey, &gcds.PropertyList{{Name: "IssueID", Value: refIssueID}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.ListIssueOverviewsPage(ctx, tissues.PageRequest{Size: 1}); !errors.Is(err, tissues.ErrInternal) {
			t.Fatalf("%s corruption = %v", name, err)
		}
		keys := []*gcds.Key{issueKey}
		if refKey != nil {
			keys = append(keys, refKey)
		}
		if err := client.DeleteMulti(ctx, keys); err != nil {
			t.Fatal(err)
		}
	}
	missingRefIssue := gcds.NameKey(tissuesds.IssueKind, "zzzzzzzzzzzzzzzzzzzzzzzzzz", projectKey)
	missingRefIssue.Namespace = namespace
	check("missing-ref", missingRefIssue, nil, "")
	badIndexIssue := gcds.NameKey(tissuesds.IssueKind, "xxxxxxxxxxxxxxxxxxxxxxxxxx", projectKey)
	badIndexIssue.Namespace = namespace
	badIndexRef := gcds.NameKey(tissuesds.IssueRefKind, "998", projectKey)
	badIndexRef.Namespace = namespace
	check("bad-index", badIndexIssue, badIndexRef, "wwwwwwwwwwwwwwwwwwwwwwwwww")
	missingParentIssue := gcds.NameKey(tissuesds.IssueKind, "vvvvvvvvvvvvvvvvvvvvvvvvvv", projectKey)
	missingParentIssue.Namespace = namespace
	missingParentRef := gcds.NameKey(tissuesds.IssueRefKind, "997", projectKey)
	missingParentRef.Namespace = namespace
	check("missing-parent", missingParentIssue, missingParentRef, missingParentIssue.Name)
	badAncestryIssue := gcds.NameKey(tissuesds.IssueKind, "uuuuuuuuuuuuuuuuuuuuuuuuuu", nil)
	badAncestryIssue.Namespace = namespace
	check("bad-ancestry", badAncestryIssue, nil, "")
	t.Log("global Issue overview rejects malformed ancestry, missing/mismatched indexes, and missing parents")
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
	kinds := []string{tissuesds.CommentKind, tissuesds.IssueRefKind, tissuesds.IssueKind, tissuesds.ProjectKind}
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
