import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { UnauthorizedError } from "./api";

vi.mock("./MarkdownEditor", () => {
  return {
    MarkdownEditor({ label, value, onChange }: { label: string; value: string; onChange: (markdown: string) => void }) {
      return <label>{label}<textarea aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} /></label>;
    },
  };
});

const stamp = "2026-01-01T00:00:00.123456789Z";
const opaque = "aaaaaaaaaaaaaaaaaaaaaaaaaa";
const parent = { id: "FLUENT-1", project_key: "FLUENT", number: 1, title: "Parent", state: "open", created: stamp, updated: stamp, description: "Body", parent_id: "", comments: [], children: [] };
const child = { ...parent, id: "FLUENT-2", number: 2, title: "Child", state: "closed", parent_id: "FLUENT-1" };
let requests: Array<{ path: string; method: string; body: string }> = [];
const existingAsset = { name: "existing.png", url: "/api/tissues/v1/issues/FLUENT-1/assets/existing.png", content_type: "image/png" as const, width: 40, height: 30, size: 2048 };

function installAPI(options: { projects?: string[]; parentFailure?: { status: number; message: string }; assets?: typeof existingAsset[]; assetListFailures?: number; uploadFailures?: number } = {}) {
  const projectKeys = options.projects ?? ["FLUENT", "TISSUES"];
  let assetListFailures = options.assetListFailures ?? 0;
  let uploadFailures = options.uploadFailures ?? 0;
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
    if (path.endsWith("/issues/FLUENT-1/assets") && method === "GET") {
      if (assetListFailures-- > 0) return new Response(JSON.stringify({ error: { kind: "internal", message: "Unable to list images" } }), { status: 500 });
      return new Response(JSON.stringify({ assets: options.assets ?? [] }), { status: 200 });
    }
    if (path.endsWith("/issues/FLUENT-1/assets") && method === "POST") {
      if (uploadFailures-- > 0) return new Response(JSON.stringify({ error: { kind: "conflict", message: "Image changed; retry upload" } }), { status: 409 });
      const selected = (init?.body as FormData).get("file") as File;
      const name = selected.name.toLowerCase().replace(/\.jpeg$/, ".jpg");
      return new Response(JSON.stringify({ name, url: `/api/tissues/v1/issues/FLUENT-1/assets/${name}`, content_type: name.endsWith(".png") ? "image/png" : "image/jpeg", width: 1200, height: 800, size: 483221 }), { status: 201 });
    }
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

beforeEach(() => {
  history.replaceState(null, "", "/?view=projects"); localStorage.clear(); requests = [];
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test-preview");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  installAPI();
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

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

  it("keeps image management exclusive to existing Issues and loads assets independently", async () => {
    history.replaceState(null, "", "/?view=issue&mode=create"); render(<App />);
    expect(await screen.findByRole("heading", { name: "Create issue" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Upload image" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Images" })).not.toBeInTheDocument();

    cleanup(); vi.unstubAllGlobals(); requests = []; installAPI({ assets: [existingAsset] });
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); render(<App />);
    expect(await screen.findByRole("heading", { name: "Images" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload image" })).toBeInTheDocument();
    expect(await screen.findByText("existing.png")).toBeInTheDocument();
    expect(screen.getByText("40 × 30 · 2 KB")).toBeInTheDocument();
  });

  it("retries an independent asset-list failure without blocking Issue editing", async () => {
    vi.unstubAllGlobals(); requests = []; installAPI({ assets: [existingAsset], assetListFailures: 1 });
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup(); render(<App />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Unable to list images");
    expect(screen.getByLabelText("Title")).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Retry images" }));
    expect(await screen.findByText("existing.png")).toBeInTheDocument();
  });

  it("validates locally and uploads an attachment without changing the Issue description", async () => {
    vi.unstubAllGlobals(); requests = [];
    installAPI({ assets: [{ ...existingAsset, name: "photo.jpg", url: "/api/tissues/v1/issues/FLUENT-1/assets/photo.jpg" }] });
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup({ applyAccept: false }); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Upload image" }));
    const input = screen.getByLabelText("Image file");
    await user.upload(input, new File([new Uint8Array(6 * 1024 * 1024 + 1)], "too-large.png", { type: "image/png" }));
    expect(screen.getByRole("alert")).toHaveTextContent("exceeds the 6 MiB");
    await user.upload(input, new File(["gif"], "notes.gif", { type: "image/gif" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Choose a PNG or JPEG");
    expect(requests.filter((request) => request.method === "POST" && request.path.endsWith("/assets"))).toHaveLength(0);

    await user.upload(input, new File(["jpeg"], "Photo.JPEG", { type: "image/jpeg" }));
    expect(screen.getByAltText("Selected image preview")).toHaveAttribute("src", "blob:test-preview");
    expect(screen.queryByLabelText("Image description / Alt text")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(await within(screen.getByRole("dialog")).findByText("1200 × 800 · 472 KB")).toBeInTheDocument();
    expect(requests.filter((request) => request.method === "POST" && request.path.endsWith("/assets"))).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /Insert|Copy Markdown/ })).not.toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Done" }));
    expect(within(screen.getByRole("region", { name: "Images" })).getAllByText("photo.jpg")).toHaveLength(1);
    expect(screen.getByLabelText("Description")).toHaveValue("Body");
    expect(requests.filter((request) => request.method === "PATCH")).toHaveLength(0);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test-preview");
  });

  it("revises same-name image presentation and opens the refreshed attachment preview", async () => {
    vi.unstubAllGlobals(); requests = [];
    const canonical = "/api/tissues/v1/issues/FLUENT-1/assets/example.png";
    installAPI({ assets: [{ ...existingAsset, name: "example.png", url: canonical }] });
    history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup(); render(<App />);
    const images = await screen.findByRole("region", { name: "Images" });
    expect(images.querySelector(".asset-card img")).toHaveAttribute("src", canonical);

    await user.click(within(images).getByRole("button", { name: "Upload image" }));
    await user.upload(screen.getByLabelText("Image file"), new File(["png"], "Example.PNG", { type: "image/png" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Upload" }));
    let dialog = screen.getByRole("dialog");
    expect(dialog.querySelector(".upload-result img")).toHaveAttribute("src", `${canonical}?preview=1`);
    expect(within(dialog).getByText("1200 × 800 · 472 KB")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(within(images).getAllByText("example.png")).toHaveLength(1);
    expect(images.querySelector(".asset-card img")).toHaveAttribute("src", `${canonical}?preview=1`);

    await user.click(within(images).getByRole("button", { name: "Upload image" }));
    await user.upload(screen.getByLabelText("Image file"), new File(["new png"], "example.png", { type: "image/png" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Upload" }));
    dialog = screen.getByRole("dialog");
    expect(dialog.querySelector(".upload-result img")).toHaveAttribute("src", `${canonical}?preview=2`);
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(within(images).getAllByText("example.png")).toHaveLength(1);
    expect(images.querySelector(".asset-card img")).toHaveAttribute("src", `${canonical}?preview=2`);

    const thumbnail = within(images).getByRole("button", { name: "View example.png" });
    await user.click(thumbnail);
    const preview = screen.getByRole("dialog");
    expect(within(preview).getByRole("heading", { name: "example.png" })).toBeInTheDocument();
    expect(within(preview).getByText("1200 × 800 · 472 KB")).toBeInTheDocument();
    expect(within(preview).getByAltText("Preview of example.png")).toHaveAttribute("src", `${canonical}?preview=2`);
    await user.click(within(preview).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(thumbnail).toHaveFocus();
  });

  it("keeps upload errors visible and retryable", async () => {
    vi.unstubAllGlobals(); requests = [];
    installAPI({ uploadFailures: 1 }); history.replaceState(null, "", "/?view=issue&issue=FLUENT-1"); const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "Upload image" }));
    await user.upload(screen.getByLabelText("Image file"), new File(["png"], "retry.png", { type: "image/png" }));
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Image changed; retry upload");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(await within(screen.getByRole("dialog")).findByText("retry.png")).toBeInTheDocument();
  });
});
