// Package datastore implements tissues persistence on Cloud Datastore.
package datastore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gcds "cloud.google.com/go/datastore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
)

const (
	ProjectKind  = "tissues_project"
	IssueKind    = "tissues_issue"
	IssueRefKind = "tissues_issue_ref"
	CommentKind  = "tissues_comment"
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
	ParentID    string `datastore:"ParentID"`
}
type issueRefEntity struct {
	IssueID string `datastore:"IssueID"`
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
func (s *Store) issueKey(projectKey, id string) *gcds.Key {
	key := gcds.NameKey(IssueKind, id, s.projectKey(projectKey))
	key.Namespace = s.namespace
	return key
}
func (s *Store) issueRefKey(ref tissues.IssueRef) *gcds.Key {
	key := gcds.NameKey(IssueRefKind, strconv.FormatInt(ref.Number, 10), s.projectKey(ref.ProjectKey))
	key.Namespace = s.namespace
	return key
}
func (s *Store) commentKey(projectKey, issueID, id string) *gcds.Key {
	key := gcds.NameKey(CommentKind, id, s.issueKey(projectKey, issueID))
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
		issue := decodeIssue(projectKey, key.Name, &entities[i])
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
		if issue.ParentID != "" {
			parentKeys = append(parentKeys, s.issueKey(issue.ProjectKey, issue.ParentID))
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
			parent := decodeIssue(pageIssue.ProjectKey, parentKeys[i].Name, &entity)
			if err := parent.Validate(); err != nil {
				return nil, fmt.Errorf("%w: issue overview contains an invalid parent", tissues.ErrInternal)
			}
			pageIssue.ParentRef = parent.Ref
		}
	}

	validationIssues := append([]*tissues.Issue{}, issuesOnPage...)
	for i, entity := range parentEntities {
		projectKey := issuesOnPage[parentIndexes[i]].ProjectKey
		validationIssues = append(validationIssues, decodeIssue(projectKey, parentKeys[i].Name, &entity))
	}
	refKeys := make([]*gcds.Key, len(validationIssues))
	refEntities := make([]issueRefEntity, len(validationIssues))
	for i, issue := range validationIssues {
		refKeys[i] = s.issueRefKey(tissues.IssueRef{ProjectKey: issue.ProjectKey, Number: issue.Number})
	}
	if len(refKeys) > 0 {
		if err := s.client.GetMulti(ctx, refKeys, refEntities); err != nil {
			return nil, fmt.Errorf("%w: issue overview contains a missing reference index", tissues.ErrInternal)
		}
		for i, index := range refEntities {
			if index.IssueID != validationIssues[i].ID {
				return nil, fmt.Errorf("%w: issue overview reference index disagrees with target issue", tissues.ErrInternal)
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
		issue := decodeIssue(projectKey, key.Name, &issueEntities[i])
		if err := issue.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, key.Name, err)
		}
		if flat[issue.ID] != nil || byNumber[issue.Number] != nil {
			return nil, fmt.Errorf("%w: duplicate issue identity or number in project %s", tissues.ErrInternal, projectKey)
		}
		flat[issue.ID], byNumber[issue.Number] = issue, issue
	}

	var refEntities []issueRefEntity
	refKeys, err := s.client.GetAll(ctx, gcds.NewQuery(IssueRefKind).Namespace(s.namespace).Ancestor(ancestor), &refEntities)
	if err != nil {
		return nil, translate(err)
	}
	seenRefs := make(map[int64]bool, len(refKeys))
	for i, key := range refKeys {
		if !directChildOf(key, ProjectKind, projectKey) {
			return nil, fmt.Errorf("%w: issue reference %q has malformed Project ancestry", tissues.ErrInternal, key.Name)
		}
		ref, parseErr := tissues.ParseIssueRef(projectKey + "-" + key.Name)
		if parseErr != nil || seenRefs[ref.Number] {
			return nil, fmt.Errorf("%w: malformed or duplicate issue reference index %q", tissues.ErrInternal, key.Name)
		}
		issue := flat[refEntities[i].IssueID]
		if issue == nil || issue.Number != ref.Number {
			return nil, fmt.Errorf("%w: issue reference %s points to missing or mismatched issue", tissues.ErrInternal, ref)
		}
		seenRefs[ref.Number] = true
	}
	for number := range byNumber {
		if !seenRefs[number] {
			return nil, fmt.Errorf("%w: issue %s-%d has no reference index", tissues.ErrInternal, projectKey, number)
		}
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
		if issue.ParentID == "" {
			roots = append(roots, issue)
			continue
		}
		parent := flat[issue.ParentID]
		if parent == nil {
			return nil, fmt.Errorf("%w: issue %q has missing parent %q inside project %s", tissues.ErrInternal, issue.ID, issue.ParentID, projectKey)
		}
		issue.ParentRef = parent.Ref
		parent.Children = append(parent.Children, issue)
	}
	if err := validateAcyclic(flat); err != nil {
		return nil, err
	}
	sortIssueTree(roots)
	return roots, nil
}

