// Package firestore implements tenant-scoped Tissues persistence on Firestore
// in Native mode.
package firestore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	gcfirestore "cloud.google.com/go/firestore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tenantsCollection  = "tenants"
	projectsCollection = "projects"
	issuesCollection   = "issues"
	commentsCollection = "comments"
)

// Store is the unbound Firestore repository root. Client construction and
// database selection belong to application composition.
type Store struct {
	client *gcfirestore.Client
}

// TenantStore is an immutable repository view rooted beneath one tenant.
type TenantStore struct {
	client   *gcfirestore.Client
	tenantID tissues.TenantID
	tenant   *gcfirestore.DocumentRef
}

func New(client *gcfirestore.Client) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("Firestore client is required")
	}
	return &Store{client: client}, nil
}

func (s *Store) ForTenant(id tissues.TenantID) (tissues.TenantRepository, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("%w: Firestore client is unavailable", tissues.ErrInternal)
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	tenant := s.client.Collection(tenantsCollection).Doc(id.String())
	return &TenantStore{client: s.client, tenantID: id, tenant: tenant}, nil
}

func (s *TenantStore) projects() *gcfirestore.CollectionRef {
	return s.tenant.Collection(projectsCollection)
}

func (s *TenantStore) issues() *gcfirestore.CollectionRef {
	return s.tenant.Collection(issuesCollection)
}

func (s *TenantStore) comments() *gcfirestore.CollectionRef {
	return s.tenant.Collection(commentsCollection)
}

func (s *TenantStore) projectRef(key string) *gcfirestore.DocumentRef {
	return s.projects().Doc(key)
}

func (s *TenantStore) issueRef(ref tissues.IssueRef) *gcfirestore.DocumentRef {
	return s.issues().Doc(ref.String())
}

func (s *TenantStore) commentRef(ref tissues.IssueRef, id string) *gcfirestore.DocumentRef {
	return s.comments().Doc(commentDocumentID(ref, id))
}

