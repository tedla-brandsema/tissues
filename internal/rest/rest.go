// Package rest is a thin HTTP transport adapter over service.Service.
//
// It decodes requests, encodes responses and maps service outcome classes to
// status codes. It owns no issue or comment semantics: everything domain-
// related lives in internal/service.
package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/service"
)

// New returns the tissues HTTP API. There is no authentication: see the
// security note in README.md and docs/SPEC.md.
func New(s *service.Service) http.Handler {
	h := &handler{svc: s}
	mux := http.NewServeMux()
	// Patterns are registered by path, not by method, so that an unsupported
	// method reaches this package and gets the same JSON error envelope as
	// everything else. Method-qualified patterns would let the mux answer in
	// plain text instead.
	mux.HandleFunc("/api/issues", h.issues)
	mux.HandleFunc("/api/issues/{id}", h.issue)
	mux.HandleFunc("/api/issues/{id}/parent", h.parent)
	mux.HandleFunc("/api/issues/{id}/close", h.close)
	mux.HandleFunc("/api/issues/{id}/reopen", h.reopen)
	mux.HandleFunc("/api/issues/{id}/comments", h.comments)
	mux.HandleFunc("/api/issues/{id}/comments/{commentID}", h.comment)
	// Anything else below /api/ is an API 404, in JSON.
	mux.HandleFunc("/api/", h.noSuchPath)
	return mux
}

type handler struct{ svc *service.Service }

// --- method dispatch --------------------------------------------------------
//
// One small switch per path. The supported combinations are exactly the nine
// service operations; everything else is 405.

func (h *handler) issues(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listIssues(w, r)
	case http.MethodPost:
		h.createIssue(w, r)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (h *handler) issue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getIssue(w, r)
	case http.MethodPut:
		h.updateIssue(w, r)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPut)
	}
}

func (h *handler) close(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	h.closeIssue(w, r)
}

func (h *handler) parent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, r, http.MethodPut)
		return
	}
	h.moveIssue(w, r)
}

func (h *handler) reopen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	h.reopenIssue(w, r)
}

func (h *handler) comments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	h.addComment(w, r)
}

func (h *handler) comment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, r, http.MethodPut)
		return
	}
	h.editComment(w, r)
}

func (h *handler) noSuchPath(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorResponse{
		Code:  CodeNotFound,
		Error: "no such API path: " + r.URL.Path,
	})
}

// --- transport representation -----------------------------------------------
//
// Transport shapes live here so that internal/model stays free of JSON tags:
// the domain does not know it is served over HTTP.

type issueJSON struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	State       string        `json:"state"`
	Created     time.Time     `json:"created"`
	Updated     time.Time     `json:"updated"`
	Description string        `json:"description"`
	ParentID    string        `json:"parent_id"`
	Children    []issueJSON   `json:"children"`
	Comments    []commentJSON `json:"comments"`
}

type commentJSON struct {
	ID      string    `json:"id"`
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Body    string    `json:"body"`
}

func toIssue(i *model.Issue) issueJSON {
	// Children and comments are always arrays, never null.
	children := make([]issueJSON, 0, len(i.Children))
	for _, c := range i.Children {
		children = append(children, toIssue(c))
	}
	comments := make([]commentJSON, 0, len(i.Comments))
	for _, c := range i.Comments {
		comments = append(comments, toComment(c))
	}
	return issueJSON{
		ID:          i.ID,
		Title:       i.Title,
		State:       string(i.State),
		Created:     i.Created,
		Updated:     i.Updated,
		Description: i.Description,
		ParentID:    i.ParentID,
		Children:    children,
		Comments:    comments,
	}
}

func toComment(c *model.Comment) commentJSON {
	return commentJSON{ID: c.ID, Author: c.Author, Created: c.Created, Updated: c.Updated, Body: c.Body}
}

func toIssues(is []*model.Issue) []issueJSON {
	out := make([]issueJSON, 0, len(is))
	for _, i := range is {
		out = append(out, toIssue(i))
	}
	return out
}

type issueListResponse struct {
	Issues []issueJSON `json:"issues"`
}

// --- requests ---------------------------------------------------------------

