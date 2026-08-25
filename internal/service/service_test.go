package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/store"
)

// --- fixtures ---------------------------------------------------------------

// git runs a raw git command to build fixtures and to inspect results
// independently of the code under test.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func configure(t *testing.T, dir, who string) {
	t.Helper()
	git(t, dir, "config", "user.name", who)
	git(t, dir, "config", "user.email", who+"@example")
	git(t, dir, "config", "commit.gpgsign", "false")
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	configure(t, dir, "tissues")
	return dir
}

func initBare(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--bare", "-b", "main")
	return dir
}

func clone(t *testing.T, src, who string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	configure(t, dst, who)
	return dst
}

// clock is the whole clock seam: a settable time, no sleeping anywhere.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newService(t *testing.T, dir string, remoteSync bool) (*Service, *clock) {
	t.Helper()
	s, err := New(context.Background(), dir, remoteSync)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)}
	s.now = c.now
	return s, c
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0 // unborn branch
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func status(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "status", "--porcelain")
}

func assertClean(t *testing.T, dir, when string) {
	t.Helper()
	if s := status(t, dir); s != "" {
		t.Errorf("%s: working tree is dirty:\n%s", when, s)
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ptr(s string) *string { return &s }

// mustCreate creates a root issue and fails the test if it does not work.
func mustCreate(t *testing.T, s *Service, title string) *model.Issue {
	t.Helper()
	iss, err := s.CreateIssue(context.Background(), CreateIssueRequest{Title: title})
	if err != nil {
		t.Fatalf("CreateIssue(%q): %v", title, err)
	}
	return iss
}

// --- semantics --------------------------------------------------------------

func TestCompleteSemanticFlow(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)

	root := mustCreate(t, s, "Root issue")
	c.advance(time.Minute)
	child, err := s.CreateIssue(ctx, CreateIssueRequest{
		ParentID: root.ID, Title: "Child issue", Description: "Original description.",
	})
	if err != nil {
		t.Fatal(err)
	}

	c.advance(time.Minute)
	if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{
		ID: child.ID, Title: ptr("Renamed child"), Description: ptr("Rewritten description."),
	}); err != nil {
		t.Fatal(err)
	}

	c.advance(time.Minute)
	first, err := s.AddComment(ctx, child.ID, "human@example", "First comment.")
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	second, err := s.AddComment(ctx, child.ID, "agent@example", "Second comment.")
	if err != nil {
		t.Fatal(err)
	}

	c.advance(time.Hour)
	if _, err := s.EditComment(ctx, child.ID, first.ID, "First comment, edited."); err != nil {
		t.Fatal(err)
	}

	c.advance(time.Minute)
	if _, err := s.CloseIssue(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	if _, err := s.ReopenIssue(ctx, child.ID); err != nil {
		t.Fatal(err)
	}

	assertClean(t, dir, "after the flow")
	if got, want := commitCount(t, dir), 8; got != want {
		t.Errorf("commits = %d, want %d (one per changed semantic operation)", got, want)
	}

	// A brand-new Service on the same directory must see identical state:
	// nothing lives in memory between calls, let alone between processes.
	fresh, _ := newService(t, dir, false)
	roots, err := fresh.ListIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("ListIssues returned %d roots, want the one root issue", len(roots))
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].ID != child.ID {
		t.Fatalf("root does not contain the child issue")
	}

	got, err := fresh.GetIssue(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Renamed child" {
		t.Errorf("Title = %q, want %q", got.Title, "Renamed child")
	}
	if got.Description != "Rewritten description." {
		t.Errorf("Description = %q", got.Description)
	}
	if got.State != model.StateOpen {
		t.Errorf("State = %q, want open after close then reopen", got.State)
	}
	if got.ParentID != root.ID {
		t.Errorf("ParentID = %q, want %q", got.ParentID, root.ID)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(got.Comments))
	}
	if got.Comments[0].ID != first.ID || got.Comments[1].ID != second.ID {
		t.Errorf("comment order = %v, want the creation order", []string{got.Comments[0].ID, got.Comments[1].ID})
	}
	if got.Comments[0].Body != "First comment, edited." {
		t.Errorf("edited body = %q", got.Comments[0].Body)
	}
	if got.Comments[0].Author != "human@example" {
		t.Errorf("author changed during edit: %q", got.Comments[0].Author)
	}
}

func TestDomainTimestamps(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	start := c.t

	iss := mustCreate(t, s, "Timestamps")
	if !iss.Created.Equal(model.Timestamp(start)) || !iss.Updated.Equal(iss.Created) {
		t.Errorf("CreateIssue: Created=%v Updated=%v, want both %v", iss.Created, iss.Updated, model.Timestamp(start))
	}

	c.advance(time.Hour)
	updated, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("Renamed")})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Created.Equal(iss.Created) {
		t.Errorf("UpdateIssue changed Created: %v, want %v", updated.Created, iss.Created)
	}
	if !updated.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("UpdateIssue: Updated = %v, want %v", updated.Updated, model.Timestamp(c.t))
	}

	c.advance(time.Hour)
	closed, err := s.CloseIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !closed.Created.Equal(iss.Created) {
		t.Errorf("CloseIssue changed Created: %v", closed.Created)
	}
	if !closed.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("CloseIssue: Updated = %v, want %v", closed.Updated, model.Timestamp(c.t))
	}

	c.advance(time.Hour)
	reopened, err := s.ReopenIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Created.Equal(iss.Created) {
		t.Errorf("ReopenIssue changed Created: %v", reopened.Created)
	}
	if !reopened.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("ReopenIssue: Updated = %v", reopened.Updated)
	}

	c.advance(time.Hour)
	added, err := s.AddComment(ctx, iss.ID, "a@example", "Body.")
	if err != nil {
		t.Fatal(err)
	}
	if !added.Created.Equal(model.Timestamp(c.t)) || !added.Updated.Equal(added.Created) {
		t.Errorf("AddComment: Created=%v Updated=%v, want both %v", added.Created, added.Updated, model.Timestamp(c.t))
	}

	c.advance(time.Hour)
	edited, err := s.EditComment(ctx, iss.ID, added.ID, "Different body.")
	if err != nil {
		t.Fatal(err)
	}
	if !edited.Created.Equal(added.Created) {
		t.Errorf("EditComment changed Created: %v, want %v", edited.Created, added.Created)
	}
	if !edited.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("EditComment: Updated = %v, want %v", edited.Updated, model.Timestamp(c.t))
	}
	if edited.Author != "a@example" {
		t.Errorf("EditComment changed Author: %q", edited.Author)
	}
}

