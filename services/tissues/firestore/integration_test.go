package firestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gcfirestore "cloud.google.com/go/firestore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
)

const (
	integrationEnabledEnv  = "TISSUES_FIRESTORE_INTEGRATION"
	integrationProjectEnv  = "TISSUES_FIRESTORE_TEST_PROJECT"
	integrationDatabaseEnv = "TISSUES_FIRESTORE_TEST_DATABASE"
)

func TestNativeIntegrationTenantBehaviorAndCommentConcurrency(t *testing.T) {
	if os.Getenv(integrationEnabledEnv) != "1" {
		t.Skip("set TISSUES_FIRESTORE_INTEGRATION=1 with explicit test project and named database")
	}
	projectID := strings.TrimSpace(os.Getenv(integrationProjectEnv))
	databaseID := strings.TrimSpace(os.Getenv(integrationDatabaseEnv))
	if projectID == "" {
		t.Fatalf("%s is required", integrationProjectEnv)
	}
	if databaseID == "" || databaseID == "(default)" {
		t.Fatalf("%s must name a non-default Firestore Native database", integrationDatabaseEnv)
	}

	ctx := context.Background()
	client, err := gcfirestore.NewClientWithDatabase(ctx, projectID, databaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	root, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	tenantText, err := tissues.NewID()
	if err != nil {
		t.Fatal(err)
	}
	tenantID := tissues.TenantID(tenantText)
	boundValue, err := root.ForTenant(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	bound := boundValue.(*TenantStore)
	defer cleanupIntegrationTenant(t, ctx, bound)
	suffix, err := tissues.NewID()
	if err != nil {
		t.Fatal(err)
	}
	projectKey := "IT" + strings.ToUpper(suffix[:10])
	created := time.Date(2098, 1, 1, 0, 0, 0, 100, time.UTC)
	createIntegrationProject(t, ctx, bound, &tissues.Project{Key: projectKey, Created: created, NextIssueNumber: 3})
	issueA := &tissues.Issue{ProjectKey: projectKey, Number: 1, Ref: projectKey + "-1", Title: "Concurrent A", State: tissues.StateOpen, Created: created, Updated: created, Description: "unchanged"}
	issueB := &tissues.Issue{ProjectKey: projectKey, Number: 2, Ref: projectKey + "-2", Title: "Concurrent B", State: tissues.StateOpen, Created: created, Updated: created, Description: "unchanged"}
	createIntegrationIssue(t, ctx, bound, issueA)
	createIntegrationIssue(t, ctx, bound, issueB)

	seedID := "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	seed := &tissues.Comment{ID: seedID, Author: "seed", Created: created, Updated: created, Body: "seed"}
	seedResult, err := addIntegrationComment(ctx, bound, issueRef(issueA), seed, created, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !seedResult.Created.Equal(created) {
		t.Fatalf("seed returned Created = %s, want %s", seedResult.Created, created)
	}
	seedComments := integrationComments(t, ctx, bound, issueRef(issueA))
	if len(seedComments) != 1 || seedComments[0].ID != seedID || !seedComments[0].Created.Equal(created) {
		t.Fatalf("persisted seed = %#v, want ID %q at %s", seedComments, seedID, created)
	}

	const concurrent = 5
	results := make([]*tissues.Comment, concurrent)
	errs := make([]error, concurrent)
	retries := make([]atomic.Int64, concurrent)
	expectedIDs := make([]string, concurrent)
	var wg sync.WaitGroup
	for index := range concurrent {
		expectedIDs[index] = strings.Repeat(string(rune('b'+index)), tissues.IDLen)
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := &tissues.Comment{ID: expectedIDs[index], Author: "Ada", Body: fmt.Sprintf("comment %d", index)}
			results[index], errs[index] = addIntegrationComment(ctx, bound, issueRef(issueA), candidate, created.Add(-time.Hour), &retries[index])
		}()
	}
	wg.Wait()
	committed := 0
	seenIDs := map[string]bool{seedID: true}
	for index, err := range errs {
		if errors.Is(err, tissues.ErrConflict) {
			t.Logf("comment %d exhausted Firestore contention retries", index)
			continue
		}
		if err != nil {
			t.Fatalf("comment %d: %v", index, err)
		}
		committed++
		if results[index].ID != expectedIDs[index] {
			t.Fatalf("retry changed pre-generated Comment ID: got %q want %q", results[index].ID, expectedIDs[index])
		}
		if seenIDs[results[index].ID] {
			t.Fatalf("duplicate committed Comment ID %q", results[index].ID)
		}
		seenIDs[results[index].ID] = true
	}
	if committed == 0 {
		t.Fatal("all concurrent comments exhausted contention")
	}

	storedA, revisionA := integrationIssueState(t, ctx, bound, issueRef(issueA))
	if !reflectIssueDomain(storedA, issueA) {
		t.Fatalf("comment insertion changed logical Issue: got %#v want %#v", storedA, issueA)
	}
	if revisionA != int64(1+committed) {
		t.Fatalf("revision = %d, want %d", revisionA, 1+committed)
	}
	comments := integrationComments(t, ctx, bound, issueRef(issueA))
	if len(comments) != 1+committed {
		t.Fatalf("comments = %d, want %d", len(comments), 1+committed)
	}
	for index, comment := range comments {
		want := created.Add(time.Duration(index) * time.Nanosecond)
		if !comment.Created.Equal(want) {
			t.Fatalf("Comment chronology[%d] = %s, want %s", index, comment.Created, want)
		}
	}

	otherID := "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	other := &tissues.Comment{ID: otherID, Author: "Grace", Body: "independent"}
	if _, err := addIntegrationComment(ctx, bound, issueRef(issueB), other, created, nil); err != nil {
		t.Fatal(err)
	}
	_, unchangedARevision := integrationIssueState(t, ctx, bound, issueRef(issueA))
	_, revisionB := integrationIssueState(t, ctx, bound, issueRef(issueB))
	if unchangedARevision != revisionA || revisionB != 1 {
		t.Fatalf("per-Issue revisions A=%d B=%d, want %d and 1", unchangedARevision, revisionB, revisionA)
	}

	observedRetry := false
	for index := range concurrent {
		if retries[index].Load() > 1 {
			observedRetry = true
			if results[index] != nil && results[index].ID != expectedIDs[index] {
				t.Fatalf("retry changed pre-generated Comment ID")
			}
		}
	}
	t.Logf("committed=%d callback_retry_observed=%t emulator=%t", committed, observedRetry, os.Getenv("FIRESTORE_EMULATOR_HOST") != "")
}

func createIntegrationProject(t *testing.T, ctx context.Context, store *TenantStore, project *tissues.Project) {
	t.Helper()
	if err := store.RunInTransaction(ctx, func(tx tissues.Transaction) error {
		if _, err := tx.GetProject(ctx, project.Key); !errors.Is(err, tissues.ErrNotFound) {
			return fmt.Errorf("expected absent Project: %w", err)
		}
		return tx.PutProject(ctx, project)
	}); err != nil {
		t.Fatal(err)
	}
}

func createIntegrationIssue(t *testing.T, ctx context.Context, store *TenantStore, issue *tissues.Issue) {
	t.Helper()
	ref := issueRef(issue)
	if err := store.RunInTransaction(ctx, func(tx tissues.Transaction) error {
		if _, err := tx.GetIssue(ctx, ref); !errors.Is(err, tissues.ErrNotFound) {
			return fmt.Errorf("expected absent Issue: %w", err)
		}
		return tx.PutIssue(ctx, issue)
	}); err != nil {
		t.Fatal(err)
	}
}

func addIntegrationComment(ctx context.Context, store *TenantStore, ref tissues.IssueRef, candidate *tissues.Comment, wall time.Time, callbacks *atomic.Int64) (*tissues.Comment, error) {
	var result *tissues.Comment
	err := store.RunInTransaction(ctx, func(tx tissues.Transaction) error {
		if callbacks != nil {
			callbacks.Add(1)
		}
		issue, err := tx.GetIssue(ctx, ref)
		if err != nil {
			return err
		}
		if _, err := tx.GetComment(ctx, ref, candidate.ID); err == nil {
			return tissues.ErrConflict
		} else if !errors.Is(err, tissues.ErrNotFound) {
			return err
		}
		latest, err := tx.GetLastComment(ctx, ref)
		if err != nil && !errors.Is(err, tissues.ErrNotFound) {
			return err
		}
		created := wall
		if latest != nil && !created.After(latest.Created) {
			created = latest.Created.Add(time.Nanosecond)
		}
		comment := &tissues.Comment{ID: candidate.ID, Author: candidate.Author, Created: created, Updated: created, Body: candidate.Body}
		if err := tx.PutComment(ctx, ref, comment); err != nil {
			return err
		}
		if err := tx.PutIssue(ctx, issue); err != nil {
			return err
		}
		copy := *comment
		result = &copy
		return nil
	})
	return result, err
}

func integrationIssueState(t *testing.T, ctx context.Context, store *TenantStore, ref tissues.IssueRef) (*tissues.Issue, int64) {
	t.Helper()
	doc, err := store.issueRef(ref).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issue, revision, err := decodeIssueSnapshot(doc)
	if err != nil {
		t.Fatal(err)
	}
	return issue, revision
}

func integrationComments(t *testing.T, ctx context.Context, store *TenantStore, ref tissues.IssueRef) []*tissues.Comment {
	t.Helper()
	docs, err := collect(ctx, store.comments().Where("issue_ref", "==", ref.String()))
	if err != nil {
		t.Fatal(err)
	}
	comments := make([]*tissues.Comment, 0, len(docs))
	for _, doc := range docs {
		storedRef, comment, err := decodeCommentSnapshot(doc)
		if err != nil || storedRef != ref {
			t.Fatalf("Comment decode = %v %v", storedRef, err)
		}
		comments = append(comments, comment)
	}
	sort.Slice(comments, func(i, j int) bool {
		if comments[i].Created.Equal(comments[j].Created) {
			return comments[i].ID < comments[j].ID
		}
		return comments[i].Created.Before(comments[j].Created)
	})
	return comments
}

func cleanupIntegrationTenant(t *testing.T, ctx context.Context, store *TenantStore) {
	t.Helper()
	for _, collection := range []*gcfirestore.CollectionRef{store.comments(), store.issues(), store.projects()} {
		iter := collection.Documents(ctx)
		for {
			doc, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				iter.Stop()
				t.Errorf("cleanup query: %v", err)
				break
			}
			if _, err := doc.Ref.Delete(ctx); err != nil {
				t.Errorf("cleanup %s: %v", doc.Ref.Path, err)
			}
		}
		iter.Stop()
	}
}

func issueRef(issue *tissues.Issue) tissues.IssueRef {
	return tissues.IssueRef{ProjectKey: issue.ProjectKey, Number: issue.Number}
}

func reflectIssueDomain(a, b *tissues.Issue) bool {
	return a.ProjectKey == b.ProjectKey && a.Number == b.Number && a.Ref == b.Ref && a.Title == b.Title && a.State == b.State && a.Created.Equal(b.Created) && a.Updated.Equal(b.Updated) && a.Description == b.Description && a.ParentRef == b.ParentRef
}
