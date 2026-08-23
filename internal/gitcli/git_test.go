package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs a raw git command to build fixtures. Production code never does
// this; only the tests do, to set up repositories.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.name", "tissues test")
	git(t, dir, "config", "user.email", "test@example")
	git(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func initBare(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--bare", "-b", "main")
	return dir
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

func TestVerify(t *testing.T) {
	ctx := context.Background()
	if err := New(initRepo(t)).Verify(ctx); err != nil {
		t.Errorf("Verify on a real repository: %v", err)
	}
	if err := New(t.TempDir()).Verify(ctx); err == nil {
		t.Error("Verify accepted a directory that is not a Git repository")
	}
	if err := New(filepath.Join(t.TempDir(), "does-not-exist")).Verify(ctx); err == nil {
		t.Error("Verify accepted a nonexistent directory")
	}
}

func TestStatusDetectsEveryKindOfDirtiness(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	g := New(dir)

	if status, err := g.Status(ctx); err != nil || status != "" {
		t.Fatalf("fresh repository: Status = %q, %v; want empty", status, err)
	}

	write(t, dir, "tracked.txt", "one\n")
	git(t, dir, "add", "--", "tracked.txt")
	git(t, dir, "commit", "-m", "base")
	if status, err := g.Status(ctx); err != nil || status != "" {
		t.Fatalf("after commit: Status = %q, %v; want empty", status, err)
	}

	t.Run("untracked", func(t *testing.T) {
		write(t, dir, "stray.txt", "stray\n")
		defer os.Remove(filepath.Join(dir, "stray.txt"))
		assertDirty(t, g, "stray.txt")
	})
	t.Run("modified tracked", func(t *testing.T) {
		write(t, dir, "tracked.txt", "two\n")
		defer git(t, dir, "checkout", "--", "tracked.txt")
		assertDirty(t, g, "tracked.txt")
	})
	t.Run("staged", func(t *testing.T) {
		write(t, dir, "tracked.txt", "three\n")
		git(t, dir, "add", "--", "tracked.txt")
		defer func() {
			git(t, dir, "reset", "--hard", "HEAD")
		}()
		assertDirty(t, g, "tracked.txt")
	})

	if status, err := g.Status(ctx); err != nil || status != "" {
		t.Fatalf("after cleanup: Status = %q, %v; want empty", status, err)
	}
}

func assertDirty(t *testing.T, g *Git, wantPath string) {
	t.Helper()
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status == "" {
		t.Fatal("Status reported a clean repository, want dirty")
	}
	if !strings.Contains(status, wantPath) {
		t.Errorf("Status = %q, want it to mention %q", status, wantPath)
	}
}

// Exact-path staging is a v0 contract: tissues must never sweep unrelated
// working-tree changes into its commits.
func TestAddStagesOnlyTheGivenPaths(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	g := New(dir)

	write(t, dir, "issues/a/issue.md", "a\n")
	write(t, dir, "issues/b/issue.md", "b\n")
	write(t, dir, "unrelated.txt", "not ours\n")
	write(t, dir, "issues/a/comments/c.md", "c\n")

	if err := g.Add(ctx, "issues/a/issue.md", "issues/a/comments/c.md"); err != nil {
		t.Fatal(err)
	}
	staged := strings.Fields(git(t, dir, "diff", "--cached", "--name-only"))
	want := []string{"issues/a/comments/c.md", "issues/a/issue.md"}
	if strings.Join(staged, " ") != strings.Join(want, " ") {
		t.Errorf("staged = %v, want exactly %v", staged, want)
	}

	// The rest must still be untracked, not merely unstaged.
	others := git(t, dir, "ls-files", "--others", "--exclude-standard")
	for _, p := range []string{"issues/b/issue.md", "unrelated.txt"} {
		if !strings.Contains(others, p) {
			t.Errorf("%q is no longer untracked; Add touched more than it was given", p)
		}
	}

	if err := g.Add(ctx); err == nil {
		t.Error("Add with no paths should be an error, not a silent no-op")
	}
}

func TestHeadAndBranchOnUnbornBranch(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	g := New(dir)

	has, err := g.HasHEAD(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("HasHEAD = true on an unborn branch")
	}
	if branch, err := g.Branch(ctx); err != nil || branch != "main" {
		t.Errorf("Branch = %q, %v; want main", branch, err)
	}
	if up, err := g.Upstream(ctx); err != nil || up != "" {
		t.Errorf("Upstream = %q, %v; want empty with no error", up, err)
	}

	write(t, dir, "f.txt", "x\n")
	if err := g.Add(ctx, "f.txt"); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(ctx, "root commit"); err != nil {
		t.Fatal(err)
	}
	has, err = g.HasHEAD(ctx)
	if err != nil || !has {
		t.Errorf("HasHEAD = %v, %v after the root commit", has, err)
	}
	head, err := g.Head(ctx)
	if err != nil || len(head) != 40 {
		t.Errorf("Head = %q, %v", head, err)
	}
	if got := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD")); got != head {
		t.Errorf("Head = %q, want %q", head, got)
	}
}

func TestPushEstablishesUpstream(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	dir := initRepo(t)
	git(t, dir, "remote", "add", "origin", bare)
	g := New(dir)

	if ok, err := g.HasRemote(ctx, "origin"); err != nil || !ok {
		t.Fatalf("HasRemote(origin) = %v, %v", ok, err)
	}
	if ok, err := g.HasRemote(ctx, "upstream"); err != nil || ok {
		t.Fatalf("HasRemote(upstream) = %v, %v", ok, err)
	}

	write(t, dir, "f.txt", "x\n")
	if err := g.Add(ctx, "f.txt"); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(ctx, "root commit"); err != nil {
		t.Fatal(err)
	}
	if err := g.PushSetUpstream(ctx, "origin"); err != nil {
		t.Fatal(err)
	}

	up, err := g.Upstream(ctx)
	if err != nil || up != "origin/main" {
		t.Errorf("Upstream = %q, %v; want origin/main", up, err)
	}
	ahead, behind, err := g.AheadBehind(ctx)
	if err != nil || ahead != 0 || behind != 0 {
		t.Errorf("AheadBehind = %d, %d, %v; want 0, 0", ahead, behind, err)
	}
	head, _ := g.Head(ctx)
	if remote := strings.TrimSpace(git(t, bare, "rev-parse", "main")); remote != head {
		t.Errorf("remote main = %q, want %q", remote, head)
	}
	// Only the current branch may have been created on the remote.
	if refs := strings.TrimSpace(git(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/")); refs != "refs/heads/main" {
		t.Errorf("remote branches = %q, want only refs/heads/main", refs)
	}
}

func TestPullFFOnlyRefusesDivergence(t *testing.T) {
	ctx := context.Background()
	bare := initBare(t)
	a := initRepo(t)
	git(t, a, "remote", "add", "origin", bare)
	ga := New(a)

	write(t, a, "f.txt", "one\n")
	mustAddCommit(t, ga, "f.txt", "base")
	if err := ga.PushSetUpstream(ctx, "origin"); err != nil {
		t.Fatal(err)
	}

	// A second clone advances the remote.
	b := t.TempDir()
	git(t, filepath.Dir(b), "clone", bare, b)
	git(t, b, "config", "user.name", "other")
	git(t, b, "config", "user.email", "other@example")
	git(t, b, "config", "commit.gpgsign", "false")
	write(t, b, "g.txt", "from b\n")
	git(t, b, "add", "--", "g.txt")
	git(t, b, "commit", "-m", "from b")
	git(t, b, "push")

	// A fast-forward works and brings the remote change in.
	if err := ga.PullFFOnly(ctx); err != nil {
		t.Fatalf("PullFFOnly on a fast-forwardable branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a, "g.txt")); err != nil {
		t.Errorf("fast-forward did not bring in the remote change: %v", err)
	}

	// Now diverge: both sides commit something different.
	write(t, b, "h.txt", "b again\n")
	git(t, b, "add", "--", "h.txt")
	git(t, b, "commit", "-m", "b again")
	git(t, b, "push")
	write(t, a, "local.txt", "a only\n")
	mustAddCommit(t, ga, "local.txt", "a only")

	before, _ := ga.Head(ctx)
	if err := ga.PullFFOnly(ctx); err == nil {
		t.Fatal("PullFFOnly accepted a divergent upstream")
	}
	after, _ := ga.Head(ctx)
	if before != after {
		t.Errorf("a failed PullFFOnly moved HEAD from %s to %s", before, after)
	}
	if status, _ := ga.Status(ctx); status != "" {
		t.Errorf("a failed PullFFOnly left the tree dirty: %q", status)
	}
}

func mustAddCommit(t *testing.T, g *Git, path, msg string) {
	t.Helper()
	ctx := context.Background()
	if err := g.Add(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(ctx, msg); err != nil {
		t.Fatal(err)
	}
}

// Cancellation must reach the git process, because REST and MCP will pass
// request contexts straight through.
func TestContextCancellationPropagates(t *testing.T) {
	g := New(initRepo(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Status(ctx); err == nil {
		t.Error("Status ignored a cancelled context")
	}
	if err := g.Commit(ctx, "should not happen"); err == nil {
		t.Error("Commit ignored a cancelled context")
	}
}

// Errors must carry git's own stderr, or diagnosing a failed transaction is
// guesswork.
func TestErrorsCarryStderr(t *testing.T) {
	g := New(initRepo(t))
	err := g.Commit(context.Background(), "nothing staged")
	if err == nil {
		t.Fatal("Commit with an empty index should fail")
	}
	if !strings.Contains(err.Error(), "git commit") {
		t.Errorf("error does not name the command: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing to commit") {
		t.Errorf("error does not carry git's own output: %v", err)
	}
}