func TestEditingAnEarlierCommentDoesNotMoveIt(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	iss := mustCreate(t, s, "Discussion")

	c.advance(time.Minute)
	first, err := s.AddComment(ctx, iss.ID, "a@example", "First.")
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	second, err := s.AddComment(ctx, iss.ID, "b@example", "Second.")
	if err != nil {
		t.Fatal(err)
	}

	// Edit the earlier comment much later than the later one was created.
	c.advance(30 * 24 * time.Hour)
	if _, err := s.EditComment(ctx, iss.ID, first.ID, "First, edited a month later."); err != nil {
		t.Fatal(err)
	}

	fresh, _ := newService(t, dir, false)
	got, err := fresh.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 2 || got.Comments[0].ID != first.ID || got.Comments[1].ID != second.ID {
		t.Fatalf("comment order changed after an edit: %v", []string{got.Comments[0].ID, got.Comments[1].ID})
	}
	if !got.Comments[0].Updated.After(got.Comments[1].Updated) {
		t.Error("fixture is wrong: the edited comment should have the later Updated")
	}
}

func TestNoOpsProduceNoCommits(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)

	iss, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Stable", Description: "Body."})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := s.AddComment(ctx, iss.ID, "a@example", "Comment body.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseIssue(ctx, iss.ID); err != nil {
		t.Fatal(err)
	}
	base := commitCount(t, dir)
	c.advance(time.Hour) // a no-op must not stamp Updated either

	noops := map[string]func() error{
		"update with identical fields": func() error {
			_, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("Stable"), Description: ptr("Body.")})
			return err
		},
		"update with no fields at all": func() error {
			_, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID})
			return err
		},
		"close an already closed issue": func() error {
			_, err := s.CloseIssue(ctx, iss.ID)
			return err
		},
		"edit a comment to its current body": func() error {
			_, err := s.EditComment(ctx, iss.ID, comment.ID, "Comment body.")
			return err
		},
	}
	for name, op := range noops {
		t.Run(name, func(t *testing.T) {
			if err := op(); err != nil {
				t.Fatalf("no-op returned an error: %v", err)
			}
			if got := commitCount(t, dir); got != base {
				t.Errorf("commits = %d, want %d (a no-op must not commit)", got, base)
			}
			assertClean(t, dir, "after a no-op")
		})
	}

	// Reopening an already open issue is the symmetric case.
	if _, err := s.ReopenIssue(ctx, iss.ID); err != nil {
		t.Fatal(err)
	}
	afterReopen := commitCount(t, dir)
	if afterReopen != base+1 {
		t.Fatalf("the first reopen should commit: %d, want %d", afterReopen, base+1)
	}
	if _, err := s.ReopenIssue(ctx, iss.ID); err != nil {
		t.Fatal(err)
	}
	if got := commitCount(t, dir); got != afterReopen {
		t.Errorf("reopening an open issue committed: %d, want %d", got, afterReopen)
	}

	// No-ops must leave the domain timestamps alone.
	fresh, _ := newService(t, dir, false)
	got, err := fresh.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Stable" || got.Description != "Body." {
		t.Errorf("no-op changed content: %q / %q", got.Title, got.Description)
	}
	if got.Comments[0].Body != "Comment body." {
		t.Errorf("no-op changed the comment: %q", got.Comments[0].Body)
	}
}

func TestCommitMessages(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)

	iss := mustCreate(t, s, "Message subject")
	if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseIssue(ctx, iss.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenIssue(ctx, iss.ID); err != nil {
		t.Fatal(err)
	}
	c, err := s.AddComment(ctx, iss.ID, "a@example", "Body.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditComment(ctx, iss.ID, c.ID, "Different body."); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"create issue " + iss.ID + ": Message subject",
		"update issue " + iss.ID,
		"close issue " + iss.ID,
		"reopen issue " + iss.ID,
		"comment " + c.ID + " on issue " + iss.ID,
		"edit comment " + c.ID + " on issue " + iss.ID,
	}
	got := strings.Split(git(t, dir, "log", "--reverse", "--format=%s"), "\n")
	if len(got) != len(want) {
		t.Fatalf("commit subjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commit %d subject = %q, want %q", i, got[i], want[i])
		}
	}
	// No trailers are added automatically.
	if body := git(t, dir, "log", "-1", "--format=%b"); body != "" {
		t.Errorf("commit body = %q, want empty", body)
	}
}

func TestNotFound(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Real issue")
	comment, err := s.AddComment(ctx, iss.ID, "a@example", "Body.")
	if err != nil {
		t.Fatal(err)
	}
	missing := store.NewID()
	other := mustCreate(t, s, "Other issue")

	cases := map[string]func() error{
		"get unknown issue": func() error { _, err := s.GetIssue(ctx, missing); return err },
		"unknown parent": func() error {
			_, err := s.CreateIssue(ctx, CreateIssueRequest{ParentID: missing, Title: "Orphan"})
			return err
		},
		"update unknown issue": func() error {
			_, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: missing, Title: ptr("x")})
			return err
		},
		"close unknown issue":   func() error { _, err := s.CloseIssue(ctx, missing); return err },
		"reopen unknown issue":  func() error { _, err := s.ReopenIssue(ctx, missing); return err },
		"comment unknown issue": func() error { _, err := s.AddComment(ctx, missing, "a@example", "Body."); return err },
		"edit unknown comment":  func() error { _, err := s.EditComment(ctx, iss.ID, missing, "Body."); return err },
		"edit comment wrong issue": func() error {
			_, err := s.EditComment(ctx, other.ID, comment.ID, "Body.")
			return err
		},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			before := commitCount(t, dir)
			err := op()
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
			}
			if got := commitCount(t, dir); got != before {
				t.Errorf("a not-found request committed: %d, want %d", got, before)
			}
			assertClean(t, dir, "after a not-found request")
		})
	}
}

