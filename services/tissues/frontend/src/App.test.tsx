import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { UnauthorizedError } from "./api";

vi.mock("./MarkdownEditor", () => ({
  MarkdownEditor: ({ label, value, onChange }: { label: string; value: string; onChange: (markdown: string) => void }) => <label>{label}<textarea aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} /></label>,
}));

const stamp = "2026-01-01T00:00:00.123456789Z";
const opaque = "aaaaaaaaaaaaaaaaaaaaaaaaaa";
const parent = { id: "FLUENT-1", project_key: "FLUENT", number: 1, title: "Parent", state: "open", created: stamp, updated: stamp, description: "Body", parent_id: "", comments: [], children: [] };
const child = { ...parent, id: "FLUENT-2", number: 2, title: "Child", state: "closed", parent_id: "FLUENT-1" };
let requests: Array<{ path: string; method: string; body: string }> = [];

function installAPI(options: { projects?: string[]; parentFailure?: { status: number; message: string } } = {}) {
  const projectKeys = options.projects ?? ["FLUENT", "TISSUES"];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input); const method = init?.method || "GET"; const body = String(init?.body || "");
    requests.push({ path, method, body });
    if (path.includes("/projects?") && method === "GET") return new Response(JSON.stringify({ projects: projectKeys.map((key) => ({ key, created: stamp })), next_cursor: "" }), { status: 200 });
    if (path.endsWith("/projects") && method === "POST") return new Response(JSON.stringify({ key: "FLUENT", created: stamp }), { status: 201 });
    if (path.endsWith("/projects/FLUENT") && method === "GET") return new Response(JSON.stringify({ key: "FLUENT", created: stamp }), { status: 200 });
    if (path.includes("/issues?page_size=") && method === "GET") {
      const project = new URL(path, "http://test").searchParams.get("project");
      const issues = [
      { project_key: "FLUENT", number: 1, id: "FLUENT-1", title: "Parent", state: "open", parent_id: "", updated: stamp },
      { project_key: "TISSUES", number: 4, id: "TISSUES-4", title: "Polish", state: "closed", parent_id: "", updated: stamp },
      ]; return new Response(JSON.stringify({ issues: project ? issues.filter((issue) => issue.project_key === project) : issues, next_cursor: "" }), { status: 200 });
    }
    if (path.endsWith("/projects/FLUENT/issues") && method === "GET") return new Response(JSON.stringify({ issues: [parent, child] }), { status: 200 });
    if (path.endsWith("/projects/TISSUES/issues") && method === "GET") return new Response(JSON.stringify({ issues: [] }), { status: 200 });
    if (path.endsWith("/projects/FLUENT/issues") && method === "POST") return new Response(JSON.stringify({ ...parent, id: "FLUENT-3", number: 3, title: "Created" }), { status: 201 });
    if (path.endsWith("/issues/FLUENT-1/parent") && method === "PUT") {
      if (options.parentFailure) return new Response(JSON.stringify({ error: { kind: options.parentFailure.status === 404 ? "not_found" : "invalid", message: options.parentFailure.message } }), { status: options.parentFailure.status });
      const payload = JSON.parse(body); return new Response(JSON.stringify({ ...parent, parent_id: payload.parent_id }), { status: 200 });
    }
    if (path.endsWith("/issues/FLUENT-1") && method === "PATCH") { const payload = JSON.parse(body); return new Response(JSON.stringify({ ...parent, title: payload.title, description: payload.description }), { status: 200 }); }
    if (path.endsWith("/issues/FLUENT-1/close") && method === "POST") return new Response(JSON.stringify({ ...parent, state: "closed" }), { status: 200 });
    if (path.endsWith("/issues/FLUENT-2/reopen") && method === "POST") return new Response(JSON.stringify({ ...child, state: "open" }), { status: 200 });
    if (path.endsWith("/issues/FLUENT-2") && method === "GET") return new Response(JSON.stringify(child), { status: 200 });
    if (path.endsWith("/issues/FLUENT-1") && method === "GET") return new Response(JSON.stringify(parent), { status: 200 });
    return new Response(JSON.stringify({ error: { kind: "not_found", message: "not found" } }), { status: 404 });
  }));
}

