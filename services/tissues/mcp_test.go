package tissues

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	coreconfig "github.com/tedla-brandsema/tissues/lib/core/config"
	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const (
	testMCPIssuer   = "https://auth.example.test"
	testMCPResource = "https://tissues.example.test/mcp"
	testMCPMetadata = "https://tissues.example.test/.well-known/oauth-protected-resource/mcp"
)

func TestMCPRoutesAreConditionalAndProtectedResourceMetadataIsCanonical(t *testing.T) {
	without := testService(t, newMemoryRepository())
	withoutMux := http.NewServeMux()
	if err := without.RegisterRoutes(withoutMux); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{mcpPath, "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := httptest.NewRecorder()
		withoutMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("auth-disabled GET %s = %d, want 404", target, rec.Code)
		}
	}

	with := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, with)
	for _, target := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		for _, method := range []string{http.MethodGet, http.MethodOptions} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
			if want := map[string]int{http.MethodGet: http.StatusOK, http.MethodOptions: http.StatusNoContent}[method]; rec.Code != want {
				t.Fatalf("%s %s = %d body=%q", method, target, rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("%s %s CORS = %q", method, target, rec.Header().Get("Access-Control-Allow-Origin"))
			}
			if method == http.MethodGet {
				var got map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got["resource"] != testMCPResource || !reflect.DeepEqual(got["authorization_servers"], []any{testMCPIssuer}) || !reflect.DeepEqual(got["scopes_supported"], []any{mcpScopeRead, mcpScopeWrite}) || !reflect.DeepEqual(got["bearer_methods_supported"], []any{"header"}) {
					t.Fatalf("metadata = %#v", got)
				}
			}
		}
		post := httptest.NewRecorder()
		mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, target, nil))
		if post.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d, want 405", target, post.Code)
		}
	}
}

func TestMCPBearerFailuresAndMethodSurface(t *testing.T) {
	svc := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, svc)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "malformed", token: "bad"},
		{name: "expired", token: "expired"},
		{name: "missing expiration", token: "missing-expiration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, mcpJSONRequest("tools/list", "", nil, tc.token))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			challenge := rec.Header().Get("WWW-Authenticate")
			if !strings.HasPrefix(challenge, "Bearer ") || !strings.Contains(challenge, `resource_metadata="`+testMCPMetadata+`"`) || !strings.Contains(challenge, `error="invalid_token"`) {
				t.Fatalf("challenge=%q", challenge)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := httptest.NewRecorder()
		request := httptest.NewRequest(method, mcpPath, nil)
		request.Header.Set("Authorization", "Bearer read")
		mux.ServeHTTP(rec, request)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Fatalf("%s /mcp = %d allow=%q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	trailing := httptest.NewRecorder()
	mux.ServeHTTP(trailing, httptest.NewRequest(http.MethodPost, "/mcp/", nil))
	if trailing.Code != http.StatusNotFound {
		t.Fatalf("POST /mcp/ = %d", trailing.Code)
	}
}

func TestMCPDiscoveryAndToolListContract(t *testing.T) {
	svc := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, svc)
	discover := doMCPRequest(t, mux, "server/discover", "", nil, "read")
	if !strings.HasPrefix(discover.Header().Get("Content-Type"), "application/json") || discover.Header().Get("Mcp-Session-Id") != "" || strings.Contains(discover.Body.String(), "event:") {
		t.Fatalf("non-JSON/sessionful discovery response: headers=%v body=%q", discover.Header(), discover.Body.String())
	}
	result := mcpResult(t, discover)
	meta, ok := result["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("discover result = %#v", result)
	}
	serverInfo, ok := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("discover metadata = %#v", meta)
	}
	if serverInfo["name"] != "tissues" || serverInfo["version"] != "1" {
		t.Fatalf("server info = %#v", serverInfo)
	}
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("discover result = %#v", result)
	}
	if len(capabilities) != 1 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	toolsCapability, ok := capabilities["tools"].(map[string]any)
	if !ok || len(toolsCapability) != 0 {
		t.Fatalf("tools capability = %#v", capabilities["tools"])
	}

	listed := doMCPRequest(t, mux, "tools/list", "", nil, "read")
	tools := mcpResult(t, listed)["tools"].([]any)
	wantPolicies := map[string]struct {
		scope                      string
		readOnly, destructive      bool
		idempotent, destructiveSet bool
	}{
		"list_projects":     {scope: mcpScopeRead, readOnly: true, idempotent: true},
		"get_project":       {scope: mcpScopeRead, readOnly: true, idempotent: true},
		"list_issues":       {scope: mcpScopeRead, readOnly: true, idempotent: true},
		"get_issue":         {scope: mcpScopeRead, readOnly: true, idempotent: true},
		"list_issue_images": {scope: mcpScopeRead, readOnly: true, idempotent: true},
		"create_project":    {scope: mcpScopeWrite, destructiveSet: true},
		"create_issue":      {scope: mcpScopeWrite, destructiveSet: true},
		"update_issue":      {scope: mcpScopeWrite, destructive: true, idempotent: true, destructiveSet: true},
		"set_issue_parent":  {scope: mcpScopeWrite, destructive: true, idempotent: true, destructiveSet: true},
		"close_issue":       {scope: mcpScopeWrite, destructive: true, idempotent: true, destructiveSet: true},
		"reopen_issue":      {scope: mcpScopeWrite, destructive: true, idempotent: true, destructiveSet: true},
		"add_comment":       {scope: mcpScopeWrite, destructiveSet: true},
		"edit_comment":      {scope: mcpScopeWrite, destructive: true, idempotent: true, destructiveSet: true},
	}
	if len(tools) != len(wantPolicies) || len(mcpToolCatalog) != len(wantPolicies) {
		t.Fatalf("tool count=%d tools=%#v", len(tools), tools)
	}
	seen := make(map[string]bool, len(tools))
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		policy, ok := wantPolicies[name]
		if !ok || seen[name] {
			t.Fatalf("unexpected or duplicate tool %q", name)
		}
		seen[name] = true
		if tool["title"] == "" || tool["description"] == "" || tool["inputSchema"] == nil || tool["outputSchema"] == nil {
			t.Fatalf("incomplete tool = %#v", tool)
		}
		annotations := tool["annotations"].(map[string]any)
		if annotations["readOnlyHint"] != policy.readOnly || annotations["idempotentHint"] != policy.idempotent || annotations["openWorldHint"] != false {
			t.Fatalf("%s annotations = %#v", name, annotations)
		}
		destructive, destructiveSet := annotations["destructiveHint"]
		if destructiveSet != policy.destructiveSet || destructiveSet && destructive != policy.destructive {
			t.Fatalf("%s destructive annotation = %#v", name, annotations)
		}
		wantSecurity := []any{map[string]any{"type": "oauth2", "scopes": []any{policy.scope}}}
		if !reflect.DeepEqual(tool["securitySchemes"], wantSecurity) {
			t.Fatalf("%s securitySchemes = %#v", name, tool["securitySchemes"])
		}
		if meta, ok := tool["_meta"].(map[string]any); ok {
			if _, nested := meta["securitySchemes"]; nested {
				t.Fatalf("securitySchemes nested under _meta: %#v", tool)
			}
		}
	}
	if len(seen) != len(wantPolicies) {
		t.Fatalf("tool names=%v", seen)
	}
}

