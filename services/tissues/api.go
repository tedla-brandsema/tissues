package tissues

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	gcpauth "github.com/tedla-brandsema/tissues/lib/gcp/auth"
)

const (
	apiBasePath = "/api/tissues/v1"
	maxJSONBody = 64 << 10
)

type projectDTO struct {
	Key     string `json:"key"`
	Created string `json:"created"`
}
type issueDTO struct {
	ID          string       `json:"id"`
	ProjectKey  string       `json:"project_key"`
	Number      int64        `json:"number"`
	Title       string       `json:"title"`
	State       State        `json:"state"`
	Created     string       `json:"created"`
	Updated     string       `json:"updated"`
	Description string       `json:"description"`
	ParentID    string       `json:"parent_id"`
	Children    []issueDTO   `json:"children"`
	Comments    []commentDTO `json:"comments"`
}
type issueOverviewDTO struct {
	ProjectKey string `json:"project_key"`
	Number     int64  `json:"number"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	State      State  `json:"state"`
	ParentID   string `json:"parent_id"`
	Updated    string `json:"updated"`
}
type commentDTO struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Body    string `json:"body"`
}
type errorEnvelope struct {
	Error struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
}
type createProjectPayload struct {
	Key string `json:"key"`
}
type createIssuePayload struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
}
type updateIssuePayload struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}
type parentPayload struct {
	ParentID *string `json:"parent_id"`
}
type createCommentPayload struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}
type editCommentPayload struct {
	Body *string `json:"body"`
}

func (s *Service) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiBasePath+"/projects", s.listProjectsHTTP)
	mux.HandleFunc("POST "+apiBasePath+"/projects", s.createProjectHTTP)
	mux.HandleFunc("GET "+apiBasePath+"/projects/{project}", s.getProjectHTTP)
	mux.HandleFunc("GET "+apiBasePath+"/projects/{project}/issues", s.listIssuesHTTP)
	mux.HandleFunc("POST "+apiBasePath+"/projects/{project}/issues", s.createIssueHTTP)
	mux.HandleFunc("GET "+apiBasePath+"/issues", s.listIssueOverviewsHTTP)
	mux.HandleFunc("GET "+apiBasePath+"/issues/{id}", s.getIssueHTTP)
	mux.HandleFunc("PATCH "+apiBasePath+"/issues/{id}", s.updateIssueHTTP)
	mux.HandleFunc("PUT "+apiBasePath+"/issues/{id}/parent", s.moveIssueHTTP)
	mux.HandleFunc("POST "+apiBasePath+"/issues/{id}/close", s.closeIssueHTTP)
	mux.HandleFunc("POST "+apiBasePath+"/issues/{id}/reopen", s.reopenIssueHTTP)
	mux.HandleFunc("POST "+apiBasePath+"/issues/{id}/comments", s.addCommentHTTP)
	mux.HandleFunc("PATCH "+apiBasePath+"/issues/{id}/comments/{commentID}", s.editCommentHTTP)
	return mux
}

func (s *Service) listProjectsHTTP(w http.ResponseWriter, r *http.Request) {
	size, err := pageSize(r)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	page, err := s.ListProjectsPage(r.Context(), size, r.URL.Query().Get("cursor"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	out := struct {
		Projects   []projectDTO `json:"projects"`
		NextCursor string       `json:"next_cursor"`
	}{Projects: make([]projectDTO, 0, len(page.Projects)), NextCursor: page.NextCursor}
	for _, project := range page.Projects {
		out.Projects = append(out.Projects, toProjectDTO(project))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Service) listIssueOverviewsHTTP(w http.ResponseWriter, r *http.Request) {
	size, err := pageSize(r)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	page, err := s.ListIssueOverviewsPage(r.Context(), size, r.URL.Query().Get("cursor"), r.URL.Query().Get("project"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	out := struct {
		Issues     []issueOverviewDTO `json:"issues"`
		NextCursor string             `json:"next_cursor"`
	}{Issues: make([]issueOverviewDTO, 0, len(page.Issues)), NextCursor: page.NextCursor}
	for _, issue := range page.Issues {
		out.Issues = append(out.Issues, toIssueOverviewDTO(issue))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Service) getProjectHTTP(w http.ResponseWriter, r *http.Request) {
	project, err := s.GetProject(r.Context(), r.PathValue("project"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProjectDTO(project))
}
func (s *Service) createProjectHTTP(w http.ResponseWriter, r *http.Request) {
	var payload createProjectPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	project, err := s.CreateProject(r.Context(), payload.Key)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toProjectDTO(project))
}
func (s *Service) listIssuesHTTP(w http.ResponseWriter, r *http.Request) {
	issues, err := s.ListIssues(r.Context(), r.PathValue("project"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	out := struct {
		Issues []issueDTO `json:"issues"`
	}{Issues: make([]issueDTO, 0, len(issues))}
	for _, issue := range issues {
		out.Issues = append(out.Issues, toIssueDTO(issue))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Service) getIssueHTTP(w http.ResponseWriter, r *http.Request) {
	issue, err := s.GetIssue(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIssueDTO(issue))
}
func (s *Service) createIssueHTTP(w http.ResponseWriter, r *http.Request) {
	var payload createIssuePayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	if strings.TrimSpace(payload.Title) == "" || payload.Description == nil {
		writeRequestError(w, errors.New("title and description are required"))
		return
	}
	issue, err := s.CreateIssue(r.Context(), r.PathValue("project"), CreateIssueRequest{Title: payload.Title, Description: *payload.Description})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toIssueDTO(issue))
}
func (s *Service) updateIssueHTTP(w http.ResponseWriter, r *http.Request) {
	var payload updateIssuePayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	if payload.Title == nil && payload.Description == nil {
		writeRequestError(w, errors.New("title or description is required"))
		return
	}
	issue, err := s.UpdateIssue(r.Context(), UpdateIssueRequest{Ref: r.PathValue("id"), Title: payload.Title, Description: payload.Description})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIssueDTO(issue))
}
func (s *Service) moveIssueHTTP(w http.ResponseWriter, r *http.Request) {
	var payload parentPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	if payload.ParentID == nil {
		writeRequestError(w, errors.New("parent_id is required"))
		return
	}
	issue, err := s.MoveIssue(r.Context(), r.PathValue("id"), *payload.ParentID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIssueDTO(issue))
}
func (s *Service) closeIssueHTTP(w http.ResponseWriter, r *http.Request) {
	if err := decodeJSON(w, r, &struct{}{}); err != nil {
		writeRequestError(w, err)
		return
	}
	issue, err := s.CloseIssue(r.Context(), r.PathValue("id"))
	s.writeStateResult(w, r, issue, err)
}
func (s *Service) reopenIssueHTTP(w http.ResponseWriter, r *http.Request) {
	if err := decodeJSON(w, r, &struct{}{}); err != nil {
		writeRequestError(w, err)
		return
	}
	issue, err := s.ReopenIssue(r.Context(), r.PathValue("id"))
	s.writeStateResult(w, r, issue, err)
}
func (s *Service) writeStateResult(w http.ResponseWriter, r *http.Request, issue *Issue, err error) {
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIssueDTO(issue))
}
func (s *Service) addCommentHTTP(w http.ResponseWriter, r *http.Request) {
	var payload createCommentPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	author := trustedAuthor(r)
	if author == "" {
		author = strings.TrimSpace(payload.Author)
	}
	if author == "" || strings.TrimSpace(payload.Body) == "" {
		writeRequestError(w, errors.New("author and body are required"))
		return
	}
	comment, err := s.AddComment(r.Context(), r.PathValue("id"), author, payload.Body)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCommentDTO(comment))
}
func (s *Service) editCommentHTTP(w http.ResponseWriter, r *http.Request) {
	var payload editCommentPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeRequestError(w, err)
		return
	}
	if payload.Body == nil || strings.TrimSpace(*payload.Body) == "" {
		writeRequestError(w, errors.New("body is required"))
		return
	}
	comment, err := s.EditComment(r.Context(), r.PathValue("id"), r.PathValue("commentID"), *payload.Body)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCommentDTO(comment))
}

func trustedAuthor(r *http.Request) string {
	if email, ok := gcpauth.EmailFromContext(r.Context()); ok {
		return email
	}
	if subject, ok := gcpauth.SubjectFromContext(r.Context()); ok {
		return subject
	}
	return ""
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("request body exceeds %d bytes", maxJSONBody)
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON document")
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}
func toProjectDTO(project *Project) projectDTO {
	return projectDTO{Key: project.Key, Created: formatJSONTime(project.Created)}
}
func toIssueOverviewDTO(issue *IssueOverview) issueOverviewDTO {
	return issueOverviewDTO{ProjectKey: issue.ProjectKey, Number: issue.Number, ID: issue.Ref, Title: issue.Title, State: issue.State, ParentID: issue.ParentRef, Updated: formatJSONTime(issue.Updated)}
}
func toIssueDTO(issue *Issue) issueDTO {
	children := make([]issueDTO, 0, len(issue.Children))
	for _, child := range issue.Children {
		children = append(children, toIssueDTO(child))
	}
	comments := make([]commentDTO, 0, len(issue.Comments))
	for _, comment := range issue.Comments {
		comments = append(comments, toCommentDTO(comment))
	}
	return issueDTO{ID: issue.Ref, ProjectKey: issue.ProjectKey, Number: issue.Number, Title: issue.Title, State: issue.State, Created: formatJSONTime(issue.Created), Updated: formatJSONTime(issue.Updated), Description: issue.Description, ParentID: issue.ParentRef, Children: children, Comments: comments}
}
func toCommentDTO(comment *Comment) commentDTO {
	return commentDTO{ID: comment.ID, Author: comment.Author, Created: formatJSONTime(comment.Created), Updated: formatJSONTime(comment.Updated), Body: comment.Body}
}
func formatJSONTime(value time.Time) string { return value.Format(time.RFC3339Nano) }
func pageSize(r *http.Request) (int, error) {
	value := r.URL.Query().Get("page_size")
	if value == "" {
		return DefaultPageSize, nil
	}
	size, err := strconv.Atoi(value)
	if err != nil || size <= 0 || size > MaxPageSize {
		return 0, fmt.Errorf("page_size must be between 1 and %d", MaxPageSize)
	}
	return size, nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeRequestError(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusBadRequest, "invalid", err.Error())
}
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeAPIError(w, http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	case errors.Is(err, ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, ErrConflict):
		writeAPIError(w, http.StatusConflict, "conflict", "request conflicts with current state")
	default:
		slog.ErrorContext(r.Context(), "tissues API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}
func writeAPIError(w http.ResponseWriter, status int, kind, message string) {
	var envelope errorEnvelope
	envelope.Error.Kind = kind
	envelope.Error.Message = message
	writeJSON(w, status, envelope)
}