func TestValidationErrors(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Host issue")

	cases := map[string]func() error{
		"empty title":     func() error { _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "   "}); return err },
		"multiline title": func() error { _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "a\nb"}); return err },
		"update to empty title": func() error {
			_, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("")})
			return err
		},
		"empty comment author": func() error { _, err := s.AddComment(ctx, iss.ID, "", "Body."); return err },
		"empty comment body":   func() error { _, err := s.AddComment(ctx, iss.ID, "a@example", "  "); return err },
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			before := commitCount(t, dir)
			if err := op(); !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want it to wrap ErrValidation", err)
			}
			if got := commitCount(t, dir); got != before {
				t.Errorf("an invalid request committed: %d, want %d", got, before)
			}
			assertClean(t, dir, "after an invalid request")
		})
	}
}

// tissues must never sweep unrelated working-tree changes into its commits,
// so a dirty repository is refused before anything is pulled or written.
func TestDirtyRepositoryIsRefused(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Original title")
	issueFile := issuePath(t, dir, iss.ID)

	dirty := map[string]func(t *testing.T){
		"untracked file": func(t *testing.T) {
			write(t, dir, "notes.txt", "unrelated work\n")
			t.Cleanup(func() { os.Remove(filepath.Join(dir, "notes.txt")) })
		},
		"modified tracked file": func(t *testing.T) {
			write(t, dir, "unrelated.txt", "committed\n")
			git(t, dir, "add", "--", "unrelated.txt")
			git(t, dir, "commit", "-m", "unrelated")
			write(t, dir, "unrelated.txt", "modified\n")
			t.Cleanup(func() { git(t, dir, "checkout", "--", "unrelated.txt") })
		},
		"staged change": func(t *testing.T) {
			write(t, dir, "staged.txt", "staged\n")
			git(t, dir, "add", "--", "staged.txt")
			t.Cleanup(func() {
				git(t, dir, "rm", "--cached", "-f", "--", "staged.txt")
				os.Remove(filepath.Join(dir, "staged.txt"))
			})
		},
	}
	for name, makeDirty := range dirty {
		t.Run(name, func(t *testing.T) {
			makeDirty(t)
			before := commitCount(t, dir)
			beforeFile, err := os.ReadFile(issueFile)
			if err != nil {
				t.Fatal(err)
			}

			for op, fn := range map[string]func() error{
				"create": func() error { _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Should not exist"}); return err },
				"update": func() error {
					_, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("Should not apply")})
					return err
				},
				"close":   func() error { _, err := s.CloseIssue(ctx, iss.ID); return err },
				"comment": func() error { _, err := s.AddComment(ctx, iss.ID, "a@example", "Should not exist."); return err },
			} {
				if err := fn(); !errors.Is(err, ErrRepository) {
					t.Errorf("%s: error = %v, want it to wrap ErrRepository", op, err)
				}
			}

			if got := commitCount(t, dir); got != before {
				t.Errorf("commits = %d, want %d", got, before)
			}
			afterFile, err := os.ReadFile(issueFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterFile) != string(beforeFile) {
				t.Error("the issue document changed despite the refusal")
			}
			// Reads still work: they only parse current filesystem state.
			if _, err := s.GetIssue(ctx, iss.ID); err != nil {
				t.Errorf("GetIssue failed on a dirty repository: %v", err)
			}
		})
	}
}

func issuePath(t *testing.T, root, id string) string {
	t.Helper()
	tree, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, ok := tree.IssueDir(id)
	if !ok {
		t.Fatalf("issue %q not found on disk", id)
	}
	return filepath.Join(root, filepath.FromSlash(dir), store.IssueFile)
}

// A repository can be valid Git while containing invalid tissues data.
func TestInvalidTissuesStateFailsClosed(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Doomed")
	file := issuePath(t, dir, iss.ID)

	// A human commits a broken document: Git is perfectly happy.
	if err := os.WriteFile(file, []byte("# Not a canonical document\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "--", filepath.ToSlash(rel))
	git(t, dir, "commit", "-m", "hand-edited")
	assertClean(t, dir, "after committing the corruption")
	base := commitCount(t, dir)

	for name, op := range map[string]func() error{
		"list":    func() error { _, err := s.ListIssues(ctx); return err },
		"get":     func() error { _, err := s.GetIssue(ctx, iss.ID); return err },
		"create":  func() error { _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "New"}); return err },
		"close":   func() error { _, err := s.CloseIssue(ctx, iss.ID); return err },
		"comment": func() error { _, err := s.AddComment(ctx, iss.ID, "a@example", "Body."); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := op(); !errors.Is(err, ErrRepository) {
				t.Fatalf("error = %v, want it to wrap ErrRepository", err)
			}
		})
	}
	if got := commitCount(t, dir); got != base {
		t.Errorf("commits = %d, want %d: nothing may be committed on top of invalid state", got, base)
	}
	assertClean(t, dir, "after refusing to work on invalid state")
}

// The service keeps no tree between calls, so a returned object is a snapshot
// and mutating it cannot reach the repository.
func TestReturnedObjectsAreNotLiveRepositoryState(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Snapshot")
	if _, err := s.AddComment(ctx, iss.ID, "a@example", "Real comment."); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := commitCount(t, dir)

	got.Title = "Tampered"
	got.Description = "Tampered."
	got.State = model.StateClosed
	got.Created = got.Created.Add(-time.Hour)
	got.Children = append(got.Children, &model.Issue{ID: store.NewID(), Title: "Phantom"})
	got.Comments[0].Body = "Tampered comment."
	got.Comments = append(got.Comments, &model.Comment{ID: store.NewID(), Author: "ghost", Body: "Phantom."})

	fresh, _ := newService(t, dir, false)
	after, err := fresh.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "Snapshot" || after.Description != "" || after.State != model.StateOpen {
		t.Errorf("repository state followed the returned object: %q %q %q", after.Title, after.Description, after.State)
	}
	if len(after.Children) != 0 {
		t.Errorf("phantom child reached the repository: %d children", len(after.Children))
	}
	if len(after.Comments) != 1 || after.Comments[0].Body != "Real comment." {
		t.Errorf("comments followed the returned object: %+v", after.Comments)
	}
	if got := commitCount(t, dir); got != base {
		t.Errorf("mutating a returned object created commits: %d, want %d", got, base)
	}
	assertClean(t, dir, "after tampering with a returned object")
}

