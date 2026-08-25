// Package webui is the server-rendered browser adapter over service.Service.
package webui

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/service"
	"github.com/yuin/goldmark"
)

//go:embed templates/*.html static/style.css
var files embed.FS

const contentSecurityPolicy = "default-src 'self'; script-src 'none'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type handler struct {
	svc       *service.Service
	templates *template.Template
	markdown  goldmark.Markdown
	css       []byte
}

type indexPage struct {
	Title  string
	Issues []treeIssue
}

type treeIssue struct {
	ID       string
	Title    string
	State    model.State
	Children []treeIssue
}

type issuePage struct {
	Title       string
	ID          string
	State       model.State
	Created     string
	Updated     string
	ParentID    string
	Description template.HTML
	Source      string
	Children    []treeIssue
	Comments    []commentView
}

type commentView struct {
	ID          string
	Author      string
	Created     string
	Updated     string
	ShowUpdated bool
	Body        template.HTML
	Source      string
}

type errorPage struct {
	Title      string
	Message    string
	ActionURL  string
	ActionText string
}

// New returns the browser UI. It owns HTTP presentation only; all issue and
// comment behavior remains in the supplied service.
func New(s *service.Service) http.Handler {
	t := template.Must(template.ParseFS(files, "templates/*.html"))
	css := mustRead("static/style.css")
	h := &handler{svc: s, templates: t, markdown: goldmark.New(), css: css}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /assets/style.css", h.stylesheet)
	mux.HandleFunc("GET /issues/{id}", h.issue)
	mux.HandleFunc("POST /issues", h.createIssue)
	mux.HandleFunc("POST /issues/{id}/update", h.updateIssue)
	mux.HandleFunc("POST /issues/{id}/close", h.closeIssue)
	mux.HandleFunc("POST /issues/{id}/reopen", h.reopenIssue)
	mux.HandleFunc("POST /issues/{id}/comments", h.addComment)
	mux.HandleFunc("POST /issues/{id}/comments/{commentID}/edit", h.editComment)
	mux.HandleFunc("/", h.notFound)
	return h.secure(mux)
}

func mustRead(name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

func (h *handler) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodPost && !sameLoopbackOrigin(r) {
			h.writeErrorPage(w, http.StatusForbidden, "Request blocked", "Mutation forms are accepted only from this local tissues page.", "", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameLoopbackOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" || raw == "null" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	if u.Scheme != requestScheme {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	issues, err := h.svc.ListIssues(r.Context())
	if err != nil {
		h.writeServiceError(w, err, "")
		return
	}
	page := indexPage{Title: "tissues", Issues: make([]treeIssue, 0, len(issues))}
	for _, issue := range issues {
		page.Issues = append(page.Issues, toTreeIssue(issue))
	}
	h.render(w, http.StatusOK, "index.html", page)
}

func toTreeIssue(issue *model.Issue) treeIssue {
	v := treeIssue{ID: issue.ID, Title: issue.Title, State: issue.State, Children: make([]treeIssue, 0, len(issue.Children))}
	for _, child := range issue.Children {
		v.Children = append(v.Children, toTreeIssue(child))
	}
	return v
}

func (h *handler) issue(w http.ResponseWriter, r *http.Request) {
	issue, err := h.svc.GetIssue(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, err, r.PathValue("id"))
		return
	}
	page, err := h.toIssuePage(issue)
	if err != nil {
		h.writeServiceError(w, err, issue.ID)
		return
	}
	h.render(w, http.StatusOK, "issue.html", page)
}

func (h *handler) toIssuePage(issue *model.Issue) (issuePage, error) {
	description, err := h.renderMarkdown(issue.Description)
	if err != nil {
		return issuePage{}, err
	}
	page := issuePage{
		Title: issue.Title, ID: issue.ID, State: issue.State,
		Created: formatTime(issue.Created), Updated: formatTime(issue.Updated), ParentID: issue.ParentID,
		Description: description, Source: issue.Description,
		Children: make([]treeIssue, 0, len(issue.Children)), Comments: make([]commentView, 0, len(issue.Comments)),
	}
	for _, child := range issue.Children {
		page.Children = append(page.Children, toTreeIssue(child))
	}
	for _, comment := range issue.Comments {
		body, err := h.renderMarkdown(comment.Body)
		if err != nil {
			return issuePage{}, err
		}
		page.Comments = append(page.Comments, commentView{
			ID: comment.ID, Author: comment.Author, Created: formatTime(comment.Created), Updated: formatTime(comment.Updated),
			ShowUpdated: !comment.Updated.Equal(comment.Created), Body: body, Source: comment.Body,
		})
	}
	return page, nil
}

func (h *handler) renderMarkdown(source string) (template.HTML, error) {
	var out bytes.Buffer
	if err := h.markdown.Convert([]byte(source), &out); err != nil {
		return "", fmt.Errorf("render Markdown: %w", err)
	}
	// Goldmark's default renderer omits raw HTML and dangerous URLs. Only its
	// deliberately safe output may cross the template.HTML boundary.
	return template.HTML(out.String()), nil
}

func formatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

func (h *handler) createIssue(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	issue, err := h.svc.CreateIssue(r.Context(), service.CreateIssueRequest{
		ParentID: r.PostForm.Get("parent_id"), Title: r.PostForm.Get("title"), Description: r.PostForm.Get("description"),
	})
	if err != nil {
		id := ""
		if issue != nil {
			id = issue.ID
		}
		h.writeServiceError(w, err, id)
		return
	}
	h.redirectIssue(w, r, issue.ID, "")
}

func (h *handler) updateIssue(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	title, description := r.PostForm.Get("title"), r.PostForm.Get("description")
	_, err := h.svc.UpdateIssue(r.Context(), service.UpdateIssueRequest{ID: id, Title: &title, Description: &description})
	if err != nil {
		h.writeServiceError(w, err, id)
		return
	}
	h.redirectIssue(w, r, id, "")
}

func (h *handler) closeIssue(w http.ResponseWriter, r *http.Request) {
	h.changeState(w, r, h.svc.CloseIssue)
}

func (h *handler) reopenIssue(w http.ResponseWriter, r *http.Request) {
	h.changeState(w, r, h.svc.ReopenIssue)
}

func (h *handler) changeState(w http.ResponseWriter, r *http.Request, change func(context.Context, string) (*model.Issue, error)) {
	if !h.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	_, err := change(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err, id)
		return
	}
	h.redirectIssue(w, r, id, "")
}

