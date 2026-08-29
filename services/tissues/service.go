package tissues

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tedla-brandsema/tissues/lib/service"
)

// Service owns tissues domain behavior and HTTP routes. It does not own a
// listener, PORT, process signals, or deployment lifecycle.
type Service struct {
	profile    service.Profile[Config]
	repo       Repository
	assets     AssetStore
	httpClient *http.Client
	now        func() time.Time
	newID      IDGenerator
	imageSlots chan struct{}
	process    imageProcessor
	mcpAuth    *MCPAuth
	mcp        *mcpRoutes
}

var _ service.Service = (*Service)(nil)

const (
	DefaultPageSize = 25
	MaxPageSize     = 100
)

type Option func(*Service)

func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) { s.httpClient = client }
}

func New(profile service.Profile[Config], repo Repository, assets AssetStore, options ...Option) (*Service, error) {
	if profile == nil {
		return nil, fmt.Errorf("tissues profile is required")
	}
	if repo == nil {
		return nil, fmt.Errorf("tissues repository is required")
	}
	if assets == nil {
		return nil, fmt.Errorf("tissues asset store is required")
	}
	if err := profile.Current().Config.ValidateConfig(); err != nil {
		return nil, err
	}
	svc := &Service{profile: profile, repo: repo, assets: assets, now: time.Now, newID: NewID, imageSlots: make(chan struct{}, 1), process: processImage}
	for _, option := range options {
		if option != nil {
			option(svc)
		}
	}
	if svc.mcpAuth != nil {
		routes, err := svc.newMCPRoutes(*svc.mcpAuth)
		if err != nil {
			return nil, err
		}
		svc.mcp = routes
	}
	return svc, nil
}

func (s *Service) acquireImageSlot(ctx context.Context) error {
	select {
	case s.imageSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseImageSlot() { <-s.imageSlots }

func (s *Service) UploadAsset(ctx context.Context, issueValue, filename string, input io.Reader) (*Asset, error) {
	issue, err := s.GetIssue(ctx, issueValue)
	if err != nil {
		return nil, err
	}
	if err := s.acquireImageSlot(ctx); err != nil {
		return nil, err
	}
	processed, err := func() (processedImage, error) {
		defer s.releaseImageSlot()
		data, err := readUploadBytes(input)
		if err != nil {
			return processedImage{}, err
		}
		return s.process(filename, data)
	}()
	if err != nil {
		if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrInternal) {
			return nil, err
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, fmt.Errorf("%w: upload request exceeds the multipart limit", ErrTooLarge)
		}
		return nil, fmt.Errorf("%w: read image upload: %v", ErrInternal, err)
	}
	return s.putProcessedAsset(ctx, issue, processed)
}

func readUploadBytes(input io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, MaxUploadBytes))
	if err != nil {
		return nil, err
	}
	var extra [1]byte
	n, err := input.Read(extra[:])
	if n > 0 {
		return nil, fmt.Errorf("%w: image file exceeds %d bytes", ErrTooLarge, MaxUploadBytes)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data, nil
}

func (s *Service) putProcessedAsset(ctx context.Context, issue *Issue, processed processedImage) (*Asset, error) {
	key := AssetKey{ProjectKey: issue.ProjectKey, IssueNumber: issue.Number, Name: processed.Name}
	asset, err := s.assets.Put(ctx, key, AssetWrite{ContentType: processed.ContentType, Width: processed.Width, Height: processed.Height, Data: processed.Data})
	if err != nil {
		return nil, assetStoreError(err)
	}
	return asset, nil
}

func (s *Service) ListAssets(ctx context.Context, issueValue string) ([]*Asset, error) {
	issue, err := s.GetIssue(ctx, issueValue)
	if err != nil {
		return nil, err
	}
	assets, err := s.assets.List(ctx, IssueRef{ProjectKey: issue.ProjectKey, Number: issue.Number})
	if err != nil {
		return nil, assetStoreError(err)
	}
	return assets, nil
}

func (s *Service) OpenAsset(ctx context.Context, issueValue, filename string) (*AssetContent, error) {
	issue, err := s.GetIssue(ctx, issueValue)
	if err != nil {
		return nil, err
	}
	name, _, err := canonicalAssetName(filename)
	if err != nil {
		return nil, err
	}
	content, err := s.assets.Open(ctx, AssetKey{ProjectKey: issue.ProjectKey, IssueNumber: issue.Number, Name: name})
	if err != nil {
		return nil, assetStoreError(err)
	}
	return content, nil
}

func assetStoreError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: asset storage: %v", ErrInternal, err)
}

func (*Service) Name() string { return "tissues" }

func (s *Service) ListProjects(ctx context.Context) ([]*Project, error) {
	var projects []*Project
	cursor := ""
	for {
		page, err := s.ListProjectsPage(ctx, MaxPageSize, cursor)
		if err != nil {
			return nil, err
		}
		projects = append(projects, page.Projects...)
		if page.NextCursor == "" {
			return projects, nil
		}
		cursor = page.NextCursor
	}
}

