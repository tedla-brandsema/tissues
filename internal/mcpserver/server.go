// Package mcpserver is a thin MCP transport adapter over service.Service.
// It owns MCP representations and tool descriptions, but no issue or comment
// semantics.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/service"
)

const instructions = "tissues is a shared Git-backed issue tracker used by humans and agents. These tools operate on the same issues humans see through REST and Markdown. Issues may recursively contain child issues; open and closed are the only lifecycle states, and comments are shared discussion. There are no claims, queues, assignments, or exclusive agent ownership semantics."

// New returns the stateless Streamable HTTP MCP endpoint for s.
func New(s *service.Service) http.Handler {
	server := newServer(s)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

func newServer(s *service.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "tissues", Version: "v0"}, &mcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcp.ServerCapabilities{},
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "list_issues",
		Description:  "List the complete tissues issue hierarchy shared by humans and agents, including children and comments. This is the shared tracker, not an agent task queue.",
		OutputSchema: issueListSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, issueListOutput, error) {
		issues, err := s.ListIssues(ctx)
		if err != nil {
			return nil, issueListOutput{}, err
		}
		return nil, issueListOutput{Issues: toIssues(issues)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "get_issue",
		Description:  "Get a tissues issue by ID, including its parent ID, state, complete child hierarchy, and comments.",
		OutputSchema: issueSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIDInput) (*mcp.CallToolResult, issueOutput, error) {
		issue, err := s.GetIssue(ctx, in.ID)
		if err != nil {
			return nil, issueOutput{}, err
		}
		return nil, toIssue(issue), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "create_issue",
		Description:  "Create a shared tissues issue. Omit parent_id for a root, or provide an issue ID for a child; containment is generic decomposition, not an Epic/Story/Task type system.",
		OutputSchema: issueSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIssueInput) (*mcp.CallToolResult, issueOutput, error) {
		issue, err := s.CreateIssue(ctx, service.CreateIssueRequest{
			ParentID:    in.ParentID,
			Title:       in.Title,
			Description: in.Description,
		})
		return issueResult(issue, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "update_issue",
		Description:  "Update an issue title and/or description. Omitted fields remain unchanged; state, ID, parentage, timestamps, children, and comments cannot be set through this tool.",
		OutputSchema: issueSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateIssueInput) (*mcp.CallToolResult, issueOutput, error) {
		issue, err := s.UpdateIssue(ctx, service.UpdateIssueRequest{ID: in.ID, Title: in.Title, Description: in.Description})
		return issueResult(issue, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "close_issue",
		Description:  "Close an issue by ID. This is idempotent and does not close child issues.",
		OutputSchema: issueSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIDInput) (*mcp.CallToolResult, issueOutput, error) {
		issue, err := s.CloseIssue(ctx, in.ID)
		return issueResult(issue, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:         "reopen_issue",
		Description:  "Reopen an issue by ID. This is idempotent.",
		OutputSchema: issueSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIDInput) (*mcp.CallToolResult, issueOutput, error) {
		issue, err := s.ReopenIssue(ctx, in.ID)
		return issueResult(issue, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_comment",
		Description: "Add a comment to a shared issue. author is caller-supplied provenance, not authenticated identity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addCommentInput) (*mcp.CallToolResult, commentOutput, error) {
		comment, err := s.AddComment(ctx, in.IssueID, in.Author, in.Body)
		return commentResult(comment, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "edit_comment",
		Description: "Replace a comment body. Author and creation time remain unchanged, and editing never changes conversational order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editCommentInput) (*mcp.CallToolResult, commentOutput, error) {
		comment, err := s.EditComment(ctx, in.IssueID, in.CommentID, in.Body)
		return commentResult(comment, err)
	})

	return server
}

type emptyInput struct{}

type issueIDInput struct {
	ID string `json:"id" jsonschema:"immutable 26-character issue ID"`
}

type createIssueInput struct {
	ParentID    string `json:"parent_id,omitempty" jsonschema:"parent issue ID; omit for a root issue"`
	Title       string `json:"title" jsonschema:"required single-line issue title"`
	Description string `json:"description,omitempty" jsonschema:"optional Markdown issue description"`
}

type updateIssueInput struct {
	ID          string  `json:"id" jsonschema:"immutable issue ID"`
	Title       *string `json:"title,omitempty" jsonschema:"new single-line title; omit to leave unchanged"`
	Description *string `json:"description,omitempty" jsonschema:"new Markdown description; omit to leave unchanged"`
}

type addCommentInput struct {
	IssueID string `json:"issue_id" jsonschema:"issue receiving the comment"`
	Author  string `json:"author" jsonschema:"caller-supplied provenance, not authenticated identity"`
	Body    string `json:"body" jsonschema:"Markdown comment body"`
}

type editCommentInput struct {
	IssueID   string `json:"issue_id" jsonschema:"issue containing the comment"`
	CommentID string `json:"comment_id" jsonschema:"comment to edit"`
	Body      string `json:"body" jsonschema:"replacement Markdown body"`
}

type issueListOutput struct {
	Issues []issueOutput `json:"issues"`
}

type issueOutput struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	State       string          `json:"state"`
	Created     string          `json:"created"`
	Updated     string          `json:"updated"`
	Description string          `json:"description"`
	ParentID    string          `json:"parent_id"`
	Children    []issueOutput   `json:"children"`
	Comments    []commentOutput `json:"comments"`
}

type commentOutput struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Body    string `json:"body"`
}

// v1.7.0 schema inference rejects recursive Go types, so the recursive issue
// output is the one schema supplied explicitly. Tool inputs and comment
// outputs continue to use the SDK's normal inference.
func issueSchema() map[string]any {
	return map[string]any{
		"$ref":  "#/$defs/issue",
		"$defs": issueSchemaDefinitions(),
	}
}

func issueListSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"issues": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/issue"}},
		},
		"required": []string{"issues"},
		"$defs":    issueSchemaDefinitions(),
	}
}

