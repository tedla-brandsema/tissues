import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, api, UnauthorizedError } from "./api";

afterEach(() => vi.restoreAllMocks());

describe("tissues API client", () => {
  it("uses opaque cursor queries and distinct overview/detail routes", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ projects: [], issues: [], next_cursor: "" }), { status: 200 }));
    await api.listProjects(25, "project-page-2");
    await api.listIssueOverviews(50, "issue-page-2", "FLUENT");
    await api.listProjectIssues("FLUENT");
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/tissues/v1/projects?page_size=25&cursor=project-page-2", undefined);
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/tissues/v1/issues?page_size=50&cursor=issue-page-2&project=FLUENT", undefined);
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/tissues/v1/projects/FLUENT/issues", undefined);
  });

  it("sends an Issue PATCH containing content only", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "FLUENT-2" }), { status: 200 }));
    await api.updateIssue("FLUENT-2", { title: "Changed", description: "Body" });
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith("/api/tissues/v1/issues/FLUENT-2", expect.objectContaining({ method: "PATCH", body: '{"title":"Changed","description":"Body"}' }));
  });

  it("preserves structured API status, kind, and message", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { kind: "invalid", message: "parent Issue ID is invalid" } }), { status: 400 }));
    const error = await api.moveIssue("FLUENT-2", "bad").catch((cause) => cause);
    expect(error).toBeInstanceOf(APIError);
    expect(error).toMatchObject({ status: 400, kind: "invalid", message: "parent Issue ID is invalid" });
  });

  it("distinguishes an expired browser session", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { message: "authentication required" } }), { status: 401 }));
    await expect(api.listProjects()).rejects.toBeInstanceOf(UnauthorizedError);
  });
});