// One mutex serializes everything: concurrent callers must not race each
// other into git's index lock or into a half-written tree.
func TestConcurrentMutationsSerialize(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, false)
	iss := mustCreate(t, s, "Contended")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.AddComment(ctx, iss.ID, "a@example", "Comment "+strconv.Itoa(i))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent AddComment %d: %v", i, err)
		}
	}

	if got := commitCount(t, dir); got != n+1 {
		t.Errorf("commits = %d, want %d", got, n+1)
	}
	assertClean(t, dir, "after concurrent mutations")

	fresh, _ := newService(t, dir, false)
	got, err := fresh.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatalf("repository is not reconstructable after concurrent mutations: %v", err)
	}
	if len(got.Comments) != n {
		t.Errorf("comments = %d, want %d", len(got.Comments), n)
	}
}

// --- Git integration --------------------------------------------------------

// The bootstrap case: an unborn branch and an empty remote. The first
// mutation must create the root commit and establish the upstream.
func TestBootstrapUnbornBranchAndEmptyRemote(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	dir := initRepo(t)
	git(t, dir, "remote", "add", "origin", bare)

	if commitCount(t, dir) != 0 {
		t.Fatal("fixture is wrong: the branch should be unborn")
	}
	if refs := git(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/"); refs != "" {
		t.Fatalf("fixture is wrong: the remote should be empty, has %q", refs)
	}

	s, _ := newService(t, dir, true)
	iss, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "First ever issue"})
	if err != nil {
		t.Fatalf("first remote-synchronized mutation: %v", err)
	}

	if got := commitCount(t, dir); got != 1 {
		t.Fatalf("commits = %d, want 1", got)
	}
	if parents := git(t, dir, "log", "-1", "--format=%P"); parents != "" {
		t.Errorf("the first commit has parents %q, want a root commit", parents)
	}
	local := git(t, dir, "rev-parse", "HEAD")
	if remote := git(t, bare, "rev-parse", "main"); remote != local {
		t.Errorf("remote main = %s, local HEAD = %s", remote, local)
	}
	if refs := git(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/"); refs != "refs/heads/main" {
		t.Errorf("remote branches = %q, want only refs/heads/main", refs)
	}
	if up := git(t, dir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/main"); up != "origin/main" {
		t.Errorf("upstream = %q, want origin/main", up)
	}
	if ab := git(t, dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "0\t0" {
		t.Errorf("ahead/behind = %q, want 0/0", ab)
	}
	assertClean(t, dir, "after the bootstrap mutation")

	fresh, _ := newService(t, dir, true)
	if _, err := fresh.GetIssue(ctx, iss.ID); err != nil {
		t.Errorf("issue not reconstructable after bootstrap: %v", err)
	}
}