func issueSchemaDefinitions() map[string]any {
	stringProperty := func() map[string]any { return map[string]any{"type": "string"} }
	comment := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": stringProperty(), "author": stringProperty(),
			"created": stringProperty(), "updated": stringProperty(), "body": stringProperty(),
		},
		"required": []string{"id", "author", "created", "updated", "body"},
	}
	issue := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": stringProperty(), "title": stringProperty(), "state": stringProperty(),
			"created": stringProperty(), "updated": stringProperty(), "description": stringProperty(),
			"parent_id": stringProperty(),
			"children":  map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/issue"}},
			"comments":  map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/comment"}},
		},
		"required": []string{"id", "title", "state", "created", "updated", "description", "parent_id", "children", "comments"},
	}
	return map[string]any{"issue": issue, "comment": comment}
}

func toIssues(issues []*model.Issue) []issueOutput {
	out := make([]issueOutput, 0, len(issues))
	for _, issue := range issues {
		out = append(out, toIssue(issue))
	}
	return out
}

func toIssue(issue *model.Issue) issueOutput {
	children := make([]issueOutput, 0, len(issue.Children))
	for _, child := range issue.Children {
		children = append(children, toIssue(child))
	}
	comments := make([]commentOutput, 0, len(issue.Comments))
	for _, comment := range issue.Comments {
		comments = append(comments, toComment(comment))
	}
	return issueOutput{
		ID:          issue.ID,
		Title:       issue.Title,
		State:       string(issue.State),
		Created:     issue.Created.Format(time.RFC3339Nano),
		Updated:     issue.Updated.Format(time.RFC3339Nano),
		Description: issue.Description,
		ParentID:    issue.ParentID,
		Children:    children,
		Comments:    comments,
	}
}

func toComment(comment *model.Comment) commentOutput {
	return commentOutput{
		ID:      comment.ID,
		Author:  comment.Author,
		Created: comment.Created.Format(time.RFC3339Nano),
		Updated: comment.Updated.Format(time.RFC3339Nano),
		Body:    comment.Body,
	}
}

func issueResult(issue *model.Issue, err error) (*mcp.CallToolResult, issueOutput, error) {
	if errors.Is(err, service.ErrNotPushed) {
		return notPushedResult(err), toIssue(issue), nil
	}
	if err != nil {
		return nil, issueOutput{}, err
	}
	return nil, toIssue(issue), nil
}

func commentResult(comment *model.Comment, err error) (*mcp.CallToolResult, commentOutput, error) {
	if errors.Is(err, service.ErrNotPushed) {
		return notPushedResult(err), toComment(comment), nil
	}
	if err != nil {
		return nil, commentOutput{}, err
	}
	return nil, toComment(comment), nil
}

func notPushedResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: "the mutation exists as a local Git commit, but publication failed; do not blindly retry it: " + err.Error(),
		}},
	}
}
