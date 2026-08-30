package tissues

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
)

type failingRepositoryRoot struct{ err error }

func (r failingRepositoryRoot) ForTenant(TenantID) (TenantRepository, error) { return nil, r.err }

type failingAssetRoot struct{ err error }

func (r failingAssetRoot) ForTenant(TenantID) (TenantAssetStore, error) { return nil, r.err }

type fixedRepositoryRoot struct{ repo TenantRepository }

func (r fixedRepositoryRoot) ForTenant(id TenantID) (TenantRepository, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return r.repo, nil
}

type fixedAssetRoot struct{ assets TenantAssetStore }

func (r fixedAssetRoot) ForTenant(id TenantID) (TenantAssetStore, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return r.assets, nil
}

type accessCountingRepositoryRoot struct {
	root       Repository
	operations int
}

func (r *accessCountingRepositoryRoot) ForTenant(id TenantID) (TenantRepository, error) {
	bound, err := r.root.ForTenant(id)
	if err != nil {
		return nil, err
	}
	return accessCountingTenantRepository{TenantRepository: bound, root: r}, nil
}

type accessCountingTenantRepository struct {
	TenantRepository
	root *accessCountingRepositoryRoot
}

func (r accessCountingTenantRepository) GetIssue(ctx context.Context, ref IssueRef) (*Issue, error) {
	r.root.operations++
	return r.TenantRepository.GetIssue(ctx, ref)
}

