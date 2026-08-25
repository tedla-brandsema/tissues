package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tedla-brandsema/tissues/internal/service"
	"github.com/tedla-brandsema/tissues/internal/store"
)

// --- fixtures ---------------------------------------------------------------

// git runs a raw git command to build fixtures and to inspect the repository
// independently of the code under test.
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

// newServer wires the real service over a real temporary Git repository.
// Nothing is mocked: every request below produces real Markdown and real
// commits.
func newServer(t *testing.T, dir string, remoteSync bool) *httptest.Server {
	t.Helper()
	svc, err := service.New(context.Background(), dir, remoteSync)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(svc))
	t.Cleanup(srv.Close)
	return srv
}

type reply struct {
	status int
	body   []byte
}

func (r reply) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decoding %s: %v", r.body, err)
	}
}

func (r reply) field(t *testing.T, name string) any {
	t.Helper()
	var m map[string]any
	r.decode(t, &m)
	return m[name]
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) reply {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: Content-Type = %q, want application/json", method, path, ct)
	}
	return reply{status: resp.StatusCode, body: data}
}

func want(t *testing.T, r reply, status int, method, path string) reply {
	t.Helper()
	if r.status != status {
		t.Fatalf("%s %s: status = %d, want %d\nbody: %s", method, path, r.status, status, r.body)
	}
	return r
}

// assertError checks the stable error envelope.
func assertError(t *testing.T, r reply, status int, code string) map[string]any {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d\nbody: %s", r.status, status, r.body)
	}
	var m map[string]any
	r.decode(t, &m)
	if m["code"] != code {
		t.Errorf("code = %v, want %q\nbody: %s", m["code"], code, r.body)
	}
	if msg, _ := m["error"].(string); msg == "" {
		t.Errorf("error message is empty\nbody: %s", r.body)
	}
	return m
}

func id(t *testing.T, r reply) string {
	t.Helper()
	var m map[string]any
	r.decode(t, &m)
	s, ok := m["id"].(string)
	if !ok || s == "" {
		t.Fatalf("no id in response: %s", r.body)
	}
	return s
}

// --- the full semantic path over HTTP ---------------------------------------

