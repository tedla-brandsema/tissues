// Package datastore implements tissues persistence on Cloud Datastore.
package datastore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
)

const (
	ProjectKind = "tissues_project"
	IssueKind   = "tissues_issue"
	CommentKind = "tissues_comment"
)

type Store struct {
	client    *gcds.Client
	namespace string
}

func New(client *gcds.Client, namespace string) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("Datastore client is required")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("Datastore namespace is required")
	}
	return &Store{client: client, namespace: namespace}, nil
}

type projectEntity struct {
	Created         int64 `datastore:"Created"`
	NextIssueNumber int64 `datastore:"NextIssueNumber"`
}
type issueEntity struct {
	Number      int64  `datastore:"Number"`
	Title       string `datastore:"Title"`
	State       string `datastore:"State"`
	Created     int64  `datastore:"Created"`
	Updated     int64  `datastore:"Updated"`
	Description string `datastore:"Description,noindex"`
	ParentRef   string `datastore:"ParentRef"`
}
type commentEntity struct {
	Author  string `datastore:"Author"`
	Created int64  `datastore:"Created"`
	Updated int64  `datastore:"Updated"`
	Body    string `datastore:"Body,noindex"`
}

func (s *Store) projectKey(projectKey string) *gcds.Key {
	key := gcds.NameKey(ProjectKind, projectKey, nil)
	key.Namespace = s.namespace
	return key
}
func (s *Store) issueKey(ref tissues.IssueRef) *gcds.Key {
	key := gcds.NameKey(IssueKind, ref.String(), s.projectKey(ref.ProjectKey))
	key.Namespace = s.namespace
	return key
}
func (s *Store) commentKey(ref tissues.IssueRef, id string) *gcds.Key {
	key := gcds.NameKey(CommentKind, id, s.issueKey(ref))
	key.Namespace = s.namespace
	return key
}

func (s *Store) ListProjectsPage(ctx context.Context, request tissues.PageRequest) (*tissues.ProjectPage, error) {
	query := gcds.NewQuery(ProjectKind).Namespace(s.namespace).Order("__key__").Limit(request.Size + 1)
	if request.Cursor != "" {
		cursor, err := gcds.DecodeCursor(request.Cursor)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cursor", tissues.ErrInvalid)
		}
		query = query.Start(cursor)
	}
	iter := s.client.Run(ctx, query)
	projects := make([]*tissues.Project, 0, request.Size)
	nextCursor := ""
	for len(projects) < request.Size {
		var entity projectEntity
		key, err := iter.Next(&entity)
		if errors.Is(err, iterator.Done) {
			return &tissues.ProjectPage{Projects: projects}, nil
		}
		if err != nil {
			return nil, translate(err)
		}
		if key.Parent != nil || key.Name == "" {
			return nil, fmt.Errorf("%w: malformed Project key", tissues.ErrInternal)
		}
		project := decodeProject(key.Name, &entity)
		if err := project.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored project %q: %v", tissues.ErrInternal, key.Name, err)
		}
		projects = append(projects, project)
	}
	cursor, err := iter.Cursor()
	if err != nil {
		return nil, translate(err)
	}
	var extra projectEntity
	if _, err := iter.Next(&extra); err == nil {
		nextCursor = cursor.String()
	} else if !errors.Is(err, iterator.Done) {
		return nil, translate(err)
	}
	return &tissues.ProjectPage{Projects: projects, NextCursor: nextCursor}, nil
}