func TestServiceBootstrapValidationAndOperationBindingFailClosed(t *testing.T) {
	profileFor := func(tenant string) *coreconfig.Slot[Config] {
		profile, err := coreconfig.NewServiceProfile("test", Config{BootstrapTenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		slot, err := coreconfig.NewSlot(profile)
		if err != nil {
			t.Fatal(err)
		}
		return slot
	}
	invalid := profileFor("default")
	if _, err := New(invalid, newMemoryRepository(), newMemoryAssetStore()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid bootstrap tenant = %v", err)
	}
	want := errors.New("bind failed")
	valid := profileFor(testTenantID.String())
	repoFailure, err := New(valid, failingRepositoryRoot{err: want}, newMemoryAssetStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repoFailure.CreateProject(context.Background(), "FAIL"); !errors.Is(err, ErrInternal) {
		t.Fatalf("repository bind = %v", err)
	}
	countingRepository := &accessCountingRepositoryRoot{root: newMemoryRepository()}
	assetFailure, err := New(valid, countingRepository, failingAssetRoot{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assetFailure.ListAssets(context.Background(), "FAIL-1"); !errors.Is(err, ErrInternal) {
		t.Fatalf("asset bind = %v", err)
	}
	if countingRepository.operations != 0 {
		t.Fatalf("asset binding failure performed %d repository operations", countingRepository.operations)
	}
}

func TestTenantResolutionFailurePerformsNoStoreAccess(t *testing.T) {
	repository := newMemoryRepository()
	assets := newMemoryAssetStore()
	svc := testServiceWithAssets(t, repository, assets)
	svc.resolveTenant = func(context.Context) (TenantID, error) {
		return "", errors.New("tenant authority unavailable")
	}
	if _, err := svc.CreateProject(context.Background(), "ALPHA"); !errors.Is(err, ErrInternal) {
		t.Fatalf("CreateProject error = %v", err)
	}
	if len(repository.tenants) != 0 || len(assets.tenants) != 0 {
		t.Fatalf("resolver failure accessed stores: repository=%d assets=%d", len(repository.tenants), len(assets.tenants))
	}
}

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
	if b.ParentRef != a.Ref {
		t.Fatalf("parent = %#v", b)
	}
	same, err := svc.MoveIssue(ctx, b.Ref, a.Ref)
	if err != nil || !same.Updated.Equal(b.Updated) || same.ParentRef != a.Ref {
		t.Fatalf("same-parent no-op = %#v, %v", same, err)
	}
	project, _ := mustMemoryTenantRepository(t, repo).GetProject(ctx, "FLUENT")
	if project.NextIssueNumber != 4 {
		t.Fatalf("allocator = %d", project.NextIssueNumber)
	}
	original := *b
	moved, err := svc.MoveIssue(ctx, b.Ref, "FLUENT-3")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ProjectKey != original.ProjectKey || moved.Number != original.Number || moved.Ref != original.Ref || moved.ParentRef != c.Ref {
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
	if err != nil || detached.ParentRef != "" {
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
	if updated.Number != issue.Number || updated.Ref != issue.Ref || updated.ProjectKey != issue.ProjectKey {
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
	svc.now = func() time.Time { return comment.Created.Add(-time.Hour) }
	third, err := svc.AddComment(ctx, issue.Ref, "Ada", "third")
	if err != nil || !third.Created.Equal(comment.Created.Add(2*time.Nanosecond)) {
		t.Fatalf("third comment ordering = %#v, %v", third, err)
	}
	svc.now = func() time.Time { return comment.Created.Add(time.Hour) }
	closed, _ := svc.CloseIssue(ctx, issue.Ref)
	reopened, _ := svc.ReopenIssue(ctx, issue.Ref)
	if closed.State != StateClosed || reopened.State != StateOpen {
		t.Fatal("state transitions failed")
	}
}

func TestCreateIssueRejectsCanonicalRefCollision(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepository()
	svc := testService(t, repo)
	_, _ = svc.CreateProject(ctx, "FLUENT")
	_, _ = svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "First", Description: "body"})
	bound := mustMemoryTenantRepository(t, repo)
	repo.mu.Lock()
	bound.data().projects["FLUENT"].NextIssueNumber = 1
	repo.mu.Unlock()
	if _, err := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Collision", Description: "body"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("canonical ref collision = %v", err)
	}
}

func TestStoredHierarchyCorruptionUsesCanonicalRefs(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(map[string]*Issue)
	}{
		{name: "missing parent", mutate: func(issues map[string]*Issue) { issues["FLUENT-2"].ParentRef = "FLUENT-999" }},
		{name: "cycle", mutate: func(issues map[string]*Issue) {
			issues["FLUENT-1"].ParentRef = "FLUENT-2"
			issues["FLUENT-2"].ParentRef = "FLUENT-1"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newMemoryRepository()
			svc := testService(t, repo)
			_, _ = svc.CreateProject(ctx, "FLUENT")
			_, _ = svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "One", Description: "body"})
			_, _ = svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Two", Description: "body"})
			bound := mustMemoryTenantRepository(t, repo)
			repo.mu.Lock()
			test.mutate(bound.data().issues["FLUENT"])
			repo.mu.Unlock()
			if _, err := svc.ListIssues(ctx, "FLUENT"); !errors.Is(err, ErrInternal) {
				t.Fatalf("corruption = %v", err)
			}
		})
	}
}

func TestMoveIssueRejectsStoredAncestorCorruption(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(map[string]*Issue)
	}{
		{name: "missing ancestor", mutate: func(issues map[string]*Issue) { issues["FLUENT-2"].ParentRef = "FLUENT-999" }},
		{name: "ancestor cycle", mutate: func(issues map[string]*Issue) {
			issues["FLUENT-2"].ParentRef = "FLUENT-3"
			issues["FLUENT-3"].ParentRef = "FLUENT-2"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newMemoryRepository()
			svc := testService(t, repo)
			_, _ = svc.CreateProject(ctx, "FLUENT")
			for _, title := range []string{"One", "Two", "Three"} {
				_, _ = svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: title, Description: "body"})
			}
			bound := mustMemoryTenantRepository(t, repo)
			repo.mu.Lock()
			test.mutate(bound.data().issues["FLUENT"])
			repo.mu.Unlock()
			if _, err := svc.MoveIssue(ctx, "FLUENT-1", "FLUENT-2"); !errors.Is(err, ErrInternal) {
				t.Fatalf("stored corruption = %v", err)
			}
		})
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
	if err != nil || updated.Title != title || updated.Description != description || updated.ParentRef != parent.Ref {
		t.Fatalf("content update = %#v, %v", updated, err)
	}
	detached, err := svc.MoveIssue(ctx, child.Ref, "")
	if err != nil || detached.ParentRef != "" {
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
	*memoryTenantRepository
	attempts    int
	seenRefs    []string
	seenNumbers []int64
}