func TestMCPMutationSchemasAreExactAndShared(t *testing.T) {
	mux := registerMCPTestRoutes(t, newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier))
	tools := mcpToolsByName(t, mux)

	for _, name := range []string{"add_comment", "edit_comment"} {
		properties := tools[name]["inputSchema"].(map[string]any)["properties"].(map[string]any)
		for _, forbidden := range []string{"author", "email", "subject", "user"} {
			if _, exists := properties[forbidden]; exists {
				t.Fatalf("%s input exposes trusted actor field %q: %#v", name, forbidden, properties)
			}
		}
	}
	assertJSONKeys(t, tools["add_comment"]["inputSchema"].(map[string]any)["properties"].(map[string]any), "body", "id")
	assertJSONKeys(t, tools["edit_comment"]["inputSchema"].(map[string]any)["properties"].(map[string]any), "body", "comment_id", "id")

	parentSchema := tools["set_issue_parent"]["inputSchema"].(map[string]any)
	if !reflect.DeepEqual(parentSchema["required"], []any{"id", "parent_id"}) || parentSchema["additionalProperties"] != false {
		t.Fatalf("set_issue_parent input schema = %#v", parentSchema)
	}
	parentAnyOf := parentSchema["properties"].(map[string]any)["parent_id"].(map[string]any)["anyOf"].([]any)
	if !reflect.DeepEqual(parentAnyOf, []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}) {
		t.Fatalf("parent_id schema = %#v", parentAnyOf)
	}

	issueSchema := tools["get_issue"]["outputSchema"]
	for _, name := range []string{"create_issue", "update_issue", "set_issue_parent", "close_issue", "reopen_issue"} {
		if !reflect.DeepEqual(tools[name]["outputSchema"], issueSchema) {
			t.Fatalf("%s issue output schema diverged", name)
		}
	}
	if !reflect.DeepEqual(tools["add_comment"]["outputSchema"], tools["edit_comment"]["outputSchema"]) {
		t.Fatal("comment output schemas diverged")
	}
	commentSchema := tools["add_comment"]["outputSchema"].(map[string]any)
	if commentSchema["additionalProperties"] != false || !reflect.DeepEqual(commentSchema["required"], []any{"comment"}) {
		t.Fatalf("comment output schema = %#v", commentSchema)
	}
	comment := commentSchema["properties"].(map[string]any)["comment"].(map[string]any)
	assertJSONKeys(t, comment["properties"].(map[string]any), "author", "body", "created", "id", "updated")
	if comment["additionalProperties"] != false || !reflect.DeepEqual(comment["required"], []any{"id", "author", "created", "updated", "body"}) {
		t.Fatalf("comment schema = %#v", comment)
	}
}

func TestMCPMutationToolsRequireWriteScopeBeforeDomainInvocation(t *testing.T) {
	inputs := map[string]map[string]any{
		"create_project":   {"key": "WRITE"},
		"create_issue":     {"project": "WRITE", "title": "Title", "description": "Markdown"},
		"update_issue":     {"id": "WRITE-1", "title": "Title"},
		"set_issue_parent": {"id": "WRITE-1", "parent_id": nil},
		"close_issue":      {"id": "WRITE-1"},
		"reopen_issue":     {"id": "WRITE-1"},
		"add_comment":      {"id": "WRITE-1", "body": "Body"},
		"edit_comment":     {"id": "WRITE-1", "comment_id": "aaaaaaaaaaaaaaaaaaaaaaaaaa", "body": "Body"},
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			repo := &transactionCountingRepository{memoryRepository: newMemoryRepository()}
			mux := registerMCPTestRoutes(t, newMCPTestService(t, repo, newMemoryAssetStore(), defaultMCPVerifier))
			response := doMCPRequest(t, mux, "tools/call", name, input, "read")
			result := mcpResult(t, response)
			if result["isError"] != true || !strings.Contains(response.Body.String(), "Authorization requires the tissues:write scope.") {
				t.Fatalf("write-scope denial = %#v body=%q", result, response.Body.String())
			}
			challenge := result["_meta"].(map[string]any)["mcp/www_authenticate"].([]any)[0].(string)
			if !strings.Contains(challenge, `resource_metadata="`+testMCPMetadata+`"`) || !strings.Contains(challenge, `scope="tissues:write"`) || !strings.Contains(challenge, `error="insufficient_scope"`) || !strings.Contains(challenge, `error_description="Authorization requires the tissues:write scope."`) {
				t.Fatalf("write-scope challenge = %q", challenge)
			}
			if repo.transactions != 0 {
				t.Fatalf("%s invoked domain with read token", name)
			}
		})
	}

	repo := &transactionCountingRepository{memoryRepository: newMemoryRepository()}
	mux := registerMCPTestRoutes(t, newMCPTestService(t, repo, newMemoryAssetStore(), defaultMCPVerifier))
	created := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "create_project", map[string]any{"key": "WRITE"}, "write"))
	if created["project"].(map[string]any)["key"] != "WRITE" || repo.transactions == 0 {
		t.Fatalf("write token did not invoke mutation: %#v transactions=%d", created, repo.transactions)
	}
}