func (s *Store) ListIssueOverviewsPage(ctx context.Context, request tissues.PageRequest) (*tissues.IssueOverviewPage, error) {
	query := gcds.NewQuery(IssueKind).Namespace(s.namespace).Order("-Updated").Limit(request.Size + 1)
	if request.ProjectKey != "" {
		query = query.Ancestor(s.projectKey(request.ProjectKey))
	}
	if request.Cursor != "" {
		cursor, err := gcds.DecodeCursor(request.Cursor)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cursor", tissues.ErrInvalid)
		}
		query = query.Start(cursor)
	}
	iter := s.client.Run(ctx, query)
	keys := make([]*gcds.Key, 0, request.Size)
	entities := make([]issueEntity, 0, request.Size)
	nextCursor := ""
	for len(keys) < request.Size {
		var entity issueEntity
		key, err := iter.Next(&entity)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, translate(err)
		}
		keys, entities = append(keys, key), append(entities, entity)
	}
	if len(keys) == request.Size {
		cursor, err := iter.Cursor()
		if err != nil {
			return nil, translate(err)
		}
		var extra issueEntity
		if _, err := iter.Next(&extra); err == nil {
			nextCursor = cursor.String()
		} else if !errors.Is(err, iterator.Done) {
			return nil, translate(err)
		}
	}
	issuesOnPage := make([]*tissues.Issue, len(keys))
	projectKeys := make([]*gcds.Key, len(keys))
	projectEntities := make([]projectEntity, len(keys))
	for i, key := range keys {
		if key == nil || key.Name == "" || key.Parent == nil || key.Parent.Kind != ProjectKind || key.Parent.Name == "" || key.Parent.Parent != nil {
			return nil, fmt.Errorf("%w: issue has malformed Project ancestry", tissues.ErrInternal)
		}
		projectKey, err := tissues.CanonicalProjectKey(key.Parent.Name)
		if err != nil || projectKey != key.Parent.Name {
			return nil, fmt.Errorf("%w: issue %q has invalid Project ancestry", tissues.ErrInternal, key.Name)
		}
		ref, err := tissues.ParseIssueRef(key.Name)
		if err != nil || ref.ProjectKey != projectKey {
			return nil, fmt.Errorf("%w: issue %q has invalid canonical key", tissues.ErrInternal, key.Name)
		}
		issue := decodeIssue(ref, &entities[i])
		if err := issue.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, key.Name, err)
		}
		issuesOnPage[i] = issue
		projectKeys[i] = s.projectKey(projectKey)
	}
	if len(projectKeys) > 0 {
		if err := s.client.GetMulti(ctx, projectKeys, projectEntities); err != nil {
			return nil, fmt.Errorf("%w: issue overview references a missing or invalid Project", tissues.ErrInternal)
		}
		for i := range projectEntities {
			if err := decodeProject(projectKeys[i].Name, &projectEntities[i]).Validate(); err != nil {
				return nil, fmt.Errorf("%w: issue overview references invalid Project %q", tissues.ErrInternal, projectKeys[i].Name)
			}
		}
	}

	parentKeys := make([]*gcds.Key, 0, len(keys))
	parentEntities := make([]issueEntity, 0, len(keys))
	parentIndexes := make([]int, 0, len(keys))
	for i, issue := range issuesOnPage {
		if issue.ParentRef != "" {
			parentRef, _ := tissues.ParseIssueRef(issue.ParentRef)
			parentKeys = append(parentKeys, s.issueKey(parentRef))
			parentEntities = append(parentEntities, issueEntity{})
			parentIndexes = append(parentIndexes, i)
		}
	}
	if len(parentKeys) > 0 {
		if err := s.client.GetMulti(ctx, parentKeys, parentEntities); err != nil {
			return nil, fmt.Errorf("%w: issue overview contains a missing parent", tissues.ErrInternal)
		}
		for i, entity := range parentEntities {
			pageIssue := issuesOnPage[parentIndexes[i]]
			parentRef, parseErr := tissues.ParseIssueRef(parentKeys[i].Name)
			if parseErr != nil {
				return nil, fmt.Errorf("%w: issue overview contains an invalid parent key", tissues.ErrInternal)
			}
			parent := decodeIssue(parentRef, &entity)
			if err := parent.Validate(); err != nil {
				return nil, fmt.Errorf("%w: issue overview contains an invalid parent", tissues.ErrInternal)
			}
			if parent.Ref != pageIssue.ParentRef {
				return nil, fmt.Errorf("%w: issue overview parent key disagrees with stored parent", tissues.ErrInternal)
			}
		}
	}

	overviews := make([]*tissues.IssueOverview, len(issuesOnPage))
	for i, issue := range issuesOnPage {
		overviews[i] = &tissues.IssueOverview{ProjectKey: issue.ProjectKey, Number: issue.Number, Ref: issue.Ref, Title: issue.Title, State: issue.State, ParentRef: issue.ParentRef, Updated: issue.Updated}
	}
	return &tissues.IssueOverviewPage{Issues: overviews, NextCursor: nextCursor}, nil
}

func (s *Store) GetProject(ctx context.Context, projectKey string) (*tissues.Project, error) {
	var entity projectEntity
	if err := s.client.Get(ctx, s.projectKey(projectKey), &entity); err != nil {
		return nil, translate(err)
	}
	project := decodeProject(projectKey, &entity)
	if err := project.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored project %q: %v", tissues.ErrInternal, projectKey, err)
	}
	return project, nil
}