// From a synchronized repository, an ordinary mutation makes exactly one new
// commit and publishes it.
func TestNormalRemoteMutation(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	dir := initRepo(t)
	git(t, dir, "remote", "add", "origin", bare)
	s, c := newService(t, dir, true)

	first := mustCreate(t, s, "First issue")
	before := commitCount(t, dir)

	c.advance(time.Minute)
	second, err := s.CreateIssue(ctx, CreateIssueRequest{ParentID: first.ID, Title: "Second issue"})
	if err != nil {
		t.Fatal(err)
	}
	if got := commitCount(t, dir); got != before+1 {
		t.Errorf("commits = %d, want %d", got, before+1)
	}
	local := git(t, dir, "rev-parse", "HEAD")
	if remote := git(t, bare, "rev-parse", "main"); remote != local {
		t.Errorf("remote = %s, local = %s: push did not publish the commit", remote, local)
	}
	if ab := git(t, dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "0\t0" {
		t.Errorf("ahead/behind = %q, want 0/0", ab)
	}
	assertClean(t, dir, "after a normal remote mutation")

	// The published repository really contains the domain state.
	check := clone(t, bare, "reader")
	reader, _ := newService(t, check, false)
	got, err := reader.GetIssue(ctx, second.ID)
	if err != nil {
		t.Fatalf("published repository does not contain the child issue: %v", err)
	}
	if got.ParentID != first.ID {
		t.Errorf("published ParentID = %q, want %q", got.ParentID, first.ID)
	}
}

// A mutation must fast-forward first and build on what the remote gained,
// never overwrite or lose it.
func TestMutationFastForwardsFromRemote(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	a := initRepo(t)
	git(t, a, "remote", "add", "origin", bare)
	sa, ca := newService(t, a, true)
	issueA := mustCreate(t, sa, "From A")

	b := clone(t, bare, "other")
	sb, _ := newService(t, b, true)
	issueB, err := sb.CreateIssue(ctx, CreateIssueRequest{Title: "From B"})
	if err != nil {
		t.Fatal(err)
	}

	// A knows nothing about B's issue yet.
	if _, err := sa.GetIssue(ctx, issueB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("A should not see B's issue before pulling: %v", err)
	}
	ca.advance(time.Minute)
	issueA2, err := sa.CreateIssue(ctx, CreateIssueRequest{Title: "From A again"})
	if err != nil {
		t.Fatalf("mutation after the remote advanced: %v", err)
	}

	// The pull brought B's work in and the mutation built on top of it.
	for _, want := range []string{issueA.ID, issueB.ID, issueA2.ID} {
		if _, err := sa.GetIssue(ctx, want); err != nil {
			t.Errorf("issue %s missing from A after the fast-forward: %v", want, err)
		}
	}
	if got := commitCount(t, a); got != 3 {
		t.Errorf("commits in A = %d, want 3", got)
	}
	if ab := git(t, a, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "0\t0" {
		t.Errorf("ahead/behind = %q, want 0/0", ab)
	}
	if merges := git(t, a, "log", "--merges", "--format=%H"); merges != "" {
		t.Errorf("a merge commit was created: %q; --ff-only must never merge", merges)
	}
	local := git(t, a, "rev-parse", "HEAD")
	if remote := git(t, bare, "rev-parse", "main"); remote != local {
		t.Errorf("remote = %s, local = %s", remote, local)
	}
	assertClean(t, a, "after fast-forwarding and mutating")

	// B can fast-forward to everything too: nothing was lost anywhere.
	git(t, b, "pull", "--ff-only")
	reader, _ := newService(t, b, false)
	for _, want := range []string{issueA.ID, issueB.ID, issueA2.ID} {
		if _, err := reader.GetIssue(ctx, want); err != nil {
			t.Errorf("issue %s missing from B: %v", want, err)
		}
	}
}

// Divergence is a hard stop. Nothing may be written, committed or changed.
func TestDivergentUpstreamAbortsBeforeAnyMutation(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	a := initRepo(t)
	git(t, a, "remote", "add", "origin", bare)
	sa, ca := newService(t, a, true)
	issueA := mustCreate(t, sa, "From A")

	// The remote moves ahead.
	b := clone(t, bare, "other")
	sb, _ := newService(t, b, true)
	issueB, err := sb.CreateIssue(ctx, CreateIssueRequest{Title: "From B"})
	if err != nil {
		t.Fatal(err)
	}

	// A commits locally without publishing, so the histories diverge.
	local, _ := newService(t, a, false)
	local.now = ca.now
	ca.advance(time.Minute)
	issueLocal, err := local.CreateIssue(ctx, CreateIssueRequest{Title: "Local only"})
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the divergence without fetching: A has its own unpublished
	// commit and does not even hold the remote tip's object, so no
	// fast-forward is possible. The service's own pull does the fetching.
	remoteHead := git(t, bare, "rev-parse", "main")
	if hasObject(t, a, remoteHead) {
		t.Fatalf("fixture is wrong: A already holds the remote tip %s", remoteHead)
	}
	if git(t, a, "rev-parse", "HEAD") == remoteHead {
		t.Fatal("fixture is wrong: the branches have not diverged")
	}

	headBefore := git(t, a, "rev-parse", "HEAD")
	countBefore := commitCount(t, a)
	treeBefore := git(t, a, "rev-parse", "HEAD^{tree}")

	ca.advance(time.Minute)
	refused, err := sa.CreateIssue(ctx, CreateIssueRequest{Title: "Must not exist"})
	if !errors.Is(err, ErrRepository) {
		t.Fatalf("error = %v, want it to wrap ErrRepository", err)
	}
	if errors.Is(err, ErrNotPushed) {
		t.Error("a divergent upstream must fail before committing, not after")
	}
	if errors.Is(err, ErrIncomplete) {
		t.Error("a divergent upstream must fail before writing, not after")
	}
	if refused != nil {
		t.Errorf("result = %+v, want nil when the repository refuses the mutation", refused)
	}

	if got := git(t, a, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved from %s to %s", headBefore, got)
	}
	if got := commitCount(t, a); got != countBefore {
		t.Errorf("commits = %d, want %d", got, countBefore)
	}
	if got := git(t, a, "rev-parse", "HEAD^{tree}"); got != treeBefore {
		t.Errorf("the committed tree changed")
	}
	// Nothing was written: a written file would show up as untracked, and the
	// index is untouched. Remote-tracking refs and FETCH_HEAD may legitimately
	// have advanced from the fetch inside the failed pull; that is normal Git
	// bookkeeping and is deliberately not asserted either way.
	assertClean(t, a, "after a refused mutation")

	// Local content is exactly what it was before the request.
	roots, err := sa.ListIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 (A's issue and A's local-only issue)", len(roots))
	}
	for _, want := range []string{issueA.ID, issueLocal.ID} {
		if _, err := sa.GetIssue(ctx, want); err != nil {
			t.Errorf("issue %s disappeared: %v", want, err)
		}
	}
	if _, err := sa.GetIssue(ctx, issueB.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B's issue should not have been pulled in: %v", err)
	}
	// The remote is untouched.
	if got := git(t, bare, "rev-parse", "main"); got != git(t, b, "rev-parse", "HEAD") {
		t.Error("the remote changed during a refused mutation")
	}
}

// A push failure never undoes the commit: the mutation stays, and the caller
// is told plainly that it was not published.
func TestPushRejectionKeepsTheLocalCommit(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	dir := initRepo(t)
	git(t, dir, "remote", "add", "origin", bare)
	s, c := newService(t, dir, true)

	first := mustCreate(t, s, "Published fine")
	published := git(t, dir, "rev-parse", "HEAD")

	// The remote now rejects every push.
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'pushes are rejected here' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c.advance(time.Minute)
	second, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Committed but not pushed"})
	if !errors.Is(err, ErrNotPushed) {
		t.Fatalf("error = %v, want it to wrap ErrNotPushed", err)
	}
	if second == nil {
		t.Fatal("the mutated issue must be returned alongside the push error")
	}
	if errors.Is(err, ErrIncomplete) {
		t.Error("a rejected push is not an incomplete transaction: the commit exists")
	}
	if errors.Is(err, ErrRepository) {
		t.Error("a rejected push is not a repository refusal: the mutation happened")
	}
	if second.Title != "Committed but not pushed" {
		t.Errorf("returned issue = %q", second.Title)
	}
	if !strings.Contains(err.Error(), "pushes are rejected here") {
		t.Errorf("error does not carry git's reason: %v", err)
	}

	// The commit stands, the tree is clean, the branch is simply ahead.
	if got := commitCount(t, dir); got != 2 {
		t.Errorf("commits = %d, want 2", got)
	}
	assertClean(t, dir, "after a rejected push")
	if ab := git(t, dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "1\t0" {
		t.Errorf("ahead/behind = %q, want 1/0", ab)
	}
	if got := git(t, bare, "rev-parse", "main"); got != published {
		t.Errorf("remote moved to %s, want %s", got, published)
	}

	// The domain mutation is present locally and survives a restart.
	fresh, _ := newService(t, dir, true)
	if _, err := fresh.GetIssue(ctx, second.ID); err != nil {
		t.Errorf("the committed issue is missing locally: %v", err)
	}
	if _, err := fresh.GetIssue(ctx, first.ID); err != nil {
		t.Errorf("the earlier issue is missing: %v", err)
	}

	// Once the remote accepts pushes again, a later mutation publishes the
	// backlog naturally.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	if _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Third"}); err != nil {
		t.Fatalf("mutation after the remote recovered: %v", err)
	}
	if ab := git(t, dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "0\t0" {
		t.Errorf("ahead/behind = %q, want 0/0 after recovery", ab)
	}
}

// Local mode never talks to a remote, even when one is configured.
func TestLocalModeNeverPushes(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	dir := initRepo(t)
	git(t, dir, "remote", "add", "origin", bare)
	s, _ := newService(t, dir, false)

	if _, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Local only"}); err != nil {
		t.Fatal(err)
	}
	if got := commitCount(t, dir); got != 1 {
		t.Errorf("commits = %d, want 1", got)
	}
	if refs := git(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/"); refs != "" {
		t.Errorf("local mode pushed to the remote: %q", refs)
	}
	if up := git(t, dir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/main"); up != "" {
		t.Errorf("local mode configured an upstream: %q", up)
	}
	assertClean(t, dir, "after a local-mode mutation")
}

// Remote-synchronized mode with no usable origin says so explicitly rather
// than inventing a remote.
func TestRemoteModeWithoutOriginReportsIt(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, true)

	iss, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Nowhere to push"})
	if !errors.Is(err, ErrNotPushed) {
		t.Fatalf("error = %v, want it to wrap ErrNotPushed", err)
	}
	if iss == nil {
		t.Fatal("the mutated issue must still be returned")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error should name the missing remote: %v", err)
	}
	if got := commitCount(t, dir); got != 1 {
		t.Errorf("commits = %d, want 1: the commit must stand", got)
	}
	assertClean(t, dir, "after a mutation with no remote")
}

