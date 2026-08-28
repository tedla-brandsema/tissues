package tissues

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestProjectLifecycleAndIndependentIssueAllocation(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepository()
	svc := testService(t, repo)
	fluent, err := svc.CreateProject(ctx, " fluent ")
	if err != nil {
		t.Fatal(err)
	}
	if fluent.Key != "FLUENT" || fluent.NextIssueNumber != 1 {
		t.Fatalf("project = %#v", fluent)
	}
	if _, err := svc.CreateProject(ctx, "fluent"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := svc.CreateProject(ctx, "bad-key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid = %v", err)
	}
	if _, err := svc.CreateProject(ctx, "tissues"); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		issue, err := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Issue", Description: "body"})
		if err != nil {
			t.Fatal(err)
		}
		if issue.Number != int64(i) || issue.Ref != "FLUENT-"+string(rune('0'+i)) {
			t.Fatalf("issue %d = %#v", i, issue)
		}
	}
	for i := 1; i <= 2; i++ {
		issue, err := svc.CreateIssue(ctx, "TISSUES", CreateIssueRequest{Title: "Issue", Description: "body"})
		if err != nil {
			t.Fatal(err)
		}
		if issue.Number != int64(i) {
			t.Fatalf("TISSUES number = %d", issue.Number)
		}
	}
	issues, err := svc.ListIssues(ctx, "fluent")
	if err != nil {
		t.Fatal(err)
	}
	if got := issueNumbers(issues); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("numbers = %v", got)
	}
	projects, err := svc.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projects[0].Key != "FLUENT" || projects[1].Key != "TISSUES" {
		t.Fatalf("projects = %#v", projects)
	}
}

func TestHierarchyByReferenceAndImmutableIdentity(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepository()
	svc := testService(t, repo)
	if _, err := svc.CreateProject(ctx, "FLUENT"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateProject(ctx, "TISSUES"); err != nil {
		t.Fatal(err)
	}
	a, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "A", Description: "a"})
	b, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "B", Description: "b"})
	c, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "C", Description: "c"})
	foreign, _ := svc.CreateIssue(ctx, "TISSUES", CreateIssueRequest{Title: "Foreign", Description: "x"})
	b, err := svc.MoveIssue(ctx, b.Ref, "FLUENT-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ParentID != a.ID || b.ParentRef != a.Ref {
		t.Fatalf("parent = %#v", b)
	}
	project, _ := repo.GetProject(ctx, "FLUENT")
	if project.NextIssueNumber != 4 {
		t.Fatalf("allocator = %d", project.NextIssueNumber)
	}
	original := *b
	moved, err := svc.MoveIssue(ctx, b.Ref, "FLUENT-3")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != original.ID || moved.ProjectKey != original.ProjectKey || moved.Number != original.Number || moved.Ref != original.Ref || moved.ParentID != c.ID {
		t.Fatalf("identity changed: %#v", moved)
	}
	if _, err := svc.MoveIssue(ctx, b.Ref, "FLUENT-999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	unchanged, err := svc.GetIssue(ctx, b.Ref)
	if err != nil || unchanged.ParentRef != c.Ref {
		t.Fatalf("ghost rejection changed parent: %#v, %v", unchanged, err)
	}
	if _, err := svc.MoveIssue(ctx, b.Ref, foreign.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross project = %v", err)
	}
	if _, err := svc.MoveIssue(ctx, b.Ref, b.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self = %v", err)
	}
	if _, err := svc.MoveIssue(ctx, c.Ref, b.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle = %v", err)
	}
	detached, err := svc.MoveIssue(ctx, b.Ref, "")
	if err != nil || detached.ParentID != "" || detached.ParentRef != "" {
		t.Fatalf("detach = %#v, %v", detached, err)
	}
}

func TestUpdatesAndCommentsRemainReferenceAddressed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepository()
	svc := testService(t, repo)
	_, _ = svc.CreateProject(ctx, "FLUENT")
	issue, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Before", Description: "old"})
	title, description := "After", "new"
	updated, err := svc.UpdateIssue(ctx, UpdateIssueRequest{Ref: "fluent-1", Title: &title, Description: &description})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != issue.ID || updated.Number != issue.Number || updated.Ref != issue.Ref || updated.ProjectKey != issue.ProjectKey {
		t.Fatal("immutable identity changed")
	}
	comment, err := svc.AddComment(ctx, issue.Ref, "Ada", "first")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := svc.EditComment(ctx, issue.Ref, comment.ID, "edited")
	if err != nil || edited.Body != "edited" || edited.ID != comment.ID {
		t.Fatalf("comment = %#v, %v", edited, err)
	}
	second, err := svc.AddComment(ctx, issue.Ref, "Ada", "second")
	if err != nil || !second.Created.Equal(comment.Created.Add(time.Nanosecond)) {
		t.Fatalf("comment ordering = %#v, %v", second, err)
	}
	closed, _ := svc.CloseIssue(ctx, issue.Ref)
	reopened, _ := svc.ReopenIssue(ctx, issue.Ref)
	if closed.State != StateClosed || reopened.State != StateOpen {
		t.Fatal("state transitions failed")
	}
}