func TestFullRESTFlow(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)

	root := id(t, want(t, do(t, srv, "POST", "/api/issues",
		`{"title":"Root issue","description":"Root description."}`), 201, "POST", "/api/issues"))

	child := id(t, want(t, do(t, srv, "POST", "/api/issues",
		`{"parent_id":"`+root+`","title":"Child issue"}`), 201, "POST", "/api/issues"))

	// List returns the hierarchy under an "issues" key.
	var list struct {
		Issues []map[string]any `json:"issues"`
	}
	want(t, do(t, srv, "GET", "/api/issues", ""), 200, "GET", "/api/issues").decode(t, &list)
	if len(list.Issues) != 1 || list.Issues[0]["id"] != root {
		t.Fatalf("list = %v, want the single root issue", list.Issues)
	}
	kids, _ := list.Issues[0]["children"].([]any)
	if len(kids) != 1 {
		t.Fatalf("root has %d children, want 1", len(kids))
	}

	// Get, update.
	got := want(t, do(t, srv, "GET", "/api/issues/"+child, ""), 200, "GET", "/api/issues/{id}")
	if got.field(t, "parent_id") != root {
		t.Errorf("parent_id = %v, want %q", got.field(t, "parent_id"), root)
	}
	updated := want(t, do(t, srv, "PUT", "/api/issues/"+child,
		`{"title":"Renamed child","description":"Rewritten."}`), 200, "PUT", "/api/issues/{id}")
	if updated.field(t, "title") != "Renamed child" {
		t.Errorf("title = %v", updated.field(t, "title"))
	}

	// Comments.
	first := id(t, want(t, do(t, srv, "POST", "/api/issues/"+child+"/comments",
		`{"author":"human@example","body":"First comment."}`), 201, "POST", "comments"))
	second := id(t, want(t, do(t, srv, "POST", "/api/issues/"+child+"/comments",
		`{"author":"agent@example","body":"Second comment."}`), 201, "POST", "comments"))

	// Record the presentation order before the edit. Both comments may well
	// share a Created second, in which case the ID tie-break decides; either
	// way the order is deterministic and the edit must not disturb it.
	before := commentOrder(t, do(t, srv, "GET", "/api/issues/"+child, ""))
	if len(before) != 2 {
		t.Fatalf("comments = %v, want two", before)
	}
	if !contains(before, first) || !contains(before, second) {
		t.Fatalf("comments = %v, want both %s and %s", before, first, second)
	}

	edited := want(t, do(t, srv, "PUT", "/api/issues/"+child+"/comments/"+first,
		`{"body":"First comment, edited."}`), 200, "PUT", "comment")
	if edited.field(t, "author") != "human@example" {
		t.Errorf("edit changed the author: %v", edited.field(t, "author"))
	}

	// Editing a comment must never move it.
	afterEdit := want(t, do(t, srv, "GET", "/api/issues/"+child, ""), 200, "GET", "issue")
	var issue issueJSON
	afterEdit.decode(t, &issue)
	if after := commentOrder(t, afterEdit); !equal(after, before) {
		t.Fatalf("comment order changed after an edit: %v, was %v", after, before)
	}
	for _, c := range issue.Comments {
		if c.ID == first && c.Body != "First comment, edited." {
			t.Errorf("edited body = %q", c.Body)
		}
	}

	// Close, close again (no-op), reopen.
	closed := want(t, do(t, srv, "POST", "/api/issues/"+child+"/close", ""), 200, "POST", "close")
	if closed.field(t, "state") != "closed" {
		t.Errorf("state = %v, want closed", closed.field(t, "state"))
	}
	beforeNoop := commitCount(t, dir)
	again := want(t, do(t, srv, "POST", "/api/issues/"+child+"/close", ""), 200, "POST", "close")
	if again.field(t, "state") != "closed" {
		t.Errorf("idempotent close returned state %v", again.field(t, "state"))
	}
	if got := commitCount(t, dir); got != beforeNoop {
		t.Errorf("an idempotent close committed: %d, want %d", got, beforeNoop)
	}
	reopened := want(t, do(t, srv, "POST", "/api/issues/"+child+"/reopen", ""), 200, "POST", "reopen")
	if reopened.field(t, "state") != "open" {
		t.Errorf("state = %v, want open", reopened.field(t, "state"))
	}

	// One commit per changed operation: create, create, update, comment,
	// comment, edit, close, reopen.
	if got := commitCount(t, dir); got != 8 {
		t.Errorf("commits = %d, want 8", got)
		t.Logf("log:\n%s", git(t, dir, "log", "--reverse", "--format=%s"))
	}
	if s := git(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("repository is dirty after the flow:\n%s", s)
	}

	// Everything must be reconstructable from the repository alone.
	fresh, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := fresh.GetIssue(context.Background(), child)
	if err != nil {
		t.Fatalf("reconstruction failed: %v", err)
	}
	if rebuilt.Title != "Renamed child" || string(rebuilt.State) != "open" || rebuilt.ParentID != root {
		t.Errorf("reconstructed issue = %+v", rebuilt)
	}
	if len(rebuilt.Comments) != 2 {
		t.Fatalf("reconstructed comments = %+v", rebuilt.Comments)
	}
	rebuiltOrder := []string{rebuilt.Comments[0].ID, rebuilt.Comments[1].ID}
	if !equal(rebuiltOrder, before) {
		t.Errorf("reconstructed comment order = %v, want %v", rebuiltOrder, before)
	}
}

func TestMoveIssueThroughREST(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	a := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Alpha"}`))
	b := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Beta"}`))
	c := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Gamma","parent_id":"`+a+`"}`))

	moved := want(t, do(t, srv, "PUT", "/api/issues/"+a+"/parent", `{"parent_id":"`+b+`"}`), 200, "PUT", "move")
	if moved.field(t, "parent_id") != b {
		t.Fatalf("moved parent_id = %v", moved.field(t, "parent_id"))
	}
	assertError(t, do(t, srv, "PUT", "/api/issues/"+b+"/parent", `{"parent_id":"`+c+`"}`), 400, CodeInvalidRequest)
	detached := want(t, do(t, srv, "PUT", "/api/issues/"+a+"/parent", `{"parent_id":""}`), 200, "PUT", "detach")
	if detached.field(t, "parent_id") != "" {
		t.Fatalf("detached parent_id = %v", detached.field(t, "parent_id"))
	}
	assertError(t, do(t, srv, "PUT", "/api/issues/"+a+"/parent", `{}`), 400, CodeInvalidRequest)
	assertError(t, do(t, srv, "PUT", "/api/issues/"+a+"/parent", `{"parent_id":"","extra":true}`), 400, CodeInvalidRequest)
	if got := git(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("repository dirty: %s", got)
	}
}