func TestMCPFiveReadToolsMapDomainDTOsAndPagination(t *testing.T) {
	repo := newMemoryRepository()
	assets := newMemoryAssetStore()
	svc := newMCPTestService(t, repo, assets, defaultMCPVerifier)
	if _, err := svc.CreateProject(context.Background(), "BETA"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateProject(context.Background(), "ALPHA"); err != nil {
		t.Fatal(err)
	}
	issue, err := svc.CreateIssue(context.Background(), "ALPHA", CreateIssueRequest{Title: "First issue", Description: "Canonical markdown"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateIssue(context.Background(), "ALPHA", CreateIssueRequest{Title: "Second issue", Description: "Child markdown"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveIssue(context.Background(), child.Ref, issue.Ref); err != nil {
		t.Fatal(err)
	}
	comment, err := svc.AddComment(context.Background(), issue.Ref, "person@example.test", "Comment **Markdown**")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 1, 2, 4, 4, 5, 0, time.UTC) }
	updatedTitle := "Second issue updated"
	if _, err := svc.UpdateIssue(context.Background(), UpdateIssueRequest{Ref: child.Ref, Title: &updatedTitle}); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.Put(context.Background(), AssetKey{ProjectKey: "ALPHA", IssueNumber: issue.Number, Name: "z.png"}, AssetWrite{ContentType: "image/png", Width: 20, Height: 10, Data: []byte("z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.Put(context.Background(), AssetKey{ProjectKey: "ALPHA", IssueNumber: issue.Number, Name: "a.jpg"}, AssetWrite{ContentType: "image/jpeg", Width: 4, Height: 3, Data: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	mux := registerMCPTestRoutes(t, svc)

	defaultProjects := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "list_projects", nil, "read"))
	assertJSONKeys(t, defaultProjects, "next_cursor", "projects")
	if got := defaultProjects["projects"].([]any); len(got) != 2 || got[0].(map[string]any)["key"] != "ALPHA" || got[1].(map[string]any)["key"] != "BETA" {
		t.Fatalf("list_projects default/ordering = %#v", defaultProjects)
	}
	if cursor, exists := defaultProjects["next_cursor"]; !exists || cursor != "" {
		t.Fatalf("list_projects final cursor missing or nonempty: %#v", defaultProjects)
	}
	projects := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "list_projects", map[string]any{"page_size": 1}, "read"))
	projectPage := projects["projects"].([]any)
	projectCursor, projectCursorExists := projects["next_cursor"]
	if len(projectPage) != 1 || projectPage[0].(map[string]any)["key"] != "ALPHA" || !projectCursorExists || projectCursor == "" {
		t.Fatalf("list_projects = %#v", projects)
	}
	assertJSONKeys(t, projectPage[0].(map[string]any), "created", "key")
	nextProjects := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "list_projects", map[string]any{"page_size": 1, "cursor": projectCursor}, "read"))
	if got := nextProjects["projects"].([]any); len(got) != 1 || got[0].(map[string]any)["key"] != "BETA" {
		t.Fatalf("list_projects cursor page = %#v", nextProjects)
	}
	if cursor, exists := nextProjects["next_cursor"]; !exists || cursor != "" {
		t.Fatalf("list_projects continuation final cursor missing or nonempty: %#v", nextProjects)
	}
	project := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "get_project", map[string]any{"project": "alpha"}, "read"))["project"].(map[string]any)
	assertJSONKeys(t, project, "created", "key")
	if project["key"] != "ALPHA" || project["created"] == "" {
		t.Fatalf("get_project = %#v", project)
	}
	if _, leaksAllocator := project["next_issue_number"]; leaksAllocator {
		t.Fatalf("get_project leaked allocator state: %#v", project)
	}
	issues := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "list_issues", map[string]any{"project": "ALPHA", "page_size": 1}, "read"))
	issueOverview := issues["issues"].([]any)[0].(map[string]any)
	assertJSONKeys(t, issueOverview, "id", "number", "parent_id", "project_key", "state", "title", "updated")
	issueCursor, issueCursorExists := issues["next_cursor"]
	if issueOverview["id"] != "ALPHA-2" || issueOverview["project_key"] != "ALPHA" || issueOverview["parent_id"] != "ALPHA-1" || !issueCursorExists || issueCursor == "" {
		t.Fatalf("list_issues = %#v", issues)
	}
	nextIssues := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "list_issues", map[string]any{"project": "ALPHA", "page_size": 1, "cursor": issueCursor}, "read"))
	if nextIssues["issues"].([]any)[0].(map[string]any)["id"] != "ALPHA-1" {
		t.Fatalf("list_issues cursor page = %#v", nextIssues)
	}
	if cursor, exists := nextIssues["next_cursor"]; !exists || cursor != "" {
		t.Fatalf("list_issues continuation final cursor missing or nonempty: %#v", nextIssues)
	}
	issueResponse := doMCPRequest(t, mux, "tools/call", "get_issue", map[string]any{"id": "ALPHA-1"}, "read")
	gotIssue := mcpStructured(t, issueResponse)["issue"].(map[string]any)
	assertJSONKeys(t, gotIssue, "children", "comments", "created", "description", "id", "number", "parent_id", "project_key", "state", "title", "updated")
	if gotIssue["id"] != "ALPHA-1" || gotIssue["description"] != "Canonical markdown" {
		t.Fatalf("get_issue = %#v", gotIssue)
	}
	children := gotIssue["children"].([]any)
	comments := gotIssue["comments"].([]any)
	if len(children) != 1 || len(comments) != 1 {
		t.Fatalf("get_issue relationships/comments = %#v", gotIssue)
	}
	assertJSONKeys(t, children[0].(map[string]any), "children", "comments", "created", "description", "id", "number", "parent_id", "project_key", "state", "title", "updated")
	assertJSONKeys(t, comments[0].(map[string]any), "author", "body", "created", "id", "updated")
	if children[0].(map[string]any)["id"] != "ALPHA-2" || children[0].(map[string]any)["parent_id"] != "ALPHA-1" || comments[0].(map[string]any)["id"] != comment.ID || comments[0].(map[string]any)["body"] != "Comment **Markdown**" {
		t.Fatalf("get_issue relationships/comments = %#v", gotIssue)
	}
	imageResponse := doMCPRequest(t, mux, "tools/call", "list_issue_images", map[string]any{"id": "ALPHA-1"}, "read")
	images := mcpStructured(t, imageResponse)["images"].([]any)
	if len(images) != 2 || images[0].(map[string]any)["name"] != "a.jpg" || images[1].(map[string]any)["name"] != "z.png" {
		t.Fatalf("list_issue_images = %#v", images)
	}
	firstImage := images[0].(map[string]any)
	assertJSONKeys(t, firstImage, "content_type", "height", "name", "size", "width")
	if firstImage["content_type"] != "image/jpeg" || firstImage["width"] != float64(4) || firstImage["height"] != float64(3) || firstImage["size"] != float64(1) {
		t.Fatalf("image DTO = %#v", firstImage)
	}
	for _, forbidden := range []string{"url", "bucket", "generation", "project_key", "issue_number", "data"} {
		if strings.Contains(imageResponse.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("list_issue_images leaked %q: %s", forbidden, imageResponse.Body.String())
		}
	}
}

