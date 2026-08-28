package tissues

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

func TestAPILifecycleRoutes(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	base := time.Date(2026, 8, 28, 9, 30, 0, 123456789, time.UTC)
	svc.now = func() time.Time { return base }
	ids := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccc", "dddddddddddddddddddddddddd"}
	svc.newID = func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	handler := registeredHandler(t, svc)

	a := apiIssue(t, handler, http.MethodPost, apiBasePath+"/issues", `{"title":"Alpha","description":"A"}`, http.StatusCreated)
	b := apiIssue(t, handler, http.MethodPost, apiBasePath+"/issues", `{"title":"Beta","description":"B"}`, http.StatusCreated)
	c := apiIssue(t, handler, http.MethodPost, apiBasePath+"/issues", `{"title":"Gamma","description":"C"}`, http.StatusCreated)
	if a.Created != base.Format(time.RFC3339Nano) || a.Updated != a.Created {
		t.Fatalf("timestamp = %q/%q", a.Created, a.Updated)
	}

	list := apiRequest(t, handler, http.MethodGet, apiBasePath+"/issues", "", http.StatusOK)
	var listed struct {
		Issues []issueDTO `json:"issues"`
	}
	decodeResponse(t, list, &listed)
	if len(listed.Issues) != 3 {
		t.Fatalf("issues = %d, want 3", len(listed.Issues))
	}
	got := apiIssue(t, handler, http.MethodGet, apiBasePath+"/issues/"+b.ID, "", http.StatusOK)
	if got.Title != "Beta" {
		t.Fatalf("get = %#v", got)
	}

	updated := apiIssue(t, handler, http.MethodPatch, apiBasePath+"/issues/"+b.ID, `{"title":"Beta edited","description":"updated"}`, http.StatusOK)
	if updated.Title != "Beta edited" || updated.Description != "updated" {
		t.Fatalf("update = %#v", updated)
	}

	for _, parentID := range []string{a.ID, c.ID, "", a.ID} {
		moved := apiIssue(t, handler, http.MethodPut, apiBasePath+"/issues/"+b.ID+"/parent", `{"parent_id":"`+parentID+`"}`, http.StatusOK)
		if moved.ParentID != parentID {
			t.Fatalf("parent = %q, want %q", moved.ParentID, parentID)
		}
	}

	closed := apiIssue(t, handler, http.MethodPost, apiBasePath+"/issues/"+b.ID+"/close", `{}`, http.StatusOK)
	if closed.State != StateClosed {
		t.Fatalf("closed state = %q", closed.State)
	}
	reopened := apiIssue(t, handler, http.MethodPost, apiBasePath+"/issues/"+b.ID+"/reopen", `{}`, http.StatusOK)
	if reopened.State != StateOpen {
		t.Fatalf("reopened state = %q", reopened.State)
	}

	commentResponse := apiRequest(t, handler, http.MethodPost, apiBasePath+"/issues/"+b.ID+"/comments", `{"author":"local person","body":"hello **world**"}`, http.StatusCreated)
	var comment commentDTO
	decodeResponse(t, commentResponse, &comment)
	if comment.Author != "local person" || comment.Body != "hello **world**" {
		t.Fatalf("comment = %#v", comment)
	}
	editedResponse := apiRequest(t, handler, http.MethodPatch, apiBasePath+"/issues/"+b.ID+"/comments/"+comment.ID, `{"body":"edited"}`, http.StatusOK)
	var edited commentDTO
	decodeResponse(t, editedResponse, &edited)
	if edited.ID != comment.ID || edited.Body != "edited" || edited.Created != comment.Created {
		t.Fatalf("edited = %#v", edited)
	}

	apiErrorKind(t, handler, http.MethodPut, apiBasePath+"/issues/"+b.ID+"/parent", `{"parent_id":"`+b.ID+`"}`, http.StatusBadRequest, "invalid")
	apiErrorKind(t, handler, http.MethodPut, apiBasePath+"/issues/"+a.ID+"/parent", `{"parent_id":"`+b.ID+`"}`, http.StatusBadRequest, "invalid")
}