type createIssueBody struct {
	ParentID    string `json:"parent_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Pointers distinguish "omitted" from "set to empty": an omitted field is
// left untouched by the service.
type updateIssueBody struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type moveIssueBody struct {
	ParentID *string `json:"parent_id"`
}

type addCommentBody struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

type editCommentBody struct {
	Body string `json:"body"`
}

// decode reads exactly one JSON object into v. Malformed JSON, a body that is
// not a JSON object, unknown fields and a trailing second value are all
// rejected.
//
// The value is taken as raw JSON first and checked for a leading '{' before
// being decoded into v. That check has to be explicit: encoding/json happily
// accepts null for a struct pointer and leaves the struct at its zero value,
// which for an update request would look exactly like "every field omitted"
// and succeed as a no-op.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("malformed request body: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("request body must be a JSON object")
	}
	obj := json.NewDecoder(bytes.NewReader(raw))
	obj.DisallowUnknownFields()
	if err := obj.Decode(v); err != nil {
		return fmt.Errorf("malformed request body: %w", err)
	}
	return nil
}

// --- handlers ---------------------------------------------------------------

func (h *handler) listIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := h.svc.ListIssues(r.Context())
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, issueListResponse{Issues: toIssues(issues)})
}

func (h *handler) getIssue(w http.ResponseWriter, r *http.Request) {
	iss, err := h.svc.GetIssue(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, toIssue(iss))
}

func (h *handler) createIssue(w http.ResponseWriter, r *http.Request) {
	var body createIssueBody
	if err := decode(r, &body); err != nil {
		writeBadRequest(w, err)
		return
	}
	iss, err := h.svc.CreateIssue(r.Context(), service.CreateIssueRequest{
		ParentID:    body.ParentID,
		Title:       body.Title,
		Description: body.Description,
	})
	h.respondIssue(w, http.StatusCreated, iss, err)
}

func (h *handler) updateIssue(w http.ResponseWriter, r *http.Request) {
	var body updateIssueBody
	if err := decode(r, &body); err != nil {
		writeBadRequest(w, err)
		return
	}
	iss, err := h.svc.UpdateIssue(r.Context(), service.UpdateIssueRequest{
		ID:          r.PathValue("id"),
		Title:       body.Title,
		Description: body.Description,
	})
	h.respondIssue(w, http.StatusOK, iss, err)
}

func (h *handler) moveIssue(w http.ResponseWriter, r *http.Request) {
	var body moveIssueBody
	if err := decode(r, &body); err != nil {
		writeBadRequest(w, err)
		return
	}
	if body.ParentID == nil {
		writeBadRequest(w, errors.New("parent_id is required"))
		return
	}
	iss, err := h.svc.MoveIssue(r.Context(), r.PathValue("id"), *body.ParentID)
	h.respondIssue(w, http.StatusOK, iss, err)
}

func (h *handler) closeIssue(w http.ResponseWriter, r *http.Request) {
	iss, err := h.svc.CloseIssue(r.Context(), r.PathValue("id"))
	h.respondIssue(w, http.StatusOK, iss, err)
}

func (h *handler) reopenIssue(w http.ResponseWriter, r *http.Request) {
	iss, err := h.svc.ReopenIssue(r.Context(), r.PathValue("id"))
	h.respondIssue(w, http.StatusOK, iss, err)
}

func (h *handler) addComment(w http.ResponseWriter, r *http.Request) {
	var body addCommentBody
	if err := decode(r, &body); err != nil {
		writeBadRequest(w, err)
		return
	}
	c, err := h.svc.AddComment(r.Context(), r.PathValue("id"), body.Author, body.Body)
	h.respondComment(w, http.StatusCreated, c, err)
}

func (h *handler) editComment(w http.ResponseWriter, r *http.Request) {
	var body editCommentBody
	if err := decode(r, &body); err != nil {
		writeBadRequest(w, err)
		return
	}
	c, err := h.svc.EditComment(r.Context(), r.PathValue("id"), r.PathValue("commentID"), body.Body)
	h.respondComment(w, http.StatusOK, c, err)
}

// respondIssue and respondComment carry the one case where the service
// returns both a result and an error: the mutation is committed locally but
// was not pushed, and the caller must be given the committed object.
func (h *handler) respondIssue(w http.ResponseWriter, code int, iss *model.Issue, err error) {
	if err != nil {
		var result any
		if iss != nil {
			result = toIssue(iss)
		}
		writeError(w, err, result)
		return
	}
	writeJSON(w, code, toIssue(iss))
}

func (h *handler) respondComment(w http.ResponseWriter, code int, c *model.Comment, err error) {
	if err != nil {
		var result any
		if c != nil {
			result = toComment(c)
		}
		writeError(w, err, result)
		return
	}
	writeJSON(w, code, toComment(c))
}

// --- errors -----------------------------------------------------------------

// The complete set of externally visible error codes.
const (
	CodeNotFound           = "not_found"
	CodeInvalidRequest     = "invalid_request"
	CodeRepositoryUnusable = "repository_unusable"
	CodeIncomplete         = "incomplete"
	CodeNotPushed          = "not_pushed"
	CodeInternal           = "internal"
)

type errorResponse struct {
	Code   string `json:"code"`
	Error  string `json:"error"`
	Result any    `json:"result,omitempty"`
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, errorResponse{
		Code:  CodeInvalidRequest,
		Error: fmt.Sprintf("method not allowed: %s is not supported on %s", r.Method, r.URL.Path),
	})
}

func writeBadRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, errorResponse{Code: CodeInvalidRequest, Error: err.Error()})
}

// writeError maps a service outcome class to a status code. result is
// non-nil only for ErrNotPushed, where the mutation is committed and durable.
func writeError(w http.ResponseWriter, err error, result any) {
	status, code, message := classify(err)
	resp := errorResponse{Code: code, Error: message}
	if code == CodeNotPushed {
		resp.Result = result
	}
	writeJSON(w, status, resp)
}

func classify(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound, CodeNotFound, err.Error()
	case errors.Is(err, service.ErrValidation):
		return http.StatusBadRequest, CodeInvalidRequest, err.Error()
	case errors.Is(err, service.ErrRepository):
		return http.StatusConflict, CodeRepositoryUnusable, err.Error()
	case errors.Is(err, service.ErrIncomplete):
		return http.StatusInternalServerError, CodeIncomplete,
			"canonical files may have been written to the working tree but the intended Git commit " +
				"was not completed; the repository needs manual repair before further changes: " + err.Error()
	case errors.Is(err, service.ErrNotPushed):
		return http.StatusBadGateway, CodeNotPushed,
			"the change is committed in the local Git repository but could not be published to the " +
				"remote; do not retry this request, the mutation already exists: " + err.Error()
	default:
		return http.StatusInternalServerError, CodeInternal, err.Error()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent, so a late encoding failure can only be
	// dropped; there is nothing useful left to say to the client.
	_ = json.NewEncoder(w).Encode(v)
}