func (s *TenantStore) ListProjectsPage(ctx context.Context, request tissues.PageRequest) (*tissues.ProjectPage, error) {
	if err := validatePageRequest(request, false); err != nil {
		return nil, err
	}
	query := s.projects().OrderBy(gcfirestore.DocumentID, gcfirestore.Asc).Limit(request.Size + 1)
	if request.Cursor != "" {
		cursor, err := decodeCursor(s.tenantID, projectCursorKind, "", request.Cursor)
		if err != nil {
			return nil, err
		}
		query = query.StartAfter(cursor.ProjectKey)
	}
	docs, err := collect(ctx, query)
	if err != nil {
		return nil, translate(err)
	}
	projects := make([]*tissues.Project, 0, min(len(docs), request.Size))
	for _, doc := range docs[:min(len(docs), request.Size)] {
		project, err := decodeProjectSnapshot(doc)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	next := ""
	if len(docs) > request.Size {
		next = encodeCursor(cursorPayload{Version: cursorVersion, TenantID: s.tenantID.String(), Kind: projectCursorKind, ProjectKey: projects[len(projects)-1].Key})
	}
	return &tissues.ProjectPage{Projects: projects, NextCursor: next}, nil
}

func (s *TenantStore) ListIssueOverviewsPage(ctx context.Context, request tissues.PageRequest) (*tissues.IssueOverviewPage, error) {
	if err := validatePageRequest(request, true); err != nil {
		return nil, err
	}
	query := s.issues().OrderBy("updated_ns", gcfirestore.Desc).OrderBy("project_key", gcfirestore.Asc).OrderBy("number", gcfirestore.Asc).Limit(request.Size + 1)
	if request.ProjectKey != "" {
		query = query.Where("project_key", "==", request.ProjectKey)
	}
	if request.Cursor != "" {
		cursor, err := decodeCursor(s.tenantID, issueCursorKind, request.ProjectKey, request.Cursor)
		if err != nil {
			return nil, err
		}
		query = query.StartAfter(cursor.UpdatedNS, cursor.ProjectKey, cursor.Number)
	}
	docs, err := collect(ctx, query)
	if err != nil {
		return nil, translate(err)
	}
	issuesOnPage := make([]*tissues.Issue, 0, min(len(docs), request.Size))
	for _, doc := range docs[:min(len(docs), request.Size)] {
		issue, _, err := decodeIssueSnapshot(doc)
		if err != nil {
			return nil, err
		}
		issuesOnPage = append(issuesOnPage, issue)
	}
	if err := s.validateIssueOverviewRelationships(ctx, issuesOnPage); err != nil {
		return nil, err
	}
	overviews := make([]*tissues.IssueOverview, 0, len(issuesOnPage))
	for _, issue := range issuesOnPage {
		overviews = append(overviews, &tissues.IssueOverview{ProjectKey: issue.ProjectKey, Number: issue.Number, Ref: issue.Ref, Title: issue.Title, State: issue.State, ParentRef: issue.ParentRef, Updated: issue.Updated})
	}
	next := ""
	if len(docs) > request.Size {
		last := overviews[len(overviews)-1]
		next = encodeCursor(cursorPayload{Version: cursorVersion, TenantID: s.tenantID.String(), Kind: issueCursorKind, Filter: request.ProjectKey, UpdatedNS: last.Updated.UnixNano(), ProjectKey: last.ProjectKey, Number: last.Number})
	}
	return &tissues.IssueOverviewPage{Issues: overviews, NextCursor: next}, nil
}

func (s *TenantStore) validateIssueOverviewRelationships(ctx context.Context, issues []*tissues.Issue) error {
	projectKeys, parentRefs := overviewRelationshipKeys(issues)

	projects := make(map[string]*tissues.Project, len(projectKeys))
	if len(projectKeys) > 0 {
		refs := make([]*gcfirestore.DocumentRef, len(projectKeys))
		for index, key := range projectKeys {
			refs[index] = s.projectRef(key)
		}
		docs, err := s.client.GetAll(ctx, refs)
		if err != nil {
			return fmt.Errorf("%w: read Issue overview Projects", tissues.ErrInternal)
		}
		for index, doc := range docs {
			project, err := decodeProjectSnapshot(doc)
			if err != nil {
				return fmt.Errorf("%w: Issue overview references invalid Project %q", tissues.ErrInternal, projectKeys[index])
			}
			projects[projectKeys[index]] = project
		}
	}

	parents := make(map[string]*tissues.Issue, len(parentRefs))
	if len(parentRefs) > 0 {
		refs := make([]*gcfirestore.DocumentRef, len(parentRefs))
		for index, value := range parentRefs {
			ref, err := tissues.ParseIssueRef(value)
			if err != nil {
				return fmt.Errorf("%w: Issue overview contains invalid ParentRef", tissues.ErrInternal)
			}
			refs[index] = s.issueRef(ref)
		}
		docs, err := s.client.GetAll(ctx, refs)
		if err != nil {
			return fmt.Errorf("%w: read Issue overview parents", tissues.ErrInternal)
		}
		for index, doc := range docs {
			parent, _, err := decodeIssueSnapshot(doc)
			if err != nil {
				return fmt.Errorf("%w: Issue overview references invalid parent %q", tissues.ErrInternal, parentRefs[index])
			}
			parents[parentRefs[index]] = parent
		}
	}

	return validateIssueOverviewRelationships(issues, projects, parents)
}

func overviewRelationshipKeys(issues []*tissues.Issue) ([]string, []string) {
	projectSet := make(map[string]struct{}, len(issues))
	parentSet := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		projectSet[issue.ProjectKey] = struct{}{}
		if issue.ParentRef != "" {
			parentSet[issue.ParentRef] = struct{}{}
		}
	}
	projectKeys := make([]string, 0, len(projectSet))
	for key := range projectSet {
		projectKeys = append(projectKeys, key)
	}
	parentRefs := make([]string, 0, len(parentSet))
	for ref := range parentSet {
		parentRefs = append(parentRefs, ref)
	}
	sort.Strings(projectKeys)
	sort.Strings(parentRefs)
	return projectKeys, parentRefs
}

