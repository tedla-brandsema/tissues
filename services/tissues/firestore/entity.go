package firestore

import (
	"fmt"
	"strings"

	gcfirestore "cloud.google.com/go/firestore"
	"github.com/tedla-brandsema/tissues/services/tissues"
)

type projectEntity struct {
	Key             string `firestore:"key"`
	CreatedNS       int64  `firestore:"created_ns"`
	NextIssueNumber int64  `firestore:"next_issue_number"`
}

type issueEntity struct {
	ProjectKey           string `firestore:"project_key"`
	Number               int64  `firestore:"number"`
	Ref                  string `firestore:"ref"`
	Title                string `firestore:"title"`
	State                string `firestore:"state"`
	CreatedNS            int64  `firestore:"created_ns"`
	UpdatedNS            int64  `firestore:"updated_ns"`
	Description          string `firestore:"description"`
	ParentRef            string `firestore:"parent_ref"`
	CommentOrderRevision int64  `firestore:"comment_order_revision"`
}

type commentEntity struct {
	ProjectKey string `firestore:"project_key"`
	IssueRef   string `firestore:"issue_ref"`
	CommentID  string `firestore:"comment_id"`
	Author     string `firestore:"author"`
	CreatedNS  int64  `firestore:"created_ns"`
	UpdatedNS  int64  `firestore:"updated_ns"`
	Body       string `firestore:"body"`
}

func encodeProject(project *tissues.Project) projectEntity {
	return projectEntity{Key: project.Key, CreatedNS: project.Created.UnixNano(), NextIssueNumber: project.NextIssueNumber}
}

func decodeProject(documentID string, entity projectEntity) (*tissues.Project, error) {
	if !validDocumentID(documentID) || entity.Key != documentID {
		return nil, fmt.Errorf("%w: Project document identity mismatch", tissues.ErrInternal)
	}
	project := &tissues.Project{Key: entity.Key, Created: unixNano(entity.CreatedNS), NextIssueNumber: entity.NextIssueNumber}
	if err := project.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid stored Project %q: %v", tissues.ErrInternal, documentID, err)
	}
	return project, nil
}

func decodeProjectSnapshot(doc *gcfirestore.DocumentSnapshot) (*tissues.Project, error) {
	var entity projectEntity
	if err := doc.DataTo(&entity); err != nil {
		return nil, fmt.Errorf("%w: decode Project", tissues.ErrInternal)
	}
	return decodeProject(doc.Ref.ID, entity)
}

func encodeIssue(issue *tissues.Issue, revision int64) issueEntity {
	return issueEntity{ProjectKey: issue.ProjectKey, Number: issue.Number, Ref: issue.Ref, Title: issue.Title, State: string(issue.State), CreatedNS: issue.Created.UnixNano(), UpdatedNS: issue.Updated.UnixNano(), Description: issue.Description, ParentRef: issue.ParentRef, CommentOrderRevision: revision}
}

func decodeIssue(documentID string, entity issueEntity) (*tissues.Issue, int64, error) {
	ref, err := tissues.ParseIssueRef(documentID)
	if err != nil || ref.String() != documentID || entity.Ref != documentID || entity.ProjectKey != ref.ProjectKey || entity.Number != ref.Number {
		return nil, 0, fmt.Errorf("%w: Issue document identity mismatch", tissues.ErrInternal)
	}
	if entity.CommentOrderRevision < 0 {
		return nil, 0, fmt.Errorf("%w: invalid stored Issue revision", tissues.ErrInternal)
	}
	issue := &tissues.Issue{ProjectKey: entity.ProjectKey, Number: entity.Number, Ref: entity.Ref, Title: entity.Title, State: tissues.State(entity.State), Created: unixNano(entity.CreatedNS), Updated: unixNano(entity.UpdatedNS), Description: entity.Description, ParentRef: entity.ParentRef}
	if err := issue.Validate(); err != nil {
		return nil, 0, fmt.Errorf("%w: invalid stored Issue %q: %v", tissues.ErrInternal, documentID, err)
	}
	return issue, entity.CommentOrderRevision, nil
}

func decodeIssueSnapshot(doc *gcfirestore.DocumentSnapshot) (*tissues.Issue, int64, error) {
	var entity issueEntity
	if err := doc.DataTo(&entity); err != nil {
		return nil, 0, fmt.Errorf("%w: decode Issue", tissues.ErrInternal)
	}
	return decodeIssue(doc.Ref.ID, entity)
}

func commentDocumentID(ref tissues.IssueRef, id string) string {
	return ref.String() + "~" + id
}

func parseCommentDocumentID(documentID string) (tissues.IssueRef, string, error) {
	if strings.Count(documentID, "~") != 1 {
		return tissues.IssueRef{}, "", fmt.Errorf("invalid compound Comment document ID")
	}
	parts := strings.SplitN(documentID, "~", 2)
	ref, err := tissues.ParseIssueRef(parts[0])
	if err != nil || ref.String() != parts[0] || !tissues.ValidID(parts[1]) {
		return tissues.IssueRef{}, "", fmt.Errorf("invalid compound Comment document ID")
	}
	return ref, parts[1], nil
}

func encodeComment(ref tissues.IssueRef, comment *tissues.Comment) commentEntity {
	return commentEntity{ProjectKey: ref.ProjectKey, IssueRef: ref.String(), CommentID: comment.ID, Author: comment.Author, CreatedNS: comment.Created.UnixNano(), UpdatedNS: comment.Updated.UnixNano(), Body: comment.Body}
}

func decodeComment(documentID string, entity commentEntity) (tissues.IssueRef, *tissues.Comment, error) {
	ref, id, err := parseCommentDocumentID(documentID)
	if err != nil || entity.ProjectKey != ref.ProjectKey || entity.IssueRef != ref.String() || entity.CommentID != id {
		return tissues.IssueRef{}, nil, fmt.Errorf("%w: Comment document identity mismatch", tissues.ErrInternal)
	}
	comment := &tissues.Comment{ID: entity.CommentID, Author: entity.Author, Created: unixNano(entity.CreatedNS), Updated: unixNano(entity.UpdatedNS), Body: entity.Body}
	if err := comment.Validate(); err != nil {
		return tissues.IssueRef{}, nil, fmt.Errorf("%w: invalid stored Comment %q: %v", tissues.ErrInternal, documentID, err)
	}
	return ref, comment, nil
}

func decodeCommentSnapshot(doc *gcfirestore.DocumentSnapshot) (tissues.IssueRef, *tissues.Comment, error) {
	var entity commentEntity
	if err := doc.DataTo(&entity); err != nil {
		return tissues.IssueRef{}, nil, fmt.Errorf("%w: decode Comment", tissues.ErrInternal)
	}
	return decodeComment(doc.Ref.ID, entity)
}