func (r *retryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	first := copyMemoryTenantData(r.data())
	if err := fn(recordingTx{memoryTx{data: first}, r}); err != nil {
		return err
	}
	r.attempts++
	// Simulate a concurrent winning allocation before the retry.
	winning := cloneProject(first.projects["FLUENT"])
	r.data().projects["FLUENT"] = winning
	r.data().issues["FLUENT"] = first.issues["FLUENT"]
	r.data().comments["FLUENT"] = first.comments["FLUENT"]
	second := copyMemoryTenantData(r.data())
	if err := fn(recordingTx{memoryTx{data: second}, r}); err != nil {
		return err
	}
	r.attempts++
	r.root.tenants[r.tenantID] = second
	return nil
}

type recordingTx struct {
	memoryTx
	owner *retryRepository
}

func (t recordingTx) PutIssue(ctx context.Context, issue *Issue) error {
	t.owner.seenRefs = append(t.owner.seenRefs, issue.Ref)
	t.owner.seenNumbers = append(t.owner.seenNumbers, issue.Number)
	return t.memoryTx.PutIssue(ctx, issue)
}

func TestTransactionRetryReallocatesRefAndPreservesTimestamp(t *testing.T) {
	ctx := context.Background()
	base := newMemoryRepository()
	svc := testService(t, base)
	project, _ := svc.CreateProject(ctx, "FLUENT")
	if project.NextIssueNumber != 1 {
		t.Fatal(project.NextIssueNumber)
	}
	retry := &retryRepository{memoryTenantRepository: mustMemoryTenantRepository(t, base)}
	svc.repo = fixedRepositoryRoot{repo: retry}
	created, err := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Retry", Description: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if retry.attempts != 2 || !reflect.DeepEqual(retry.seenRefs, []string{"FLUENT-1", "FLUENT-2"}) || !reflect.DeepEqual(retry.seenNumbers, []int64{1, 2}) {
		t.Fatalf("retry refs=%v numbers=%v", retry.seenRefs, retry.seenNumbers)
	}
	if created.Number != 2 || created.Ref != "FLUENT-2" {
		t.Fatalf("created = %#v", created)
	}
	if !created.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)) {
		t.Fatalf("created time = %v", created.Created)
	}
	project, err = retry.GetProject(ctx, "FLUENT")
	if err != nil || project.NextIssueNumber != 3 {
		t.Fatalf("allocator = %#v, %v", project, err)
	}
}

func TestIssueCreationDoesNotUseIDGeneratorButCommentsDo(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, newMemoryRepository())
	_, _ = svc.CreateProject(ctx, "FLUENT")
	calls := 0
	svc.newID = func() (string, error) {
		calls++
		return "cccccccccccccccccccccccccc", nil
	}
	issue, err := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Issue", Description: "body"})
	if err != nil || calls != 0 || issue.Ref != "FLUENT-1" {
		t.Fatalf("Issue creation = %#v, %v; ID calls=%d", issue, err, calls)
	}
	comment, err := svc.AddComment(ctx, issue.Ref, "Ada", "body")
	if err != nil || calls != 1 || comment.ID != "cccccccccccccccccccccccccc" {
		t.Fatalf("Comment creation = %#v, %v; ID calls=%d", comment, err, calls)
	}
}

type sequenceRepository struct {
	*memoryTenantRepository
	operations []string
	before     *Issue
	unchanged  bool
}

func (r *sequenceRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	return r.memoryTenantRepository.RunInTransaction(ctx, func(tx Transaction) error {
		return fn(&sequenceTx{Transaction: tx, owner: r})
	})
}

type sequenceTx struct {
	Transaction
	owner *sequenceRepository
}

func (t *sequenceTx) GetIssue(ctx context.Context, ref IssueRef) (*Issue, error) {
	t.owner.operations = append(t.owner.operations, "GetIssue")
	issue, err := t.Transaction.GetIssue(ctx, ref)
	if err == nil {
		t.owner.before = cloneIssue(issue)
	}
	return issue, err
}
func (t *sequenceTx) GetComment(ctx context.Context, ref IssueRef, id string) (*Comment, error) {
	t.owner.operations = append(t.owner.operations, "GetComment")
	return t.Transaction.GetComment(ctx, ref, id)
}
func (t *sequenceTx) GetLastComment(ctx context.Context, ref IssueRef) (*Comment, error) {
	t.owner.operations = append(t.owner.operations, "GetLastComment")
	return t.Transaction.GetLastComment(ctx, ref)
}
func (t *sequenceTx) PutComment(ctx context.Context, ref IssueRef, comment *Comment) error {
	t.owner.operations = append(t.owner.operations, "PutComment")
	return t.Transaction.PutComment(ctx, ref, comment)
}
func (t *sequenceTx) PutIssue(ctx context.Context, issue *Issue) error {
	t.owner.operations = append(t.owner.operations, "PutIssue")
	t.owner.unchanged = reflect.DeepEqual(t.owner.before, issue)
	return t.Transaction.PutIssue(ctx, issue)
}