// --- transaction outcome classes --------------------------------------------

func hasObject(t *testing.T, dir, sha string) bool {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "-e", sha+"^{commit}")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func commentPath(t *testing.T, root, issueID, commentID string) string {
	t.Helper()
	tree, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, ok := tree.IssueDir(issueID)
	if !ok {
		t.Fatalf("issue %q not found on disk", issueID)
	}
	return filepath.Join(root, filepath.FromSlash(dir), store.CommentsDir, commentID+".md")
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The window between writing canonical files and recording them as a commit
// is its own outcome class: the mutation is neither refused nor durable.
func TestIncompleteTransactionWhenCommitFails(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	first := mustCreate(t, s, "Committed fine")
	base := commitCount(t, dir)
	assertClean(t, dir, "before the failing transaction")

	// Make git commit fail reliably, after the store has written and staged.
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'commits are rejected here' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c.advance(time.Minute)
	got, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Written but not committed"})

	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("error = %v, want it to wrap ErrIncomplete", err)
	}
	if errors.Is(err, ErrRepository) {
		t.Error("an incomplete transaction must not read as a clean repository refusal")
	}
	if errors.Is(err, ErrNotPushed) {
		t.Error("an incomplete transaction must not read as a durable local commit")
	}
	if got != nil {
		t.Errorf("result = %+v, want nil: the mutation was not recorded", got)
	}
	if !strings.Contains(err.Error(), "commits are rejected here") {
		t.Errorf("error does not carry git's reason: %v", err)
	}

	// No commit exists.
	if n := commitCount(t, dir); n != base {
		t.Errorf("commits = %d, want %d", n, base)
	}

	// The canonical file the attempt wrote is in the working tree, staged.
	staged := git(t, dir, "diff", "--cached", "--name-only")
	if !strings.HasSuffix(staged, "/"+store.IssueFile) {
		t.Fatalf("staged = %q, want the issue document the attempt wrote", staged)
	}
	written := read(t, filepath.Join(dir, filepath.FromSlash(staged)))
	if !strings.Contains(written, "Written but not committed") {
		t.Errorf("the staged document is not the attempted mutation:\n%s", written)
	}
	if status(t, dir) == "" {
		t.Error("the repository should be dirty after a failed transaction")
	}

	// The next mutation is refused by the ordinary clean-repository
	// precondition, and that refusal is a repository refusal, not another
	// incomplete transaction.
	next, err := s.CloseIssue(ctx, first.ID)
	if !errors.Is(err, ErrRepository) {
		t.Fatalf("follow-up error = %v, want it to wrap ErrRepository", err)
	}
	if errors.Is(err, ErrIncomplete) {
		t.Error("the follow-up refusal must not read as an incomplete transaction")
	}
	if next != nil {
		t.Errorf("follow-up result = %+v, want nil", next)
	}
	if n := commitCount(t, dir); n != base {
		t.Errorf("commits after the follow-up = %d, want %d", n, base)
	}
}

// A domain object comes back only when the mutation is real: on success, on
// an idempotent no-op, or alongside ErrNotPushed where the commit is durable.
func TestResultObjectInvariant(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)

	iss, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Subject", Description: "Body."})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := s.AddComment(ctx, iss.ID, "a@example", "Original body.")
	if err != nil {
		t.Fatal(err)
	}
	issueFile := issuePath(t, dir, iss.ID)
	commentFile := commentPath(t, dir, iss.ID, comment.ID)
	issueBefore, commentBefore := read(t, issueFile), read(t, commentFile)
	base := commitCount(t, dir)
	c.advance(time.Hour)

	t.Run("validation returns nil and changes nothing", func(t *testing.T) {
		got, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("   ")})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if got != nil {
			t.Errorf("result = %+v, want nil", got)
		}
		if read(t, issueFile) != issueBefore {
			t.Error("the canonical issue document changed")
		}

		edited, err := s.EditComment(ctx, iss.ID, comment.ID, "   \n  ")
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error = %v, want ErrValidation", err)
		}
		if edited != nil {
			t.Errorf("result = %+v, want nil", edited)
		}
		if read(t, commentFile) != commentBefore {
			t.Error("the canonical comment document changed")
		}
	})

	t.Run("not found returns nil", func(t *testing.T) {
		missing := store.NewID()
		if got, err := s.CloseIssue(ctx, missing); got != nil || !errors.Is(err, ErrNotFound) {
			t.Errorf("CloseIssue = %+v, %v; want nil, ErrNotFound", got, err)
		}
		if got, err := s.AddComment(ctx, missing, "a@example", "Body."); got != nil || !errors.Is(err, ErrNotFound) {
			t.Errorf("AddComment = %+v, %v; want nil, ErrNotFound", got, err)
		}
		if got, err := s.EditComment(ctx, iss.ID, missing, "Body."); got != nil || !errors.Is(err, ErrNotFound) {
			t.Errorf("EditComment = %+v, %v; want nil, ErrNotFound", got, err)
		}
	})

	t.Run("repository refusal returns nil", func(t *testing.T) {
		write(t, dir, "unrelated.txt", "someone else's work\n")
		defer os.Remove(filepath.Join(dir, "unrelated.txt"))

		if got, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("New title")}); got != nil || !errors.Is(err, ErrRepository) {
			t.Errorf("UpdateIssue = %+v, %v; want nil, ErrRepository", got, err)
		}
		if got, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Nope"}); got != nil || !errors.Is(err, ErrRepository) {
			t.Errorf("CreateIssue = %+v, %v; want nil, ErrRepository", got, err)
		}
		if got, err := s.EditComment(ctx, iss.ID, comment.ID, "Different body."); got != nil || !errors.Is(err, ErrRepository) {
			t.Errorf("EditComment = %+v, %v; want nil, ErrRepository", got, err)
		}
		if read(t, issueFile) != issueBefore || read(t, commentFile) != commentBefore {
			t.Error("a refused mutation changed canonical state")
		}
	})

	t.Run("no-op returns the current object", func(t *testing.T) {
		got, err := s.UpdateIssue(ctx, UpdateIssueRequest{ID: iss.ID, Title: ptr("Subject"), Description: ptr("Body.")})
		if err != nil {
			t.Fatalf("no-op returned an error: %v", err)
		}
		if got == nil {
			t.Fatal("a no-op must return the current issue")
		}
		if got.Title != "Subject" || got.State != model.StateOpen {
			t.Errorf("no-op result = %q %q", got.Title, got.State)
		}
		if !got.Updated.Equal(iss.Updated) {
			t.Errorf("no-op advanced Updated: %v, want %v", got.Updated, iss.Updated)
		}

		edited, err := s.EditComment(ctx, iss.ID, comment.ID, "Original body.")
		if err != nil {
			t.Fatalf("no-op returned an error: %v", err)
		}
		if edited == nil {
			t.Fatal("a no-op must return the current comment")
		}
		if edited.Body != "Original body." || !edited.Updated.Equal(comment.Updated) {
			t.Errorf("no-op comment = %q, Updated %v", edited.Body, edited.Updated)
		}
	})

	if n := commitCount(t, dir); n != base {
		t.Errorf("commits = %d, want %d: none of these outcomes may commit", n, base)
	}
	assertClean(t, dir, "after the outcome-class checks")
}

