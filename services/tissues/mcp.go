package tissues

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const (
	mcpPath           = "/mcp"
	mcpScopeRead      = "tissues:read"
	mcpScopeWrite     = "tissues:write"
	mcpMaxBodyBytes   = 1 << 20
	mcpProtocolLatest = "2026-07-28"
)

// MCPVerifiedToken is the transport-neutral token identity accepted by the
// Tissues MCP boundary.
type MCPVerifiedToken struct {
	Subject   string
	Email     string
	ClientID  string
	Scopes    []string
	ExpiresAt time.Time
}

// MCPAuth contains only the canonical OAuth identities and verification seam
// needed by the Tissues MCP surface.
type MCPAuth struct {
	Issuer   string
	Resource string
	Verify   func(context.Context, string) (MCPVerifiedToken, error)
}

// WithMCPAuth enables the protected MCP and protected-resource metadata routes.
func WithMCPAuth(auth MCPAuth) Option {
	return func(s *Service) { s.mcpAuth = &auth }
}

type mcpRoutes struct {
	endpoint http.Handler
	metadata http.Handler
}

type mcpToolSpec struct {
	Name        string
	Title       string
	Description string
	Scope       string
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

var mcpToolCatalog = []mcpToolSpec{
	{Name: "list_projects", Title: "List projects", Description: "List Tissues projects. Returns project keys, creation times, and an opaque continuation cursor.", Scope: mcpScopeRead, ReadOnly: true, Idempotent: true},
	{Name: "get_project", Title: "Get project", Description: "Get one Tissues project by key. Returns its canonical key and creation time.", Scope: mcpScopeRead, ReadOnly: true, Idempotent: true},
	{Name: "list_issues", Title: "List issues", Description: "List Tissues issue summaries, optionally filtered by project. Returns canonical issue and parent references plus an opaque continuation cursor.", Scope: mcpScopeRead, ReadOnly: true, Idempotent: true},
	{Name: "get_issue", Title: "Get issue", Description: "Get one Tissues issue by canonical PROJECT-NUMBER reference, including its description, children, and comments.", Scope: mcpScopeRead, ReadOnly: true, Idempotent: true},
	{Name: "list_issue_images", Title: "List issue images", Description: "List image attachments for one canonical PROJECT-NUMBER issue reference. Returns canonical filenames, dimensions, media types, and byte sizes.", Scope: mcpScopeRead, ReadOnly: true, Idempotent: true},
	{Name: "create_project", Title: "Create project", Description: "Create a Tissues project from a project key.", Scope: mcpScopeWrite},
	{Name: "create_issue", Title: "Create issue", Description: "Create a Tissues issue with a title and Markdown description.", Scope: mcpScopeWrite},
	{Name: "update_issue", Title: "Update issue", Description: "Replace the title, Markdown description, or both for a Tissues issue.", Scope: mcpScopeWrite, Destructive: true, Idempotent: true},
	{Name: "set_issue_parent", Title: "Set issue parent", Description: "Set or detach the parent of a Tissues issue.", Scope: mcpScopeWrite, Destructive: true, Idempotent: true},
	{Name: "close_issue", Title: "Close issue", Description: "Set a Tissues issue state to closed.", Scope: mcpScopeWrite, Destructive: true, Idempotent: true},
	{Name: "reopen_issue", Title: "Reopen issue", Description: "Set a Tissues issue state to open.", Scope: mcpScopeWrite, Destructive: true, Idempotent: true},
	{Name: "add_comment", Title: "Add comment", Description: "Add a Markdown comment attributed to the authenticated actor.", Scope: mcpScopeWrite},
	{Name: "edit_comment", Title: "Edit comment", Description: "Replace the Markdown body of a Tissues comment.", Scope: mcpScopeWrite, Destructive: true, Idempotent: true},
}

type listProjectsInput struct {
	PageSize int    `json:"page_size,omitempty" jsonschema:"number of projects to return, from 1 through 100; defaults to 25"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque continuation cursor returned by an earlier list_projects call"`
}
type listProjectsOutput struct {
	Projects   []mcpProjectDTO `json:"projects"`
	NextCursor string          `json:"next_cursor"`
}
type getProjectInput struct {
	Project string `json:"project" jsonschema:"project key, such as TISSUES"`
}
type getProjectOutput struct {
	Project mcpProjectDTO `json:"project"`
}
type listIssuesInput struct {
	PageSize int    `json:"page_size,omitempty" jsonschema:"number of issues to return, from 1 through 100; defaults to 25"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque continuation cursor returned by an earlier list_issues call"`
	Project  string `json:"project,omitempty" jsonschema:"optional project key filter"`
}
type listIssuesOutput struct {
	Issues     []mcpIssueOverviewDTO `json:"issues"`
	NextCursor string                `json:"next_cursor"`
}
type getIssueInput struct {
	ID string `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
}
type getIssueOutput struct {
	Issue mcpIssueDTO `json:"issue"`
}

func emptyIssueOutput() getIssueOutput {
	return getIssueOutput{Issue: mcpIssueDTO{Children: []mcpIssueDTO{}, Comments: []mcpCommentDTO{}}}
}

type listIssueImagesInput struct {
	ID string `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
}
type listIssueImagesOutput struct {
	Images []mcpImageDTO `json:"images"`
}
type createProjectInput struct {
	Key string `json:"key" jsonschema:"project key, such as FLUENT"`
}
type createProjectOutput struct {
	Project mcpProjectDTO `json:"project"`
}
type createIssueInput struct {
	Project     string `json:"project" jsonschema:"project key, such as FLUENT"`
	Title       string `json:"title" jsonschema:"issue title"`
	Description string `json:"description" jsonschema:"canonical Markdown description"`
}
type updateIssueInput struct {
	ID          string  `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
	Title       *string `json:"title,omitempty" jsonschema:"optional replacement title"`
	Description *string `json:"description,omitempty" jsonschema:"optional replacement Markdown description"`
}
type setIssueParentInput struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
}
type issueIDInput struct {
	ID string `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
}
type addCommentInput struct {
	ID   string `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
	Body string `json:"body" jsonschema:"canonical Markdown comment body"`
}
type editCommentInput struct {
	ID        string `json:"id" jsonschema:"canonical issue reference in PROJECT-NUMBER form"`
	CommentID string `json:"comment_id" jsonschema:"public comment ID"`
	Body      string `json:"body" jsonschema:"replacement canonical Markdown comment body"`
}
type mcpCommentOutput struct {
	Comment mcpCommentDTO `json:"comment"`
}
type mcpProjectDTO struct {
	Key     string `json:"key"`
	Created string `json:"created"`
}
type mcpIssueOverviewDTO struct {
	ID         string `json:"id"`
	ProjectKey string `json:"project_key"`
	Number     int64  `json:"number"`
	Title      string `json:"title"`
	State      State  `json:"state"`
	ParentID   string `json:"parent_id"`
	Updated    string `json:"updated"`
}
type mcpIssueDTO struct {
	ID          string          `json:"id"`
	ProjectKey  string          `json:"project_key"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	State       State           `json:"state"`
	Created     string          `json:"created"`
	Updated     string          `json:"updated"`
	Description string          `json:"description"`
	ParentID    string          `json:"parent_id"`
	Children    []mcpIssueDTO   `json:"children"`
	Comments    []mcpCommentDTO `json:"comments"`
}
type mcpCommentDTO struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Body    string `json:"body"`
}
type mcpImageDTO struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Size        int64  `json:"size"`
}

