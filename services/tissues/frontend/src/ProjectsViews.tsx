import { FormEvent, useEffect, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@tissues/frontend/components/ui/button";
import { Input } from "@tissues/frontend/components/ui/input";
import { Skeleton } from "@tissues/frontend/components/ui/skeleton";
import { api, Project } from "./api";
import { Route } from "./routes";

const pageSize = 25;

export function ProjectsOverview({ navigate, handleError }: { navigate: (route: Route) => void; handleError: (cause: unknown) => boolean }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [cursor, setCursor] = useState("");
  const [nextCursor, setNextCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  async function load(value: string) {
    setLoading(true); setError("");
    try {
      const page = await api.listProjects(pageSize, value);
      setProjects(page.projects); setNextCursor(page.next_cursor); setCursor(value);
    } catch (cause) {
      if (!handleError(cause)) setError(cause instanceof Error ? cause.message : "Unable to load Projects");
    } finally { setLoading(false); }
  }
  useEffect(() => { void load(""); }, []);
  return <div className="overview-view">
    <header className="view-heading"><div><h1>Projects</h1><p>Stable namespaces for numbered Issues.</p></div><Button onClick={() => navigate({ view: "project", mode: "create" })}><Plus /> Project</Button></header>
    {error ? <div className="error-state"><p>{error}</p><Button variant="outline" onClick={() => void load(cursor)}><RefreshCw /> Try again</Button></div> : loading ? <div className="skeletons"><Skeleton /><Skeleton /><Skeleton /></div> : <div className="table-frame">
      <table><thead><tr><th>Project ID</th><th>Created</th></tr></thead><tbody>
        {projects.map((project) => <tr key={project.key}><td><button className="table-link" onClick={() => navigate({ view: "project", project: project.key })}>{project.key}</button></td><td>{new Date(project.created).toLocaleString()}</td></tr>)}
      </tbody></table>
      {!projects.length && <p className="table-empty">No Projects yet. Use + Project to create the first stable Issue namespace.</p>}
    </div>}
    <div className="pagination"><Button variant="outline" disabled={!history.length || loading} onClick={() => { const previous = history.at(-1)!; setHistory((items) => items.slice(0, -1)); void load(previous); }}>Previous</Button><Button variant="outline" disabled={!nextCursor || loading} onClick={() => { setHistory((items) => [...items, cursor]); void load(nextCursor); }}>Next</Button></div>
  </div>;
}

export function ProjectView({ route, navigate, handleError }: { route: Extract<Route, { view: "project" }>; navigate: (route: Route) => void; handleError: (cause: unknown) => boolean }) {
  const create = "mode" in route;
  const [project, setProject] = useState<Project>();
  const [key, setKey] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (create) return;
    void api.getProject(route.project).then(setProject).catch((cause) => { if (!handleError(cause)) setError(cause instanceof Error ? cause.message : "Unable to load Project"); });
  }, [create, create ? "" : route.project]);
  async function submit(event: FormEvent) {
    event.preventDefault(); setPending(true); setError("");
    try {
      const created = await api.createProject(key);
      toast.success(`Project ${created.key} created`);
      navigate({ view: "project", project: created.key });
    } catch (cause) {
      if (!handleError(cause)) setError(cause instanceof Error ? cause.message : "Unable to create Project");
    } finally { setPending(false); }
  }
  if (create) return <div className="form-view"><header><h1>Create project</h1><p>The Project ID becomes the prefix for Issue IDs.</p></header><form onSubmit={submit} className="form-stack">
    <label>Project ID<Input required maxLength={16} placeholder="Project ID" value={key} onChange={(event) => setKey(event.target.value.toUpperCase())} /></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="form-actions"><Button type="button" variant="outline" onClick={() => navigate({ view: "projects" })}>Cancel</Button><Button type="submit" disabled={pending || !key.trim()}>Create</Button></div>
  </form></div>;
  return <div className="form-view"><header><p className="eyebrow">Project</p><h1>{route.project}</h1></header>{error ? <p className="form-error" role="alert">{error}</p> : project ? <dl className="read-only-fields"><div><dt>Project ID</dt><dd>{project.key}</dd></div><div><dt>Created</dt><dd>{new Date(project.created).toLocaleString()}</dd></div></dl> : <div className="skeletons"><Skeleton /><Skeleton /></div>}</div>;
}