func (s *Store) ResolveIssue(ctx context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	var index issueRefEntity
	if err := s.client.Get(ctx, s.issueRefKey(ref), &index); err != nil {
		return nil, translate(err)
	}
	var entity issueEntity
	if err := s.client.Get(ctx, s.issueKey(ref.ProjectKey, index.IssueID), &entity); err != nil {
		if errors.Is(err, gcds.ErrNoSuchEntity) {
			return nil, fmt.Errorf("%w: issue reference %s maps to a missing issue", tissues.ErrInternal, ref)
		}
		return nil, translate(err)
	}
	if point := decodeIssue(ref.ProjectKey, index.IssueID, &entity); point.Number != ref.Number {
		return nil, fmt.Errorf("%w: issue reference %s disagrees with target issue", tissues.ErrInternal, ref)
	}
	roots, err := s.ListIssues(ctx, ref.ProjectKey)
	if err != nil {
		return nil, err
	}
	if issue := findIssue(roots, index.IssueID); issue != nil {
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
func (t *transaction) GetIssue(_ context.Context, projectKey, id string) (*tissues.Issue, error) {
	var entity issueEntity
	if err := t.tx.Get(t.store.issueKey(projectKey, id), &entity); err != nil {
		return nil, translate(err)
	}
	issue := decodeIssue(projectKey, id, &entity)
	if err := issue.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, id, err)
	}
	return issue, nil
}
func (t *transaction) ResolveIssue(ctx context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	var index issueRefEntity
	if err := t.tx.Get(t.store.issueRefKey(ref), &index); err != nil {
		return nil, translate(err)
	}
	issue, err := t.GetIssue(ctx, ref.ProjectKey, index.IssueID)
	if err != nil {
		if errors.Is(err, tissues.ErrNotFound) {
			return nil, fmt.Errorf("%w: issue reference %s maps to a missing issue", tissues.ErrInternal, ref)
		}
		return nil, err
	}
	if issue.Number != ref.Number {
		return nil, fmt.Errorf("%w: issue reference %s disagrees with target issue", tissues.ErrInternal, ref)
	}
	if issue.ParentID != "" {
		parent, err := t.GetIssue(ctx, ref.ProjectKey, issue.ParentID)
		if err != nil {
			if errors.Is(err, tissues.ErrNotFound) {
				return nil, fmt.Errorf("%w: issue %s has a missing parent", tissues.ErrInternal, ref)
			}
			return nil, err
		}
		issue.ParentRef = parent.Ref
	}
	return issue, nil
}
func (t *transaction) PutIssueRef(_ context.Context, ref tissues.IssueRef, issueID string) error {
	if err := ref.Validate(); err != nil || !tissues.ValidID(issueID) {
		return fmt.Errorf("%w: invalid issue reference mapping", tissues.ErrInvalid)
	}
	_, err := t.tx.Put(t.store.issueRefKey(ref), &issueRefEntity{IssueID: issueID})
	return translate(err)
}
func (t *transaction) GetComment(_ context.Context, projectKey, issueID, id string) (*tissues.Comment, error) {
	var entity commentEntity
	if err := t.tx.Get(t.store.commentKey(projectKey, issueID, id), &entity); err != nil {
		return nil, translate(err)
	}
	comment := decodeComment(id, &entity)
	if err := comment.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored comment %q: %v", tissues.ErrInternal, id, err)
	}
	return comment, nil
}
func (t *transaction) ListComments(ctx context.Context, projectKey, issueID string) ([]*tissues.Comment, error) {
	var entities []commentEntity
	keys, err := t.store.client.GetAll(ctx, gcds.NewQuery(CommentKind).Namespace(t.store.namespace).Ancestor(t.store.issueKey(projectKey, issueID)), &entities)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*tissues.Comment, len(keys))
	for i, key := range keys {
		if key.Parent == nil || key.Parent.Name != issueID || !directChildOf(key.Parent, ProjectKind, projectKey) {
			return nil, fmt.Errorf("%w: comment %q has malformed ancestry", tissues.ErrInternal, key.Name)
		}
		out[i] = decodeComment(key.Name, &entities[i])
	}
	tissues.SortComments(out)
	return out, nil
}
func (t *transaction) PutIssue(_ context.Context, issue *tissues.Issue) error {
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.issueKey(issue.ProjectKey, issue.ID), encodeIssue(issue))
	return translate(err)
}
func (t *transaction) PutComment(_ context.Context, projectKey, issueID string, comment *tissues.Comment) error {
	if err := comment.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.commentKey(projectKey, issueID, comment.ID), encodeComment(comment))
	return translate(err)
}

func encodeProject(p *tissues.Project) *projectEntity {
	return &projectEntity{Created: p.Created.UnixNano(), NextIssueNumber: p.NextIssueNumber}
}
func decodeProject(key string, e *projectEntity) *tissues.Project {
	return &tissues.Project{Key: key, Created: time.Unix(0, e.Created).UTC(), NextIssueNumber: e.NextIssueNumber}
}
func encodeIssue(i *tissues.Issue) *issueEntity {
	return &issueEntity{Number: i.Number, Title: i.Title, State: string(i.State), Created: i.Created.UnixNano(), Updated: i.Updated.UnixNano(), Description: i.Description, ParentID: i.ParentID}
}
func decodeIssue(projectKey, id string, e *issueEntity) *tissues.Issue {
	ref := tissues.IssueRef{ProjectKey: projectKey, Number: e.Number}
	return &tissues.Issue{ID: id, ProjectKey: projectKey, Number: e.Number, Ref: ref.String(), Title: e.Title, State: tissues.State(e.State), Created: time.Unix(0, e.Created).UTC(), Updated: time.Unix(0, e.Updated).UTC(), Description: e.Description, ParentID: e.ParentID}
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
	for id := range flat {
		seen := map[string]bool{}
		for current := id; current != ""; {
			issue := flat[current]
			if issue == nil {
				return fmt.Errorf("%w: missing issue %q in hierarchy", tissues.ErrInternal, current)
			}
			if seen[current] {
				return fmt.Errorf("%w: stored hierarchy cycle at issue %q", tissues.ErrInternal, current)
			}
			seen[current] = true
			current = issue.ParentID
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
func findIssue(issues []*tissues.Issue, id string) *tissues.Issue {
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