func (s *Service) ListProjectsPage(ctx context.Context, size int, cursor string) (*ProjectPage, error) {
	if size <= 0 || size > MaxPageSize {
		return nil, fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalid, MaxPageSize)
	}
	page, err := s.repo.ListProjectsPage(ctx, PageRequest{Size: size, Cursor: cursor})
	if err != nil {
		return nil, persistenceError(err)
	}
	for i, project := range page.Projects {
		page.Projects[i] = cloneProject(project)
	}
	return page, nil
}

func (s *Service) ListIssueOverviewsPage(ctx context.Context, size int, cursor, projectKey string) (*IssueOverviewPage, error) {
	if size <= 0 || size > MaxPageSize {
		return nil, fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalid, MaxPageSize)
	}
	if projectKey != "" {
		var err error
		projectKey, err = canonicalProjectInput(projectKey)
		if err != nil {
			return nil, err
		}
		if _, err := s.repo.GetProject(ctx, projectKey); err != nil {
			return nil, persistenceError(err)
		}
	}
	page, err := s.repo.ListIssueOverviewsPage(ctx, PageRequest{Size: size, Cursor: cursor, ProjectKey: projectKey})
	if err != nil {
		return nil, persistenceError(err)
	}
	return page, nil
}

func (s *Service) GetProject(ctx context.Context, key string) (*Project, error) {
	canonical, err := canonicalProjectInput(key)
	if err != nil {
		return nil, err
	}
	project, err := s.repo.GetProject(ctx, canonical)
	if err != nil {
		return nil, persistenceError(err)
	}
	return cloneProject(project), nil
}

func (s *Service) CreateProject(ctx context.Context, key string) (*Project, error) {
	canonical, err := canonicalProjectInput(key)
	if err != nil {
		return nil, err
	}
	created := &Project{Key: canonical, Created: Timestamp(s.now()), NextIssueNumber: 1}
	if err := created.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		if _, getErr := tx.GetProject(ctx, canonical); getErr == nil {
			return fmt.Errorf("%w: project %q already exists", ErrConflict, canonical)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		return tx.PutProject(ctx, created)
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return cloneProject(created), nil
}

func (s *Service) ListIssues(ctx context.Context, projectKey string) ([]*Issue, error) {
	canonical, err := canonicalProjectInput(projectKey)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.GetProject(ctx, canonical); err != nil {
		return nil, persistenceError(err)
	}
	issues, err := s.repo.ListIssues(ctx, canonical)
	if err != nil {
		return nil, persistenceError(err)
	}
	sortIssues(issues)
	return issues, nil
}

func (s *Service) GetIssue(ctx context.Context, value string) (*Issue, error) {
	ref, err := issueRefInput(value)
	if err != nil {
		return nil, err
	}
	issue, err := s.repo.ResolveIssue(ctx, ref)
	if err != nil {
		return nil, persistenceError(err)
	}
	return issue, nil
}

type CreateIssueRequest struct {
	Title       string
	Description string
}

func (s *Service) CreateIssue(ctx context.Context, projectKey string, req CreateIssueRequest) (*Issue, error) {
	projectKey, err := canonicalProjectInput(projectKey)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternal, err)
	}
	now := Timestamp(s.now())
	var result *Issue
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		project, err := tx.GetProject(ctx, projectKey)
		if err != nil {
			return err
		}
		if project.NextIssueNumber <= 0 {
			return fmt.Errorf("%w: project %s has invalid allocator state", ErrInternal, projectKey)
		}
		ref := IssueRef{ProjectKey: projectKey, Number: project.NextIssueNumber}
		if _, getErr := tx.ResolveIssue(ctx, ref); getErr == nil {
			return fmt.Errorf("%w: issue reference %s already exists", ErrConflict, ref)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		if _, getErr := tx.GetIssue(ctx, projectKey, id); getErr == nil {
			return fmt.Errorf("%w: issue ID %q already exists", ErrConflict, id)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		created := &Issue{ID: id, ProjectKey: projectKey, Number: ref.Number, Ref: ref.String(), Title: req.Title, State: StateOpen, Created: now, Updated: now, Description: req.Description}
		if err := created.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if err := tx.PutIssue(ctx, created); err != nil {
			return err
		}
		if err := tx.PutIssueRef(ctx, ref, id); err != nil {
			return err
		}
		project.NextIssueNumber++
		if err := tx.PutProject(ctx, project); err != nil {
			return err
		}
		result = cloneIssue(created)
		return nil
	})
	if err != nil {
		return nil, persistenceError(err)
	}
	return result, nil
}

type UpdateIssueRequest struct {
	Ref                string
	Title, Description *string
}