func commentOrder(t *testing.T, r reply) []string {
	t.Helper()
	var issue issueJSON
	r.decode(t, &issue)
	ids := make([]string, len(issue.Comments))
	for i, c := range issue.Comments {
		ids[i] = c.ID
	}
	return ids
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- transport shape --------------------------------------------------------

func TestTransportRepresentation(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	root := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Parent","description":"Body."}`))
	child := id(t, do(t, srv, "POST", "/api/issues", `{"parent_id":"`+root+`","title":"Child"}`))
	commentID := id(t, do(t, srv, "POST", "/api/issues/"+root+"/comments", `{"author":"a@example","body":"Hi."}`))

	r := want(t, do(t, srv, "GET", "/api/issues/"+root, ""), 200, "GET", "issue")
	body := string(r.body)

	for _, name := range []string{"id", "title", "state", "created", "updated", "description", "parent_id", "children", "comments"} {
		if !strings.Contains(body, `"`+name+`"`) {
			t.Errorf("issue JSON is missing %q:\n%s", name, body)
		}
	}
	for _, leaked := range []string{"ParentID", "Created", "Updated", "Description", "Children", "Comments", "ID"} {
		if strings.Contains(body, `"`+leaked+`"`) {
			t.Errorf("Go field name %q leaked into JSON:\n%s", leaked, body)
		}
	}

	var m map[string]any
	r.decode(t, &m)
	if m["parent_id"] != "" {
		t.Errorf("root parent_id = %v, want the empty string", m["parent_id"])
	}
	for _, ts := range []string{"created", "updated"} {
		s, ok := m[ts].(string)
		if !ok {
			t.Fatalf("%s is not a JSON string: %v", ts, m[ts])
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Errorf("%s = %q is not RFC3339: %v", ts, s, err)
		}
	}
	// The child carries its real parent.
	kids := m["children"].([]any)
	if len(kids) != 1 || kids[0].(map[string]any)["parent_id"] != root {
		t.Errorf("child parent_id = %v, want %q", kids[0].(map[string]any)["parent_id"], root)
	}
	comments := m["comments"].([]any)
	if len(comments) != 1 || comments[0].(map[string]any)["id"] != commentID {
		t.Errorf("comments = %v", comments)
	}

	// Empty collections must be [], never null, at every level.
	leaf := want(t, do(t, srv, "GET", "/api/issues/"+child, ""), 200, "GET", "issue")
	if !strings.Contains(string(leaf.body), `"children":[]`) {
		t.Errorf("empty children is not []:\n%s", leaf.body)
	}
	if !strings.Contains(string(leaf.body), `"comments":[]`) {
		t.Errorf("empty comments is not []:\n%s", leaf.body)
	}
	if strings.Contains(string(leaf.body), "null") {
		t.Errorf("response contains null:\n%s", leaf.body)
	}

	// A comment response exposes exactly the comment fields.
	cr := want(t, do(t, srv, "PUT", "/api/issues/"+root+"/comments/"+commentID, `{"body":"Edited."}`), 200, "PUT", "comment")
	var cm map[string]any
	cr.decode(t, &cm)
	got := make([]string, 0, len(cm))
	for k := range cm {
		got = append(got, k)
	}
	if len(got) != 5 {
		t.Errorf("comment fields = %v, want exactly id, author, created, updated, body", got)
	}
	for _, name := range []string{"id", "author", "created", "updated", "body"} {
		if _, ok := cm[name]; !ok {
			t.Errorf("comment JSON is missing %q: %s", name, cr.body)
		}
	}

	// Empty issue list is still an array under "issues".
	empty := newServer(t, initRepo(t), false)
	el := want(t, do(t, empty, "GET", "/api/issues", ""), 200, "GET", "/api/issues")
	if strings.TrimSpace(string(el.body)) != `{"issues":[]}` {
		t.Errorf("empty list = %s, want {\"issues\":[]}", el.body)
	}
}

// --- strict request decoding ------------------------------------------------

func TestStrictJSONDecoding(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Host"}`))
	comment := id(t, do(t, srv, "POST", "/api/issues/"+issue+"/comments", `{"author":"a@example","body":"Hi."}`))
	base := commitCount(t, dir)

	cases := []struct{ name, method, path, body string }{
		{"malformed json", "POST", "/api/issues", `{"title":`},
		{"empty body", "POST", "/api/issues", ``},

		// A request body must be a JSON object. null is the dangerous one:
		// encoding/json accepts it for a struct pointer and leaves the struct
		// zero-valued, which made PUT look like a successful no-op update.
		{"null on create", "POST", "/api/issues", `null`},
		{"null on update", "PUT", "/api/issues/" + issue, `null`},
		{"null on move", "PUT", "/api/issues/" + issue + "/parent", `null`},
		{"null on add comment", "POST", "/api/issues/" + issue + "/comments", `null`},
		{"null on edit comment", "PUT", "/api/issues/" + issue + "/comments/" + comment, `null`},
		{"null with whitespace", "PUT", "/api/issues/" + issue, "  \n null \n "},

		{"string body", "POST", "/api/issues", `"just a string"`},
		{"array body", "POST", "/api/issues", `[{"title":"X"}]`},
		{"number body", "PUT", "/api/issues/" + issue, `42`},
		{"boolean body", "POST", "/api/issues/" + issue + "/comments", `true`},
		{"unknown field", "POST", "/api/issues", `{"title":"X","state":"closed"}`},
		{"unknown field on update", "PUT", "/api/issues/" + issue, `{"title":"X","created":"2026-01-01T00:00:00Z"}`},
		{"unknown field on move", "PUT", "/api/issues/" + issue + "/parent", `{"parent_id":"","state":"open"}`},
		{"missing parent on move", "PUT", "/api/issues/" + issue + "/parent", `{}`},
		{"unknown field on comment", "POST", "/api/issues/" + issue + "/comments", `{"author":"a@example","body":"x","id":"nope"}`},
		{"comment id not editable", "PUT", "/api/issues/" + issue + "/comments/" + comment, `{"body":"x","id":"nope"}`},
		{"comment author not editable", "PUT", "/api/issues/" + issue + "/comments/" + comment, `{"body":"x","author":"someone@else"}`},
		{"comment timestamps not editable", "PUT", "/api/issues/" + issue + "/comments/" + comment, `{"body":"x","updated":"2026-01-01T00:00:00Z"}`},
		{"trailing json value", "POST", "/api/issues", `{"title":"X"} {"title":"Y"}`},
		{"trailing garbage", "POST", "/api/issues", `{"title":"X"} nonsense`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := assertError(t, do(t, srv, c.method, c.path, c.body), 400, CodeInvalidRequest)
			if _, ok := m["result"]; ok {
				t.Error("a rejected request returned a result")
			}
			if got := commitCount(t, dir); got != base {
				t.Errorf("a rejected request committed: %d, want %d", got, base)
			}
		})
	}

	// The issue and its comment must be exactly as they were: no rejected
	// body reached the service.
	after := want(t, do(t, srv, "GET", "/api/issues/"+issue, ""), 200, "GET", "issue")
	var issueBody issueJSON
	after.decode(t, &issueBody)
	if issueBody.Title != "Host" || issueBody.Description != "" {
		t.Errorf("issue changed: %q / %q", issueBody.Title, issueBody.Description)
	}
	if len(issueBody.Comments) != 1 || issueBody.Comments[0].Body != "Hi." {
		t.Errorf("comment changed: %+v", issueBody.Comments)
	}
}

