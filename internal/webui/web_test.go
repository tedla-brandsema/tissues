package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tedla-brandsema/tissues/internal/model"
	"github.com/tedla-brandsema/tissues/internal/service"
	"github.com/tedla-brandsema/tissues/internal/store"
)

const testHost = "127.0.0.1:8080"

func TestIndexAndIssuePages(t *testing.T) {
	dir, svc, h := newTestHandler(t, false)
	root := createIssue(t, svc, "", "Root <script>alert(1)</script>", "**bold**\n\n`code`\n\n- list\n\n[link](https://example.com)\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1))")
	child := createIssue(t, svc, root.ID, "Nested child", "Child body.")
	if _, err := svc.CloseIssue(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	comment, err := svc.AddComment(context.Background(), root.ID, `<img src=x onerror=alert(1)>`, "Comment **body**.")
	if err != nil {
		t.Fatal(err)
	}

	index := request(t, h, http.MethodGet, "/", nil, "")
	wantStatus(t, index, http.StatusOK)
	wantHTMLHeaders(t, index)
	indexBody := index.Body.String()
	for _, want := range []string{"tissues", "Root &lt;script&gt;alert(1)&lt;/script&gt;", "Nested child", `/issues/` + root.ID, `/issues/` + child.ID, "state-open", "state-closed", `action="/issues"`} {
		if !strings.Contains(indexBody, want) {
			t.Errorf("index missing %q:\n%s", want, indexBody)
		}
	}
	if strings.Count(indexBody, `<ul class="issue-tree">`) < 2 {
		t.Errorf("index does not render recursive nesting:\n%s", indexBody)
	}
	if strings.Contains(indexBody, `<a href="/issues/`+root.ID+`">`+root.ID+`</a>`) {
		t.Error("the immutable ID became the primary issue link label")
	}

	detail := request(t, h, http.MethodGet, "/issues/"+root.ID, nil, "")
	wantStatus(t, detail, http.StatusOK)
	body := detail.Body.String()
	for _, want := range []string{
		"Root &lt;script&gt;alert(1)&lt;/script&gt;", "state-open", "<strong>bold</strong>", "<code>code</code>", "<ul>",
		`<a href="https://example.com">link</a>`, "Nested child", `/issues/` + child.ID,
		`action="/issues/` + root.ID + `/close"`, `action="/issues/` + root.ID + `/update"`,
		`name="parent_id" value="` + root.ID + `"`, `action="/issues/` + root.ID + `/comments"`,
		"&lt;img src=x onerror=alert(1)&gt;", "Comment <strong>body</strong>", "Edit comment",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("raw Markdown/title script became active HTML:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), `href="javascript:`) {
		t.Errorf("dangerous Markdown URL was emitted as a link:\n%s", body)
	}
	if strings.Contains(body, `<img src=x onerror=alert(1)>`) {
		t.Errorf("comment author was not escaped:\n%s", body)
	}
	if !strings.Contains(body, `id="comment-`+comment.ID+`"`) {
		t.Error("comment anchor is missing")
	}
	if got := git(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("reads dirtied repository: %s", got)
	}
}

func TestFullHTMLMutationFlowAndReconstruction(t *testing.T) {
	dir, _, h := newTestHandler(t, false)

	rootResp := postForm(t, h, "/issues", url.Values{"title": {"Root"}, "description": {"Root body"}}, validOrigin)
	rootID := redirectedID(t, rootResp)
	childResp := postForm(t, h, "/issues", url.Values{"parent_id": {rootID}, "title": {"Child"}, "description": {"Before"}}, validOrigin)
	childID := redirectedID(t, childResp)

	wantRedirect(t, postForm(t, h, "/issues/"+childID+"/update", url.Values{"title": {"Renamed child"}, "description": {"After"}}, validOrigin), "/issues/"+childID)
	one := postForm(t, h, "/issues/"+childID+"/comments", url.Values{"author": {"one"}, "body": {"First"}}, validOrigin)
	wantStatus(t, one, http.StatusSeeOther)
	two := postForm(t, h, "/issues/"+childID+"/comments", url.Values{"author": {"two"}, "body": {"Second"}}, validOrigin)
	wantStatus(t, two, http.StatusSeeOther)

	svc, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.GetIssue(context.Background(), childID)
	if err != nil || len(before.Comments) != 2 {
		t.Fatalf("comments before edit = %+v, %v", before, err)
	}
	firstID := before.Comments[0].ID
	wantRedirect(t, postForm(t, h, "/issues/"+childID+"/comments/"+firstID+"/edit", url.Values{"body": {"First edited"}}, validOrigin), "/issues/"+childID+"#comment-"+firstID)
	wantRedirect(t, postForm(t, h, "/issues/"+childID+"/close", url.Values{}, validOrigin), "/issues/"+childID)

	base := commitCount(t, dir)
	wantRedirect(t, postForm(t, h, "/issues/"+childID+"/close", url.Values{}, validOrigin), "/issues/"+childID)
	if got := commitCount(t, dir); got != base {
		t.Errorf("idempotent close created a commit: got %d, want %d", got, base)
	}
	wantRedirect(t, postForm(t, h, "/issues/"+childID+"/reopen", url.Values{}, validOrigin), "/issues/"+childID)

	if got := commitCount(t, dir); got != 8 {
		t.Errorf("effective mutation commits = %d, want 8\n%s", got, git(t, dir, "log", "--format=%s"))
	}
	if got := git(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("final repository is dirty: %s", got)
	}
	fresh, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fresh.GetIssue(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Renamed child" || got.Description != "After" || got.State != "open" || got.ParentID != rootID {
		t.Errorf("reconstructed issue = %+v", got)
	}
	if len(got.Comments) != 2 || got.Comments[0].ID != firstID || got.Comments[0].Body != "First edited" || got.Comments[1].Body != "Second" {
		t.Errorf("reconstructed comment order/content = %+v", got.Comments)
	}
}

func TestOriginProtection(t *testing.T) {
	dir, _, h := newTestHandler(t, false)
	cases := []struct {
		name, origin, host string
	}{
		{"missing", "", testHost},
		{"foreign", "https://evil.example", testHost},
		{"null", "null", testHost},
		{"malformed", "://bad", testHost},
		{"non-loopback", "http://example.com", "example.com"},
		{"host mismatch", "http://127.0.0.1:9999", testHost},
		{"scheme mismatch", "https://127.0.0.1:8080", testHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := commitCount(t, dir)
			r := formRequest(t, h, "/issues", url.Values{"title": {"Blocked"}}, tc.origin, tc.host, "application/x-www-form-urlencoded")
			wantStatus(t, r, http.StatusForbidden)
			wantHTMLHeaders(t, r)
			if got := commitCount(t, dir); got != before {
				t.Errorf("rejected origin committed: %d -> %d", before, got)
			}
			if got := git(t, dir, "status", "--porcelain"); got != "" {
				t.Errorf("rejected origin changed canonical files: %s", got)
			}
		})
	}

	allowed := []struct{ origin, host string }{
		{"http://127.0.0.1:8080", testHost},
		{"http://localhost:8080", "localhost:8080"},
		{"http://[::1]:8080", "[::1]:8080"},
	}
	for i, tc := range allowed {
		r := formRequest(t, h, "/issues", url.Values{"title": {"Allowed " + string(rune('A'+i))}}, tc.origin, tc.host, "application/x-www-form-urlencoded")
		wantStatus(t, r, http.StatusSeeOther)
	}
	if got := commitCount(t, dir); got != len(allowed) {
		t.Errorf("allowed-origin commits = %d, want %d", got, len(allowed))
	}
}

func TestHTMLErrorMapping(t *testing.T) {
	t.Run("validation and not found", func(t *testing.T) {
		_, _, h := newTestHandler(t, false)
		invalid := postForm(t, h, "/issues", url.Values{"title": {""}}, validOrigin)
		wantStatus(t, invalid, http.StatusBadRequest)
		wantHTMLHeaders(t, invalid)
		missing := request(t, h, http.MethodGet, "/issues/"+store.NewID(), nil, "")
		wantStatus(t, missing, http.StatusNotFound)
		if !strings.Contains(missing.Body.String(), "Not found") {
			t.Errorf("404 is not a human HTML error: %s", missing.Body.String())
		}
	})

	t.Run("form media type", func(t *testing.T) {
		_, _, h := newTestHandler(t, false)
		r := formRequest(t, h, "/issues", url.Values{"title": {"Wrong transport"}}, validOrigin, testHost, "application/json")
		wantStatus(t, r, http.StatusBadRequest)
	})

	t.Run("dirty repository", func(t *testing.T) {
		dir, _, h := newTestHandler(t, false)
		if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := postForm(t, h, "/issues", url.Values{"title": {"Refused"}}, validOrigin)
		wantStatus(t, r, http.StatusConflict)
		if commitCount(t, dir) != 0 {
			t.Error("repository refusal committed")
		}
	})

	t.Run("incomplete transaction", func(t *testing.T) {
		dir, _, h := newTestHandler(t, false)
		hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := postForm(t, h, "/issues", url.Values{"title": {"Written"}}, validOrigin)
		wantStatus(t, r, http.StatusInternalServerError)
		for _, phrase := range []string{"Files were written", "commit did not complete", "Repair the repository"} {
			if !strings.Contains(r.Body.String(), phrase) {
				t.Errorf("incomplete page missing %q: %s", phrase, r.Body.String())
			}
		}
		if commitCount(t, dir) != 0 || git(t, dir, "diff", "--cached", "--name-only") == "" {
			t.Error("incomplete transaction evidence is wrong")
		}
	})
}

func TestNotPushedIsACommittedHTMLWarning(t *testing.T) {
	dir, _, h := newTestHandler(t, true)
	r := postForm(t, h, "/issues", url.Values{"title": {"Durable locally"}}, validOrigin)
	wantStatus(t, r, http.StatusBadGateway)
	wantHTMLHeaders(t, r)
	body := r.Body.String()
	for _, phrase := range []string{"committed locally", "publication", "Do not submit the form again"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("not-pushed warning missing %q: %s", phrase, body)
		}
	}
	if commitCount(t, dir) != 1 || git(t, dir, "status", "--porcelain") != "" {
		t.Fatal("not_pushed did not leave one clean local commit")
	}
	fresh, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := fresh.ListIssues(context.Background())
	if err != nil || len(issues) != 1 {
		t.Fatalf("fresh service did not reconstruct mutation: %+v, %v", issues, err)
	}
	if !strings.Contains(body, `/issues/`+issues[0].ID) {
		t.Errorf("warning lacks resulting issue link: %s", body)
	}
}

func TestClosedIssueShowsOnlyReopen(t *testing.T) {
	_, svc, h := newTestHandler(t, false)
	issue := createIssue(t, svc, "", "Closed", "")
	if _, err := svc.CloseIssue(context.Background(), issue.ID); err != nil {
		t.Fatal(err)
	}
	r := request(t, h, http.MethodGet, "/issues/"+issue.ID, nil, "")
	body := r.Body.String()
	if !strings.Contains(body, `/reopen"`) || strings.Contains(body, `/close"`) {
		t.Errorf("closed issue actions are wrong: %s", body)
	}
}

const validOrigin = "http://" + testHost

func newTestHandler(t *testing.T, remote bool) (string, *service.Service, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "tissues"},
		{"config", "user.email", "tissues@example"},
		{"config", "commit.gpgsign", "false"},
	} {
		git(t, dir, args...)
	}
	svc, err := service.New(context.Background(), dir, remote)
	if err != nil {
		t.Fatal(err)
	}
	return dir, svc, New(svc)
}

func createIssue(t *testing.T, svc *service.Service, parent, title, description string) *model.Issue {
	t.Helper()
	issue, err := svc.CreateIssue(context.Background(), service.CreateIssueRequest{ParentID: parent, Title: title, Description: description})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func postForm(t *testing.T, h http.Handler, path string, values url.Values, origin string) *httptest.ResponseRecorder {
	t.Helper()
	return formRequest(t, h, path, values, origin, testHost, "application/x-www-form-urlencoded")
}

func formRequest(t *testing.T, h http.Handler, path string, values url.Values, origin, host, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func request(t *testing.T, h http.Handler, method, path string, body io.Reader, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Host = testHost
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func wantStatus(t *testing.T, rr *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status = %d, want %d: %s", rr.Code, status, rr.Body.String())
	}
}

func wantRedirect(t *testing.T, rr *httptest.ResponseRecorder, location string) {
	t.Helper()
	wantStatus(t, rr, http.StatusSeeOther)
	if got := rr.Header().Get("Location"); got != location {
		t.Errorf("Location = %q, want %q", got, location)
	}
}

func redirectedID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	wantStatus(t, rr, http.StatusSeeOther)
	location := rr.Header().Get("Location")
	id := strings.TrimPrefix(location, "/issues/")
	if len(id) != 26 || strings.Contains(id, "/") {
		t.Fatalf("create Location = %q", location)
	}
	return id
}

func wantHTMLHeaders(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if got := rr.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rr.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return 0
	}
	out := git(t, dir, "rev-list", "--count", "HEAD")
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
