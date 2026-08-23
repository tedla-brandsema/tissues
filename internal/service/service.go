// Package service is the single application service. It owns every v0 issue
// and comment operation and maps each one to exactly one Git commit.
//
// REST and MCP will be thin adapters over this package; there is no separate
// REST semantic and MCP semantic. No generic filesystem or Git operation is
// exposed through it.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tedla-brandsema/tissues/internal/gitcli"
	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/store"
)

// The error vocabulary later adapters need to tell outcomes apart. Each
// sentinel says something different about what happened to canonical state.
var (
	// ErrNotFound is an unknown issue or comment ID. Nothing was written.
	ErrNotFound = errors.New("not found")
	// ErrValidation is ordinary bad input: an empty title, an empty comment
	// body, and so on. Nothing was written.
	ErrValidation = errors.New("invalid request")
	// ErrRepository means the repository prevented the requested mutation
	// before any canonical file was written: a dirty working tree, tissues
	// content that fails validation, a Git inspection failure, or an upstream
	// that will not fast-forward. Canonical state is untouched.
	ErrRepository = errors.New("repository unusable")
	// ErrIncomplete means the requested mutation changed canonical files in
	// the working tree but could not be completely recorded as a Git commit.
	// The intended commit does not exist. The repository is left dirty or
	// staged and needs human repair; until it is repaired the clean-repository
	// precondition refuses further mutations with ErrRepository.
	ErrIncomplete = errors.New("mutation written but not committed")
	// ErrNotPushed means the semantic mutation was committed locally and is
	// durable: the commit exists, the working tree is clean, and the branch is
	// simply ahead of its upstream. Only publication failed, so the mutated
	// object is returned alongside this error.
	ErrNotPushed = errors.New("committed locally but not pushed")
)

// A mutating service method returns a non-nil domain object only when the
// semantic operation succeeded — including an idempotent no-op — or when the
// error is ErrNotPushed, where the mutation is committed and durable locally.
// Every other outcome returns nil, so a caller can never mistake a transient
// in-memory object for canonical state. issueResult and commentResult are the
// postcondition that enforces this.
func issueResult(iss *model.Issue, err error) (*model.Issue, error) {
	if err != nil && !errors.Is(err, ErrNotPushed) {
		return nil, err
	}
	return iss, err
}

func commentResult(c *model.Comment, err error) (*model.Comment, error) {
	if err != nil && !errors.Is(err, ErrNotPushed) {
		return nil, err
	}
	return c, err
}

// writeFailed reports a store write that did not complete. Files may already
// exist in the working tree, so this is an incomplete transaction, not a
// clean refusal.
func writeFailed(err error) error {
	return fmt.Errorf("%w: the store could not complete the write: %v", ErrIncomplete, err)
}

// Service performs tissues operations against one Git repository.
//
// One process serves one repository. Every operation, read or write, holds mu
// for its whole duration, so a read can never observe a half-finished pull,
// write or commit.
type Service struct {
	mu     sync.Mutex
	root   string
	git    *gitcli.Git
	remote bool // synchronize with a Git remote on every mutation
	now    func() time.Time
}

// New opens the repository at root. With remoteSync set, each mutation pulls
// before and pushes after; otherwise mutations are purely local commits.
func New(ctx context.Context, root string, remoteSync bool) (*Service, error) {
	g := gitcli.New(root)
	if err := g.Verify(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepository, err)
	}
	return &Service{root: root, git: g, remote: remoteSync, now: time.Now}, nil
}

// ListIssues returns the complete issue hierarchy, reconstructed from the
// current filesystem state.
func (s *Service) ListIssues(ctx context.Context) ([]*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.load()
	if err != nil {
		return nil, err
	}
	return t.Roots(), nil
}

// GetIssue returns one issue by its immutable ID, with its child hierarchy
// and its comments in canonical order.
func (s *Service) GetIssue(ctx context.Context, id string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.load()
	if err != nil {
		return nil, err
	}
	iss, ok := t.Issue(id)
	if !ok {
		return nil, fmt.Errorf("issue %q: %w", id, ErrNotFound)
	}
	return iss, nil
}

// CreateIssueRequest is the caller-supplied part of a new issue. The service
// owns everything else: the ID, the open state and both timestamps.
type CreateIssueRequest struct {
	ParentID    string // empty creates a root issue
	Title       string
	Description string
}