func toMCPProjectDTO(project *Project) mcpProjectDTO {
	return mcpProjectDTO{Key: project.Key, Created: formatMCPTime(project.Created)}
}

func toMCPIssueOverviewDTO(issue *IssueOverview) mcpIssueOverviewDTO {
	return mcpIssueOverviewDTO{ID: issue.Ref, ProjectKey: issue.ProjectKey, Number: issue.Number, Title: issue.Title, State: issue.State, ParentID: issue.ParentRef, Updated: formatMCPTime(issue.Updated)}
}

func toMCPIssueDTO(issue *Issue) mcpIssueDTO {
	children := make([]mcpIssueDTO, 0, len(issue.Children))
	for _, child := range issue.Children {
		children = append(children, toMCPIssueDTO(child))
	}
	comments := make([]mcpCommentDTO, 0, len(issue.Comments))
	for _, comment := range issue.Comments {
		comments = append(comments, toMCPCommentDTO(comment))
	}
	return mcpIssueDTO{ID: issue.Ref, ProjectKey: issue.ProjectKey, Number: issue.Number, Title: issue.Title, State: issue.State, Created: formatMCPTime(issue.Created), Updated: formatMCPTime(issue.Updated), Description: issue.Description, ParentID: issue.ParentRef, Children: children, Comments: comments}
}

