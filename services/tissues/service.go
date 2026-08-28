package tissues

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tedla-brandsema/tissues/lib/service"
)

// Service owns tissues domain behavior and HTTP routes. It does not own a
// listener, PORT, process signals, or deployment lifecycle.
type Service struct {
	profile    service.Profile[Config]
	repo       Repository
	httpClient *http.Client
	now        func() time.Time
	newID      IDGenerator
}

var _ service.Service = (*Service)(nil)

type Option func(*Service)

// WithHTTPClient supplies the client used for relying-party authentication.
// A nil/default client preserves net/http's standard client behavior.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) { s.httpClient = client }
}

func New(profile service.Profile[Config], repo Repository, options ...Option) (*Service, error) {
	if profile == nil {
		return nil, fmt.Errorf("tissues profile is required")
	}
	if repo == nil {
		return nil, fmt.Errorf("tissues repository is required")
	}
	if err := profile.Current().Config.ValidateConfig(); err != nil {
		return nil, err
	}
	svc := &Service{profile: profile, repo: repo, now: time.Now, newID: NewID}
	for _, option := range options {
		if option != nil {
			option(svc)
		}
	}
	return svc, nil
}

func (*Service) Name() string { return "tissues" }

func (s *Service) ListIssues(ctx context.Context) ([]*Issue, error) {
	issues, err := s.repo.ListIssues(ctx)
	if err != nil {
		return nil, persistenceError(err)
	}
	sortIssues(issues)
	return issues, nil
}

func (s *Service) GetIssue(ctx context.Context, id string) (*Issue, error) {
	issue, err := s.repo.GetIssue(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	return issue, nil
}

type CreateIssueRequest struct{ ParentID, Title, Description string }

func (s *Service) CreateIssue(ctx context.Context, req CreateIssueRequest) (*Issue, error) {
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternal, err)
	}
	now := Timestamp(s.now())
	created := &Issue{ID: id, Title: req.Title, State: StateOpen, Created: now, Updated: now, Description: req.Description, ParentID: req.ParentID}
	if err := created.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		if _, getErr := tx.GetIssue(ctx, id); getErr == nil {
			return fmt.Errorf("%w: issue ID %q already exists", ErrConflict, id)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		if req.ParentID != "" {
			if _, err := tx.GetIssue(ctx, req.ParentID); err != nil {
				return err
			}
		}
		return tx.PutIssue(ctx, created)
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return cloneIssue(created), nil
}

type UpdateIssueRequest struct {
	ID                 string
	Title, Description *string
}