func TestMCPTimeFormattingIsRFC3339Nano(t *testing.T) {
	value := time.Date(2026, time.August, 29, 12, 34, 56, 123456789, time.FixedZone("test", 2*60*60))
	if got, want := formatMCPTime(value), value.Format(time.RFC3339Nano); got != want {
		t.Fatalf("formatMCPTime() = %q, want %q", got, want)
	}
}

func TestMCPProjectAndIssueMutationsMapDomainAndPreserveIdempotency(t *testing.T) {
	repo := newMemoryRepository()
	svc := newMCPTestService(t, repo, newMemoryAssetStore(), defaultMCPVerifier)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	svc.now = func() time.Time { return now }
	mux := registerMCPTestRoutes(t, svc)

	projectResponse := doMCPRequest(t, mux, "tools/call", "create_project", map[string]any{"key": "fluent"}, "write")
	project := mcpStructured(t, projectResponse)["project"].(map[string]any)
	assertJSONKeys(t, project, "created", "key")
	if project["key"] != "FLUENT" {
		t.Fatalf("create_project = %#v", project)
	}
	duplicate := doMCPRequest(t, mux, "tools/call", "create_project", map[string]any{"key": "FLUENT"}, "write")
	if result := mcpResult(t, duplicate); result["isError"] != true || !strings.Contains(duplicate.Body.String(), "request conflicts with current state") {
		t.Fatalf("duplicate project result=%#v body=%q", result, duplicate.Body.String())
	}

	parentResponse := doMCPRequest(t, mux, "tools/call", "create_issue", map[string]any{"project": "FLUENT", "title": "Parent", "description": "Parent **Markdown**"}, "write")
	parentOutput := mcpStructured(t, parentResponse)["issue"].(map[string]any)
	if parentOutput["id"] != "FLUENT-1" || parentOutput["state"] != "open" || parentOutput["description"] != "Parent **Markdown**" {
		t.Fatalf("create_issue parent = %#v", parentOutput)
	}
	targetResponse := doMCPRequest(t, mux, "tools/call", "create_issue", map[string]any{"project": "FLUENT", "title": "Target", "description": "Target **Markdown**"}, "write")
	targetOutput := mcpStructured(t, targetResponse)["issue"].(map[string]any)
	if targetOutput["id"] != "FLUENT-2" || targetOutput["state"] != "open" {
		t.Fatalf("create_issue target = %#v", targetOutput)
	}
	parentDomain, err := svc.GetIssue(context.Background(), "FLUENT-1")
	if err != nil {
		t.Fatal(err)
	}
	targetDomain, err := svc.GetIssue(context.Background(), "FLUENT-2")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	titleResponse := doMCPRequest(t, mux, "tools/call", "update_issue", map[string]any{"id": "FLUENT-2", "title": "Title only"}, "write")
	titleUpdate := mcpStructured(t, titleResponse)["issue"].(map[string]any)
	if titleUpdate["title"] != "Title only" || titleUpdate["description"] != "Target **Markdown**" {
		t.Fatalf("title-only update = %#v", titleUpdate)
	}
	now = now.Add(time.Hour)
	descriptionResponse := doMCPRequest(t, mux, "tools/call", "update_issue", map[string]any{"id": "FLUENT-2", "description": "Description only"}, "write")
	descriptionUpdate := mcpStructured(t, descriptionResponse)["issue"].(map[string]any)
	if descriptionUpdate["title"] != "Title only" || descriptionUpdate["description"] != "Description only" {
		t.Fatalf("description-only update = %#v", descriptionUpdate)
	}
	now = now.Add(time.Hour)
	bothArgs := map[string]any{"id": "FLUENT-2", "title": "Both", "description": "Both **Markdown**"}
	bothResponse := doMCPRequest(t, mux, "tools/call", "update_issue", bothArgs, "write")
	bothUpdate := mcpStructured(t, bothResponse)["issue"].(map[string]any)
	if bothUpdate["title"] != "Both" || bothUpdate["description"] != "Both **Markdown**" {
		t.Fatalf("combined update = %#v", bothUpdate)
	}
	updated := bothUpdate["updated"]
	now = now.Add(time.Hour)
	repeatedUpdateResponse := doMCPRequest(t, mux, "tools/call", "update_issue", bothArgs, "write")
	if repeated := mcpStructured(t, repeatedUpdateResponse)["issue"].(map[string]any); repeated["updated"] != updated {
		t.Fatalf("identical update advanced timestamp: before=%v after=%v", updated, repeated["updated"])
	}
	emptyTitle := doMCPRequest(t, mux, "tools/call", "update_issue", map[string]any{"id": "FLUENT-2", "title": ""}, "write")
	if result := mcpResult(t, emptyTitle); result["isError"] != true || !strings.Contains(emptyTitle.Body.String(), "invalid request") {
		t.Fatalf("present empty title result=%#v body=%q", result, emptyTitle.Body.String())
	}
	emptyUpdate := doMCPRequest(t, mux, "tools/call", "update_issue", map[string]any{"id": "FLUENT-2"}, "write")
	if result := mcpResult(t, emptyUpdate); result["isError"] != true || !strings.Contains(emptyUpdate.Body.String(), "invalid request") {
		t.Fatalf("empty update result=%#v body=%q", result, emptyUpdate.Body.String())
	}

	now = now.Add(time.Hour)
	parentArgs := map[string]any{"id": "FLUENT-2", "parent_id": "FLUENT-1"}
	setParentResponse := doMCPRequest(t, mux, "tools/call", "set_issue_parent", parentArgs, "write")
	parented := mcpStructured(t, setParentResponse)["issue"].(map[string]any)
	if parented["parent_id"] != "FLUENT-1" {
		t.Fatalf("set parent = %#v", parented)
	}
	parentUpdated := parented["updated"]
	now = now.Add(time.Hour)
	repeatedParentResponse := doMCPRequest(t, mux, "tools/call", "set_issue_parent", parentArgs, "write")
	if repeated := mcpStructured(t, repeatedParentResponse)["issue"].(map[string]any); repeated["updated"] != parentUpdated {
		t.Fatalf("identical parent advanced timestamp: before=%v after=%v", parentUpdated, repeated["updated"])
	}
	cycle := doMCPRequest(t, mux, "tools/call", "set_issue_parent", map[string]any{"id": "FLUENT-1", "parent_id": "FLUENT-2"}, "write")
	if result := mcpResult(t, cycle); result["isError"] != true || !strings.Contains(cycle.Body.String(), "invalid request") {
		t.Fatalf("cycle result=%#v body=%q", result, cycle.Body.String())
	}
	missingParent := doMCPRequest(t, mux, "tools/call", "set_issue_parent", map[string]any{"id": "FLUENT-2"}, "write")
	if result := mcpResult(t, missingParent); result["isError"] != true {
		t.Fatalf("missing parent_id accepted: %#v", result)
	}
	now = now.Add(time.Hour)
	detachResponse := doMCPRequest(t, mux, "tools/call", "set_issue_parent", map[string]any{"id": "FLUENT-2", "parent_id": nil}, "write")
	if detached := mcpStructured(t, detachResponse)["issue"].(map[string]any); detached["parent_id"] != "" {
		t.Fatalf("detach = %#v", detached)
	}

	now = now.Add(time.Hour)
	closeResponse := doMCPRequest(t, mux, "tools/call", "close_issue", map[string]any{"id": "FLUENT-2"}, "write")
	closed := mcpStructured(t, closeResponse)["issue"].(map[string]any)
	if closed["state"] != "closed" {
		t.Fatalf("close = %#v", closed)
	}
	closedUpdated := closed["updated"]
	now = now.Add(time.Hour)
	repeatedCloseResponse := doMCPRequest(t, mux, "tools/call", "close_issue", map[string]any{"id": "FLUENT-2"}, "write")
	if repeated := mcpStructured(t, repeatedCloseResponse)["issue"].(map[string]any); repeated["state"] != "closed" || repeated["updated"] != closedUpdated {
		t.Fatalf("repeated close = %#v", repeated)
	}

	now = now.Add(time.Hour)
	reopenResponse := doMCPRequest(t, mux, "tools/call", "reopen_issue", map[string]any{"id": "FLUENT-2"}, "write")
	reopened := mcpStructured(t, reopenResponse)["issue"].(map[string]any)
	if reopened["state"] != "open" {
		t.Fatalf("reopen = %#v", reopened)
	}
	reopenedUpdated := reopened["updated"]
	now = now.Add(time.Hour)
	repeatedReopenResponse := doMCPRequest(t, mux, "tools/call", "reopen_issue", map[string]any{"id": "FLUENT-2"}, "write")
	if repeated := mcpStructured(t, repeatedReopenResponse)["issue"].(map[string]any); repeated["state"] != "open" || repeated["updated"] != reopenedUpdated {
		t.Fatalf("repeated reopen = %#v", repeated)
	}

	_ = parentDomain
	_ = targetDomain
}