func (s *Store) ListIssues(ctx context.Context, projectKey string) ([]*tissues.Issue, error) {
	if _, err := s.GetProject(ctx, projectKey); err != nil {
		return nil, err
	}
	ancestor := s.projectKey(projectKey)
	var issueEntities []issueEntity
	issueKeys, err := s.client.GetAll(ctx, gcds.NewQuery(IssueKind).Namespace(s.namespace).Ancestor(ancestor), &issueEntities)
	if err != nil {
		return nil, translate(err)
	}
	flat := make(map[string]*tissues.Issue, len(issueKeys))
	byNumber := make(map[int64]*tissues.Issue, len(issueKeys))
	for i, key := range issueKeys {
		if !directChildOf(key, ProjectKind, projectKey) {
			return nil, fmt.Errorf("%w: issue %q has malformed Project ancestry", tissues.ErrInternal, key.Name)
		}
		ref, parseErr := tissues.ParseIssueRef(key.Name)
		if parseErr != nil || ref.ProjectKey != projectKey {
			return nil, fmt.Errorf("%w: issue %q has invalid canonical key", tissues.ErrInternal, key.Name)
		}
		issue := decodeIssue(ref, &issueEntities[i])
		if err := issue.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, key.Name, err)
		}
		if flat[issue.Ref] != nil || byNumber[issue.Number] != nil {
			return nil, fmt.Errorf("%w: duplicate issue identity or number in project %s", tissues.ErrInternal, projectKey)
		}
		flat[issue.Ref], byNumber[issue.Number] = issue, issue
	}

	var commentEntities []commentEntity
	commentKeys, err := s.client.GetAll(ctx, gcds.NewQuery(CommentKind).Namespace(s.namespace).Ancestor(ancestor), &commentEntities)
	if err != nil {
		return nil, translate(err)
	}
	for i, key := range commentKeys {
		if key.Parent == nil || !directChildOf(key.Parent, ProjectKind, projectKey) {
			return nil, fmt.Errorf("%w: comment %q has malformed Issue ancestry", tissues.ErrInternal, key.Name)
		}
		issue := flat[key.Parent.Name]
		if issue == nil {
			return nil, fmt.Errorf("%w: comment %q has missing Issue ancestor %q", tissues.ErrInternal, key.Name, key.Parent.Name)
		}
		comment := decodeComment(key.Name, &commentEntities[i])
		if err := comment.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored comment %q: %v", tissues.ErrInternal, key.Name, err)
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
			return nil, fmt.Errorf("%w: issue %q has missing parent %q inside project %s", tissues.ErrInternal, issue.Ref, issue.ParentRef, projectKey)
		}
		parent.Children = append(parent.Children, issue)
	}
	if err := validateAcyclic(flat); err != nil {
		return nil, err
	}
	sortIssueTree(roots)
	return roots, nil
}

func (s *Store) GetIssue(ctx context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	var entity issueEntity
	if err := s.client.Get(ctx, s.issueKey(ref), &entity); err != nil {
		return nil, translate(err)
	}
	point := decodeIssue(ref, &entity)
	if err := point.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored issue %s: %v", tissues.ErrInternal, ref, err)
	}
	roots, err := s.ListIssues(ctx, ref.ProjectKey)
	if err != nil {
		return nil, err
	}
	if issue := findIssue(roots, ref.String()); issue != nil {
		return issue, nil
	}
	return nil, fmt.Errorf("%w: point-read issue %s missing from project view", tissues.ErrInternal, ref)
}

func (s *Store) RunInTransaction(ctx context.Context, fn func(tissues.Transaction) error) error {
	_, err := s.client.RunInTransaction(ctx, func(tx *gcds.Transaction) error { return fn(&transaction{store: s, tx: tx}) }, gcds.MaxAttempts(20))
	return translate(err)
}

type transaction struct {
	store *Store
	tx    *gcds.Transaction
}