func (s *Service) CreateIssue(ctx context.Context, req CreateIssueRequest) (*model.Issue, error) {
	var created *model.Issue
	err := s.mutate(ctx, func(t *store.Tree) ([]string, string, error) {
		if req.ParentID != "" {
			if _, ok := t.Issue(req.ParentID); !ok {
				return nil, "", fmt.Errorf("parent issue %q: %w", req.ParentID, ErrNotFound)
			}
		}
		now := model.Timestamp(s.now())
		iss := &model.Issue{
			ID:          store.NewID(),
			Title:       req.Title,
			State:       model.StateOpen,
			Created:     now,
			Updated:     now,
			Description: req.Description,
		}
		if err := iss.Validate(); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrValidation, err)
		}
		path, err := t.CreateIssue(req.ParentID, iss)
		if err != nil {
			return nil, "", writeFailed(err)
		}
		created = iss
		return []string{path}, fmt.Sprintf("create issue %s: %s", iss.ID, iss.Title), nil
	})
	return issueResult(created, err)
}

// UpdateIssueRequest changes an issue's title and description. A nil field is
// left untouched. Nothing else about an issue may be updated: not its ID, its
// state, its creation time, its parent, its children or its comments.
type UpdateIssueRequest struct {
	ID          string
	Title       *string
	Description *string
}

func (s *Service) UpdateIssue(ctx context.Context, req UpdateIssueRequest) (*model.Issue, error) {
	var updated *model.Issue
	err := s.mutate(ctx, func(t *store.Tree) ([]string, string, error) {
		iss, ok := t.Issue(req.ID)
		if !ok {
			return nil, "", fmt.Errorf("issue %q: %w", req.ID, ErrNotFound)
		}
		updated = iss
		changed := false
		if req.Title != nil && *req.Title != iss.Title {
			iss.Title = *req.Title
			changed = true
		}
		if req.Description != nil && *req.Description != iss.Description {
			iss.Description = *req.Description
			changed = true
		}
		if !changed {
			return nil, "", nil // no-op: nothing written, nothing committed
		}
		iss.Updated = model.Timestamp(s.now())
		if err := iss.Validate(); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrValidation, err)
		}
		path, err := t.WriteIssue(iss)
		if err != nil {
			return nil, "", writeFailed(err)
		}
		return []string{path}, "update issue " + iss.ID, nil
	})
	return issueResult(updated, err)
}

// CloseIssue closes an open issue. Closing an already-closed issue succeeds
// without writing or committing, and never cascades to children.
func (s *Service) CloseIssue(ctx context.Context, id string) (*model.Issue, error) {
	return s.setState(ctx, id, model.StateClosed, "close issue ")
}

// ReopenIssue reopens a closed issue. Reopening an already-open issue
// succeeds without writing or committing.
func (s *Service) ReopenIssue(ctx context.Context, id string) (*model.Issue, error) {
	return s.setState(ctx, id, model.StateOpen, "reopen issue ")
}

func (s *Service) setState(ctx context.Context, id string, state model.State, msg string) (*model.Issue, error) {
	var issue *model.Issue
	err := s.mutate(ctx, func(t *store.Tree) ([]string, string, error) {
		iss, ok := t.Issue(id)
		if !ok {
			return nil, "", fmt.Errorf("issue %q: %w", id, ErrNotFound)
		}
		issue = iss
		if iss.State == state {
			return nil, "", nil // idempotent no-op
		}
		iss.State = state
		iss.Updated = model.Timestamp(s.now())
		path, err := t.WriteIssue(iss)
		if err != nil {
			return nil, "", writeFailed(err)
		}
		return []string{path}, msg + iss.ID, nil
	})
	return issueResult(issue, err)
}

// AddComment appends a comment to an issue. Author is self-asserted domain
// provenance: it records who a comment claims to be from and authenticates
// nothing.
func (s *Service) AddComment(ctx context.Context, issueID, author, body string) (*model.Comment, error) {
	var added *model.Comment
	err := s.mutate(ctx, func(t *store.Tree) ([]string, string, error) {
		if _, ok := t.Issue(issueID); !ok {
			return nil, "", fmt.Errorf("issue %q: %w", issueID, ErrNotFound)
		}
		now := model.Timestamp(s.now())
		c := &model.Comment{ID: store.NewID(), Author: author, Created: now, Updated: now, Body: body}
		if err := c.Validate(); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrValidation, err)
		}
		path, err := t.CreateComment(issueID, c)
		if err != nil {
			return nil, "", writeFailed(err)
		}
		added = c
		return []string{path}, fmt.Sprintf("comment %s on issue %s", c.ID, issueID), nil
	})
	return commentResult(added, err)
}

