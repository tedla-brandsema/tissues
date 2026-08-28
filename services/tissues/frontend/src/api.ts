export type Comment = { id: string; author: string; created: string; updated: string; body: string };
export type Issue = { id: string; title: string; state: "open" | "closed"; created: string; updated: string; description: string; parent_id: string; children: Issue[]; comments: Comment[] };
type APIError = { error?: { message?: string } };
export class UnauthorizedError extends Error {}

const base = "/api/tissues/v1";
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${base}${path}`, init);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as APIError;
    const message = body.error?.message || `Request failed (${response.status})`;
    if (response.status === 401) throw new UnauthorizedError(message);
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}
const json = (method: string, body: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export const api = {
  list: async () => (await request<{ issues: Issue[] }>("/issues")).issues,
  get: (id: string) => request<Issue>(`/issues/${encodeURIComponent(id)}`),
  create: (body: { title: string; description: string; parent_id: string }) => request<Issue>("/issues", json("POST", body)),
  update: (id: string, body: { title: string; description: string }) => request<Issue>(`/issues/${encodeURIComponent(id)}`, json("PATCH", body)),
  move: (id: string, parent_id: string) => request<Issue>(`/issues/${encodeURIComponent(id)}/parent`, json("PUT", { parent_id })),
  state: (id: string, state: "close" | "reopen") => request<Issue>(`/issues/${encodeURIComponent(id)}/${state}`, json("POST", {})),
  comment: (id: string, author: string, body: string) => request<Comment>(`/issues/${encodeURIComponent(id)}/comments`, json("POST", author ? { author, body } : { body })),
  editComment: (id: string, commentID: string, body: string) => request<Comment>(`/issues/${encodeURIComponent(id)}/comments/${encodeURIComponent(commentID)}`, json("PATCH", { body })),
};