// The one outcome that returns both an object and an error.
func TestNotPushedReturnsTheCommittedObject(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, _ := newService(t, dir, true) // remote mode with no origin configured

	iss, err := s.CreateIssue(ctx, CreateIssueRequest{Title: "Durable locally"})
	if !errors.Is(err, ErrNotPushed) {
		t.Fatalf("error = %v, want ErrNotPushed", err)
	}
	if iss == nil {
		t.Fatal("ErrNotPushed must return the committed object")
	}
	if iss.Title != "Durable locally" {
		t.Errorf("result = %q", iss.Title)
	}
	if errors.Is(err, ErrIncomplete) || errors.Is(err, ErrRepository) {
		t.Error("ErrNotPushed must not read as a refusal or an incomplete transaction")
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commits = %d, want 1: the commit must be durable", n)
	}
	assertClean(t, dir, "after a failed publication")

	// It really is on disk, not just in the returned object.
	fresh, _ := newService(t, dir, false)
	if _, err := fresh.GetIssue(ctx, iss.ID); err != nil {
		t.Errorf("the committed issue is not readable: %v", err)
	}
}

// --- comment chronology -----------------------------------------------------

// Created is what orders a conversation, so comments must come back in the
// order the service was called in even when the clock does not move at all.
// A frozen clock is the strongest version of the rapid-submission case: with
// whole-second timestamps every comment here would tie on Created and fall
// through to the random-ID tie-break.
func TestCommentsCreatedAtTheSameInstantKeepSubmissionOrder(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	iss := mustCreate(t, s, "Rapid conversation")

	frozen := c.t // never advanced again
	var added []*model.Comment
	for _, body := range []string{"First.", "Second.", "Third.", "Fourth."} {
		got, err := s.AddComment(ctx, iss.ID, "a@example", body)
		if err != nil {
			t.Fatalf("AddComment(%q): %v", body, err)
		}
		added = append(added, got)
	}
	if !c.t.Equal(frozen) {
		t.Fatal("fixture is wrong: the clock must not have advanced")
	}

	// Strictly increasing Created, despite one single wall-clock instant.
	for i := 1; i < len(added); i++ {
		if !added[i].Created.After(added[i-1].Created) {
			t.Errorf("comment %d Created %v is not after comment %d Created %v",
				i, added[i].Created, i-1, added[i-1].Created)
		}
	}
	if !added[0].Created.Equal(model.Timestamp(frozen)) {
		t.Errorf("first comment Created = %v, want the clock instant %v", added[0].Created, model.Timestamp(frozen))
	}
	// Created == Updated at creation still holds.
	for i, got := range added {
		if !got.Updated.Equal(got.Created) {
			t.Errorf("comment %d: Updated %v != Created %v", i, got.Updated, got.Created)
		}
	}

	want := []string{added[0].ID, added[1].ID, added[2].ID, added[3].ID}
	assertOrder := func(what string, svc *Service) {
		t.Helper()
		got, err := svc.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ids := commentIDs(got); !equalStrings(ids, want) {
			t.Errorf("%s: comment order = %v, want submission order %v", what, ids, want)
		}
	}
	assertOrder("in place", s)

	fresh, _ := newService(t, dir, false)
	assertOrder("after reload", fresh)

	// Editing the earliest comment much later must not move it.
	c.advance(24 * time.Hour)
	if _, err := s.EditComment(ctx, iss.ID, added[0].ID, "First, edited a day later."); err != nil {
		t.Fatal(err)
	}
	assertOrder("after editing the first comment", s)
	again, _ := newService(t, dir, false)
	assertOrder("after editing and reloading", again)
}

