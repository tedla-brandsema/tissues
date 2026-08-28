package tissues

import (
	"context"
	"time"
)

type PageRequest struct {
	Size       int
	Cursor     string
	ProjectKey string
}

type ProjectPage struct {
	Projects   []*Project
	NextCursor string
}

type IssueOverview struct {
	ProjectKey string
	Number     int64
	Ref        string
	Title      string
	State      State
	ParentRef  string
	Updated    time.Time
}

type IssueOverviewPage struct {
	Issues     []*IssueOverview
	NextCursor string
}

// Repository is the persistence boundary required by tissues today.
type Repository interface {
	ListProjectsPage(context.Context, PageRequest) (*ProjectPage, error)
	ListIssueOverviewsPage(context.Context, PageRequest) (*IssueOverviewPage, error)
	GetProject(context.Context, string) (*Project, error)
	ListIssues(context.Context, string) ([]*Issue, error)
	ResolveIssue(context.Context, IssueRef) (*Issue, error)
	RunInTransaction(context.Context, func(Transaction) error) error
}

// Transaction exposes only tissues reads and writes. Implementations may
// retry the callback, so callers must prepare IDs, timestamps, and side-effect
// free logical inputs before invoking RunInTransaction.
type Transaction interface {
	GetProject(context.Context, string) (*Project, error)
	PutProject(context.Context, *Project) error
	GetIssue(context.Context, string, string) (*Issue, error)
	ResolveIssue(context.Context, IssueRef) (*Issue, error)
	PutIssueRef(context.Context, IssueRef, string) error
	GetComment(context.Context, string, string, string) (*Comment, error)
	ListComments(context.Context, string, string) ([]*Comment, error)
	PutIssue(context.Context, *Issue) error
	PutComment(context.Context, string, string, *Comment) error
}