beforeEach(() => { history.replaceState(null, "", "/?view=projects"); localStorage.clear(); requests = []; installAPI(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("navigation and view architecture", () => {
  it("keeps the side navigation to exactly Projects and Issues", async () => {
    render(<App />); const navigation = screen.getByLabelText("Product navigation");
    expect(within(within(navigation).getByRole("navigation")).getAllByRole("button").map((button) => button.textContent)).toEqual(["Projects", "Issues"]);
    expect(within(navigation).queryByText(/create project/i)).not.toBeInTheDocument();
    expect(within(navigation).queryByText(/issue tree|open|closed|all/i)).not.toBeInTheDocument();
    expect(await screen.findAllByRole("button", { name: /Project$/ })).toHaveLength(1);
  });

  it("uses main-view Project create and immutable existing views", async () => {
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Project" }));
    await user.type(screen.getByLabelText("Project ID"), "fluent");
    expect(screen.getByLabelText("Project ID")).toHaveValue("FLUENT");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByRole("heading", { name: "FLUENT" })).toBeInTheDocument();
    expect(screen.getByText(new Date(stamp).toLocaleString())).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /key/i })).not.toBeInTheDocument();
  });

  it("lists global Issues from multiple Projects without opaque IDs", async () => {
    history.replaceState(null, "", "/?view=issues"); render(<App />);
    expect(await screen.findByText("FLUENT-1")).toBeInTheDocument();
    expect(screen.getByText("TISSUES-4")).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(opaque);
    expect(screen.getAllByRole("button", { name: /Issue$/ })).toHaveLength(1);
  });

  it("persists the overview Project filter and uses it as the create default", async () => {
    history.replaceState(null, "", "/?view=issues"); const user = userEvent.setup(); render(<App />);
    const filter = await screen.findByRole("combobox", { name: "Project" }); await user.selectOptions(filter, "FLUENT");
    expect(localStorage.getItem("tissues.issues-project-filter")).toBe("FLUENT");
    await waitFor(() => expect(requests.at(-1)?.path).toContain("project=FLUENT"));
    await user.selectOptions(filter, ""); expect(localStorage.getItem("tissues.issues-project-filter")).toBeNull();
    await user.selectOptions(filter, "FLUENT");
    await user.click(screen.getByRole("button", { name: "Issue" }));
    expect(await screen.findByLabelText("Project ID")).toHaveValue("FLUENT");
    await user.selectOptions(screen.getByLabelText("Project ID"), "TISSUES");
    expect(localStorage.getItem("tissues.issues-project-filter")).toBe("FLUENT");
  });
});

describe("Issue editing and feedback", () => {
  it("saves content separately and changes hierarchy in the parent dialog", async () => {
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup(); render(<App />);
    const title = await screen.findByLabelText("Title"); await user.clear(title); await user.type(title, "Changed");
    const body = screen.getByLabelText("Description"); await user.clear(body); await user.type(body, "Changed body");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(requests.filter((request) => request.method === "PATCH")).toEqual([expect.objectContaining({ path: "/api/tissues/v1/issues/FLUENT-1", body: '{"title":"Changed","description":"Changed body"}' })]));
    await user.click(screen.getByRole("button", { name: "Set parent" }));
    await user.type(screen.getByLabelText("Parent issue ID"), "FLUENT-2");
    await user.click(screen.getByRole("button", { name: "Save parent" }));
    await waitFor(() => expect(requests.filter((request) => request.method === "PUT")).toEqual([expect.objectContaining({ path: "/api/tissues/v1/issues/FLUENT-1/parent", body: '{"parent_id":"FLUENT-2"}' })]));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it.each([
    [404, "resource not found", "Parent issue does not exist."],
    [400, "parent reference must belong to project FLUENT", "Parent issue must belong to Project FLUENT."],
    [400, "issue FLUENT-1 cannot be its own parent", "An Issue cannot be its own parent."],
    [400, "issue FLUENT-1 cannot be moved beneath descendant FLUENT-2", "Parent issue would create a hierarchy cycle."],
  ])("keeps form values and maps parent failure %s", async (status, message, expected) => {
    vi.unstubAllGlobals(); requests = []; installAPI({ parentFailure: { status, message } }); history.replaceState(null, "", "/?view=issue&issue=FLUENT-1");
    const user = userEvent.setup(); render(<App />); await user.click(await screen.findByRole("button", { name: "Set parent" })); const parentInput = screen.getByLabelText("Parent issue ID"); await user.type(parentInput, "FLUENT-999");
    await user.click(screen.getByRole("button", { name: "Save parent" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
    expect(parentInput).toHaveValue("FLUENT-999"); expect(parentInput).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: "Save parent" })).toBeInTheDocument();
  });

  it("requires application confirmation before Close and Reopen", async () => {
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Close issue" }));
    expect(requests.some((request) => request.method === "POST")).toBe(false);
    const dialog = screen.getByRole("alertdialog"); expect(within(dialog).getByText("Close FLUENT-1?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" })); expect(requests.some((request) => request.method === "POST")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Close issue" })); await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Close issue" }));
    await waitFor(() => expect(requests.filter((request) => request.path.endsWith("/close") && request.method === "POST")).toHaveLength(1));

    cleanup(); requests = []; history.replaceState(null, "", "/?view=issue&issue=FLUENT-2"); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Reopen issue" }));
    expect(requests.some((request) => request.method === "POST")).toBe(false);
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reopen issue" }));
    await waitFor(() => expect(requests.filter((request) => request.path.endsWith("/reopen") && request.method === "POST")).toHaveLength(1));
  });

  it("recovers an expired session without a generic API error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "authentication required" } }), { status: 401 })));
    const recoverSession = vi.fn(() => true); render(<App recoverSession={recoverSession} />);
    await waitFor(() => expect(recoverSession).toHaveBeenCalledWith(expect.any(UnauthorizedError)));
    expect(screen.queryByText("authentication required")).not.toBeInTheDocument();
  });
});
