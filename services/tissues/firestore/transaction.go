package firestore

import (
	"context"
	"errors"
	"fmt"

	gcfirestore "cloud.google.com/go/firestore"
	"github.com/tedla-brandsema/tissues/services/tissues"
	"google.golang.org/api/iterator"
)

type readState struct {
	exists bool
}

type issueReadState struct {
	exists   bool
	revision int64
}

type transaction struct {
	store         *TenantStore
	tx            *gcfirestore.Transaction
	projectsRead  map[string]readState
	issuesRead    map[string]issueReadState
	commentsRead  map[string]readState
	writesStarted bool
}

func (t *transaction) requireReadPhase() error {
	if t.writesStarted {
		return fmt.Errorf("%w: Firestore transaction reads must precede writes", tissues.ErrInternal)
	}
	return nil
}

func (t *transaction) beginWrite() { t.writesStarted = true }

func (t *transaction) GetProject(_ context.Context, key string) (*tissues.Project, error) {
	if err := t.requireReadPhase(); err != nil {
		return nil, err
	}
	canonical, err := canonicalProject(key)
	if err != nil {
		return nil, err
	}
	doc, err := t.tx.Get(t.store.projectRef(canonical))
	if err != nil {
		if errors.Is(translate(err), tissues.ErrNotFound) {
			t.projectsRead[canonical] = readState{}
		}
		return nil, translate(err)
	}
	project, err := decodeProjectSnapshot(doc)
	if err != nil {
		return nil, err
	}
	t.projectsRead[canonical] = readState{exists: true}
	return project, nil
}

func (t *transaction) PutProject(_ context.Context, project *tissues.Project) error {
	if project == nil || project.Validate() != nil {
		return fmt.Errorf("%w: invalid Project", tissues.ErrInvalid)
	}
	state, ok := t.projectsRead[project.Key]
	if !ok {
		return fmt.Errorf("%w: Project must be read before write", tissues.ErrInternal)
	}
	t.beginWrite()
	if state.exists {
		return translate(t.tx.Set(t.store.projectRef(project.Key), encodeProject(project)))
	}
	return translate(t.tx.Create(t.store.projectRef(project.Key), encodeProject(project)))
}

func (t *transaction) GetIssue(_ context.Context, ref tissues.IssueRef) (*tissues.Issue, error) {
	if err := t.requireReadPhase(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid Issue reference", tissues.ErrInvalid)
	}
	doc, err := t.tx.Get(t.store.issueRef(ref))
	if err != nil {
		if errors.Is(translate(err), tissues.ErrNotFound) {
			t.issuesRead[ref.String()] = issueReadState{}
		}
		return nil, translate(err)
	}
	issue, revision, err := decodeIssueSnapshot(doc)
	if err != nil {
		return nil, err
	}
	t.issuesRead[ref.String()] = issueReadState{exists: true, revision: revision}
	return issue, nil
}

func (t *transaction) PutIssue(_ context.Context, issue *tissues.Issue) error {
	if issue == nil || issue.Validate() != nil {
		return fmt.Errorf("%w: invalid Issue", tissues.ErrInvalid)
	}
	state, ok := t.issuesRead[issue.Ref]
	if !ok {
		return fmt.Errorf("%w: Issue must be read before write", tissues.ErrInternal)
	}
	revision, err := issueWriteRevision(state)
	if err != nil {
		return err
	}
	t.beginWrite()
	entity := encodeIssue(issue, revision)
	if state.exists {
		return translate(t.tx.Set(t.store.issueRef(tissues.IssueRef{ProjectKey: issue.ProjectKey, Number: issue.Number}), entity))
	}
	return translate(t.tx.Create(t.store.issueRef(tissues.IssueRef{ProjectKey: issue.ProjectKey, Number: issue.Number}), entity))
}

func issueWriteRevision(state issueReadState) (int64, error) {
	if !state.exists {
		return 0, nil
	}
	return checkedRevision(state.revision)
}

func (t *transaction) GetComment(_ context.Context, ref tissues.IssueRef, id string) (*tissues.Comment, error) {
	if err := t.requireReadPhase(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil || !tissues.ValidID(id) {
		return nil, fmt.Errorf("%w: invalid Comment identity", tissues.ErrInvalid)
	}
	documentID := commentDocumentID(ref, id)
	doc, err := t.tx.Get(t.store.commentRef(ref, id))
	if err != nil {
		if errors.Is(translate(err), tissues.ErrNotFound) {
			t.commentsRead[documentID] = readState{}
		}
		return nil, translate(err)
	}
	storedRef, comment, err := decodeCommentSnapshot(doc)
	if err != nil {
		return nil, err
	}
	if storedRef != ref || comment.ID != id {
		return nil, fmt.Errorf("%w: Comment lookup identity mismatch", tissues.ErrInternal)
	}
	t.commentsRead[documentID] = readState{exists: true}
	return comment, nil
}

func (t *transaction) GetLastComment(_ context.Context, ref tissues.IssueRef) (*tissues.Comment, error) {
	if err := t.requireReadPhase(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid Issue reference", tissues.ErrInvalid)
	}
	query := t.store.comments().Where("issue_ref", "==", ref.String()).OrderBy("created_ns", gcfirestore.Desc).OrderBy("comment_id", gcfirestore.Desc).Limit(1)
	iter := t.tx.Documents(query)
	defer iter.Stop()
	doc, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, tissues.ErrNotFound
	}
	if err != nil {
		return nil, translate(err)
	}
	storedRef, comment, err := decodeCommentSnapshot(doc)
	if err != nil {
		return nil, err
	}
	if storedRef != ref {
		return nil, fmt.Errorf("%w: latest Comment escaped Issue query", tissues.ErrInternal)
	}
	return comment, nil
}

func (t *transaction) PutComment(_ context.Context, ref tissues.IssueRef, comment *tissues.Comment) error {
	if err := ref.Validate(); err != nil || comment == nil || comment.Validate() != nil {
		return fmt.Errorf("%w: invalid Comment", tissues.ErrInvalid)
	}
	documentID := commentDocumentID(ref, comment.ID)
	state, ok := t.commentsRead[documentID]
	if !ok {
		return fmt.Errorf("%w: Comment must be read before write", tissues.ErrInternal)
	}
	t.beginWrite()
	if state.exists {
		return translate(t.tx.Set(t.store.commentRef(ref, comment.ID), encodeComment(ref, comment)))
	}
	return translate(t.tx.Create(t.store.commentRef(ref, comment.ID), encodeComment(ref, comment)))
}

var _ tissues.Transaction = (*transaction)(nil)
