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
)

const (
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

type issueEntity struct {
	Title       string `datastore:"Title"`
	State       string `datastore:"State"`
	Created     int64  `datastore:"Created"`
	Updated     int64  `datastore:"Updated"`
	Description string `datastore:"Description,noindex"`
	ParentID    string `datastore:"ParentID"`
}

type commentEntity struct {
	Author  string `datastore:"Author"`
	Created int64  `datastore:"Created"`
	Updated int64  `datastore:"Updated"`
	Body    string `datastore:"Body,noindex"`
}

func (s *Store) issueKey(id string) *gcds.Key {
	key := gcds.NameKey(IssueKind, id, nil)
	key.Namespace = s.namespace
	return key
}
func (s *Store) commentKey(issueID, id string) *gcds.Key {
	key := gcds.NameKey(CommentKind, id, s.issueKey(issueID))
	key.Namespace = s.namespace
	return key
}

func (s *Store) ListIssues(ctx context.Context) ([]*tissues.Issue, error) {
	var issueEntities []issueEntity
	issueKeys, err := s.client.GetAll(ctx, gcds.NewQuery(IssueKind).Namespace(s.namespace), &issueEntities)
	if err != nil {
		return nil, translate(err)
	}
	flat := make(map[string]*tissues.Issue, len(issueKeys))
	for i, key := range issueKeys {
		issue := decodeIssue(key.Name, &issueEntities[i])
		if err := issue.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, key.Name, err)
		}
		flat[issue.ID] = issue
	}
	var commentEntities []commentEntity
	commentKeys, err := s.client.GetAll(ctx, gcds.NewQuery(CommentKind).Namespace(s.namespace), &commentEntities)
	if err != nil {
		return nil, translate(err)
	}
	for i, key := range commentKeys {
		if key.Parent == nil {
			return nil, fmt.Errorf("%w: comment %q has no Issue ancestor", tissues.ErrInternal, key.Name)
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
			return nil, fmt.Errorf("%w: issue %q has missing parent %q", tissues.ErrInternal, issue.ID, issue.ParentID)
		}
		parent.Children = append(parent.Children, issue)
	}
	if err := validateAcyclic(flat); err != nil {
		return nil, err
	}
	sortIssueTree(roots)
	return roots, nil
}

func (s *Store) GetIssue(ctx context.Context, id string) (*tissues.Issue, error) {
	var ent issueEntity
	if err := s.client.Get(ctx, s.issueKey(id), &ent); err != nil {
		return nil, translate(err)
	}
	if err := decodeIssue(id, &ent).Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, id, err)
	}
	roots, err := s.ListIssues(ctx)
	if err != nil {
		return nil, err
	}
	if issue := findIssue(roots, id); issue != nil {
		return issue, nil
	}
	return nil, fmt.Errorf("%w: point-read issue %q missing from list", tissues.ErrInternal, id)
}

func (s *Store) RunInTransaction(ctx context.Context, fn func(tissues.Transaction) error) error {
	_, err := s.client.RunInTransaction(ctx, func(tx *gcds.Transaction) error { return fn(&transaction{store: s, tx: tx}) })
	return translate(err)
}

type transaction struct {
	store *Store
	tx    *gcds.Transaction
}

func (t *transaction) GetIssue(_ context.Context, id string) (*tissues.Issue, error) {
	var ent issueEntity
	if err := t.tx.Get(t.store.issueKey(id), &ent); err != nil {
		return nil, translate(err)
	}
	issue := decodeIssue(id, &ent)
	if err := issue.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored issue %q: %v", tissues.ErrInternal, id, err)
	}
	return issue, nil
}
func (t *transaction) GetComment(_ context.Context, issueID, id string) (*tissues.Comment, error) {
	var ent commentEntity
	if err := t.tx.Get(t.store.commentKey(issueID, id), &ent); err != nil {
		return nil, translate(err)
	}
	comment := decodeComment(id, &ent)
	if err := comment.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored comment %q: %v", tissues.ErrInternal, id, err)
	}
	return comment, nil
}
func (t *transaction) ListComments(ctx context.Context, issueID string) ([]*tissues.Comment, error) {
	// The Datastore Go client does not expose transactional queries. This
	// ancestor query is side-effect free and callback-retry safe; the Issue
	// point read above remains the transaction's integrity boundary.
	var entities []commentEntity
	keys, err := t.store.client.GetAll(ctx, gcds.NewQuery(CommentKind).Namespace(t.store.namespace).Ancestor(t.store.issueKey(issueID)), &entities)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]*tissues.Comment, len(keys))
	for i, key := range keys {
		out[i] = decodeComment(key.Name, &entities[i])
	}
	tissues.SortComments(out)
	return out, nil
}
func (t *transaction) PutIssue(_ context.Context, issue *tissues.Issue) error {
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.issueKey(issue.ID), encodeIssue(issue))
	return translate(err)
}
func (t *transaction) PutComment(_ context.Context, issueID string, comment *tissues.Comment) error {
	if err := comment.Validate(); err != nil {
		return fmt.Errorf("%w: %v", tissues.ErrInvalid, err)
	}
	_, err := t.tx.Put(t.store.commentKey(issueID, comment.ID), encodeComment(comment))
	return translate(err)
}

func encodeIssue(i *tissues.Issue) *issueEntity {
	return &issueEntity{Title: i.Title, State: string(i.State), Created: i.Created.UnixNano(), Updated: i.Updated.UnixNano(), Description: i.Description, ParentID: i.ParentID}
}
func decodeIssue(id string, e *issueEntity) *tissues.Issue {
	return &tissues.Issue{ID: id, Title: e.Title, State: tissues.State(e.State), Created: time.Unix(0, e.Created).UTC(), Updated: time.Unix(0, e.Updated).UTC(), Description: e.Description, ParentID: e.ParentID}
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
func validateAcyclic(flat map[string]*tissues.Issue) error {
	for id := range flat {
		seen := map[string]bool{}
		for current := id; current != ""; current = flat[current].ParentID {
			if seen[current] {
				return fmt.Errorf("%w: stored hierarchy cycle at issue %q", tissues.ErrInternal, current)
			}
			seen[current] = true
		}
	}
	return nil
}
func sortIssueTree(issues []*tissues.Issue) {
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
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
