package tissues

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/lib/core/config"
)

func TestIssueLifecycleHierarchyAndComments(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepository()
	svc := testService(t, repo)
	base := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }
	ids := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccc", "dddddddddddddddddddddddddd", "eeeeeeeeeeeeeeeeeeeeeeeeee", "ffffffffffffffffffffffffff"}
	svc.newID = func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	a, err := svc.CreateIssue(ctx, CreateIssueRequest{Title: "A", Description: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateIssue(ctx, CreateIssueRequest{Title: "B", Description: "beta", ParentID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.CreateIssue(ctx, CreateIssueRequest{Title: "C"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.CreateIssue(ctx, CreateIssueRequest{Title: "D", ParentID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	dCreated, dUpdated := d.Created, d.Updated
	if a.State != StateOpen || !a.Created.Equal(a.Updated) || a.Created.Location() != time.UTC {
		t.Fatalf("created A=%#v", a)
	}
	roots, err := svc.ListIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0].ID != a.ID || roots[0].Children[0].ID != b.ID {
		t.Fatalf("roots=%#v", roots)
	}
	newTitle, newDescription := "B updated", "new markdown"
	base = base.Add(time.Minute)
	updated, err := svc.UpdateIssue(ctx, UpdateIssueRequest{ID: b.ID, Title: &newTitle, Description: &newDescription})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != b.ID || updated.Title != newTitle || updated.Description != newDescription || !updated.Created.Equal(b.Created) || !updated.Updated.Equal(base) {
		t.Fatalf("updated=%#v", updated)
	}
	firstUpdated := updated.Updated
	base = base.Add(time.Minute)
	moved, err := svc.MoveIssue(ctx, b.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != b.ID || moved.ParentID != c.ID || moved.Title != newTitle || !moved.Updated.Equal(base) {
		t.Fatalf("moved=%#v", moved)
	}
	readBWithSubtree, _ := svc.GetIssue(ctx, b.ID)
	if len(readBWithSubtree.Children) != 1 || readBWithSubtree.Children[0].ID != d.ID || !readBWithSubtree.Children[0].Created.Equal(dCreated) || !readBWithSubtree.Children[0].Updated.Equal(dUpdated) {
		t.Fatalf("move did not preserve subtree: %#v", readBWithSubtree)
	}
	if _, err := svc.MoveIssue(ctx, b.ID, b.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self-parent error=%v", err)
	}
	if _, err := svc.MoveIssue(ctx, c.ID, b.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error=%v", err)
	}
	readC, _ := svc.GetIssue(ctx, c.ID)
	if len(readC.Children) != 1 || readC.Children[0].ID != b.ID {
		t.Fatalf("rejected cycle changed hierarchy: %#v", readC)
	}
	base = base.Add(time.Minute)
	detached, err := svc.MoveIssue(ctx, b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if detached.ParentID != "" {
		t.Fatalf("detach=%#v", detached)
	}
	base = base.Add(time.Minute)
	if _, err := svc.MoveIssue(ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveIssue(ctx, b.ID, "zzzzzzzzzzzzzzzzzzzzzzzzzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing parent error=%v", err)
	}
	base = base.Add(time.Minute)
	closed, err := svc.CloseIssue(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != StateClosed {
		t.Fatalf("closed=%#v", closed)
	}
	closedAt := closed.Updated
	base = base.Add(time.Minute)
	closedAgain, err := svc.CloseIssue(ctx, b.ID)
	if err != nil || !closedAgain.Updated.Equal(closedAt) {
		t.Fatalf("repeated close=%#v,%v", closedAgain, err)
	}
	base = base.Add(time.Minute)
	opened, err := svc.ReopenIssue(ctx, b.ID)
	if err != nil || opened.State != StateOpen || !opened.Updated.Equal(base) {
		t.Fatalf("reopen=%#v,%v", opened, err)
	}
	comment1, err := svc.AddComment(ctx, b.ID, "agent@example", "first")
	if err != nil {
		t.Fatal(err)
	}
	comment2, err := svc.AddComment(ctx, b.ID, "human@example", "second")
	if err != nil {
		t.Fatal(err)
	}
	if !comment2.Created.Equal(comment1.Created.Add(time.Nanosecond)) {
		t.Fatalf("comment times %v,%v", comment1.Created, comment2.Created)
	}
	base = base.Add(time.Hour)
	edited, err := svc.EditComment(ctx, b.ID, comment1.ID, "first edited")
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != comment1.ID || edited.Author != comment1.Author || !edited.Created.Equal(comment1.Created) || !edited.Updated.Equal(base) {
		t.Fatalf("edited=%#v", edited)
	}
	readB, err := svc.GetIssue(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(readB.Comments) != 2 || readB.Comments[0].ID != comment1.ID || readB.Comments[1].ID != comment2.ID {
		t.Fatalf("comments=%#v", readB.Comments)
	}
	if firstUpdated.Equal(readB.Updated) {
		t.Fatal("expected subsequent mutations to advance issue timestamp")
	}
}

func TestNotFoundAndValidation(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	ctx := context.Background()
	svc.newID = func() (string, error) { return "aaaaaaaaaaaaaaaaaaaaaaaaaa", nil }
	svc.now = func() time.Time { return time.Now().UTC() }
	if _, err := svc.GetIssue(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIssue error=%v", err)
	}
	if _, err := svc.CreateIssue(ctx, CreateIssueRequest{Title: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateIssue error=%v", err)
	}
}
func TestIDFailureIsInternal(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	svc.newID = func() (string, error) { return "", errors.New("no entropy") }
	if _, err := svc.CreateIssue(context.Background(), CreateIssueRequest{Title: "x"}); !errors.Is(err, ErrInternal) {
		t.Fatalf("error=%v", err)
	}
}

func TestTransactionRetriesReuseLogicalIDAndTimestamp(t *testing.T) {
	repo := &retryRepository{}
	svc := testService(t, repo)
	fixed := time.Date(2026, 8, 28, 12, 0, 0, 7, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() (string, error) { return "aaaaaaaaaaaaaaaaaaaaaaaaaa", nil }
	created, err := svc.CreateIssue(context.Background(), CreateIssueRequest{Title: "retry safe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.puts) != 2 {
		t.Fatalf("PutIssue calls=%d", len(repo.puts))
	}
	for _, put := range repo.puts {
		if put.ID != created.ID || !put.Created.Equal(fixed) || !put.Updated.Equal(fixed) {
			t.Fatalf("retry changed logical input: %#v", put)
		}
	}
}

func testService(t *testing.T, repo Repository) *Service {
	t.Helper()
	profile, err := config.NewServiceProfile("test", Config{Enabled: true, Storage: StorageConfig{ProjectID: "example", Namespace: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := config.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(slot, repo)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type memoryRepository struct {
	issues   map[string]*Issue
	comments map[string]map[string]*Comment
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{issues: map[string]*Issue{}, comments: map[string]map[string]*Comment{}}
}
func (m *memoryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	return fn(memoryTx{m})
}
func (m *memoryRepository) ListIssues(context.Context) ([]*Issue, error) {
	flat := map[string]*Issue{}
	for id, i := range m.issues {
		flat[id] = cloneIssue(i)
		flat[id].Children = nil
		flat[id].Comments = nil
		for _, c := range m.comments[id] {
			flat[id].Comments = append(flat[id].Comments, cloneComment(c))
		}
	}
	var roots []*Issue
	for _, i := range flat {
		if i.ParentID == "" {
			roots = append(roots, i)
		} else {
			flat[i.ParentID].Children = append(flat[i.ParentID].Children, i)
		}
	}
	sortIssues(roots)
	return roots, nil
}
func (m *memoryRepository) GetIssue(ctx context.Context, id string) (*Issue, error) {
	roots, _ := m.ListIssues(ctx)
	if issue := findIssue(roots, id); issue != nil {
		return issue, nil
	}
	return nil, ErrNotFound
}

type memoryTx struct{ repo *memoryRepository }

func (t memoryTx) GetIssue(_ context.Context, id string) (*Issue, error) {
	i := t.repo.issues[id]
	if i == nil {
		return nil, ErrNotFound
	}
	return cloneIssue(i), nil
}
func (t memoryTx) GetComment(_ context.Context, issueID, id string) (*Comment, error) {
	c := t.repo.comments[issueID][id]
	if c == nil {
		return nil, ErrNotFound
	}
	return cloneComment(c), nil
}
func (t memoryTx) ListComments(_ context.Context, issueID string) ([]*Comment, error) {
	var out []*Comment
	for _, c := range t.repo.comments[issueID] {
		out = append(out, cloneComment(c))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out, nil
}
func (t memoryTx) PutIssue(_ context.Context, i *Issue) error {
	t.repo.issues[i.ID] = cloneIssue(i)
	t.repo.issues[i.ID].Children = nil
	t.repo.issues[i.ID].Comments = nil
	return nil
}
func (t memoryTx) PutComment(_ context.Context, issueID string, c *Comment) error {
	if t.repo.comments[issueID] == nil {
		t.repo.comments[issueID] = map[string]*Comment{}
	}
	t.repo.comments[issueID][c.ID] = cloneComment(c)
	return nil
}

type retryRepository struct{ puts []*Issue }

func (r *retryRepository) ListIssues(context.Context) ([]*Issue, error)     { return nil, nil }
func (r *retryRepository) GetIssue(context.Context, string) (*Issue, error) { return nil, ErrNotFound }
func (r *retryRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	for range 2 {
		if err := fn(retryTx{r}); err != nil {
			return err
		}
	}
	return nil
}

type retryTx struct{ repo *retryRepository }

func (retryTx) GetIssue(context.Context, string) (*Issue, error)             { return nil, ErrNotFound }
func (retryTx) GetComment(context.Context, string, string) (*Comment, error) { return nil, ErrNotFound }
func (retryTx) ListComments(context.Context, string) ([]*Comment, error)     { return nil, nil }
func (t retryTx) PutIssue(_ context.Context, i *Issue) error {
	t.repo.puts = append(t.repo.puts, cloneIssue(i))
	return nil
}
func (retryTx) PutComment(context.Context, string, *Comment) error { return nil }