func (s *Service) UpdateIssue(ctx context.Context, req UpdateIssueRequest) (*Issue, error) {
	now := Timestamp(s.now())
	var result *Issue
	err := s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.GetIssue(ctx, req.ID)
		if err != nil {
			return err
		}
		changed := false
		if req.Title != nil && *req.Title != issue.Title {
			issue.Title = *req.Title
			changed = true
		}
		if req.Description != nil && *req.Description != issue.Description {
			issue.Description = *req.Description
			changed = true
		}
		if changed {
			issue.Updated = now
			if err := issue.Validate(); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if err := tx.PutIssue(ctx, issue); err != nil {
				return err
			}
		}
		result = cloneIssue(issue)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

func (s *Service) MoveIssue(ctx context.Context, id, parentID string) (*Issue, error) {
	now := Timestamp(s.now())
	var result *Issue
	err := s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		if parentID == id {
			return fmt.Errorf("%w: issue %q cannot be its own parent", ErrInvalid, id)
		}
		if parentID != "" {
			visited := map[string]bool{}
			for current := parentID; current != ""; {
				if current == id {
					return fmt.Errorf("%w: issue %q cannot be moved beneath its descendant %q", ErrInvalid, id, parentID)
				}
				if visited[current] {
					return fmt.Errorf("%w: existing hierarchy cycle at issue %q", ErrInternal, current)
				}
				visited[current] = true
				parent, getErr := tx.GetIssue(ctx, current)
				if getErr != nil {
					return getErr
				}
				current = parent.ParentID
			}
		}
		if issue.ParentID != parentID {
			issue.ParentID = parentID
			issue.Updated = now
			if err := tx.PutIssue(ctx, issue); err != nil {
				return err
			}
		}
		result = cloneIssue(issue)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

func (s *Service) CloseIssue(ctx context.Context, id string) (*Issue, error) {
	return s.setState(ctx, id, StateClosed)
}
func (s *Service) ReopenIssue(ctx context.Context, id string) (*Issue, error) {
	return s.setState(ctx, id, StateOpen)
}

func (s *Service) setState(ctx context.Context, id string, state State) (*Issue, error) {
	now := Timestamp(s.now())
	var result *Issue
	err := s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		if issue.State != state {
			issue.State = state
			issue.Updated = now
			if err := tx.PutIssue(ctx, issue); err != nil {
				return err
			}
		}
		result = cloneIssue(issue)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

func (s *Service) AddComment(ctx context.Context, issueID, author, body string) (*Comment, error) {
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternal, err)
	}
	wallTime := Timestamp(s.now())
	var result *Comment
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.GetIssue(ctx, issueID)
		if err != nil {
			return err
		}
		if _, getErr := tx.GetComment(ctx, issueID, id); getErr == nil {
			return fmt.Errorf("%w: comment ID %q already exists", ErrConflict, id)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		comments, err := tx.ListComments(ctx, issueID)
		if err != nil {
			return err
		}
		SortComments(comments)
		created := wallTime
		if n := len(comments); n > 0 && !created.After(comments[n-1].Created) {
			created = comments[n-1].Created.Add(time.Nanosecond)
		}
		comment := &Comment{ID: id, Author: author, Created: created, Updated: created, Body: body}
		if err := comment.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if err := tx.PutComment(ctx, issueID, comment); err != nil {
			return err
		}
		// Rewriting the unchanged canonical Issue makes it the serialization
		// boundary for concurrent comment appends. A retry then observes the
		// winning comment and reapplies the +1ns rule with the same ID/wall time.
		if err := tx.PutIssue(ctx, issue); err != nil {
			return err
		}
		result = cloneComment(comment)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

func (s *Service) EditComment(ctx context.Context, issueID, commentID, body string) (*Comment, error) {
	now := Timestamp(s.now())
	var result *Comment
	err := s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		if _, err := tx.GetIssue(ctx, issueID); err != nil {
			return err
		}
		comment, err := tx.GetComment(ctx, issueID, commentID)
		if err != nil {
			return err
		}
		if comment.Body != body {
			comment.Body = body
			comment.Updated = now
			if err := comment.Validate(); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if err := tx.PutComment(ctx, issueID, comment); err != nil {
				return err
			}
		}
		result = cloneComment(comment)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

func persistenceError(err error) error {
	for _, known := range []error{ErrNotFound, ErrInvalid, ErrConflict, ErrInternal} {
		if errors.Is(err, known) {
			return err
		}
	}
	return ErrInternal
}

func findIssue(issues []*Issue, id string) *Issue {
	for _, issue := range issues {
		if issue.ID == id {
			return issue
		}
		if found := findIssue(issue.Children, id); found != nil {
			return found
		}
	}
	return nil
}
func cloneComment(c *Comment) *Comment {
	if c == nil {
		return nil
	}
	out := *c
	return &out
}
func cloneIssue(i *Issue) *Issue {
	if i == nil {
		return nil
	}
	out := *i
	out.Children = make([]*Issue, len(i.Children))
	for n, c := range i.Children {
		out.Children[n] = cloneIssue(c)
	}
	out.Comments = make([]*Comment, len(i.Comments))
	for n, c := range i.Comments {
		out.Comments[n] = cloneComment(c)
	}
	return &out
}
