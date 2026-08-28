package tissues

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectAndIssueIDAPIRoutes(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	handler := svc.apiHandler()
	var project projectDTO
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]string{"key": " fluent "}, http.StatusCreated, &project)
	if project.Key != "FLUENT" || project.Created == "" {
		t.Fatalf("project = %#v", project)
	}
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]string{"key": "FLUENT"}, http.StatusConflict, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]string{"key": "bad-key"}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]string{"key": "TISSUES"}, http.StatusCreated, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects/FLUENT", nil, http.StatusOK, &project)
	var projects struct {
		Projects   []projectDTO `json:"projects"`
		NextCursor string       `json:"next_cursor"`
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects?page_size=25", nil, http.StatusOK, &projects)
	if len(projects.Projects) != 2 || projects.Projects[0].Key != "FLUENT" || projects.Projects[1].Key != "TISSUES" {
		t.Fatalf("projects = %#v", projects)
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects", nil, http.StatusOK, &projects)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects?page_size=100", nil, http.StatusOK, &projects)

	var first, second, foreign issueDTO
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/FLUENT/issues", map[string]string{"title": "First", "description": "# markdown"}, http.StatusCreated, &first)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/FLUENT/issues", map[string]string{"title": "Second", "description": "body"}, http.StatusCreated, &second)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/TISSUES/issues", map[string]string{"title": "Foreign", "description": "body"}, http.StatusCreated, &foreign)
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_id": "FLUENT-1"}, http.StatusOK, &second)
	if first.ID != "FLUENT-1" || first.Number != 1 || first.ProjectKey != "FLUENT" {
		t.Fatalf("first = %#v", first)
	}
	if second.ID != "FLUENT-2" || second.ParentID != first.ID {
		t.Fatalf("second = %#v", second)
	}
	if foreign.ID != "TISSUES-1" {
		t.Fatalf("foreign = %#v", foreign)
	}

	var raw map[string]any
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues/FLUENT-2", nil, http.StatusOK, &raw)
	if raw["id"] != "FLUENT-2" || raw["parent_id"] != "FLUENT-1" {
		t.Fatalf("Issue IDs = %#v", raw)
	}
	for _, forbidden := range []string{"ref", "parent_ref"} {
		if _, exists := raw[forbidden]; exists {
			t.Fatalf("public Issue exposed %s: %#v", forbidden, raw)
		}
	}
	var list struct {
		Issues []issueDTO `json:"issues"`
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects/FLUENT/issues", nil, http.StatusOK, &list)
	if len(list.Issues) != 1 || len(list.Issues[0].Children) != 1 {
		t.Fatalf("tree = %#v", list)
	}

	title := "Updated"
	apiRequest(t, handler, http.MethodPatch, "/api/tissues/v1/issues/FLUENT-2", map[string]any{"title": title}, http.StatusOK, &second)
	if second.Title != title || second.ID != "FLUENT-2" {
		t.Fatalf("updated = %#v", second)
	}
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_id": ""}, http.StatusOK, &second)
	if second.ParentID != "" {
		t.Fatalf("detached = %#v", second)
	}
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_id": "FLUENT-1"}, http.StatusOK, &second)
	description := "atomic body"
	apiRequest(t, handler, http.MethodPatch, "/api/tissues/v1/issues/FLUENT-2", map[string]any{"title": "Atomic", "description": description}, http.StatusOK, &second)
	if second.Title != "Atomic" || second.Description != description || second.ParentID != "FLUENT-1" {
		t.Fatalf("content update = %#v", second)
	}
	apiRequest(t, handler, http.MethodPatch, "/api/tissues/v1/issues/FLUENT-2", map[string]any{"title": "Rejected", "parent_id": ""}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/FLUENT/issues", map[string]any{"title": "Rejected", "description": "body", "parent_id": ""}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/issues/FLUENT-2/close", map[string]any{}, http.StatusOK, &second)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/issues/FLUENT-2/reopen", map[string]any{}, http.StatusOK, &second)
	var comment commentDTO
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/issues/FLUENT-2/comments", map[string]string{"author": "Ada", "body": "note"}, http.StatusCreated, &comment)
	apiRequest(t, handler, http.MethodPatch, "/api/tissues/v1/issues/FLUENT-2/comments/"+comment.ID, map[string]string{"body": "edited"}, http.StatusOK, &comment)
	if comment.Body != "edited" {
		t.Fatalf("comment = %#v", comment)
	}

	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues/not-a-ref", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues/FLUENT-999", nil, http.StatusNotFound, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues/%23FLUENT-2", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_id": "#FLUENT-1"}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_id": "TISSUES-1"}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPatch, "/api/tissues/v1/issues/FLUENT-2", map[string]string{"parent_ref": "#FLUENT-1"}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPut, "/api/tissues/v1/issues/FLUENT-2/parent", map[string]string{"parent_ref": "#FLUENT-1"}, http.StatusBadRequest, nil)
	var overview struct {
		Issues     []map[string]any `json:"issues"`
		NextCursor string           `json:"next_cursor"`
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?page_size=2", nil, http.StatusOK, &overview)
	if len(overview.Issues) != 2 || overview.NextCursor == "" {
		t.Fatalf("overview page = %#v", overview)
	}
	for _, item := range overview.Issues {
		for _, forbidden := range []string{"ref", "parent_ref", "children", "comments", "description"} {
			if _, exists := item[forbidden]; exists {
				t.Fatalf("overview exposed %s: %#v", forbidden, item)
			}
		}
	}
	if overview.Issues[1]["id"] != "FLUENT-2" || overview.Issues[1]["parent_id"] != "FLUENT-1" {
		t.Fatalf("overview Issue IDs = %#v", overview.Issues[1])
	}
	var lastOverview struct {
		Issues     []map[string]any `json:"issues"`
		NextCursor string           `json:"next_cursor"`
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?page_size=2&cursor="+overview.NextCursor, nil, http.StatusOK, &lastOverview)
	if len(lastOverview.Issues) != 1 || lastOverview.NextCursor != "" {
		t.Fatalf("last overview page = %#v", lastOverview)
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?page_size=2&project=FLUENT", nil, http.StatusOK, &lastOverview)
	if len(lastOverview.Issues) != 2 || lastOverview.NextCursor != "" {
		t.Fatalf("filtered overview = %#v", lastOverview)
	}
	for _, item := range lastOverview.Issues {
		if item["project_key"] != "FLUENT" {
			t.Fatalf("filtered overview leaked Project: %#v", item)
		}
	}
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?project=bad-key", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?project=MISSING", nil, http.StatusNotFound, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects?page_size=0", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/projects?page_size=101", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?page_size=nope", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodGet, "/api/tissues/v1/issues?page_size=2&cursor=invalid", nil, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/issues", map[string]any{}, http.StatusMethodNotAllowed, nil)
}

func TestAPIStrictJSONAndRedaction(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	handler := svc.apiHandler()
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]any{"key": "FLUENT", "extra": true}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects", map[string]string{"key": "FLUENT"}, http.StatusCreated, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/FLUENT/issues", map[string]string{"title": "Old field", "description": "body", "parent_ref": ""}, http.StatusBadRequest, nil)
	apiRequest(t, handler, http.MethodPost, "/api/tissues/v1/projects/FLUENT/issues", map[string]string{"title": "Old field", "description": "body", "parent_id": ""}, http.StatusBadRequest, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tissues/v1/projects", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("content type = %d", response.Code)
	}
	writeResponse := httptest.NewRecorder()
	writeServiceError(writeResponse, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("provider secret detail"))
	if strings.Contains(writeResponse.Body.String(), "provider secret") || !strings.Contains(writeResponse.Body.String(), "internal server error") {
		t.Fatalf("redaction = %s", writeResponse.Body.String())
	}
}

func apiRequest(t *testing.T, handler http.Handler, method, path string, body any, status int, target any) {
	t.Helper()
	var input *bytes.Reader
	if body == nil {
		input = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, input)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %s: %v", response.Body.String(), err)
		}
	}
}

var _ Repository = (*memoryRepository)(nil)