// EditComment replaces a comment's body. Its ID, author and creation time are
// preserved, so the edit cannot move it: ordering is Created ASC, ID ASC and
// never consults Updated. An identical body is a no-op.
func (s *Service) EditComment(ctx context.Context, issueID, commentID, body string) (*model.Comment, error) {
	var edited *model.Comment
	err := s.mutate(ctx, func(t *store.Tree) ([]string, string, error) {
		if _, ok := t.Issue(issueID); !ok {
			return nil, "", fmt.Errorf("issue %q: %w", issueID, ErrNotFound)
		}
		c, ok := t.Comment(issueID, commentID)
		if !ok {
			return nil, "", fmt.Errorf("comment %q on issue %q: %w", commentID, issueID, ErrNotFound)
		}
		edited = c
		if c.Body == body {
			return nil, "", nil // no-op
		}
		c.Body = body
		c.Updated = model.Timestamp(s.now())
		if err := c.Validate(); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrValidation, err)
		}
		path, err := t.WriteComment(issueID, c)
		if err != nil {
			return nil, "", writeFailed(err)
		}
		return []string{path}, fmt.Sprintf("edit comment %s on issue %s", commentID, issueID), nil
	})
	return commentResult(edited, err)
}

// mutate runs one semantic mutation as one Git transaction.
//
// fn applies the mutation to a freshly loaded tree and returns the exact
// repository-relative paths it wrote, plus the commit message. Returning no
// paths means the operation was a no-op: nothing is staged and no commit is
// made.
func (s *Service) mutate(ctx context.Context, fn func(*store.Tree) ([]string, string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Everything that can fail without touching the filesystem happens first.
	if err := s.requireClean(ctx); err != nil {
		return err
	}
	if s.remote {
		if err := s.pull(ctx); err != nil {
			return err
		}
	}
	t, err := s.load()
	if err != nil {
		return err
	}
	paths, message, err := fn(t)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	// Past this point canonical files exist on disk, so a failure is an
	// incomplete transaction rather than a clean refusal: the repository is
	// left dirty or staged and the clean precondition will refuse further
	// mutations until a human repairs it.
	if err := s.git.Add(ctx, paths...); err != nil {
		return fmt.Errorf("%w: files were written but could not be staged: %v", ErrIncomplete, err)
	}
	if err := s.git.Commit(ctx, message); err != nil {
		return fmt.Errorf("%w: files were written and staged but could not be committed: %v", ErrIncomplete, err)
	}
	if s.remote {
		return s.push(ctx)
	}
	return nil
}

// requireClean refuses to mutate a repository that has modified tracked
// files, staged changes or untracked files, so tissues can never commit or
// interact with unrelated working-tree changes.
func (s *Service) requireClean(ctx context.Context) error {
	status, err := s.git.Status(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRepository, err)
	}
	if status != "" {
		return fmt.Errorf("%w: the working tree has uncommitted changes:\n%s", ErrRepository, status)
	}
	return nil
}

// load reads and validates the whole issue tree. A repository can be valid
// Git while containing invalid tissues data, so this fails closed.
func (s *Service) load() (*store.Tree, error) {
	t, err := store.Load(s.root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepository, err)
	}
	return t, nil
}

// pull fast-forwards from the upstream, if there is one to fast-forward
// from. A divergent upstream is a hard stop: v0 does not merge, rebase or
// retry, and the mutation is abandoned before anything is written.
//
// git pull fetches before it integrates, so a failed fast-forward may still
// have updated remote-tracking refs and FETCH_HEAD. That is normal Git
// bookkeeping, not a tissues mutation, and is neither suppressed nor undone.
// What tissues guarantees is that the request writes no canonical file,
// leaves the index untouched, does not move HEAD and creates no commit.
func (s *Service) pull(ctx context.Context) error {
	head, err := s.git.HasHEAD(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRepository, err)
	}
	if !head {
		return nil // unborn branch: nothing to synchronize with yet
	}
	upstream, err := s.git.Upstream(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRepository, err)
	}
	if upstream == "" {
		return nil
	}
	if err := s.git.PullFFOnly(ctx); err != nil {
		return fmt.Errorf("%w: cannot fast-forward from %s; resolve the divergence with git and retry: %v",
			ErrRepository, upstream, err)
	}
	return nil
}

// push publishes the commit just made, establishing the upstream on the
// first push. A failure here never undoes the commit.
func (s *Service) push(ctx context.Context) error {
	upstream, err := s.git.Upstream(ctx)
	if err != nil {
		return notPushed(err)
	}
	if upstream != "" {
		if err := s.git.Push(ctx); err != nil {
			return notPushed(err)
		}
		return nil
	}
	ok, err := s.git.HasRemote(ctx, "origin")
	if err != nil {
		return notPushed(err)
	}
	if !ok {
		return notPushed(errors.New(`no remote named "origin" is configured`))
	}
	if err := s.git.PushSetUpstream(ctx, "origin"); err != nil {
		return notPushed(err)
	}
	return nil
}

func notPushed(err error) error { return fmt.Errorf("%w: %v", ErrNotPushed, err) }