func TestMCPCommentMutationsUseTrustedActorAndPreserveAuthor(t *testing.T) {
	repo := newMemoryRepository()
	svc := newMCPTestService(t, repo, newMemoryAssetStore(), defaultMCPVerifier)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	svc.now = func() time.Time { return now }
	if _, err := svc.CreateProject(context.Background(), "COMMENTS"); err != nil {
		t.Fatal(err)
	}
	issue, err := svc.CreateIssue(context.Background(), "COMMENTS", CreateIssueRequest{Title: "Comments", Description: "Markdown"})
	if err != nil {
		t.Fatal(err)
	}
	mux := registerMCPTestRoutes(t, svc)
	args := map[string]any{"id": issue.Ref, "body": "Comment **Markdown**"}

	firstResponse := doMCPRequest(t, mux, "tools/call", "add_comment", args, "write")
	first := mcpStructured(t, firstResponse)["comment"].(map[string]any)
	assertJSONKeys(t, first, "author", "body", "created", "id", "updated")
	if first["author"] != "person@example.test" || first["body"] != "Comment **Markdown**" {
		t.Fatalf("email-attributed comment = %#v", first)
	}
	second := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "add_comment", args, "write"))["comment"].(map[string]any)
	if second["id"] == first["id"] {
		t.Fatalf("identical add_comment was idempotent: first=%#v second=%#v", first, second)
	}
	fallback := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "add_comment", map[string]any{"id": issue.Ref, "body": "Subject"}, "subject-only"))["comment"].(map[string]any)
	if fallback["author"] != "subject-fallback" {
		t.Fatalf("subject fallback comment = %#v", fallback)
	}

	beforeRejected, err := svc.GetIssue(context.Background(), issue.Ref)
	if err != nil {
		t.Fatal(err)
	}
	rejected := doMCPRequest(t, mux, "tools/call", "add_comment", map[string]any{"id": issue.Ref, "body": "Forged", "author": "attacker@example.test"}, "write")
	if result := mcpResult(t, rejected); result["isError"] != true {
		t.Fatalf("forged author accepted: %#v", result)
	}
	afterRejected, err := svc.GetIssue(context.Background(), issue.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRejected.Comments) != len(beforeRejected.Comments) {
		t.Fatal("schema-rejected author input invoked AddComment")
	}

	now = now.Add(time.Hour)
	editArgs := map[string]any{"id": issue.Ref, "comment_id": first["id"], "body": "Replacement **Markdown**"}
	editedResponse := doMCPRequest(t, mux, "tools/call", "edit_comment", editArgs, "other-write")
	edited := mcpStructured(t, editedResponse)["comment"].(map[string]any)
	if edited["author"] != first["author"] || edited["body"] != "Replacement **Markdown**" {
		t.Fatalf("shared-workspace edit = %#v", edited)
	}
	editedUpdated := edited["updated"]
	now = now.Add(time.Hour)
	repeated := mcpStructured(t, doMCPRequest(t, mux, "tools/call", "edit_comment", editArgs, "other-write"))["comment"].(map[string]any)
	if repeated["author"] != first["author"] || repeated["updated"] != editedUpdated {
		t.Fatalf("identical edit changed author/timestamp: %#v", repeated)
	}

	missingActor := doMCPRequest(t, mux, "tools/call", "add_comment", args, "write-no-actor")
	if result := mcpResult(t, missingActor); result["isError"] != true || !strings.Contains(missingActor.Body.String(), "internal server error") {
		t.Fatalf("missing actor failure result=%#v body=%q", result, missingActor.Body.String())
	}
}

