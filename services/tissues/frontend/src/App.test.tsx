import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { UnauthorizedError } from "./api";

vi.mock("./MarkdownEditor", () => ({
  MarkdownEditor: ({ label, value, onChange }: { label: string; value: string; onChange: (markdown: string) => void }) => <label>{label}<textarea aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} /></label>,
}));

const issue = { id: "parent", title: "Parent", state: "open", created: "2026-01-01T00:00:00.123456789Z", updated: "2026-01-01T00:00:00.123456789Z", description: "# Safe Markdown\n\n<script>alert(1)</script>", parent_id: "", comments: [], children: [{ id: "child", title: "Child", state: "closed", created: "2026-01-01T00:00:00Z", updated: "2026-01-01T00:00:00Z", description: "child", parent_id: "parent", comments: [], children: [] }] };
let requests: Array<{ path: string; method: string; body: string }> = [];

beforeEach(() => {
  history.replaceState(null, "", "/");
  localStorage.clear();
  requests = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const method = init?.method || "GET"; requests.push({ path, method, body: String(init?.body || "") });
    if (path.endsWith("/issues") && method === "GET") return new Response(JSON.stringify({ issues: [issue] }), { status: 200 });
    if (path.endsWith("/issues") && method === "POST") return new Response(JSON.stringify({ ...issue, id: "created", title: "Created" }), { status: 201 });
    if (path.endsWith("/comments") && method === "POST") return new Response(JSON.stringify({ id: "comment", author: "Ada", body: "A note", created: issue.created, updated: issue.updated }), { status: 201 });
    return new Response(JSON.stringify(issue), { status: 200 });
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("workspace", () => {
  it("renders hierarchy, filters state, and selects detail", async () => {
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Parent" }));
    expect(await screen.findByRole("heading", { name: "Parent" })).toBeInTheDocument();
    expect(document.querySelector("script")).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "Closed" }));
    expect(screen.getByRole("button", { name: "Child" })).toBeInTheDocument();
    expect(location.search).toContain("issue=parent"); expect(location.search).toContain("view=closed");
  });

  it("opens the create workflow and persists the comment author", async () => {
    const user = userEvent.setup(); render(<App />);
    await user.click(within(screen.getByLabelText("Issue navigator")).getByRole("button", { name: /new issue/i }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Create issue");
    await user.type(screen.getByLabelText("Title"), "Created"); await user.type(screen.getByLabelText("Description"), "Body");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(requests.some((request) => request.method === "POST" && request.path.endsWith("/issues"))).toBe(true));
    await user.click(await screen.findByRole("button", { name: "Parent" }));
    expect(await screen.findByLabelText("Comment author")).toBeRequired();
    await user.type(screen.getByLabelText("Comment author"), "Ada");
    await user.type(screen.getByLabelText("Comment body"), "A note");
    await user.click(screen.getByRole("button", { name: "Comment" }));
    await waitFor(() => expect(localStorage.getItem("tissues.comment-author")).toBe("Ada"));
  });

  it("uses trusted identity without a manual author in authenticated mode", async () => {
    localStorage.setItem("tissues.comment-author", "Local Ada");
    const user = userEvent.setup(); render(<App bootstrap={{ enabled: true, author: "person@example.test" }} />);
    await user.click(await screen.findByRole("button", { name: "Parent" }));
    expect(screen.queryByLabelText("Comment author")).not.toBeInTheDocument();
    expect(screen.getByText(/Commenting as/)).toHaveTextContent("Commenting as person@example.test");
    await user.type(screen.getByLabelText("Comment body"), "Authenticated note");
    await user.click(screen.getByRole("button", { name: "Comment" }));
    await waitFor(() => {
      const request = requests.find((item) => item.path.endsWith("/comments") && item.method === "POST");
      expect(request?.body).toBe('{"body":"Authenticated note"}');
    });
    expect(localStorage.getItem("tissues.comment-author")).toBe("Local Ada");
  });

  it("runs state and attach workflows through canonical API refreshes", async () => {
    const user = userEvent.setup(); render(<App />); await user.click(await screen.findByRole("button", { name: "Parent" }));
    await user.click(screen.getAllByRole("button", { name: "Close" }).at(-1)!);
    await waitFor(() => expect(requests.some((request) => request.path.endsWith("/parent/close") && request.method === "POST")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Move" })); await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Child" }));
    await waitFor(() => expect(requests.some((request) => request.path.endsWith("/parent/parent") && request.method === "PUT" && request.body.includes("child"))).toBe(true));
  });

  it("shows a recoverable backend error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "temporarily unavailable" } }), { status: 500 })));
    render(<App />); expect(await screen.findByText("temporarily unavailable")).toBeInTheDocument(); expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("recovers an expired session without showing a generic API error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "authentication required" } }), { status: 401 })));
    const recoverSession = vi.fn(() => true);
    render(<App bootstrap={{ enabled: true, author: "person@example.test" }} recoverSession={recoverSession} />);
    await waitFor(() => expect(recoverSession).toHaveBeenCalledWith(expect.any(UnauthorizedError)));
    expect(screen.queryByText("authentication required")).not.toBeInTheDocument();
  });
});