func toMCPCommentDTO(comment *Comment) mcpCommentDTO {
	return mcpCommentDTO{ID: comment.ID, Author: comment.Author, Created: formatMCPTime(comment.Created), Updated: formatMCPTime(comment.Updated), Body: comment.Body}
}

func toMCPImageDTO(asset *Asset) mcpImageDTO {
	return mcpImageDTO{Name: asset.Key.Name, ContentType: asset.ContentType, Width: asset.Width, Height: asset.Height, Size: asset.Size}
}

func formatMCPTime(value time.Time) string { return value.Format(time.RFC3339Nano) }

func (s *Service) newMCPRoutes(authConfig MCPAuth) (*mcpRoutes, error) {
	issuer, resource, metadataURL, err := validateMCPAuth(authConfig)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "tissues", Version: "1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
	})
	s.registerMCPTools(server, metadataURL)

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          mcpMaxBodyBytes,
		PropagateRequestCancellation: true,
	})
	var endpoint http.Handler = decorateMCPToolSecuritySchemes(streamable)
	verifier := func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		verified, verifyErr := authConfig.Verify(ctx, token)
		if verifyErr != nil {
			return nil, fmt.Errorf("%w: access token rejected", mcpauth.ErrInvalidToken)
		}
		return toSDKTokenInfo(verified), nil
	}
	endpoint = mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{
		ResourceMetadataURL:    metadataURL,
		Scopes:                 nil,
		AllowMissingExpiration: false,
		ClockSkew:              0,
	})(endpoint)
	endpoint = http.NewCrossOriginProtection().Handler(endpoint)
	endpoint = addBearerChallengeErrors(endpoint)

	metadata := mcpauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   []string{issuer},
		ScopesSupported:        []string{mcpScopeRead, mcpScopeWrite},
		BearerMethodsSupported: []string{"header"},
	})
	return &mcpRoutes{endpoint: endpoint, metadata: metadata}, nil
}

func toSDKTokenInfo(verified MCPVerifiedToken) *mcpauth.TokenInfo {
	return &mcpauth.TokenInfo{
		Scopes:     append([]string(nil), verified.Scopes...),
		Expiration: verified.ExpiresAt,
		UserID:     verified.Subject,
		Extra: map[string]any{
			"email":     verified.Email,
			"client_id": verified.ClientID,
		},
	}
}

func validateMCPAuth(config MCPAuth) (issuer, resource, metadataURL string, err error) {
	if config.Verify == nil {
		return "", "", "", fmt.Errorf("MCP token verifier is required")
	}
	issuerURL, err := url.Parse(config.Issuer)
	if err != nil || (issuerURL.Scheme != "http" && issuerURL.Scheme != "https") || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.Path != "" || issuerURL.RawPath != "" || issuerURL.RawQuery != "" || issuerURL.ForceQuery || issuerURL.Fragment != "" {
		return "", "", "", fmt.Errorf("MCP issuer must be an absolute origin")
	}
	resourceURL, err := url.Parse(config.Resource)
	if err != nil || (resourceURL.Scheme != "http" && resourceURL.Scheme != "https") || resourceURL.Host == "" || resourceURL.User != nil || resourceURL.Path != mcpPath || resourceURL.RawPath != "" || resourceURL.RawQuery != "" || resourceURL.ForceQuery || resourceURL.Fragment != "" {
		return "", "", "", fmt.Errorf("MCP resource must be an absolute URL ending in /mcp")
	}
	metadata := *resourceURL
	metadata.Path = "/.well-known/oauth-protected-resource/mcp"
	return config.Issuer, config.Resource, metadata.String(), nil
}

func toolAnnotations(spec mcpToolSpec) *mcp.ToolAnnotations {
	openWorld := spec.OpenWorld
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: spec.ReadOnly, IdempotentHint: spec.Idempotent, OpenWorldHint: &openWorld}
	if !spec.ReadOnly {
		destructive := spec.Destructive
		annotations.DestructiveHint = &destructive
	}
	return annotations
}

func toolDefinition(spec mcpToolSpec) *mcp.Tool {
	tool := &mcp.Tool{Name: spec.Name, Title: spec.Title, Description: spec.Description, Annotations: toolAnnotations(spec)}
	if spec.Name == "set_issue_parent" {
		tool.InputSchema = setIssueParentInputSchema()
	}
	switch spec.Name {
	case "get_issue", "create_issue", "update_issue", "set_issue_parent", "close_issue", "reopen_issue":
		tool.OutputSchema = mcpIssueOutputSchema()
	}
	return tool
}