func TestMCPPerToolScopeStepUpAndActorBridge(t *testing.T) {
	base := newMemoryRepository()
	actor := &actorRepository{memoryRepository: base}
	svc := newMCPTestService(t, actor, newMemoryAssetStore(), defaultMCPVerifier)
	if _, err := svc.CreateProject(context.Background(), "ACTOR"); err != nil {
		t.Fatal(err)
	}
	mux := registerMCPTestRoutes(t, svc)

	denied := doMCPRequest(t, mux, "tools/call", "get_project", map[string]any{"project": "ACTOR"}, "none")
	result := mcpResult(t, denied)
	if result["isError"] != true {
		t.Fatalf("scope denial result = %#v", result)
	}
	meta := result["_meta"].(map[string]any)
	challenges := meta["mcp/www_authenticate"].([]any)
	if len(challenges) != 1 || !strings.Contains(challenges[0].(string), `resource_metadata="`+testMCPMetadata+`"`) || !strings.Contains(challenges[0].(string), `scope="tissues:read"`) || !strings.Contains(challenges[0].(string), `error="insufficient_scope"`) || !strings.Contains(challenges[0].(string), `error_description="Authorization requires the tissues:read scope."`) {
		t.Fatalf("scope challenge = %#v", challenges)
	}
	if actor.called {
		t.Fatal("domain method called despite insufficient scope")
	}

	doMCPRequest(t, mux, "tools/call", "get_project", map[string]any{"project": "ACTOR"}, "read")
	if actor.subject != "subject-1" || actor.email != "person@example.test" {
		t.Fatalf("actor context = %q / %q", actor.subject, actor.email)
	}
	actor.called = false
	doMCPRequest(t, mux, "tools/call", "get_project", map[string]any{"project": "ACTOR"}, "write")
	if !actor.called {
		t.Fatal("normal issued write-scope set could not call a read tool")
	}
}

func TestMCPInputAndDomainErrorsAreSafe(t *testing.T) {
	svc := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, svc)
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "get_project", args: map[string]any{"project": "bad key"}, want: "invalid request"},
		{name: "get_project", args: map[string]any{"project": "MISSING"}, want: "resource not found"},
		{name: "list_issue_images", args: map[string]any{"id": "bad"}, want: "invalid request"},
	} {
		response := doMCPRequest(t, mux, "tools/call", tc.name, tc.args, "read")
		result := mcpResult(t, response)
		if result["isError"] != true || !strings.Contains(response.Body.String(), tc.want) {
			t.Fatalf("%s result=%#v body=%q", tc.name, result, response.Body.String())
		}
	}

	secret := "projects/internal/opaque-index"
	failing := newMCPTestService(t, failingRepository{err: fmt.Errorf("%s unavailable", secret)}, newMemoryAssetStore(), defaultMCPVerifier)
	response := doMCPRequest(t, registerMCPTestRoutes(t, failing), "tools/call", "list_projects", nil, "read")
	if !strings.Contains(response.Body.String(), "internal server error") || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("unexpected error leaked: %q", response.Body.String())
	}
}

func TestMCPTransportHardening(t *testing.T) {
	svc := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, svc)

	crossOrigin := mcpJSONRequest("tools/list", "", nil, "read")
	crossOrigin.Header.Set("Origin", "https://evil.example")
	crossOrigin.Host = "tissues.example.test"
	crossRec := httptest.NewRecorder()
	mux.ServeHTTP(crossRec, crossOrigin)
	if crossRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%q", crossRec.Code, crossRec.Body.String())
	}

	oversize := mcpJSONRequest("tools/list", "", nil, "read")
	oversize.Body = ioNopCloser{Reader: strings.NewReader(strings.Repeat("x", mcpMaxBodyBytes+1))}
	overRec := httptest.NewRecorder()
	mux.ServeHTTP(overRec, oversize)
	if overRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%q", overRec.Code, overRec.Body.String())
	}

	wrongContentType := mcpJSONRequest("tools/list", "", nil, "read")
	wrongContentType.Header.Set("Content-Type", "text/plain")
	contentTypeRec := httptest.NewRecorder()
	mux.ServeHTTP(contentTypeRec, wrongContentType)
	if contentTypeRec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type status=%d body=%q", contentTypeRec.Code, contentTypeRec.Body.String())
	}

	sameOrigin := mcpJSONRequest("tools/list", "", nil, "read")
	sameOrigin.Host = "tissues.example.test"
	sameOrigin.Header.Set("Origin", "http://tissues.example.test")
	sameOriginRec := httptest.NewRecorder()
	mux.ServeHTTP(sameOriginRec, sameOrigin)
	if sameOriginRec.Code != http.StatusOK {
		t.Fatalf("same-origin status=%d body=%q", sameOriginRec.Code, sameOriginRec.Body.String())
	}

	rebinding := mcpJSONRequest("tools/list", "", nil, "read")
	rebinding.Host = "evil.example"
	rebinding = rebinding.WithContext(context.WithValue(rebinding.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}))
	rebindRec := httptest.NewRecorder()
	mux.ServeHTTP(rebindRec, rebinding)
	if rebindRec.Code != http.StatusForbidden || !strings.Contains(rebindRec.Body.String(), "invalid Host") {
		t.Fatalf("rebinding status=%d body=%q", rebindRec.Code, rebindRec.Body.String())
	}
}

