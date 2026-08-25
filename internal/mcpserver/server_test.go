package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tedla-brandsema/tissues/internal/service"
)

var toolNames = []string{
	"add_comment",
	"close_issue",
	"create_issue",
	"edit_comment",
	"get_issue",
	"list_issues",
	"move_issue",
	"reopen_issue",
	"update_issue",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.name", "tissues")
	git(t, dir, "config", "user.email", "tissues@example")
	git(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newService(t *testing.T, dir string, remote bool) *service.Service {
	t.Helper()
	svc, err := service.New(context.Background(), dir, remote)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func connect(t *testing.T, svc *service.Service) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newServer(svc).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "tissues-test", Version: "v0"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func call(t *testing.T, session *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) transport/protocol error: %v", name, err)
	}
	return result
}

func structured[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("structured output %s: %v", data, err)
	}
	return out
}

func errorText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatal("tool result IsError = false, want true")
	}
	if len(result.Content) == 0 {
		t.Fatal("tool error has no content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("error content type = %T, want *mcp.TextContent", result.Content[0])
	}
	return text.Text
}

func TestToolRegistrationAndSchemas(t *testing.T) {
	dir := initRepo(t)
	session := connect(t, newService(t, dir, false))
	serverInstructions := session.InitializeResult().Instructions
	for _, phrase := range []string{"shared Git-backed issue tracker", "humans and agents", "no claims"} {
		if !strings.Contains(serverInstructions, phrase) {
			t.Errorf("server instructions do not contain %q: %q", phrase, serverInstructions)
		}
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listed.Tools))
	byName := make(map[string]*mcp.Tool)
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
		byName[tool.Name] = tool
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has an empty description", tool.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, toolNames) {
		t.Fatalf("tools = %v, want exactly %v", got, toolNames)
	}

	wantRequired(t, byName["create_issue"], []string{"title"}, []string{"parent_id", "description"})
	wantRequired(t, byName["update_issue"], []string{"id"}, []string{"title", "description"})
	wantRequired(t, byName["move_issue"], []string{"id", "parent_id"}, nil)
	wantRequired(t, byName["add_comment"], []string{"issue_id", "author", "body"}, nil)
	wantRequired(t, byName["edit_comment"], []string{"issue_id", "comment_id", "body"}, nil)
	if byName["create_issue"].OutputSchema == nil || byName["list_issues"].OutputSchema == nil {
		t.Error("typed output schemas were not exposed")
	}
}

func wantRequired(t *testing.T, tool *mcp.Tool, required, optional []string) {
	t.Helper()
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("%s input schema type = %T", tool.Name, tool.InputSchema)
	}
	set := make(map[string]bool)
	for _, value := range schema["required"].([]any) {
		set[value.(string)] = true
	}
	for _, name := range required {
		if !set[name] {
			t.Errorf("%s schema: %q is not required", tool.Name, name)
		}
	}
	for _, name := range optional {
		if set[name] {
			t.Errorf("%s schema: %q is unexpectedly required", tool.Name, name)
		}
	}
}