func TestAPIStrictJSONFailures(t *testing.T) {
	handler := registeredHandler(t, testService(t, newMemoryRepository()))
	cases := []struct {
		name, body string
	}{
		{"malformed", `{"title":`},
		{"unknown field", `{"title":"x","description":"","extra":true}`},
		{"multiple documents", `{"title":"x","description":""} {}`},
		{"missing description", `{"title":"x"}`},
		{"oversized", `{"title":"x","description":"` + strings.Repeat("x", maxJSONBody) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiErrorKind(t, handler, http.MethodPost, apiBasePath+"/issues", tc.body, http.StatusBadRequest, "invalid")
		})
	}

	request := httptest.NewRequest(http.MethodPost, apiBasePath+"/issues", strings.NewReader(`{"title":"x","description":""}`))
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("media type status = %d", recorder.Code)
	}

	stateRequest := httptest.NewRequest(http.MethodPost, apiBasePath+"/issues/missing/close", nil)
	stateRecorder := httptest.NewRecorder()
	handler.ServeHTTP(stateRecorder, stateRequest)
	if stateRecorder.Code != http.StatusBadRequest {
		t.Fatalf("state mutation without JSON status = %d", stateRecorder.Code)
	}
}

func TestAPIErrorMappingAndInternalRedaction(t *testing.T) {
	missing := registeredHandler(t, testService(t, newMemoryRepository()))
	apiErrorKind(t, missing, http.MethodGet, apiBasePath+"/issues/missing", "", http.StatusNotFound, "not_found")

	conflictService := testService(t, failureRepository{err: ErrConflict})
	conflictService.newID = func() (string, error) { return "aaaaaaaaaaaaaaaaaaaaaaaaaa", nil }
	apiErrorKind(t, registeredHandler(t, conflictService), http.MethodPost, apiBasePath+"/issues", `{"title":"x","description":""}`, http.StatusConflict, "conflict")

	internal := registeredHandler(t, testService(t, failureRepository{err: errors.New("datastore project secret-path diagnostic")}))
	response := apiRequest(t, internal, http.MethodGet, apiBasePath+"/issues", "", http.StatusInternalServerError)
	if strings.Contains(response.Body.String(), "datastore") || strings.Contains(response.Body.String(), "secret-path") {
		t.Fatalf("internal response leaked detail: %s", response.Body.String())
	}
	var envelope errorEnvelope
	decodeResponse(t, response, &envelope)
	if envelope.Error.Kind != "internal" || envelope.Error.Message != "internal server error" {
		t.Fatalf("error = %#v", envelope)
	}
}

func TestAPICommentAuthorUsesTrustedIdentity(t *testing.T) {
	svc := testService(t, newMemoryRepository())
	ids := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"}
	svc.newID = func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	issue, err := svc.CreateIssue(context.Background(), CreateIssueRequest{Title: "Issue"})
	if err != nil {
		t.Fatal(err)
	}
	handler := registeredHandler(t, svc)
	request := httptest.NewRequest(http.MethodPost, apiBasePath+"/issues/"+issue.ID+"/comments", strings.NewReader(`{"author":"spoofed","body":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(gcpauth.WithEmail(gcpauth.WithSubject(request.Context(), "subject-1"), "trusted@example.test"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var comment commentDTO
	decodeResponse(t, recorder, &comment)
	if comment.Author != "trusted@example.test" {
		t.Fatalf("author = %q", comment.Author)
	}

	apiErrorKind(t, handler, http.MethodPost, apiBasePath+"/issues/"+issue.ID+"/comments", `{"body":"no author"}`, http.StatusBadRequest, "invalid")
}

func registeredHandler(t *testing.T, svc *Service) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	if err := svc.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
	return mux
}

func apiIssue(t *testing.T, handler http.Handler, method, path, body string, status int) issueDTO {
	t.Helper()
	response := apiRequest(t, handler, method, path, body, status)
	var issue issueDTO
	decodeResponse(t, response, &issue)
	return issue
}

func apiErrorKind(t *testing.T, handler http.Handler, method, path, body string, status int, kind string) {
	t.Helper()
	response := apiRequest(t, handler, method, path, body, status)
	var envelope errorEnvelope
	decodeResponse(t, response, &envelope)
	if envelope.Error.Kind != kind || envelope.Error.Message == "" {
		t.Fatalf("error = %#v", envelope)
	}
}

func apiRequest(t *testing.T, handler http.Handler, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	return recorder
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatal(err)
	}
}

type failureRepository struct{ err error }

func (r failureRepository) ListIssues(context.Context) ([]*Issue, error) {
	return nil, r.err
}
func (r failureRepository) GetIssue(context.Context, string) (*Issue, error) {
	return nil, r.err
}
func (r failureRepository) RunInTransaction(context.Context, func(Transaction) error) error {
	return r.err
}