func TestMCPVerifierRunsExactlyOncePerRequest(t *testing.T) {
	calls := 0
	verifier := func(ctx context.Context, token string) (MCPVerifiedToken, error) {
		calls++
		return defaultMCPVerifier(ctx, token)
	}
	svc := newMCPTestService(t, newMemoryRepository(), newMemoryAssetStore(), verifier)
	doMCPRequest(t, registerMCPTestRoutes(t, svc), "tools/list", "", nil, "read")
	if calls != 1 {
		t.Fatalf("verifier calls=%d, want 1", calls)
	}
}

func TestMCPPropagatesRequestCancellationToDomainWork(t *testing.T) {
	repo := &cancellationRepository{memoryRepository: newMemoryRepository(), started: make(chan struct{})}
	svc := newMCPTestService(t, repo, newMemoryAssetStore(), defaultMCPVerifier)
	mux := registerMCPTestRoutes(t, svc)
	request := mcpJSONRequest("tools/call", "list_projects", nil, "read")
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-repo.started:
	case <-time.After(2 * time.Second):
		t.Fatal("domain work did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled MCP request did not stop domain work")
	}
}

func TestBearerChallengeCompatibilityWriter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		challenge string
		want      string
	}{
		{name: "unauthorized", status: 401, challenge: `Bearer realm="mcp", resource_metadata="https://example.test/prm"`, want: `error="invalid_token"`},
		{name: "forbidden", status: 403, challenge: `Bearer scope="tissues:read"`, want: `error="insufficient_scope"`},
		{name: "preserve existing", status: 401, challenge: `Bearer realm="mcp", error="custom"`, want: `error="custom"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := addBearerChallengeErrors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", tc.challenge)
				w.Header().Set("X-Preserved", "yes")
				w.WriteHeader(tc.status)
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, mcpPath, nil))
			got := rec.Header().Get("WWW-Authenticate")
			if !strings.Contains(got, tc.want) || !strings.Contains(got, `realm="mcp"`) && tc.name != "forbidden" || rec.Header().Get("X-Preserved") != "yes" {
				t.Fatalf("challenge=%q headers=%v", got, rec.Header())
			}
			if tc.name == "preserve existing" && strings.Count(got, "error=") != 1 {
				t.Fatalf("existing error changed: %q", got)
			}
		})
	}
}

func TestBearerChallengeCompatibilityWriterBlackBoxesSDKMiddleware(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verifier  mcpauth.TokenVerifier
		scopes    []string
		status    int
		wantError string
	}{
		{
			name: "invalid token", status: http.StatusUnauthorized, wantError: `error="invalid_token"`,
			verifier: func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
				return nil, fmt.Errorf("%w: rejected", mcpauth.ErrInvalidToken)
			},
		},
		{
			name: "insufficient scope", status: http.StatusForbidden, scopes: []string{mcpScopeRead}, wantError: `error="insufficient_scope"`,
			verifier: func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
				return &mcpauth.TokenInfo{Expiration: time.Now().Add(time.Hour)}, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			verifier := func(ctx context.Context, token string, request *http.Request) (*mcpauth.TokenInfo, error) {
				calls++
				return tc.verifier(ctx, token, request)
			}
			sdk := mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{ResourceMetadataURL: testMCPMetadata, Scopes: tc.scopes})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, mcpPath, nil)
			request.Header.Set("Authorization", "Bearer token")
			addBearerChallengeErrors(sdk).ServeHTTP(recorder, request)
			challenge := recorder.Header().Get("WWW-Authenticate")
			if recorder.Code != tc.status || !strings.Contains(challenge, `resource_metadata="`+testMCPMetadata+`"`) || !strings.Contains(challenge, tc.wantError) || calls != 1 || strings.Count(challenge, "error=") != 1 {
				t.Fatalf("status=%d challenge=%q verifier calls=%d", recorder.Code, challenge, calls)
			}
			if tc.status == http.StatusForbidden && !strings.Contains(challenge, `scope="tissues:read"`) {
				t.Fatalf("scope challenge=%q", challenge)
			}
		})
	}
	calls := 0
	verifier := func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		calls++
		return &mcpauth.TokenInfo{Scopes: []string{mcpScopeRead}, Expiration: time.Now().Add(time.Hour)}, nil
	}
	sdk := mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{ResourceMetadataURL: testMCPMetadata, Scopes: []string{mcpScopeRead}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Success", "unchanged")
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, mcpPath, nil)
	request.Header.Set("Authorization", "Bearer token")
	addBearerChallengeErrors(sdk).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("WWW-Authenticate") != "" || recorder.Header().Get("X-Success") != "unchanged" || calls != 1 {
		t.Fatalf("success changed: status=%d headers=%v calls=%d", recorder.Code, recorder.Header(), calls)
	}
}

func TestToolSecurityDecoratorPassesNonListAndErrorsByteForByte(t *testing.T) {
	body := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n")
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	})
	for _, method := range []string{"tools/call", "tools/list"} {
		req := httptest.NewRequest(http.MethodPost, mcpPath, nil)
		req.Header.Set("Mcp-Protocol-Version", mcpProtocolLatest)
		req.Header.Set("Mcp-Method", method)
		rec := httptest.NewRecorder()
		decorateMCPToolSecuritySchemes(stub).ServeHTTP(rec, req)
		if !bytes.Equal(rec.Body.Bytes(), body) || rec.Code != http.StatusBadRequest {
			t.Fatalf("%s response changed: %d %q", method, rec.Code, rec.Body.String())
		}
	}
}

func defaultMCPVerifier(_ context.Context, token string) (MCPVerifiedToken, error) {
	switch token {
	case "read":
		return MCPVerifiedToken{Subject: "subject-1", Email: "person@example.test", ClientID: "client-1", Scopes: []string{mcpScopeRead}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "write":
		return MCPVerifiedToken{Subject: "subject-1", Email: "person@example.test", ClientID: "client-1", Scopes: []string{mcpScopeRead, mcpScopeWrite}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "subject-only":
		return MCPVerifiedToken{Subject: "subject-fallback", ClientID: "client-1", Scopes: []string{mcpScopeRead, mcpScopeWrite}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "other-write":
		return MCPVerifiedToken{Subject: "subject-2", Email: "other@example.test", ClientID: "client-2", Scopes: []string{mcpScopeRead, mcpScopeWrite}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "write-no-actor":
		return MCPVerifiedToken{ClientID: "client-1", Scopes: []string{mcpScopeRead, mcpScopeWrite}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "none":
		return MCPVerifiedToken{Subject: "subject-1", Scopes: nil, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "expired":
		return MCPVerifiedToken{Subject: "subject-1", Scopes: []string{mcpScopeRead}, ExpiresAt: time.Now().Add(-time.Hour)}, nil
	case "missing-expiration":
		return MCPVerifiedToken{Subject: "subject-1", Scopes: []string{mcpScopeRead}}, nil
	default:
		return MCPVerifiedToken{}, errors.New("rejected")
	}
}

func TestMCPTokenInfoAdapterCopiesClaims(t *testing.T) {
	expires := time.Unix(1234, 0)
	verified := MCPVerifiedToken{Subject: "subject-1", Email: "person@example.test", ClientID: "client-1", Scopes: []string{mcpScopeRead}, ExpiresAt: expires}
	info := toSDKTokenInfo(verified)
	if info.UserID != verified.Subject || !info.Expiration.Equal(expires) || !reflect.DeepEqual(info.Scopes, verified.Scopes) || info.Extra["email"] != verified.Email || info.Extra["client_id"] != verified.ClientID {
		t.Fatalf("token info = %#v", info)
	}
	verified.Scopes[0] = "mutated"
	if info.Scopes[0] != mcpScopeRead {
		t.Fatalf("token scopes were not copied: %#v", info.Scopes)
	}
}

func newMCPTestService(t *testing.T, repo Repository, assets AssetStore, verifier func(context.Context, string) (MCPVerifiedToken, error)) *Service {
	t.Helper()
	profile, err := coreconfig.NewServiceProfile("test", Config{})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := coreconfig.NewSlot(profile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(slot, repo, assets, WithMCPAuth(MCPAuth{Issuer: testMCPIssuer, Resource: testMCPResource, Verify: verifier}))
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	sequence := 0
	svc.newID = func() (string, error) {
		sequence++
		return fmt.Sprintf("aaaaaaaaaaaaaaaaaaaaaaaaa%c", 'a'+rune(sequence)), nil
	}
	return svc
}

func registerMCPTestRoutes(t *testing.T, svc *Service) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	return mux
}

func mcpJSONRequest(method, name string, arguments map[string]any, token string) *http.Request {
	params := map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    mcpProtocolLatest,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}}
	if name != "" {
		params["name"] = name
		if arguments != nil {
			params["arguments"] = arguments
		}
	}
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request := httptest.NewRequest(http.MethodPost, mcpPath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", mcpProtocolLatest)
	request.Header.Set("Mcp-Method", method)
	if name != "" {
		request.Header.Set("Mcp-Name", name)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func doMCPRequest(t *testing.T, handler http.Handler, method, name string, arguments map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mcpJSONRequest(method, name, arguments, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s/%s status=%d body=%q headers=%v", method, name, rec.Code, rec.Body.String(), rec.Header())
	}
	return rec
}

func mcpResult(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != nil {
		t.Fatalf("MCP error = %#v", body["error"])
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", body["result"])
	}
	return result
}

func mcpStructured(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	result := mcpResult(t, response)
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v", result)
	}
	return structured
}

func mcpToolsByName(t *testing.T, handler http.Handler) map[string]map[string]any {
	t.Helper()
	tools := mcpResult(t, doMCPRequest(t, handler, "tools/list", "", nil, "read"))["tools"].([]any)
	byName := make(map[string]map[string]any, len(tools))
	for _, raw := range tools {
		tool := raw.(map[string]any)
		byName[tool["name"].(string)] = tool
	}
	return byName
}

type actorRepository struct {
	*memoryRepository
	called  bool
	subject string
	email   string
}

type transactionCountingRepository struct {
	*memoryRepository
	transactions int
}

func (r *transactionCountingRepository) RunInTransaction(ctx context.Context, fn func(Transaction) error) error {
	r.transactions++
	return r.memoryRepository.RunInTransaction(ctx, fn)
}

func (r *actorRepository) GetProject(ctx context.Context, key string) (*Project, error) {
	r.called = true
	r.subject, _ = gcpauth.SubjectFromContext(ctx)
	r.email, _ = gcpauth.EmailFromContext(ctx)
	return r.memoryRepository.GetProject(ctx, key)
}

type failingRepository struct{ err error }

func (r failingRepository) ListProjectsPage(context.Context, PageRequest) (*ProjectPage, error) {
	return nil, r.err
}
func (r failingRepository) ListIssueOverviewsPage(context.Context, PageRequest) (*IssueOverviewPage, error) {
	return nil, r.err
}
func (r failingRepository) GetProject(context.Context, string) (*Project, error) { return nil, r.err }
func (r failingRepository) ListIssues(context.Context, string) ([]*Issue, error) { return nil, r.err }
func (r failingRepository) GetIssue(context.Context, IssueRef) (*Issue, error)   { return nil, r.err }
func (r failingRepository) RunInTransaction(context.Context, func(Transaction) error) error {
	return r.err
}

type cancellationRepository struct {
	*memoryRepository
	started chan struct{}
}

func (r *cancellationRepository) ListProjectsPage(ctx context.Context, _ PageRequest) (*ProjectPage, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }

func TestMCPToolCatalogNamesAreUnique(t *testing.T) {
	names := make([]string, 0, len(mcpToolCatalog))
	for _, spec := range mcpToolCatalog {
		names = append(names, spec.Name)
		if spec.OpenWorld {
			t.Fatalf("%s unexpectedly open-world", spec.Name)
		}
		if spec.ReadOnly {
			if spec.Scope != mcpScopeRead || spec.Destructive || !spec.Idempotent {
				t.Fatalf("read policy for %s = %#v", spec.Name, spec)
			}
		} else if spec.Scope != mcpScopeWrite {
			t.Fatalf("write policy for %s = %#v", spec.Name, spec)
		}
	}
	sort.Strings(names)
	want := []string{"add_comment", "close_issue", "create_issue", "create_project", "edit_comment", "get_issue", "get_project", "list_issue_images", "list_issues", "list_projects", "reopen_issue", "set_issue_parent", "update_issue"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names=%v want=%v", names, want)
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Fatalf("duplicate MCP tool %q", names[i])
		}
	}
}

func assertJSONKeys(t *testing.T, value map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v; value=%#v", got, want, value)
	}
}