func TestAddCommentReadWriteOrderAndSerializationFence(t *testing.T) {
	ctx := context.Background()
	base := newMemoryRepository()
	svc := testService(t, base)
	_, _ = svc.CreateProject(ctx, "FLUENT")
	issue, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Issue", Description: "body"})
	repo := &sequenceRepository{memoryTenantRepository: mustMemoryTenantRepository(t, base)}
	svc.repo = fixedRepositoryRoot{repo: repo}
	if _, err := svc.AddComment(ctx, issue.Ref, "Ada", "body"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GetIssue", "GetComment", "GetLastComment", "PutComment", "PutIssue"}
	if !reflect.DeepEqual(repo.operations, want) || !repo.unchanged {
		t.Fatalf("operations=%v unchanged Issue=%v", repo.operations, repo.unchanged)
	}
}

type commentRetryRepository struct {
	*memoryTenantRepository
	attempts int
	ids      []string
	created  []time.Time
}

func (r *commentRetryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	r.root.mu.Lock()
	defer r.root.mu.Unlock()
	first := copyMemoryTenantData(r.data())
	if err := fn(commentRecordingTx{memoryTx: memoryTx{data: first}, owner: r}); err != nil {
		return err
	}
	r.attempts++
	ref := "FLUENT-1"
	winner := &Comment{ID: "zzzzzzzzzzzzzzzzzzzzzzzzzz", Author: "winner", Created: r.created[0], Updated: r.created[0], Body: "winner"}
	if r.data().comments["FLUENT"][ref] == nil {
		r.data().comments["FLUENT"][ref] = map[string]*Comment{}
	}
	r.data().comments["FLUENT"][ref][winner.ID] = winner
	second := copyMemoryTenantData(r.data())
	if err := fn(commentRecordingTx{memoryTx: memoryTx{data: second}, owner: r}); err != nil {
		return err
	}
	r.attempts++
	r.root.tenants[r.tenantID] = second
	return nil
}

type commentRecordingTx struct {
	memoryTx
	owner *commentRetryRepository
}

func (t commentRecordingTx) PutComment(ctx context.Context, ref IssueRef, comment *Comment) error {
	t.owner.ids = append(t.owner.ids, comment.ID)
	t.owner.created = append(t.owner.created, comment.Created)
	return t.memoryTx.PutComment(ctx, ref, comment)
}

func TestAddCommentRetryKeepsIDAndRecomputesChronology(t *testing.T) {
	ctx := context.Background()
	base := newMemoryRepository()
	svc := testService(t, base)
	_, _ = svc.CreateProject(ctx, "FLUENT")
	issue, _ := svc.CreateIssue(ctx, "FLUENT", CreateIssueRequest{Title: "Issue", Description: "body"})
	first, err := svc.AddComment(ctx, issue.Ref, "Ada", "first")
	if err != nil {
		t.Fatal(err)
	}
	repo := &commentRetryRepository{memoryTenantRepository: mustMemoryTenantRepository(t, base)}
	svc.repo = fixedRepositoryRoot{repo: repo}
	comment, err := svc.AddComment(ctx, issue.Ref, "Ada", "retried")
	if err != nil {
		t.Fatal(err)
	}
	if repo.attempts != 2 || len(repo.ids) != 2 || repo.ids[0] != repo.ids[1] || comment.ID != repo.ids[0] {
		t.Fatalf("attempts=%d IDs=%v comment=%#v", repo.attempts, repo.ids, comment)
	}
	if !repo.created[0].Equal(first.Created.Add(time.Nanosecond)) || !repo.created[1].Equal(first.Created.Add(2*time.Nanosecond)) || !comment.Created.Equal(repo.created[1]) {
		t.Fatalf("created=%v final=%v", repo.created, comment.Created)
	}
}