func TestFullMCPSemanticFlow(t *testing.T) {
	dir := initRepo(t)
	session := connect(t, newService(t, dir, false))

	root := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{
		"title": "Root", "description": "Shared work.",
	}))
	child := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{
		"parent_id": root.ID, "title": "Child",
	}))
	list := structured[issueListOutput](t, call(t, session, "list_issues", map[string]any{}))
	if len(list.Issues) != 1 || len(list.Issues[0].Children) != 1 || list.Issues[0].Children[0].ID != child.ID {
		t.Fatalf("list hierarchy = %+v", list)
	}
	gotChild := structured[issueOutput](t, call(t, session, "get_issue", map[string]any{"id": child.ID}))
	if gotChild.ParentID != root.ID || gotChild.State != "open" {
		t.Fatalf("child = %+v", gotChild)
	}
	updated := structured[issueOutput](t, call(t, session, "update_issue", map[string]any{
		"id": child.ID, "description": "Refined through MCP.",
	}))
	if updated.Title != "Child" || updated.Description != "Refined through MCP." {
		t.Fatalf("partial update = %+v", updated)
	}

	first := structured[commentOutput](t, call(t, session, "add_comment", map[string]any{
		"issue_id": root.ID, "author": "architect", "body": "First",
	}))
	second := structured[commentOutput](t, call(t, session, "add_comment", map[string]any{
		"issue_id": root.ID, "author": "implementing-agent", "body": "Second",
	}))
	edited := structured[commentOutput](t, call(t, session, "edit_comment", map[string]any{
		"issue_id": root.ID, "comment_id": first.ID, "body": "First, clarified",
	}))
	if edited.ID != first.ID || edited.Created != first.Created || edited.Body != "First, clarified" {
		t.Fatalf("edited comment = %+v, original = %+v", edited, first)
	}
	rootAfter := structured[issueOutput](t, call(t, session, "get_issue", map[string]any{"id": root.ID}))
	if len(rootAfter.Comments) != 2 || rootAfter.Comments[0].ID != first.ID || rootAfter.Comments[1].ID != second.ID {
		t.Fatalf("rapid comment order = %+v", rootAfter.Comments)
	}

	closed := structured[issueOutput](t, call(t, session, "close_issue", map[string]any{"id": child.ID}))
	if closed.State != "closed" {
		t.Fatalf("closed state = %q", closed.State)
	}
	base := commitCount(t, dir)
	closedAgain := structured[issueOutput](t, call(t, session, "close_issue", map[string]any{"id": child.ID}))
	if closedAgain.Updated != closed.Updated || commitCount(t, dir) != base {
		t.Error("idempotent close changed the issue or created a commit")
	}
	reopened := structured[issueOutput](t, call(t, session, "reopen_issue", map[string]any{"id": child.ID}))
	if reopened.State != "open" {
		t.Fatalf("reopened state = %q", reopened.State)
	}
	if got := commitCount(t, dir); got != 8 {
		t.Fatalf("commits = %d, want 8 effective mutations", got)
	}
	if status := git(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("repository is dirty: %s", status)
	}

	fresh := connect(t, newService(t, dir, false))
	reconstructed := structured[issueOutput](t, call(t, fresh, "get_issue", map[string]any{"id": root.ID}))
	if len(reconstructed.Children) != 1 || len(reconstructed.Comments) != 2 || reconstructed.Comments[0].Body != "First, clarified" || reconstructed.Children[0].State != "open" {
		t.Fatalf("reconstructed root = %+v", reconstructed)
	}
}

func TestMoveIssueThroughMCP(t *testing.T) {
	dir := initRepo(t)
	session := connect(t, newService(t, dir, false))
	a := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Alpha"}))
	b := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Beta"}))
	c := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Gamma", "parent_id": a.ID}))

	moved := structured[issueOutput](t, call(t, session, "move_issue", map[string]any{"id": a.ID, "parent_id": b.ID}))
	if moved.ParentID != b.ID || len(moved.Children) != 1 || moved.Children[0].ID != c.ID {
		t.Fatalf("moved issue = %+v", moved)
	}
	cycle := call(t, session, "move_issue", map[string]any{"id": b.ID, "parent_id": c.ID})
	if !strings.Contains(errorText(t, cycle), "invalid request") {
		t.Fatalf("cycle result = %+v", cycle)
	}
	detached := structured[issueOutput](t, call(t, session, "move_issue", map[string]any{"id": a.ID, "parent_id": ""}))
	if detached.ParentID != "" || len(detached.Children) != 1 {
		t.Fatalf("detached issue = %+v", detached)
	}
	missingParent := call(t, session, "move_issue", map[string]any{"id": a.ID})
	if !strings.Contains(errorText(t, missingParent), "parent_id") {
		t.Fatalf("missing parent_id result = %+v", missingParent)
	}
}