func mcpIssueOutputSchema() map[string]any {
	comment := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string"}, "author": map[string]any{"type": "string"},
			"created": map[string]any{"type": "string"}, "updated": map[string]any{"type": "string"},
			"body": map[string]any{"type": "string"},
		},
		"required": []string{"id", "author", "created", "updated", "body"},
	}
	issue := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string"}, "project_key": map[string]any{"type": "string"},
			"number": map[string]any{"type": "integer"}, "title": map[string]any{"type": "string"},
			"state": map[string]any{"type": "string"}, "created": map[string]any{"type": "string"},
			"updated": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"parent_id": map[string]any{"type": "string"},
			"children":  map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/issue"}},
			"comments":  map[string]any{"type": "array", "items": comment},
		},
		"required": []string{"id", "project_key", "number", "title", "state", "created", "updated", "description", "parent_id", "children", "comments"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"issue": map[string]any{"$ref": "#/$defs/issue"}},
		"required":   []string{"issue"}, "$defs": map[string]any{"issue": issue},
	}
}

func setIssueParentInputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
			"parent_id": map[string]any{"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "null"},
			}},
		},
		"required": []string{"id", "parent_id"},
	}
}

func (s *Service) registerMCPTools(server *mcp.Server, metadataURL string) {
	for _, spec := range mcpToolCatalog {
		switch spec.Name {
		case "list_projects":
			mcp.AddTool(server, toolDefinition(spec), s.listProjectsMCP(metadataURL, spec))
		case "get_project":
			mcp.AddTool(server, toolDefinition(spec), s.getProjectMCP(metadataURL, spec))
		case "list_issues":
			mcp.AddTool(server, toolDefinition(spec), s.listIssuesMCP(metadataURL, spec))
		case "get_issue":
			mcp.AddTool(server, toolDefinition(spec), s.getIssueMCP(metadataURL, spec))
		case "list_issue_images":
			mcp.AddTool(server, toolDefinition(spec), s.listIssueImagesMCP(metadataURL, spec))
		case "create_project":
			mcp.AddTool(server, toolDefinition(spec), s.createProjectMCP(metadataURL, spec))
		case "create_issue":
			mcp.AddTool(server, toolDefinition(spec), s.createIssueMCP(metadataURL, spec))
		case "update_issue":
			mcp.AddTool(server, toolDefinition(spec), s.updateIssueMCP(metadataURL, spec))
		case "set_issue_parent":
			mcp.AddTool(server, toolDefinition(spec), s.setIssueParentMCP(metadataURL, spec))
		case "close_issue":
			mcp.AddTool(server, toolDefinition(spec), s.closeIssueMCP(metadataURL, spec))
		case "reopen_issue":
			mcp.AddTool(server, toolDefinition(spec), s.reopenIssueMCP(metadataURL, spec))
		case "add_comment":
			mcp.AddTool(server, toolDefinition(spec), s.addCommentMCP(metadataURL, spec))
		case "edit_comment":
			mcp.AddTool(server, toolDefinition(spec), s.editCommentMCP(metadataURL, spec))
		default:
			panic("unregistered MCP tool " + spec.Name)
		}
	}
}

func (s *Service) listProjectsMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[listProjectsInput, listProjectsOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, listProjectsOutput{}, nil
		}
		page, err := s.ListProjectsPage(ctx, mcpPageSize(input.PageSize), input.Cursor)
		if err != nil {
			return nil, listProjectsOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		out := listProjectsOutput{Projects: make([]mcpProjectDTO, 0, len(page.Projects)), NextCursor: page.NextCursor}
		for _, project := range page.Projects {
			out.Projects = append(out.Projects, toMCPProjectDTO(project))
		}
		return nil, out, nil
	}
}

func (s *Service) getProjectMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[getProjectInput, getProjectOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getProjectInput) (*mcp.CallToolResult, getProjectOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, getProjectOutput{}, nil
		}
		project, err := s.GetProject(ctx, input.Project)
		if err != nil {
			return nil, getProjectOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getProjectOutput{Project: toMCPProjectDTO(project)}, nil
	}
}