// An empty object is a JSON object: it stays a legitimate no-op update, and
// must not be caught by the object-only rule.
func TestEmptyObjectIsAValidNoOpUpdate(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Unchanged","description":"Body."}`))
	base := commitCount(t, dir)

	before := want(t, do(t, srv, "GET", "/api/issues/"+issue, ""), 200, "GET", "issue")
	var was issueJSON
	before.decode(t, &was)

	r := want(t, do(t, srv, "PUT", "/api/issues/"+issue, `{}`), 200, "PUT", "/api/issues/{id}")
	var got issueJSON
	r.decode(t, &got)
	if got.ID != issue || got.Title != "Unchanged" || got.Description != "Body." {
		t.Errorf("no-op update returned %+v", got)
	}
	if got.State != "open" {
		t.Errorf("state = %q", got.State)
	}
	if !got.Updated.Equal(was.Updated) {
		t.Errorf("no-op update advanced updated: %v, was %v", got.Updated, was.Updated)
	}
	if _, ok := r.field(t, "code").(string); ok {
		t.Errorf("a successful no-op returned an error envelope: %s", r.body)
	}
	if n := commitCount(t, dir); n != base {
		t.Errorf("commits = %d, want %d: a no-op must not commit", n, base)
	}

	// An empty object is equally fine on a comment edit, where body is
	// required — the service, not the transport, decides that.
	commentID := id(t, do(t, srv, "POST", "/api/issues/"+issue+"/comments", `{"author":"a@example","body":"Hi."}`))
	assertError(t, do(t, srv, "PUT", "/api/issues/"+issue+"/comments/"+commentID, `{}`), 400, CodeInvalidRequest)
}

