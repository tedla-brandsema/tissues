package tissues

import "context"

// Repository is the persistence boundary required by tissues today.
type Repository interface {
	ListIssues(context.Context) ([]*Issue, error)
	GetIssue(context.Context, string) (*Issue, error)
	RunInTransaction(context.Context, func(Transaction) error) error
}

// Transaction exposes only tissues reads and writes. Implementations may
// retry the callback, so callers must prepare IDs, timestamps, and side-effect
// free logical inputs before invoking RunInTransaction.
type Transaction interface {
	GetIssue(context.Context, string) (*Issue, error)
	GetComment(context.Context, string, string) (*Comment, error)
	ListComments(context.Context, string) ([]*Comment, error)
	PutIssue(context.Context, *Issue) error
	PutComment(context.Context, string, *Comment) error
}
