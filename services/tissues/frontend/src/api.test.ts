import { afterEach, describe, expect, it, vi } from "vitest";
import { api, UnauthorizedError } from "./api";

afterEach(() => vi.restoreAllMocks());

describe("tissues API client", () => {
  it("uses the same-origin API and strict JSON mutations", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "1", title: "First" }), { status: 201, headers: { "Content-Type": "application/json" } }));
    await api.create({ title: "First", description: "Body", parent_id: "" });
    expect(fetchMock).toHaveBeenCalledWith("/api/tissues/v1/issues", expect.objectContaining({ method: "POST", headers: { "Content-Type": "application/json" } }));
  });

  it("surfaces the structured API message", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { kind: "invalid", message: "title is required" } }), { status: 400 }));
    await expect(api.create({ title: "", description: "", parent_id: "" })).rejects.toThrow("title is required");
  });

  it("distinguishes an expired browser session", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { message: "authentication required" } }), { status: 401 }));
    await expect(api.list()).rejects.toBeInstanceOf(UnauthorizedError);
  });

  it("addresses reopen and detach through their explicit operations", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ id: "1", state: "open", parent_id: "" }), { status: 200 }));
    await api.state("1", "reopen");
    await api.move("1", "");
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/tissues/v1/issues/1/reopen", expect.objectContaining({ method: "POST" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/tissues/v1/issues/1/parent", expect.objectContaining({ method: "PUT", body: '{"parent_id":""}' }));
  });
});