func (s *Service) listIssuesMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[listIssuesInput, listIssuesOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input listIssuesInput) (*mcp.CallToolResult, listIssuesOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, listIssuesOutput{}, nil
		}
		page, err := s.ListIssueOverviewsPage(ctx, mcpPageSize(input.PageSize), input.Cursor, input.Project)
		if err != nil {
			return nil, listIssuesOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		out := listIssuesOutput{Issues: make([]mcpIssueOverviewDTO, 0, len(page.Issues)), NextCursor: page.NextCursor}
		for _, issue := range page.Issues {
			out.Issues = append(out.Issues, toMCPIssueOverviewDTO(issue))
		}
		return nil, out, nil
	}
}

func (s *Service) getIssueMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[getIssueInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getIssueInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		issue, err := s.GetIssue(ctx, input.ID)
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) listIssueImagesMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[listIssueImagesInput, listIssueImagesOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input listIssueImagesInput) (*mcp.CallToolResult, listIssueImagesOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, listIssueImagesOutput{}, nil
		}
		if _, err := issueRefInput(input.ID); err != nil {
			return nil, listIssueImagesOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		assets, err := s.ListAssets(ctx, input.ID)
		if err != nil {
			return nil, listIssueImagesOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		SortAssets(assets)
		out := listIssueImagesOutput{Images: make([]mcpImageDTO, 0, len(assets))}
		for _, asset := range assets {
			out.Images = append(out.Images, toMCPImageDTO(asset))
		}
		return nil, out, nil
	}
}

func (s *Service) createProjectMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[createProjectInput, createProjectOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input createProjectInput) (*mcp.CallToolResult, createProjectOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, createProjectOutput{}, nil
		}
		project, err := s.CreateProject(ctx, input.Key)
		if err != nil {
			return nil, createProjectOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, createProjectOutput{Project: toMCPProjectDTO(project)}, nil
	}
}

func (s *Service) createIssueMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[createIssueInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input createIssueInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		issue, err := s.CreateIssue(ctx, input.Project, CreateIssueRequest{Title: input.Title, Description: input.Description})
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) updateIssueMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[updateIssueInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input updateIssueInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		if input.Title == nil && input.Description == nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, fmt.Errorf("%w: title or description is required", ErrInvalid))
		}
		issue, err := s.UpdateIssue(ctx, UpdateIssueRequest{Ref: input.ID, Title: input.Title, Description: input.Description})
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) setIssueParentMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[setIssueParentInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input setIssueParentInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		parent := ""
		if input.ParentID != nil {
			parent = *input.ParentID
		}
		issue, err := s.MoveIssue(ctx, input.ID, parent)
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) closeIssueMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[issueIDInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input issueIDInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		issue, err := s.CloseIssue(ctx, input.ID)
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) reopenIssueMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[issueIDInput, getIssueOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input issueIDInput) (*mcp.CallToolResult, getIssueOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, emptyIssueOutput(), nil
		}
		issue, err := s.ReopenIssue(ctx, input.ID)
		if err != nil {
			return nil, getIssueOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, getIssueOutput{Issue: toMCPIssueDTO(issue)}, nil
	}
}

func (s *Service) addCommentMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[addCommentInput, mcpCommentOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input addCommentInput) (*mcp.CallToolResult, mcpCommentOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, mcpCommentOutput{}, nil
		}
		author, err := mcpCommentAuthor(ctx)
		if err != nil {
			return nil, mcpCommentOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		comment, err := s.AddComment(ctx, input.ID, author, input.Body)
		if err != nil {
			return nil, mcpCommentOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, mcpCommentOutput{Comment: toMCPCommentDTO(comment)}, nil
	}
}

func (s *Service) editCommentMCP(metadataURL string, spec mcpToolSpec) mcp.ToolHandlerFor[editCommentInput, mcpCommentOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input editCommentInput) (*mcp.CallToolResult, mcpCommentOutput, error) {
		ctx, denied := authorizeMCPTool(ctx, req, spec, metadataURL)
		if denied != nil {
			return denied, mcpCommentOutput{}, nil
		}
		comment, err := s.EditComment(ctx, input.ID, input.CommentID, input.Body)
		if err != nil {
			return nil, mcpCommentOutput{}, safeMCPToolError(ctx, spec.Name, err)
		}
		return nil, mcpCommentOutput{Comment: toMCPCommentDTO(comment)}, nil
	}
}

func mcpPageSize(size int) int {
	if size == 0 {
		return DefaultPageSize
	}
	return size
}

