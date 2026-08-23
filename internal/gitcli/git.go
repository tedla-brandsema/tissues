// Package gitcli is a deliberately narrow wrapper around the installed git
// executable. It exists only to support the service's write transaction, and
// is not a general-purpose Git library.
package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Git runs git commands in one repository working directory.
type Git struct {
	dir string
}

func New(dir string) *Git { return &Git{dir: dir} }

// Dir returns the working directory git commands run in.
func (g *Git) Dir() string { return g.dir }

// Verify reports whether dir is inside a Git working tree.
func (g *Git) Verify(ctx context.Context) error {
	out, err := g.run(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return fmt.Errorf("%s is not a Git repository: %w", g.dir, err)
	}
	if strings.TrimSpace(out) != "true" {
		return fmt.Errorf("%s is not a Git working tree", g.dir)
	}
	return nil
}

// Status returns porcelain status output with trailing whitespace trimmed.
// An empty string means the working tree and index are both clean: no
// modified tracked files, no staged changes and no untracked files.
func (g *Git) Status(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "status", "--porcelain")
	return strings.TrimRight(out, "\n"), err
}

// HasHEAD reports whether the current branch has any commit yet.
func (g *Git) HasHEAD(ctx context.Context) (bool, error) {
	out, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil // unborn branch
		}
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Head returns the commit currently at HEAD.
func (g *Git) Head(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Branch returns the current branch name, which exists even on an unborn
// branch. It is empty when HEAD is detached.
func (g *Git) Branch(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "branch", "--show-current")
	return strings.TrimSpace(out), err
}

// Upstream returns the upstream of the current branch, or "" if the branch
// has none. Unlike rev-parse @{u} this does not conflate "no upstream" with
// a command failure.
func (g *Git) Upstream(ctx context.Context) (string, error) {
	branch, err := g.Branch(ctx)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", errors.New("HEAD is detached")
	}
	out, err := g.run(ctx, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	return strings.TrimSpace(out), err
}

// HasRemote reports whether a remote of that name is configured.
func (g *Git) HasRemote(ctx context.Context, name string) (bool, error) {
	out, err := g.run(ctx, "remote")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Fields(out) {
		if line == name {
			return true, nil
		}
	}
	return false, nil
}

// PullFFOnly integrates the upstream, refusing anything but a fast-forward.
// Divergence is a hard failure; nothing is merged, rebased or retried.
func (g *Git) PullFFOnly(ctx context.Context) error {
	_, err := g.run(ctx, "pull", "--ff-only")
	return err
}

// Add stages exactly the given repository-relative paths and nothing else.
func (g *Git) Add(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return errors.New("git add: no paths given")
	}
	_, err := g.run(ctx, append([]string{"add", "--"}, paths...)...)
	return err
}

// Commit records the staged changes.
func (g *Git) Commit(ctx context.Context, message string) error {
	_, err := g.run(ctx, "commit", "-m", message)
	return err
}

// Push publishes the current branch to its existing upstream.
func (g *Git) Push(ctx context.Context) error {
	_, err := g.run(ctx, "push")
	return err
}

// PushSetUpstream publishes the current branch to remote and records it as
// the branch's upstream. It creates only the current branch on the remote.
func (g *Git) PushSetUpstream(ctx context.Context, remote string) error {
	_, err := g.run(ctx, "push", "--set-upstream", remote, "HEAD")
	return err
}

// AheadBehind reports how many commits the current branch is ahead of and
// behind its upstream.
func (g *Git) AheadBehind(ctx context.Context) (ahead, behind int, err error) {
	out, err := g.run(ctx, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscan(out, &ahead, &behind); err != nil {
		return 0, 0, fmt.Errorf("git rev-list: unexpected output %q", out)
	}
	return ahead, behind, nil
}

// run executes git with the given arguments. Arguments are passed directly:
// there is no shell anywhere in this package.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// git reports some failures on stdout ("nothing to commit"), so fall
		// back to it rather than returning a bare exit status.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