func (h *handler) addComment(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	comment, err := h.svc.AddComment(r.Context(), id, r.PostForm.Get("author"), r.PostForm.Get("body"))
	if err != nil {
		h.writeServiceError(w, err, id)
		return
	}
	h.redirectIssue(w, r, id, "comment-"+comment.ID)
}

func (h *handler) editComment(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	id, commentID := r.PathValue("id"), r.PathValue("commentID")
	_, err := h.svc.EditComment(r.Context(), id, commentID, r.PostForm.Get("body"))
	if err != nil {
		h.writeServiceError(w, err, id)
		return
	}
	h.redirectIssue(w, r, id, "comment-"+commentID)
}

func (h *handler) parseForm(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		h.writeErrorPage(w, http.StatusBadRequest, "Invalid request", "Browser forms must use application/x-www-form-urlencoded.", "", "")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.writeErrorPage(w, http.StatusBadRequest, "Invalid request", "The submitted form could not be read.", "", "")
		return false
	}
	return true
}

func (h *handler) redirectIssue(w http.ResponseWriter, r *http.Request, id, fragment string) {
	target := "/issues/" + url.PathEscape(id)
	if fragment != "" {
		target += "#" + fragment
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *handler) writeServiceError(w http.ResponseWriter, err error, issueID string) {
	if errors.Is(err, service.ErrNotPushed) {
		target := ""
		if issueID != "" {
			target = "/issues/" + url.PathEscape(issueID)
		}
		h.writeErrorPage(w, http.StatusBadGateway, "Committed locally; publication failed",
			"The mutation is committed locally, but publication to the Git remote failed. Do not submit the form again. Repair Git normally; a later successful mutation can publish the backlog.", target, "View the committed issue")
		return
	}
	status, title, message := http.StatusInternalServerError, "Unexpected error", "The request could not be completed."
	switch {
	case errors.Is(err, service.ErrNotFound):
		status, title, message = http.StatusNotFound, "Not found", err.Error()
	case errors.Is(err, service.ErrValidation):
		status, title, message = http.StatusBadRequest, "Invalid request", err.Error()
	case errors.Is(err, service.ErrRepository):
		status, title, message = http.StatusConflict, "Repository unavailable", err.Error()
	case errors.Is(err, service.ErrIncomplete):
		status, title, message = http.StatusInternalServerError, "Mutation incomplete", "Files were written but the Git commit did not complete. Repair the repository before trying another mutation."
	}
	h.writeErrorPage(w, status, title, message, "", "")
}

func (h *handler) notFound(w http.ResponseWriter, _ *http.Request) {
	h.writeErrorPage(w, http.StatusNotFound, "Not found", "No such browser page.", "/", "Return to issues")
}

func (h *handler) stylesheet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.css)
}

func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	var out bytes.Buffer
	if err := h.templates.ExecuteTemplate(&out, name, data); err != nil {
		h.writeErrorPage(w, http.StatusInternalServerError, "Template error", "The page could not be rendered.", "", "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}

func (h *handler) writeErrorPage(w http.ResponseWriter, status int, title, message, actionURL, actionText string) {
	var out bytes.Buffer
	data := errorPage{Title: title, Message: message, ActionURL: actionURL, ActionText: actionText}
	if err := h.templates.ExecuteTemplate(&out, "error.html", data); err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}