func TestUpdateIssueAppliesContentWithoutChangingParent(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, newMemoryRepository())
	_, _ = svc.CreateProject(ctx, "FLUENT")
	parent, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Parent", Description: "parent"})
	child, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Child", Description: "child"})
	child, _ = svc.MoveIssue(ctx, child.Ref, parent.Ref)
	title, description := "Updated", "updated body"
	updated, err := svc.UpdateIssue(ctx, UpdateIssueRequest{Ref: child.Ref, Title: &title, Description: &description})
	if err != nil || updated.Title != title || updated.Description != description || updated.ParentID != parent.ID || updated.ParentRef != parent.Ref {
		t.Fatalf("content update = %#v, %v", updated, err)
	}
	detached, err := svc.MoveIssue(ctx, child.Ref, "")
	if err != nil || detached.ParentID != "" || detached.ParentRef != "" {
		t.Fatalf("detach = %#v, %v", detached, err)
	}
}

func TestPagedProjectsAndGlobalIssueOverview(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, newMemoryRepository())
	for _, key := range []string{"TISSUES", "FLUENT", "TELONAUTICS"} {
		_, _ = svc.CreateProject(ctx, key)
		_, _ = svc.CreateIssue(ctx, key, CreateIssueRequest{Title: key, Description: "body"})
	}
	first, err := svc.ListProjectsPage(ctx, 2, "")
	if err != nil || len(first.Projects) != 2 || first.Projects[0].Key != "FLUENT" || first.Projects[1].Key != "TELONAUTICS" || first.NextCursor == "" {
		t.Fatalf("first Project page = %#v, %v", first, err)
	}
	second, err := svc.ListProjectsPage(ctx, 2, first.NextCursor)
	if err != nil || len(second.Projects) != 1 || second.Projects[0].Key != "TISSUES" || second.NextCursor != "" {
		t.Fatalf("second Project page = %#v, %v", second, err)
	}
	issues, err := svc.ListIssueOverviewsPage(ctx, 2, "", "")
	if err != nil || len(issues.Issues) != 2 || issues.NextCursor == "" {
		t.Fatalf("first Issue page = %#v, %v", issues, err)
	}
	last, err := svc.ListIssueOverviewsPage(ctx, 2, issues.NextCursor, "")
	if err != nil || len(last.Issues) != 1 || last.NextCursor != "" {
		t.Fatalf("last Issue page = %#v, %v", last, err)
	}
	for _, size := range []int{0, 101} {
		if _, err := svc.ListProjectsPage(ctx, size, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Project page size %d = %v", size, err)
		}
	}
	filtered, err := svc.ListIssueOverviewsPage(ctx, 2, "", "fluent")
	if err != nil || len(filtered.Issues) != 1 || filtered.Issues[0].ProjectKey != "FLUENT" {
		t.Fatalf("filtered Issues = %#v, %v", filtered, err)
	}
	if _, err := svc.ListIssueOverviewsPage(ctx, 2, "", "bad-key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid Project = %v", err)
	}
	if _, err := svc.ListIssueOverviewsPage(ctx, 2, "", "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Project = %v", err)
	}
	if _, err := svc.ListIssueOverviewsPage(ctx, 2, "not-a-cursor", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cursor = %v", err)
	}
}

type retryRepository struct {
	*memoryRepository
	attempts    int
	seenIDs     []string
	seenNumbers []int64
}

func (r *retryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := r.copy()
	if err := fn(recordingTx{memoryTx{repo: first}, r}); err != nil {
		return err
	}
	r.attempts++
	// Simulate a concurrent winning allocation before the retry.
	winning := cloneProject(first.projects["FLUENT"])
	winning.NextIssueNumber = 2
	r.projects["FLUENT"] = winning
	second := r.copy()
	if err := fn(recordingTx{memoryTx{repo: second}, r}); err != nil {
		return err
	}
	r.attempts++
	r.projects, r.issues, r.refs, r.comments = second.projects, second.issues, second.refs, second.comments
	return nil
}

type recordingTx struct {
	memoryTx
	owner *retryRepository
}

func (t recordingTx) PutIssue(ctx context.Context, issue *Issue) error {
	t.owner.seenIDs = append(t.owner.seenIDs, issue.ID)
	t.owner.seenNumbers = append(t.owner.seenNumbers, issue.Number)
	return t.memoryTx.PutIssue(ctx, issue)
}

func TestTransactionRetryReusesIDAndTimestampButReallocatesNumber(t *testing.T) {
	ctx := context.Background()
	base := newMemoryRepository()
	svc := testService(t, base)
	project, _ := svc.CreateProject(ctx, "FLUENT")
	if project.NextIssueNumber != 1 {
		t.Fatal(project.NextIssueNumber)
	}
	retry := &retryRepository{memoryRepository: base}
	svc.repo = retry
	created, err := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Retry", Description: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if retry.attempts != 2 || retry.seenIDs[0] != retry.seenIDs[1] || retry.seenNumbers[0] != 1 || retry.seenNumbers[1] != 2 {
		t.Fatalf("retry IDs=%v numbers=%v", retry.seenIDs, retry.seenNumbers)
	}
	if created.Number != 2 || created.Ref != "FLUENT-2" {
		t.Fatalf("created = %#v", created)
	}
	if !created.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)) {
		t.Fatalf("created time = %v", created.Created)
	}
}