func validateIssueOverviewRelationships(issues []*tissues.Issue, projects map[string]*tissues.Project, parents map[string]*tissues.Issue) error {
	for _, issue := range issues {
		project := projects[issue.ProjectKey]
		if project == nil {
			return fmt.Errorf("%w: Issue %q references missing Project %q", tissues.ErrInternal, issue.Ref, issue.ProjectKey)
		}
		if err := project.Validate(); err != nil || project.Key != issue.ProjectKey {
			return fmt.Errorf("%w: Issue %q references invalid Project %q", tissues.ErrInternal, issue.Ref, issue.ProjectKey)
		}
		if issue.ParentRef == "" {
			continue
		}
		parent := parents[issue.ParentRef]
		if parent == nil {
			return fmt.Errorf("%w: Issue %q references missing parent %q", tissues.ErrInternal, issue.Ref, issue.ParentRef)
		}
		if err := parent.Validate(); err != nil || parent.Ref != issue.ParentRef || parent.ProjectKey != issue.ProjectKey {
			return fmt.Errorf("%w: Issue %q references invalid parent %q", tissues.ErrInternal, issue.Ref, issue.ParentRef)
		}
	}
	return nil
}

func (s *TenantStore) GetProject(ctx context.Context, key string) (*tissues.Project, error) {
	canonical, err := canonicalProject(key)
	if err != nil {
		return nil, err
	}
	doc, err := s.projectRef(canonical).Get(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return decodeProjectSnapshot(doc)
}

func (s *TenantStore) ListIssues(ctx context.Context, projectKey string) ([]*tissues.Issue, error) {
	canonical, err := canonicalProject(projectKey)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetProject(ctx, canonical); err != nil {
		return nil, err
	}
	issueDocs, err := collect(ctx, s.issues().Where("project_key", "==", canonical))
	if err != nil {
		return nil, translate(err)
	}
	commentDocs, err := collect(ctx, s.comments().Where("project_key", "==", canonical))
	if err != nil {
		return nil, translate(err)
	}
	return reconstructIssueTree(canonical, issueDocs, commentDocs)
}

func (s *TenantStore) GetIssue(ctx context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid Issue reference", tissues.ErrInvalid)
	}
	doc, err := s.issueRef(ref).Get(ctx)
	if err != nil {
		return nil, translate(err)
	}
	if _, _, err := decodeIssueSnapshot(doc); err != nil {
		return nil, err
	}
	roots, err := s.ListIssues(ctx, ref.ProjectKey)
	if err != nil {
		return nil, err
	}
	if issue := findIssue(roots, ref.String()); issue != nil {
		return issue, nil
	}
	return nil, fmt.Errorf("%w: point-read Issue %s missing from project view", tissues.ErrInternal, ref)
}

func (s *TenantStore) RunInTransaction(ctx context.Context, fn func(tissues.Transaction) error) error {
	if fn == nil {
		return fmt.Errorf("%w: transaction callback is required", tissues.ErrInvalid)
	}
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *gcfirestore.Transaction) error {
		return fn(&transaction{
			store:        s,
			tx:           tx,
			projectsRead: make(map[string]readState),
			issuesRead:   make(map[string]issueReadState),
			commentsRead: make(map[string]readState),
		})
	})
	return translate(err)
}

func collect(ctx context.Context, query gcfirestore.Query) ([]*gcfirestore.DocumentSnapshot, error) {
	iter := query.Documents(ctx)
	defer iter.Stop()
	var docs []*gcfirestore.DocumentSnapshot
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
}