func TestOrdinaryServiceErrorsAreToolErrors(t *testing.T) {
	t.Run("validation and not found", func(t *testing.T) {
		dir := initRepo(t)
		session := connect(t, newService(t, dir, false))
		invalid := call(t, session, "create_issue", map[string]any{"title": ""})
		if !strings.Contains(errorText(t, invalid), "invalid request") || invalid.StructuredContent != nil {
			t.Fatalf("validation result = %+v", invalid)
		}
		issue := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Exists"}))
		base := commitCount(t, dir)
		invalidBody := call(t, session, "add_comment", map[string]any{
			"issue_id": issue.ID, "author": "architect", "body": "",
		})
		if !strings.Contains(errorText(t, invalidBody), "invalid request") || invalidBody.StructuredContent != nil || commitCount(t, dir) != base {
			t.Fatalf("invalid comment result = %+v", invalidBody)
		}
		missing := call(t, session, "get_issue", map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaa"})
		if !strings.Contains(errorText(t, missing), "not found") || missing.StructuredContent != nil {
			t.Fatalf("not-found result = %+v", missing)
		}
		missingComment := call(t, session, "edit_comment", map[string]any{
			"issue_id": issue.ID, "comment_id": "bbbbbbbbbbbbbbbbbbbbbbbbbb", "body": "x",
		})
		if !strings.Contains(errorText(t, missingComment), "not found") || commitCount(t, dir) != base {
			t.Fatalf("missing comment result = %+v", missingComment)
		}
	})

	t.Run("repository refusal", func(t *testing.T) {
		dir := initRepo(t)
		session := connect(t, newService(t, dir, false))
		issue := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Clean"}))
		if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("dirty"), 0o644); err != nil {
			t.Fatal(err)
		}
		base := commitCount(t, dir)
		result := call(t, session, "update_issue", map[string]any{"id": issue.ID, "title": "Blocked"})
		if !strings.Contains(errorText(t, result), "repository unusable") || result.StructuredContent != nil || commitCount(t, dir) != base {
			t.Fatalf("repository refusal = %+v", result)
		}
	})

	t.Run("incomplete", func(t *testing.T) {
		dir := initRepo(t)
		session := connect(t, newService(t, dir, false))
		call(t, session, "create_issue", map[string]any{"title": "Committed"})
		base := commitCount(t, dir)
		hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		result := call(t, session, "create_issue", map[string]any{"title": "Written, not committed"})
		if !strings.Contains(errorText(t, result), "mutation written but not committed") || result.StructuredContent != nil {
			t.Fatalf("incomplete result = %+v", result)
		}
		if commitCount(t, dir) != base || git(t, dir, "diff", "--cached", "--name-only") == "" {
			t.Fatal("incomplete mutation did not leave the expected staged canonical write")
		}
		refused := call(t, session, "create_issue", map[string]any{"title": "Blocked afterward"})
		if !strings.Contains(errorText(t, refused), "repository unusable") {
			t.Fatalf("subsequent result = %+v", refused)
		}
	})
}

func TestNotPushedRetainsStructuredIssueAndComment(t *testing.T) {
	dir := initRepo(t)
	session := connect(t, newService(t, dir, true))

	createdResult := call(t, session, "create_issue", map[string]any{"title": "Durable locally"})
	for _, phrase := range []string{"local Git commit", "publication failed", "do not blindly retry"} {
		if !strings.Contains(errorText(t, createdResult), phrase) {
			t.Errorf("issue warning does not contain %q", phrase)
		}
	}
	created := structured[issueOutput](t, createdResult)
	if created.ID == "" || created.Title != "Durable locally" {
		t.Fatalf("structured issue = %+v", created)
	}

	commentResult := call(t, session, "add_comment", map[string]any{
		"issue_id": created.ID, "author": "implementing-agent", "body": "Also durable locally.",
	})
	if !strings.Contains(errorText(t, commentResult), "do not blindly retry") {
		t.Fatalf("comment warning = %q", errorText(t, commentResult))
	}
	comment := structured[commentOutput](t, commentResult)
	if comment.ID == "" || comment.Body != "Also durable locally." {
		t.Fatalf("structured comment = %+v", comment)
	}
	parentResult := call(t, session, "create_issue", map[string]any{"title": "Destination"})
	parent := structured[issueOutput](t, parentResult)
	moveResult := call(t, session, "move_issue", map[string]any{"id": created.ID, "parent_id": parent.ID})
	if !strings.Contains(errorText(t, moveResult), "do not blindly retry") {
		t.Fatalf("move warning = %q", errorText(t, moveResult))
	}
	moved := structured[issueOutput](t, moveResult)
	if moved.ID != created.ID || moved.ParentID != parent.ID {
		t.Fatalf("structured moved issue = %+v", moved)
	}
	if commitCount(t, dir) != 4 || git(t, dir, "status", "--porcelain") != "" {
		t.Fatal("not_pushed mutations are not clean durable local commits")
	}
	fresh := newService(t, dir, false)
	issue, err := fresh.GetIssue(context.Background(), created.ID)
	if err != nil || issue.ParentID != parent.ID || len(issue.Comments) != 1 || issue.Comments[0].ID != comment.ID {
		t.Fatalf("fresh service issue = %+v, err = %v", issue, err)
	}
}

func TestStreamableHTTP(t *testing.T) {
	dir := initRepo(t)
	httpServer := newHTTPTestServer(t, newService(t, dir, false))
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             httpServer,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil || len(listed.Tools) != 9 {
		t.Fatalf("HTTP ListTools = %+v, err = %v", listed, err)
	}
	created := structured[issueOutput](t, call(t, session, "create_issue", map[string]any{"title": "Over Streamable HTTP"}))
	if created.Title != "Over Streamable HTTP" || commitCount(t, dir) != 1 {
		t.Fatalf("HTTP-created issue = %+v", created)
	}
}

func newHTTPTestServer(t *testing.T, svc *service.Service) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(svc))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL + "/mcp"
}