func authorizeMCPTool(ctx context.Context, req *mcp.CallToolRequest, spec mcpToolSpec, metadataURL string) (context.Context, *mcp.CallToolResult) {
	info := req.Extra.TokenInfo
	if info == nil || !slices.Contains(info.Scopes, spec.Scope) {
		challenge := fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q, error="insufficient_scope"`, metadataURL, spec.Scope)
		return ctx, &mcp.CallToolResult{
			Meta:    mcp.Meta{"mcp/www_authenticate": []string{challenge}},
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Authorization requires the %s scope.", spec.Scope)}},
			IsError: true,
		}
	}
	ctx = gcpauth.WithSubject(ctx, info.UserID)
	if email, ok := info.Extra["email"].(string); ok && email != "" {
		ctx = gcpauth.WithEmail(ctx, email)
	}
	return ctx, nil
}

func mcpCommentAuthor(ctx context.Context) (string, error) {
	if email, ok := gcpauth.EmailFromContext(ctx); ok {
		return email, nil
	}
	if subject, ok := gcpauth.SubjectFromContext(ctx); ok {
		return subject, nil
	}
	return "", fmt.Errorf("%w: authenticated actor identity is unavailable", ErrInternal)
}

func safeMCPToolError(ctx context.Context, tool string, err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return errors.New("invalid request")
	case errors.Is(err, ErrNotFound):
		return errors.New("resource not found")
	case errors.Is(err, ErrConflict):
		return errors.New("request conflicts with current state")
	default:
		slog.ErrorContext(ctx, "Tissues MCP tool failed", "tool", tool, "error", err)
		return errors.New("internal server error")
	}
}

var bearerErrorParameter = regexp.MustCompile(`(?i)(?:^|,)\s*error\s*=`)

func addBearerChallengeErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&bearerChallengeWriter{ResponseWriter: w}, r)
	})
}

// bearerChallengeWriter is a temporary compatibility shim for
// modelcontextprotocol/go-sdk#1134. It only augments the SDK's Bearer error
// challenges and can be removed once the pinned SDK emits the error parameter.
type bearerChallengeWriter struct {
	http.ResponseWriter
}

func (w *bearerChallengeWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		errorCode := "invalid_token"
		if status == http.StatusForbidden {
			errorCode = "insufficient_scope"
		}
		values := w.Header().Values("WWW-Authenticate")
		if len(values) > 0 {
			w.Header().Del("WWW-Authenticate")
			for _, value := range values {
				trimmed := strings.TrimSpace(value)
				if len(trimmed) >= len("Bearer") && strings.EqualFold(trimmed[:len("Bearer")], "Bearer") && !bearerErrorParameter.MatchString(trimmed[len("Bearer"):]) {
					value += fmt.Sprintf(`, error=%q`, errorCode)
				}
				w.Header().Add("WWW-Authenticate", value)
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func decorateMCPToolSecuritySchemes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Protocol-Version") != mcpProtocolLatest || r.Header.Get("Mcp-Method") != "tools/list" {
			next.ServeHTTP(w, r)
			return
		}
		buffer := &bufferedResponseWriter{header: make(http.Header)}
		next.ServeHTTP(buffer, r)
		if buffer.status == 0 {
			buffer.status = http.StatusOK
		}
		body := buffer.body.Bytes()
		if buffer.status >= 200 && buffer.status < 300 && strings.HasPrefix(buffer.header.Get("Content-Type"), "application/json") {
			body = addToolSecuritySchemes(body)
		}
		copyHeader(w.Header(), buffer.header)
		w.WriteHeader(buffer.status)
		_, _ = w.Write(body)
	})
}

type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }
func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func copyHeader(dst, src http.Header) {
	for name, values := range src {
		dst[name] = append([]string(nil), values...)
	}
}

func addToolSecuritySchemes(body []byte) []byte {
	var response map[string]any
	if json.Unmarshal(body, &response) != nil {
		return body
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		return body
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		return body
	}
	known := make(map[string]mcpToolSpec, len(mcpToolCatalog))
	for _, spec := range mcpToolCatalog {
		known[spec.Name] = spec
	}
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		spec, ok := known[fmt.Sprint(tool["name"])]
		if !ok {
			continue
		}
		tool["securitySchemes"] = []any{map[string]any{"type": "oauth2", "scopes": []string{spec.Scope}}}
	}
	decorated, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return append(decorated, '\n')
}
