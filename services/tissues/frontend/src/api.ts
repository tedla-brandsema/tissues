export type Project = { key: string; created: string };
export type ProjectPage = { projects: Project[]; next_cursor: string };
export type Comment = { id: string; author: string; created: string; updated: string; body: string };
export type Issue = {
  id: string;
  project_key: string;
  number: number;
  title: string;
  state: "open" | "closed";
  created: string;
  updated: string;
  description: string;
  parent_id: string;
  children: Issue[];
  comments: Comment[];
};
export type IssueOverview = Pick<Issue, "project_key" | "number" | "id" | "title" | "state" | "parent_id" | "updated">;
export type IssueOverviewPage = { issues: IssueOverview[]; next_cursor: string };

type ErrorEnvelope = { error?: { kind?: string; message?: string } };
export class APIError extends Error {
  constructor(public status: number, public kind: string, message: string) {
    super(message);
    this.name = "APIError";
  }
}
export class UnauthorizedError extends APIError {
  constructor(message: string) {
    super(401, "unauthorized", message);
    this.name = "UnauthorizedError";
  }
}

const base = "/api/tissues/v1";
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${base}${path}`, init);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ErrorEnvelope;
    const message = body.error?.message || `Request failed (${response.status})`;
    const kind = body.error?.kind || "unknown";
    if (response.status === 401) throw new UnauthorizedError(message);
    throw new APIError(response.status, kind, message);
  }
  return response.json() as Promise<T>;
}
const json = (method: string, body: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
function pageQuery(pageSize: number, cursor = "", projectID = "") {
  const query = new URLSearchParams({ page_size: String(pageSize) });
  if (cursor) query.set("cursor", cursor);
  if (projectID) query.set("project", projectID);
  return query.toString();
}

export const api = {
  listProjects: (pageSize = 25, cursor = "") => request<ProjectPage>(`/projects?${pageQuery(pageSize, cursor)}`),
  getProject: (key: string) => request<Project>(`/projects/${encodeURIComponent(key)}`),
  createProject: (key: string) => request<Project>("/projects", json("POST", { key })),
  listIssueOverviews: (pageSize = 25, cursor = "", projectID = "") => request<IssueOverviewPage>(`/issues?${pageQuery(pageSize, cursor, projectID)}`),
  listProjectIssues: async (projectKey: string) => (await request<{ issues: Issue[] }>(`/projects/${encodeURIComponent(projectKey)}/issues`)).issues,
  getIssue: (id: string) => request<Issue>(`/issues/${encodeURIComponent(id)}`),
  createIssue: (projectKey: string, body: { title: string; description: string }) => request<Issue>(`/projects/${encodeURIComponent(projectKey)}/issues`, json("POST", body)),
  updateIssue: (id: string, body: { title?: string; description?: string }) => request<Issue>(`/issues/${encodeURIComponent(id)}`, json("PATCH", body)),
  moveIssue: (id: string, parent_id: string) => request<Issue>(`/issues/${encodeURIComponent(id)}/parent`, json("PUT", { parent_id })),
  state: (id: string, state: "close" | "reopen") => request<Issue>(`/issues/${encodeURIComponent(id)}/${state}`, json("POST", {})),
  comment: (id: string, author: string, body: string) => request<Comment>(`/issues/${encodeURIComponent(id)}/comments`, json("POST", author ? { author, body } : { body })),
  editComment: (id: string, commentID: string, body: string) => request<Comment>(`/issues/${encodeURIComponent(id)}/comments/${encodeURIComponent(commentID)}`, json("PATCH", { body })),
};

export async function listAllProjects(): Promise<Project[]> {
  const projects: Project[] = [];
  let cursor = "";
  do {
    const page = await api.listProjects(100, cursor);
    projects.push(...page.projects);
    cursor = page.next_cursor;
  } while (cursor);
  return projects;
}
