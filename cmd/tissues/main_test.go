package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tedla-brandsema/tissues/internal/service"
)

func TestRunRejectsBadCommands(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":      {},
		"unknown command": {"frobnicate"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(args, &out); err == nil {
				t.Fatal("run returned nil, want a non-zero outcome")
			}
			if !strings.Contains(out.String(), "tissues serve") {
				t.Errorf("usage was not printed:\n%s", out.String())
			}
		})
	}
}

// The loopback default and remote-sync default are both load-bearing: v0 has
// no authentication, and remote synchronization is the normal mode.
func TestServeFlagDefaults(t *testing.T) {
	cfg, err := parseServe(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q, want 127.0.0.1:8080 (loopback)", cfg.addr)
	}
	if !strings.HasPrefix(cfg.addr, "127.0.0.1:") {
		t.Errorf("default addr %q is not loopback-only", cfg.addr)
	}
	if !cfg.remoteSync {
		t.Error("default remote-sync = false, want true")
	}
	if cfg.repo != "." {
		t.Errorf("default repo = %q, want .", cfg.repo)
	}
}

func TestServeFlagOverrides(t *testing.T) {
	cfg, err := parseServe([]string{"-repo", "/tmp/x", "-addr", "127.0.0.1:9999", "-remote-sync=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.repo != "/tmp/x" || cfg.addr != "127.0.0.1:9999" || cfg.remoteSync {
		t.Errorf("parsed config = %+v", cfg)
	}

	var out bytes.Buffer
	if _, err := parseServe([]string{"-nope"}, &out); err == nil {
		t.Error("an unknown flag should fail")
	}
	if _, err := parseServe([]string{"extra"}, &out); err == nil {
		t.Error("an unexpected positional argument should fail")
	}
}

// serve must fail before it ever listens if the repository is unusable.
func TestServeRejectsInvalidRepositoryBeforeListening(t *testing.T) {
	// Port 0 would succeed at binding, so if serve listened at all this would
	// block rather than return.
	cfg := serveConfig{repo: t.TempDir(), addr: "127.0.0.1:0", remoteSync: false}
	err := serve(cfg, &bytes.Buffer{})
	if err == nil {
		t.Fatal("serve accepted a directory that is not a Git repository")
	}
	if !strings.Contains(err.Error(), "not a Git repository") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// An empty but valid repository must start: no issues/ directory is normal.
func TestServeAcceptsEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "tissues"},
		{"config", "user.email", "tissues@example"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Hold a real listener on an ephemeral port and hand serve the same
	// address. serve then gets past startup validation and fails at
	// ListenAndServe with address-already-in-use — deterministic, and with no
	// dependence on privileged ports or a fixed port number.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	cfg := serveConfig{repo: dir, addr: held.Addr().String(), remoteSync: false}
	err = serve(cfg, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a listen failure on an address that is already bound")
	}
	if strings.Contains(err.Error(), "not a Git repository") || strings.Contains(err.Error(), "repository unusable") {
		t.Fatalf("startup validation rejected a valid empty repository: %v", err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "listen" {
		t.Errorf("error = %v, want a listen failure (startup validation should have passed)", err)
	}
}

// serverHandler must expose all three adapters over the exact same service
// pointer. This proves cross-transport visibility in every direction.
func TestServerHandlerSharesServiceBetweenWebRESTAndMCP(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "tissues"},
		{"config", "user.email", "tissues@example"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	svc, err := service.New(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(serverHandler(svc))
	defer httpServer.Close()

	location := requestBrowserForm(t, httpServer.Client(), httpServer.URL, "/issues", url.Values{
		"title": {"Created through browser"}, "description": {"Initial browser description."},
	})
	issueID := strings.TrimPrefix(location, "/issues/")
	if len(issueID) != 26 {
		t.Fatalf("browser create Location = %q", location)
	}
	restIssue := requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/issues/"+issueID, "")
	if restIssue["title"] != "Created through browser" {
		t.Fatalf("REST did not see browser issue: %v", restIssue)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "parity-test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		HTTPClient:           httpServer.Client(),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got := callToolJSON(t, session, "get_issue", map[string]any{"id": issueID})
	if got["title"] != "Created through browser" {
		t.Fatalf("MCP did not see browser issue: %v", got)
	}
	callToolJSON(t, session, "add_comment", map[string]any{
		"issue_id": issueID, "author": "agent", "body": "Visible in the browser.",
	})
	page, err := httpServer.Client().Get(httpServer.URL + "/issues/" + issueID)
	if err != nil {
		t.Fatal(err)
	}
	pageBody, err := io.ReadAll(page.Body)
	page.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if page.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "Visible in the browser.") {
		t.Fatalf("browser did not see MCP comment: status %d\n%s", page.StatusCode, pageBody)
	}

	requestBrowserForm(t, httpServer.Client(), httpServer.URL, "/issues/"+issueID+"/close", url.Values{})
	restIssue = requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/issues/"+issueID, "")
	if restIssue["state"] != "closed" {
		t.Fatalf("REST did not see browser close: %v", restIssue)
	}
	requestJSON(t, httpServer.Client(), http.MethodPut, httpServer.URL+"/api/issues/"+issueID, `{"description":"Refined through REST."}`)
	got = callToolJSON(t, session, "get_issue", map[string]any{"id": issueID})
	if got["description"] != "Refined through REST." || got["state"] != "closed" {
		t.Fatalf("MCP did not see final REST mutation: %v", got)
	}
	page, err = httpServer.Client().Get(httpServer.URL + "/issues/" + issueID)
	if err != nil {
		t.Fatal(err)
	}
	pageBody, err = io.ReadAll(page.Body)
	page.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pageBody), "Refined through REST.") {
		t.Fatalf("browser did not see REST update:\n%s", pageBody)
	}
}

func requestBrowserForm(t *testing.T, client *http.Client, origin, path string, values url.Values) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, origin+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST %s: status %d: %s", path, resp.StatusCode, body)
	}
	return resp.Header.Get("Location")
}

func requestJSON(t *testing.T, client *http.Client, method, url, body string) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status %d: %s", method, url, resp.StatusCode, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func callToolJSON(t *testing.T, session *mcp.ClientSession, name string, arguments any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned a tool error: %+v", name, result.Content)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}