// The nanosecond bump only applies when the clock has not moved on; a normal
// advancing clock is used as-is.
func TestAddCommentUsesTheClockWhenItAdvances(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	iss := mustCreate(t, s, "Slow conversation")

	c.advance(time.Minute)
	first, err := s.AddComment(ctx, iss.ID, "a@example", "First.")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created.Equal(model.Timestamp(c.t)) {
		t.Errorf("Created = %v, want the clock %v", first.Created, model.Timestamp(c.t))
	}

	c.advance(time.Minute)
	second, err := s.AddComment(ctx, iss.ID, "a@example", "Second.")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Created.Equal(model.Timestamp(c.t)) {
		t.Errorf("Created = %v, want the clock %v", second.Created, model.Timestamp(c.t))
	}

	// A clock that steps backwards must still not break the ordering.
	c.t = c.t.Add(-time.Hour)
	third, err := s.AddComment(ctx, iss.ID, "a@example", "Third.")
	if err != nil {
		t.Fatal(err)
	}
	if !third.Created.After(second.Created) {
		t.Errorf("Created = %v, want strictly after %v despite the clock going backwards",
			third.Created, second.Created)
	}
	fresh, _ := newService(t, dir, false)
	got, err := fresh.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ids := commentIDs(got); !equalStrings(ids, []string{first.ID, second.ID, third.ID}) {
		t.Errorf("order = %v, want submission order", ids)
	}
}

func TestMoveIssueAttachMoveDetachAndReload(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	a := mustCreate(t, s, "Alpha")
	b := mustCreate(t, s, "Beta")
	child, err := s.CreateIssue(ctx, CreateIssueRequest{ParentID: a.ID, Title: "Alpha detail"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := s.AddComment(ctx, child.ID, "human@example", "Preserve this.")
	if err != nil {
		t.Fatal(err)
	}
	created := a.Created
	childUpdated := child.Updated
	base := commitCount(t, dir)

	c.advance(time.Hour)
	moved, err := s.MoveIssue(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ParentID != b.ID || !moved.Created.Equal(created) || !moved.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("moved issue = %#v", moved)
	}
	if got := git(t, dir, "log", "-1", "--pretty=%s"); got != "move issue "+a.ID+" under "+b.ID {
		t.Errorf("move subject = %q", got)
	}

	c.advance(time.Hour)
	detached, err := s.MoveIssue(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if detached.ParentID != "" || !detached.Updated.Equal(model.Timestamp(c.t)) {
		t.Errorf("detached issue = %#v", detached)
	}
	if got := git(t, dir, "log", "-1", "--pretty=%s"); got != "detach issue "+a.ID {
		t.Errorf("detach subject = %q", got)
	}
	if got := commitCount(t, dir); got != base+2 {
		t.Fatalf("commit count = %d, want %d", got, base+2)
	}
	assertClean(t, dir, "after moves")

	fresh, _ := newService(t, dir, false)
	got, err := fresh.GetIssue(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "" || len(got.Children) != 1 || got.Children[0].ID != child.ID {
		t.Fatalf("reloaded subtree = %#v", got)
	}
	if !got.Children[0].Updated.Equal(childUpdated) || len(got.Children[0].Comments) != 1 || got.Children[0].Comments[0].ID != comment.ID {
		t.Fatalf("reloaded child changed = %#v", got.Children[0])
	}
}

func TestMoveIssueNoOpsAndRejectionsDoNotCommit(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	s, c := newService(t, dir, false)
	a := mustCreate(t, s, "Alpha")
	b, err := s.CreateIssue(ctx, CreateIssueRequest{ParentID: a.ID, Title: "Beta"})
	if err != nil {
		t.Fatal(err)
	}
	base := commitCount(t, dir)
	updated := b.Updated
	c.advance(time.Hour)

	for _, tc := range []struct {
		name   string
		id     string
		parent string
		want   error
	}{
		{"same parent", b.ID, a.ID, nil},
		{"self", a.ID, a.ID, ErrValidation},
		{"descendant", a.ID, b.ID, ErrValidation},
		{"unknown issue", strings.Repeat("z", store.IDLen), "", ErrNotFound},
		{"unknown parent", b.ID, strings.Repeat("y", store.IDLen), ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.MoveIssue(ctx, tc.id, tc.parent)
			if tc.want == nil {
				if err != nil || got == nil || !got.Updated.Equal(updated) {
					t.Fatalf("MoveIssue = %#v, %v", got, err)
				}
			} else if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("MoveIssue = %#v, %v; want nil, %v", got, err, tc.want)
			}
			if n := commitCount(t, dir); n != base {
				t.Fatalf("commit count = %d, want %d", n, base)
			}
			assertClean(t, dir, tc.name)
		})
	}
}

func TestMoveIssueIncompleteAndNotPushedOutcomes(t *testing.T) {
	ctx := context.Background()
	t.Run("incomplete stages the subtree rename", func(t *testing.T) {
		dir := initRepo(t)
		s, _ := newService(t, dir, false)
		a := mustCreate(t, s, "Alpha")
		b := mustCreate(t, s, "Beta")
		child, err := s.CreateIssue(ctx, CreateIssueRequest{ParentID: a.ID, Title: "Nested"})
		if err != nil {
			t.Fatal(err)
		}
		base := commitCount(t, dir)
		hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		moved, err := s.MoveIssue(ctx, a.ID, b.ID)
		if moved != nil || !errors.Is(err, ErrIncomplete) {
			t.Fatalf("MoveIssue = %#v, %v", moved, err)
		}
		if got := commitCount(t, dir); got != base {
			t.Fatalf("commit count = %d, want %d", got, base)
		}
		staged := git(t, dir, "diff", "--cached", "--name-status", "-M")
		for _, id := range []string{a.ID, child.ID} {
			if !strings.Contains(staged, id) {
				t.Errorf("staged rename lacks %s: %s", id, staged)
			}
		}
	})

	t.Run("not pushed returns the durable moved issue", func(t *testing.T) {
		dir := initRepo(t)
		local, _ := newService(t, dir, false)
		a := mustCreate(t, local, "Alpha")
		b := mustCreate(t, local, "Beta")
		remote, _ := newService(t, dir, true)
		moved, err := remote.MoveIssue(ctx, a.ID, b.ID)
		if moved == nil || moved.ParentID != b.ID || !errors.Is(err, ErrNotPushed) {
			t.Fatalf("MoveIssue = %#v, %v", moved, err)
		}
		assertClean(t, dir, "after unpushed move")
		fresh, _ := newService(t, dir, false)
		got, readErr := fresh.GetIssue(ctx, a.ID)
		if readErr != nil || got.ParentID != b.ID {
			t.Fatalf("durable move = %#v, %v", got, readErr)
		}
	})
}

func commentIDs(i *model.Issue) []string {
	ids := make([]string, len(i.Comments))
	for n, c := range i.Comments {
		ids[n] = c.ID
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
