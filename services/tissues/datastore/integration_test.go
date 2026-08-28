package datastore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/lib/core/config"
	"github.com/tedla-brandsema/tissues/services/tissues"
	tissuesds "github.com/tedla-brandsema/tissues/services/tissues/datastore"
)

func TestRealDatastoreDogfood(t *testing.T) {
	if os.Getenv("TISSUES_GCP_INTEGRATION") != "1" {
		t.Skip("set TISSUES_GCP_INTEGRATION=1 for real Datastore test")
	}
	project := strings.TrimSpace(os.Getenv("TISSUES_GCP_TEST_PROJECT"))
	if project == "" {
		t.Fatal("TISSUES_GCP_TEST_PROJECT is required")
	}
	ctx := context.Background()
	client, err := gcds.NewClient(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	suffix, err := tissues.NewID()
	if err != nil {
		t.Fatal(err)
	}
	namespace := "tissues-it-" + suffix[:12]
	t.Logf("integration namespace: %s", namespace)
	defer cleanupNamespace(t, ctx, client, namespace)
	repo, err := tissuesds.New(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewServiceProfile("integration", tissues.Config{Enabled: true, Storage: tissues.StorageConfig{ProjectID: project, Namespace: namespace}})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := tissues.New(slot, repo)
	if err != nil {
		t.Fatal(err)
	}
	a := mustCreate(t, svc, ctx, "A", "alpha")
	b := mustCreate(t, svc, ctx, "B", "beta")
	c := mustCreate(t, svc, ctx, "C", "gamma")
	for _, want := range []*tissues.Issue{a, b, c} {
		got, readErr := svc.GetIssue(ctx, want.ID)
		if readErr != nil || got.ID != want.ID || got.Title != want.Title {
			t.Fatalf("point read %s=%#v,%v", want.ID, got, readErr)
		}
	}
	listed, err := svc.ListIssues(ctx)
	if err != nil || len(listed) != 3 {
		t.Fatalf("ListIssues=%d,%v", len(listed), err)
	}
	b, err = svc.MoveIssue(ctx, b.ID, a.ID)
	if err != nil || b.ParentID != a.ID {
		t.Fatalf("attach=%#v,%v", b, err)
	}
	readA, _ := svc.GetIssue(ctx, a.ID)
	if len(readA.Children) != 1 || readA.Children[0].ID != b.ID {
		t.Fatalf("attach readback=%#v", readA)
	}
	originalID, originalTitle := b.ID, b.Title
	b, err = svc.MoveIssue(ctx, b.ID, c.ID)
	if err != nil || b.ID != originalID || b.Title != originalTitle || b.ParentID != c.ID {
		t.Fatalf("move=%#v,%v", b, err)
	}
	b, err = svc.MoveIssue(ctx, b.ID, "")
	if err != nil || b.ParentID != "" {
		t.Fatalf("detach=%#v,%v", b, err)
	}
	b, err = svc.MoveIssue(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	comment, err := svc.AddComment(ctx, b.ID, "integration-agent", "first")
	if err != nil {
		t.Fatal(err)
	}
	readB, err := svc.GetIssue(ctx, b.ID)
	if err != nil || len(readB.Comments) != 1 || readB.Comments[0].ID != comment.ID {
		t.Fatalf("comment read=%#v,%v", readB, err)
	}
	edited, err := svc.EditComment(ctx, b.ID, comment.ID, "edited")
	if err != nil || edited.Body != "edited" || edited.ID != comment.ID || !edited.Created.Equal(comment.Created) {
		t.Fatalf("edit=%#v,%v", edited, err)
	}
	second, err := svc.AddComment(ctx, b.ID, "integration-agent", "second")
	if err != nil {
		t.Fatal(err)
	}
	readB, _ = svc.GetIssue(ctx, b.ID)
	if len(readB.Comments) != 2 || readB.Comments[0].ID != comment.ID || readB.Comments[1].ID != second.ID {
		t.Fatalf("comment order=%#v", readB.Comments)
	}
	closed, err := svc.CloseIssue(ctx, b.ID)
	if err != nil || closed.State != tissues.StateClosed {
		t.Fatalf("close=%#v,%v", closed, err)
	}
	closedRead, _ := svc.GetIssue(ctx, b.ID)
	if closedRead.State != tissues.StateClosed {
		t.Fatalf("closed read=%#v", closedRead)
	}
	opened, err := svc.ReopenIssue(ctx, b.ID)
	if err != nil || opened.State != tissues.StateOpen {
		t.Fatalf("reopen=%#v,%v", opened, err)
	}
	if _, err = svc.MoveIssue(ctx, b.ID, b.ID); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("self-parent error=%v", err)
	}
	if _, err = svc.MoveIssue(ctx, a.ID, b.ID); !errors.Is(err, tissues.ErrInvalid) {
		t.Fatalf("cycle error=%v", err)
	}
	readA, _ = svc.GetIssue(ctx, a.ID)
	if readA.ParentID != "" || len(readA.Children) != 1 || readA.Children[0].ID != b.ID {
		t.Fatalf("rejections changed hierarchy=%#v", readA)
	}
	t.Log("immediate point reads, global list queries, ancestor comment queries, and relationship reads were consistent without sleeps or retries")
}

func mustCreate(t *testing.T, svc *tissues.Service, ctx context.Context, title, description string) *tissues.Issue {
	t.Helper()
	issue, err := svc.CreateIssue(ctx, tissues.CreateIssueRequest{Title: title, Description: description})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}
func cleanupNamespace(t *testing.T, ctx context.Context, client *gcds.Client, namespace string) {
	t.Helper()
	clean := true
	for _, kind := range []string{tissuesds.CommentKind, tissuesds.IssueKind} {
		keys, err := client.GetAll(ctx, gcds.NewQuery(kind).Namespace(namespace).KeysOnly(), nil)
		if err != nil {
			clean = false
			t.Errorf("cleanup query namespace=%s kind=%s: %v", namespace, kind, err)
			continue
		}
		if len(keys) > 0 {
			if err := client.DeleteMulti(ctx, keys); err != nil {
				clean = false
				t.Errorf("cleanup delete namespace=%s kind=%s keys=%v: %v", namespace, kind, keys, err)
			}
		}
	}
	for _, kind := range []string{tissuesds.CommentKind, tissuesds.IssueKind} {
		keys, err := client.GetAll(ctx, gcds.NewQuery(kind).Namespace(namespace).KeysOnly(), nil)
		if err != nil || len(keys) != 0 {
			clean = false
			t.Errorf("cleanup residual namespace=%s kind=%s keys=%v error=%v", namespace, kind, keys, err)
		}
	}
	if clean {
		t.Logf("cleanup verified zero residual tissues_issue and tissues_comment entities in namespace %s", namespace)
	}
}