func (s *Service) UpdateIssue(ctx context.Context, req UpdateIssueRequest) (*Issue, error) {
	ref, err := issueRefInput(req.Ref)
	if err != nil {
		return nil, err
	}
	now := Timestamp(s.now())
	var result *Issue
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.ResolveIssue(ctx, ref)
		if err != nil {
			return err
		}
		changed := false
		if req.Title != nil && *req.Title != issue.Title {
			issue.Title, changed = *req.Title, true
		}
		if req.Description != nil && *req.Description != issue.Description {
			issue.Description, changed = *req.Description, true
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

func (s *Service) MoveIssue(ctx context.Context, issueValue, parentValue string) (*Issue, error) {
	ref, err := issueRefInput(issueValue)
	if err != nil {
		return nil, err
	}
	now := Timestamp(s.now())
	var result *Issue
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.ResolveIssue(ctx, ref)
		if err != nil {
			return err
		}
		parentID, parentCanonical, err := resolveParent(ctx, tx, issue, parentValue)
		if err != nil {
			return err
		}
		if issue.ParentID != parentID || issue.ParentRef != parentCanonical {
			issue.ParentID, issue.ParentRef, issue.Updated = parentID, parentCanonical, now
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

func resolveParent(ctx context.Context, tx Transaction, issue *Issue, parentValue string) (string, string, error) {
	if parentValue == "" {
		return "", "", nil
	}
	parentRef, err := issueRefInput(parentValue)
	if err != nil {
		return "", "", err
	}
	if parentRef.ProjectKey != issue.ProjectKey {
		return "", "", fmt.Errorf("%w: parent reference must belong to project %s", ErrInvalid, issue.ProjectKey)
	}
	parent, err := tx.ResolveIssue(ctx, parentRef)
	if err != nil {
		return "", "", err
	}
	if parent.ProjectKey != issue.ProjectKey {
		return "", "", fmt.Errorf("%w: parent reference must belong to project %s", ErrInvalid, issue.ProjectKey)
	}
	if parent.ID == issue.ID {
		return "", "", fmt.Errorf("%w: issue %s cannot be its own parent", ErrInvalid, issue.Ref)
	}
	visited := map[string]bool{}
	for current := parent; current != nil; {
		if current.ID == issue.ID {
			return "", "", fmt.Errorf("%w: issue %s cannot be moved beneath descendant %s", ErrInvalid, issue.Ref, parent.Ref)
		}
		if visited[current.ID] {
			return "", "", fmt.Errorf("%w: existing hierarchy cycle at issue %s", ErrInternal, current.Ref)
		}
		visited[current.ID] = true
		if current.ParentID == "" {
			break
		}
		current, err = tx.GetIssue(ctx, issue.ProjectKey, current.ParentID)
		if err != nil {
			return "", "", err
		}
	}
	return parent.ID, parent.Ref, nil
}

func (s *Service) CloseIssue(ctx context.Context, ref string) (*Issue, error) {
	return s.setState(ctx, ref, StateClosed)
}
func (s *Service) ReopenIssue(ctx context.Context, ref string) (*Issue, error) {
	return s.setState(ctx, ref, StateOpen)
}

func (s *Service) setState(ctx context.Context, value string, state State) (*Issue, error) {
	ref, err := issueRefInput(value)
	if err != nil {
		return nil, err
	}
	now := Timestamp(s.now())
	var result *Issue
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.ResolveIssue(ctx, ref)
		if err != nil {
			return err
		}
		if issue.State != state {
			issue.State, issue.Updated = state, now
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

func (s *Service) AddComment(ctx context.Context, issueValue, author, body string) (*Comment, error) {
	ref, err := issueRefInput(issueValue)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternal, err)
	}
	wallTime := Timestamp(s.now())
	var result *Comment
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.ResolveIssue(ctx, ref)
		if err != nil {
			return err
		}
		if _, getErr := tx.GetComment(ctx, issue.ProjectKey, issue.ID, id); getErr == nil {
			return fmt.Errorf("%w: comment ID %q already exists", ErrConflict, id)
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		comments, err := tx.ListComments(ctx, issue.ProjectKey, issue.ID)
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
		if err := tx.PutComment(ctx, issue.ProjectKey, issue.ID, comment); err != nil {
			return err
		}
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

func (s *Service) EditComment(ctx context.Context, issueValue, commentID, body string) (*Comment, error) {
	ref, err := issueRefInput(issueValue)
	if err != nil {
		return nil, err
	}
	now := Timestamp(s.now())
	var result *Comment
	err = s.repo.RunInTransaction(ctx, func(tx Transaction) error {
		issue, err := tx.ResolveIssue(ctx, ref)
		if err != nil {
			return err
		}
		comment, err := tx.GetComment(ctx, issue.ProjectKey, issue.ID, commentID)
		if err != nil {
			return err
		}
		if comment.Body != body {
			comment.Body, comment.Updated = body, now
			if err := comment.Validate(); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if err := tx.PutComment(ctx, issue.ProjectKey, issue.ID, comment); err != nil {
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

func canonicalProjectInput(value string) (string, error) {
	key, err := CanonicalProjectKey(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return key, nil
}

func issueRefInput(value string) (IssueRef, error) {
	ref, err := ParseIssueRef(value)
	if err != nil {
		return IssueRef{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return ref, nil
}

func persistenceError(err error) error {
	for _, known := range []error{ErrNotFound, ErrInvalid, ErrConflict, ErrInternal} {
		if errors.Is(err, known) {
			return err
		}
	}
	return ErrInternal
}

func cloneProject(project *Project) *Project {
	if project == nil {
		return nil
	}
	out := *project
	return &out
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