func (t *transaction) GetProject(_ context.Context, key string) (*tissues.Project, error) {
	var entity projectEntity
	if err := t.tx.Get(t.store.projectKey(key), &entity); err != nil {
		return nil, translate(err)
	}
	project := decodeProject(key, &entity)
	if err := project.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored project %q: %v", tissues.ErrInternal, key, err)
	}
	return project, nil
}
func (t *transaction) PutProject(_ context.Context, project *tissues.Project) error {
	if err := project.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.projectKey(project.Key), encodeProject(project))
	return translate(err)
}
func (t *transaction) GetIssue(_ context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	var entity issueEntity
	if err := t.tx.Get(t.store.issueKey(ref), &entity); err != nil {
		return nil, translate(err)
	}
	issue := decodeIssue(ref, &entity)
	if err := issue.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, ref, err)
	}
	return issue, nil
}
func (t *transaction) GetComment(_ context.Context, ref tissues.IssueRef, id string) (*tissues.Comment, error) {
	var entity commentEntity
	if err := t.tx.Get(t.store.commentKey(ref, id), &entity); err != nil {
		return nil, translate(err)
	}
	comment := decodeComment(id, &entity)
	if err := comment.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored comment %q: %v", tissues.ErrInternal, id, err)
	}
	return comment, nil
}
func (t *transaction) GetLastComment(ctx context.Context, ref tissues.IssueRef) (*tissues.Comment, error) {
	var entities []commentEntity
	query := gcds.NewQuery(CommentKind).Namespace(t.store.namespace).Ancestor(t.store.issueKey(ref)).Order("-Created").Order("-__key__").Limit(1).Transaction(t.tx)
	keys, err := t.store.client.GetAll(ctx, query, &entities)
	if err != nil {
		return nil, translate(err)
	}
	if len(keys) == 0 {
		return nil, tissues.ErrNotFound
	}
	if keys[0].Parent == nil || keys[0].Parent.Name != ref.String() || !directChildOf(keys[0].Parent, ProjectKind, ref.ProjectKey) {
		return nil, fmt.Errorf("%w: comment %q has malformed ancestry", tissues.ErrInternal, keys[0].Name)
	}
	comment := decodeComment(keys[0].Name, &entities[0])
	if err := comment.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored comment %q: %v", tissues.ErrInternal, keys[0].Name, err)
	}
	return comment, nil
}
func (t *transaction) PutIssue(_ context.Context, issue *tissues.Issue) error {
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	ref, _ := tissues.ParseIssueRef(issue.Ref)
	_, err := t.tx.Put(t.store.issueKey(ref), encodeIssue(issue))
	return translate(err)
}
func (t *transaction) PutComment(_ context.Context, ref tissues.IssueRef, comment *tissues.Comment) error {
	if err := comment.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.commentKey(ref, comment.ID), encodeComment(comment))
	return translate(err)
}

func encodeProject(p *tissues.Project) *projectEntity {
	return &projectEntity{Created: p.Created.UnixNano(), NextIssueNumber: p.NextIssueNumber}
}
func decodeProject(key string, e *projectEntity) *tissues.Project {
	return &tissues.Project{Key: key, Created: time.Unix(0, e.Created).UTC(), NextIssueNumber: e.NextIssueNumber}
}
func encodeIssue(i *tissues.Issue) *issueEntity {
	return &issueEntity{Number: i.Number, Title: i.Title, State: string(i.State), Created: i.Created.UnixNano(), Updated: i.Updated.UnixNano(), Description: i.Description, ParentRef: i.ParentRef}
}
func decodeIssue(ref tissues.IssueRef, e *issueEntity) *tissues.Issue {
	return &tissues.Issue{ProjectKey: ref.ProjectKey, Number: e.Number, Ref: ref.String(), Title: e.Title, State: tissues.State(e.State), Created: time.Unix(0, e.Created).UTC(), Updated: time.Unix(0, e.Updated).UTC(), Description: e.Description, ParentRef: e.ParentRef}
}
func encodeComment(c *tissues.Comment) *commentEntity {
	return &commentEntity{Author: c.Author, Created: c.Created.UnixNano(), Updated: c.Updated.UnixNano(), Body: c.Body}
}
func decodeComment(id string, e *commentEntity) *tissues.Comment {
	return &tissues.Comment{ID: id, Author: e.Author, Created: time.Unix(0, e.Created).UTC(), Updated: time.Unix(0, e.Updated).UTC(), Body: e.Body}
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gcds.ErrNoSuchEntity) {
		return tissues.ErrNotFound
	}
	if errors.Is(err, gcds.ErrConcurrentTransaction) {
		return tissues.ErrConflict
	}
	for _, known := range []error{tissues.ErrNotFound, tissues.ErrInvalid, tissues.ErrConflict, tissues.ErrInternal} {
		if errors.Is(err, known) {
			return err
		}
	}
	return tissues.ErrInternal
}
func directChildOf(key *gcds.Key, parentKind, parentName string) bool {
	return key != nil && key.Parent != nil && key.Parent.Kind == parentKind && key.Parent.Name == parentName && key.Parent.Parent == nil
}
func validateAcyclic(flat map[string]*tissues.Issue) error {
	for ref := range flat {
		seen := map[string]bool{}
		for current := ref; current != ""; {
			issue := flat[current]
			if issue == nil {
				return fmt.Errorf("%w: missing issue %q in hierarchy", tissues.ErrInternal, current)
			}
			if seen[current] {
				return fmt.Errorf("%w: stored hierarchy cycle at issue %q", tissues.ErrInternal, current)
			}
			seen[current] = true
			current = issue.ParentRef
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