func reconstructIssueTree(projectKey string, issueDocs, commentDocs []*gcfirestore.DocumentSnapshot) ([]*tissues.Issue, error) {
	flat := make(map[string]*tissues.Issue, len(issueDocs))
	byNumber := make(map[int64]*tissues.Issue, len(issueDocs))
	for _, doc := range issueDocs {
		issue, _, err := decodeIssueSnapshot(doc)
		if err != nil {
			return nil, err
		}
		if issue.ProjectKey != projectKey {
			return nil, fmt.Errorf("%w: Issue %q escaped Project query", tissues.ErrInternal, issue.Ref)
		}
		if flat[issue.Ref] != nil || byNumber[issue.Number] != nil {
			return nil, fmt.Errorf("%w: duplicate Issue identity or number in Project %s", tissues.ErrInternal, projectKey)
		}
		flat[issue.Ref], byNumber[issue.Number] = issue, issue
	}
	for _, doc := range commentDocs {
		ref, comment, err := decodeCommentSnapshot(doc)
		if err != nil {
			return nil, err
		}
		if ref.ProjectKey != projectKey {
			return nil, fmt.Errorf("%w: Comment %q escaped Project query", tissues.ErrInternal, comment.ID)
		}
		issue := flat[ref.String()]
		if issue == nil {
			return nil, fmt.Errorf("%w: Comment %q references missing Issue %q", tissues.ErrInternal, comment.ID, ref)
		}
		issue.Comments = append(issue.Comments, comment)
	}
	var roots []*tissues.Issue
	for _, issue := range flat {
		if issue.ParentRef == "" {
			roots = append(roots, issue)
			continue
		}
		parent := flat[issue.ParentRef]
		if parent == nil {
			return nil, fmt.Errorf("%w: Issue %q has missing parent %q", tissues.ErrInternal, issue.Ref, issue.ParentRef)
		}
		parent.Children = append(parent.Children, issue)
	}
	if err := validateAcyclic(flat); err != nil {
		return nil, err
	}
	sortIssueTree(roots)
	return roots, nil
}

func validateAcyclic(flat map[string]*tissues.Issue) error {
	for ref := range flat {
		seen := make(map[string]bool)
		for current := ref; current != ""; current = flat[current].ParentRef {
			issue := flat[current]
			if issue == nil {
				return fmt.Errorf("%w: missing Issue %q in hierarchy", tissues.ErrInternal, current)
			}
			if seen[current] {
				return fmt.Errorf("%w: stored hierarchy cycle at Issue %q", tissues.ErrInternal, current)
			}
			seen[current] = true
		}
	}
	return nil
}

func sortIssueTree(issues []*tissues.Issue) {
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number < issues[j].Number })
	for _, issue := range issues {
		sortIssueTree(issue.Children)
		tissues.SortComments(issue.Comments)
	}
}

func findIssue(issues []*tissues.Issue, ref string) *tissues.Issue {
	for _, issue := range issues {
		if issue.Ref == ref {
			return issue
		}
		if found := findIssue(issue.Children, ref); found != nil {
			return found
		}
	}
	return nil
}

func validatePageRequest(request tissues.PageRequest, allowFilter bool) error {
	if request.Size <= 0 || request.Size > tissues.MaxPageSize {
		return fmt.Errorf("%w: page size must be between 1 and %d", tissues.ErrInvalid, tissues.MaxPageSize)
	}
	if !allowFilter && request.ProjectKey != "" {
		return fmt.Errorf("%w: Project filter is not valid for Project pagination", tissues.ErrInvalid)
	}
	if request.ProjectKey != "" {
		canonical, err := canonicalProject(request.ProjectKey)
		if err != nil || canonical != request.ProjectKey {
			return fmt.Errorf("%w: invalid Project filter", tissues.ErrInvalid)
		}
	}
	return nil
}

func canonicalProject(key string) (string, error) {
	canonical, err := tissues.CanonicalProjectKey(key)
	if err != nil || canonical != key {
		return "", fmt.Errorf("%w: invalid canonical Project key", tissues.ErrInvalid)
	}
	return canonical, nil
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{tissues.ErrNotFound, tissues.ErrInvalid, tissues.ErrConflict, tissues.ErrInternal} {
		if errors.Is(err, known) {
			return err
		}
	}
	switch status.Code(err) {
	case codes.NotFound:
		return tissues.ErrNotFound
	case codes.Aborted, codes.AlreadyExists:
		return tissues.ErrConflict
	default:
		return tissues.ErrInternal
	}
}

func checkedRevision(previous int64) (int64, error) {
	if previous < 0 || previous == math.MaxInt64 {
		return 0, fmt.Errorf("%w: invalid comment order revision", tissues.ErrInternal)
	}
	return previous + 1, nil
}

func unixNano(value int64) time.Time { return time.Unix(0, value).UTC() }

func validDocumentID(value string) bool {
	return value != "" && !strings.Contains(value, "/")
}

var _ tissues.Repository = (*Store)(nil)
var _ tissues.TenantRepository = (*TenantStore)(nil)