// --- error mapping ----------------------------------------------------------

func TestValidationMapsTo400(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Host"}`))
	base := commitCount(t, dir)

	cases := []struct{ name, method, path, body string }{
		{"empty title", "POST", "/api/issues", `{"title":"   "}`},
		{"missing title", "POST", "/api/issues", `{"description":"no title"}`},
		{"multiline title", "POST", "/api/issues", `{"title":"a\nb"}`},
		{"update to empty title", "PUT", "/api/issues/" + issue, `{"title":""}`},
		{"empty comment author", "POST", "/api/issues/" + issue + "/comments", `{"author":"","body":"x"}`},
		{"empty comment body", "POST", "/api/issues/" + issue + "/comments", `{"author":"a@example","body":"  "}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := assertError(t, do(t, srv, c.method, c.path, c.body), 400, CodeInvalidRequest)
			if _, ok := m["result"]; ok {
				t.Errorf("a validation error returned a result: %s", do(t, srv, c.method, c.path, c.body).body)
			}
			if got := commitCount(t, dir); got != base {
				t.Errorf("an invalid request committed: %d, want %d", got, base)
			}
		})
	}
}

func TestNotFoundMapsTo404(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Host"}`))
	missing := store.NewID()

	cases := []struct{ name, method, path, body string }{
		{"get", "GET", "/api/issues/" + missing, ""},
		{"update", "PUT", "/api/issues/" + missing, `{"title":"X"}`},
		{"close", "POST", "/api/issues/" + missing + "/close", ""},
		{"reopen", "POST", "/api/issues/" + missing + "/reopen", ""},
		{"comment on unknown issue", "POST", "/api/issues/" + missing + "/comments", `{"author":"a@example","body":"x"}`},
		{"unknown parent", "POST", "/api/issues", `{"parent_id":"` + missing + `","title":"Orphan"}`},
		{"edit unknown comment", "PUT", "/api/issues/" + issue + "/comments/" + missing, `{"body":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := assertError(t, do(t, srv, c.method, c.path, c.body), 404, CodeNotFound)
			if _, ok := m["result"]; ok {
				t.Error("a not-found error returned a result")
			}
		})
	}
}

func TestDirtyRepositoryMapsTo409(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Original title"}`))
	base := commitCount(t, dir)

	// Unrelated work in the tree.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, method, path, body string }{
		{"create", "POST", "/api/issues", `{"title":"Should not exist"}`},
		{"update", "PUT", "/api/issues/" + issue, `{"title":"Should not apply"}`},
		{"close", "POST", "/api/issues/" + issue + "/close", ""},
		{"comment", "POST", "/api/issues/" + issue + "/comments", `{"author":"a@example","body":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := assertError(t, do(t, srv, c.method, c.path, c.body), 409, CodeRepositoryUnusable)
			if _, ok := m["result"]; ok {
				t.Error("a repository refusal returned a result")
			}
		})
	}
	if got := commitCount(t, dir); got != base {
		t.Errorf("commits = %d, want %d", got, base)
	}
	// Reads still work, and the issue is untouched.
	r := want(t, do(t, srv, "GET", "/api/issues/"+issue, ""), 200, "GET", "issue")
	if r.field(t, "title") != "Original title" {
		t.Errorf("title changed despite the refusal: %v", r.field(t, "title"))
	}
}

// An incomplete transaction: canonical files written, commit not completed.
func TestIncompleteMapsTo500(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	do(t, srv, "POST", "/api/issues", `{"title":"Committed fine"}`)
	base := commitCount(t, dir)

	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'commits are rejected here' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := do(t, srv, "POST", "/api/issues", `{"title":"Written but not committed"}`)
	m := assertError(t, r, 500, CodeIncomplete)
	if _, ok := m["result"]; ok {
		t.Errorf("an incomplete transaction returned a result: %s", r.body)
	}
	msg, _ := m["error"].(string)
	for _, phrase := range []string{"written", "commit", "repair"} {
		if !strings.Contains(msg, phrase) {
			t.Errorf("error message does not mention %q: %q", phrase, msg)
		}
	}
	if got := commitCount(t, dir); got != base {
		t.Errorf("commits = %d, want %d: nothing may have been committed", got, base)
	}
	// The attempted write really is sitting in the working tree, staged.
	if staged := git(t, dir, "diff", "--cached", "--name-only"); !strings.HasSuffix(staged, "/issue.md") {
		t.Errorf("staged = %q, want the document the attempt wrote", staged)
	}
	// The next request is refused by the ordinary clean-repository rule.
	assertError(t, do(t, srv, "POST", "/api/issues", `{"title":"Blocked"}`), 409, CodeRepositoryUnusable)
}

// The mutation is committed locally but could not be published.
func TestNotPushedMapsTo502WithResult(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, true) // remote-synchronized, but no origin exists

	r := do(t, srv, "POST", "/api/issues", `{"title":"Durable locally"}`)
	m := assertError(t, r, 502, CodeNotPushed)

	result, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("not_pushed must carry the committed object: %s", r.body)
	}
	if result["title"] != "Durable locally" {
		t.Errorf("result title = %v", result["title"])
	}
	issueID, _ := result["id"].(string)
	if issueID == "" {
		t.Fatal("result has no id")
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "do not retry") {
		t.Errorf("error message should warn against retrying: %q", msg)
	}

	// Independently: the mutation really is a local Git commit.
	if got := commitCount(t, dir); got != 1 {
		t.Fatalf("commits = %d, want 1", got)
	}
	if s := git(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("working tree is dirty: %s", s)
	}
	if subject := git(t, dir, "log", "-1", "--format=%s"); subject != "create issue "+issueID+": Durable locally" {
		t.Errorf("commit subject = %q", subject)
	}
	fresh, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.GetIssue(context.Background(), issueID); err != nil {
		t.Errorf("the committed issue is not readable from the repository: %v", err)
	}

	// A comment mutation behaves the same way and carries a comment result.
	cr := do(t, srv, "POST", "/api/issues/"+issueID+"/comments", `{"author":"a@example","body":"Also durable."}`)
	cm := assertError(t, cr, 502, CodeNotPushed)
	cres, ok := cm["result"].(map[string]any)
	if !ok || cres["body"] != "Also durable." {
		t.Fatalf("comment not_pushed result = %v", cm["result"])
	}
	if got := commitCount(t, dir); got != 2 {
		t.Errorf("commits = %d, want 2", got)
	}
}

func TestMoveNotPushedReturnsMovedIssue(t *testing.T) {
	dir := initRepo(t)
	local, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := local.CreateIssue(context.Background(), service.CreateIssueRequest{Title: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := local.CreateIssue(context.Background(), service.CreateIssueRequest{Title: "Beta"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, dir, true)
	r := do(t, srv, "PUT", "/api/issues/"+a.ID+"/parent", `{"parent_id":"`+b.ID+`"}`)
	m := assertError(t, r, http.StatusBadGateway, CodeNotPushed)
	result, ok := m["result"].(map[string]any)
	if !ok || result["id"] != a.ID || result["parent_id"] != b.ID {
		t.Fatalf("move not_pushed result = %v", m["result"])
	}
	if got := git(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("repository dirty: %s", got)
	}
}

// --- routing ----------------------------------------------------------------

func TestMethodAndPathHandling(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Host"}`))

	comment := id(t, do(t, srv, "POST", "/api/issues/"+issue+"/comments", `{"author":"a@example","body":"Hi."}`))
	base := commitCount(t, dir)

	// Every unsupported method on a real API path gets the same JSON error
	// envelope as anything else. The stdlib mux would answer in plain text.
	notAllowed := []struct{ name, method, path string }{
		{"delete issue", "DELETE", "/api/issues/" + issue},
		{"delete collection", "DELETE", "/api/issues"},
		{"put on collection", "PUT", "/api/issues"},
		{"post on single issue", "POST", "/api/issues/" + issue},
		{"patch on single issue", "PATCH", "/api/issues/" + issue},
		{"get on close", "GET", "/api/issues/" + issue + "/close"},
		{"put on close", "PUT", "/api/issues/" + issue + "/close"},
		{"get on reopen", "GET", "/api/issues/" + issue + "/reopen"},
		{"get on comments", "GET", "/api/issues/" + issue + "/comments"},
		{"post on parent", "POST", "/api/issues/" + issue + "/parent"},
		{"delete comment", "DELETE", "/api/issues/" + issue + "/comments/" + comment},
		{"post on comment", "POST", "/api/issues/" + issue + "/comments/" + comment},
	}
	for _, c := range notAllowed {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, srv, c.method, c.path, "")
			m := assertError(t, r, http.StatusMethodNotAllowed, CodeInvalidRequest)
			if msg, _ := m["error"].(string); !strings.Contains(msg, "method not allowed") {
				t.Errorf("error = %q, want it to say method not allowed", msg)
			}
			if _, ok := m["result"]; ok {
				t.Error("a 405 returned a result")
			}
		})
	}

	// Unknown API paths are JSON 404s with the existing not_found code.
	for _, path := range []string{"/api/nope", "/api/issues/" + issue + "/nonsense", "/api/issues/" + issue + "/comments/" + comment + "/extra", "/api/"} {
		t.Run("unknown path "+path, func(t *testing.T) {
			assertError(t, do(t, srv, "GET", path, ""), http.StatusNotFound, CodeNotFound)
		})
	}

	// The non-API root is not an API response and stays as the mux serves it.
	t.Run("non-api root", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("GET /: status = %d, want 404\n%s", resp.StatusCode, body)
		}
	})

	if got := commitCount(t, dir); got != base {
		t.Errorf("routing probes changed the repository: %d commits, want %d", got, base)
	}
}

