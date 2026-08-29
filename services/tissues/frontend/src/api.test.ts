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

  it("lists assets using the encoded Issue route", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ assets: [] }), { status: 200 }));
    await expect(api.listAssets("FLUENT/1")).resolves.toEqual({ assets: [] });
    expect(fetchMock).toHaveBeenCalledWith("/api/tissues/v1/issues/FLUENT%2F1/assets", undefined);
  });

  it("uploads one file field as FormData without setting multipart Content-Type", async () => {
    const asset = { name: "example.png", url: "/api/tissues/v1/issues/FLUENT-1/assets/example.png", content_type: "image/png", width: 2, height: 3, size: 80 };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(asset), { status: 201 }));
    const file = new File(["png"], "Example.PNG", { type: "image/png" });
    await expect(api.uploadAsset("FLUENT-1", file)).resolves.toEqual(asset);
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/tissues/v1/issues/FLUENT-1/assets");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBeInstanceOf(FormData);
    expect([...((init?.body as FormData).entries())]).toEqual([["file", file]]);
    expect(new Headers(init?.headers).has("Content-Type")).toBe(false);
  });

  it.each([
    [400, "invalid"],
    [409, "conflict"],
    [413, "too_large"],
  ])("preserves structured upload error %s", async (status, kind) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { kind, message: `asset ${kind}` } }), { status }));
    const error = await api.uploadAsset("FLUENT-1", new File(["x"], "x.png")).catch((cause) => cause);
    expect(error).toBeInstanceOf(APIError);
    expect(error).toMatchObject({ status, kind, message: `asset ${kind}` });
  });

  it("preserves UnauthorizedError for asset requests", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { kind: "unauthorized", message: "authentication required" } }), { status: 401 }));
    await expect(api.listAssets("FLUENT-1")).rejects.toBeInstanceOf(UnauthorizedError);
    await expect(api.uploadAsset("FLUENT-1", new File(["x"], "x.png"))).rejects.toBeInstanceOf(UnauthorizedError);
  });
});
