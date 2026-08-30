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

// Repository is the root persistence boundary. Domain operations become
// available only after binding an immutable TenantID.
type Repository interface {
	ForTenant(TenantID) (TenantRepository, error)
}

// TenantRepository is the persistence boundary for one immutable tenant.
// It has no operation that can escape to or select another tenant.
type TenantRepository interface {
	ListProjectsPage(context.Context, PageRequest) (*ProjectPage, error)
	ListIssueOverviewsPage(context.Context, PageRequest) (*IssueOverviewPage, error)
	GetProject(context.Context, string) (*Project, error)
	ListIssues(context.Context, string) ([]*Issue, error)
	GetIssue(context.Context, IssueRef) (*Issue, error)
	RunInTransaction(context.Context, func(Transaction) error) error
}

// Transaction exposes only tissues reads and writes. Implementations may
// retry the callback, so callers must prepare IDs, timestamps, and side-effect
// free logical inputs before invoking RunInTransaction.
type Transaction interface {
	GetProject(context.Context, string) (*Project, error)
	PutProject(context.Context, *Project) error
	GetIssue(context.Context, IssueRef) (*Issue, error)
	// PutIssue is also the intentional per-Issue serialization fence used by
	// AddComment. Persistence implementations must not elide that call.
	PutIssue(context.Context, *Issue) error
	GetComment(context.Context, IssueRef, string) (*Comment, error)
	GetLastComment(context.Context, IssueRef) (*Comment, error)
	PutComment(context.Context, IssueRef, *Comment) error
}