// Comments submitted back-to-back over HTTP must come back in submission
// order. Nothing here depends on the random IDs breaking a tie the right way.
func TestImmediateCommentsPreserveSubmissionOrder(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)
	issue := id(t, do(t, srv, "POST", "/api/issues", `{"title":"Rapid conversation"}`))

	var submitted []string
	for _, body := range []string{"one", "two", "three", "four", "five"} {
		r := want(t, do(t, srv, "POST", "/api/issues/"+issue+"/comments",
			`{"author":"a@example","body":"`+body+`"}`), 201, "POST", "comments")
		submitted = append(submitted, id(t, r))
	}

	var issueBody issueJSON
	want(t, do(t, srv, "GET", "/api/issues/"+issue, ""), 200, "GET", "issue").decode(t, &issueBody)
	got := make([]string, len(issueBody.Comments))
	for i, c := range issueBody.Comments {
		got[i] = c.ID
	}
	if !equal(got, submitted) {
		t.Fatalf("comment order = %v, want submission order %v", got, submitted)
	}
	// Strictly increasing created timestamps, not a tie broken by ID.
	for i := 1; i < len(issueBody.Comments); i++ {
		if !issueBody.Comments[i].Created.After(issueBody.Comments[i-1].Created) {
			t.Errorf("comment %d created %v is not after comment %d created %v",
				i, issueBody.Comments[i].Created, i-1, issueBody.Comments[i-1].Created)
		}
	}
	// Editing the first one does not move it.
	want(t, do(t, srv, "PUT", "/api/issues/"+issue+"/comments/"+submitted[0], `{"body":"one, edited"}`), 200, "PUT", "comment")
	var afterEdit issueJSON
	want(t, do(t, srv, "GET", "/api/issues/"+issue, ""), 200, "GET", "issue").decode(t, &afterEdit)
	for i, c := range afterEdit.Comments {
		if c.ID != submitted[i] {
			t.Fatalf("order changed after an edit: %v", afterEdit.Comments)
		}
	}
}

// Request cancellation must reach the service, and through it git.
func TestRequestContextReachesTheService(t *testing.T) {
	dir := initRepo(t)
	srv := newServer(t, dir, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/issues", bytes.NewReader([]byte(`{"title":"X"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client().Do(req); err == nil {
		t.Error("a cancelled request should not succeed")
	}
	if got := commitCount(t, dir); got != 0 {
		t.Errorf("a cancelled request committed: %d", got)
	}
}
