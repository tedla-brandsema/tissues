import { FormEvent, useEffect, useMemo, useState } from "react";
import { MessageSquarePlus, Plus, RefreshCw } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { toast } from "sonner";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@tissues/frontend/components/ui/alert-dialog";
import { Badge } from "@tissues/frontend/components/ui/badge";
import { Button } from "@tissues/frontend/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@tissues/frontend/components/ui/dialog";
import { Input } from "@tissues/frontend/components/ui/input";
import { Separator } from "@tissues/frontend/components/ui/separator";
import { Skeleton } from "@tissues/frontend/components/ui/skeleton";
import { APIError, api, Comment, Issue, IssueOverview, listAllProjects, Project } from "./api";
import { AuthBootstrap } from "./auth";
import { MarkdownEditor } from "./MarkdownEditor";
import { ParentIssueInput } from "./ParentIssueInput";
import { Route } from "./routes";

const pageSize = 25;
const projectFilterKey = "tissues.issues-project-filter";
const flatten = (issues: Issue[]): Issue[] => issues.flatMap((issue) => [issue, ...flatten(issue.children)]);

export function IssuesOverview({ navigate, handleError }: { navigate: (route: Route) => void; handleError: (cause: unknown) => boolean }) {
  const [issues, setIssues] = useState<IssueOverview[]>([]);
  const [cursor, setCursor] = useState("");
  const [nextCursor, setNextCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectFilter, setProjectFilter] = useState("");
  async function load(value: string, projectID = projectFilter) {
    setLoading(true); setError("");
    try {
      const page = await api.listIssueOverviews(pageSize, value, projectID);
      setIssues(page.issues); setNextCursor(page.next_cursor); setCursor(value);
    } catch (cause) {
      if (!handleError(cause)) setError(cause instanceof Error ? cause.message : "Unable to load Issues");
    } finally { setLoading(false); }
  }
  useEffect(() => { void (async () => {
    try {
      const available = await listAllProjects(); setProjects(available);
      const saved = localStorage.getItem(projectFilterKey) || "";
      const initial = available.some((project) => project.key === saved) ? saved : "";
      if (!initial) localStorage.removeItem(projectFilterKey);
      setProjectFilter(initial); await load("", initial);
    } catch (cause) {
      if (!handleError(cause)) setError(cause instanceof Error ? cause.message : "Unable to load Issues");
      setLoading(false);
    }
  })(); }, []);
  function changeFilter(value: string) {
    setProjectFilter(value); setHistory([]); setCursor(""); setNextCursor("");
    if (value) localStorage.setItem(projectFilterKey, value); else localStorage.removeItem(projectFilterKey);
    void load("", value);
  }
  return <div className="overview-view">
    <header className="view-heading"><div><h1>Issues</h1><p>Work across every Project, ordered by recent activity.</p></div><Button onClick={() => navigate({ view: "issue", mode: "create" })}><Plus /> Issue</Button></header>
    <div className="overview-controls"><label>Project<select aria-label="Project" value={projectFilter} onChange={(event) => changeFilter(event.target.value)}><option value="">All projects</option>{projects.map((project) => <option key={project.key}>{project.key}</option>)}</select></label></div>
    {error ? <div className="error-state"><p>{error}</p><Button variant="outline" onClick={() => void load(cursor, projectFilter)}><RefreshCw /> Try again</Button></div> : loading ? <div className="skeletons"><Skeleton /><Skeleton /><Skeleton /></div> : <div className="table-frame">
      <table><thead><tr><th>Issue ID</th><th>Title</th><th>Project</th><th>State</th><th>Parent</th><th>Updated</th></tr></thead><tbody>
        {issues.map((issue) => <tr key={issue.id}><td><button className="table-link issue-id" onClick={() => navigate({ view: "issue", issue: issue.id })}>{issue.id}</button></td><td><button className="table-link" onClick={() => navigate({ view: "issue", issue: issue.id })}>{issue.title}</button></td><td>{issue.project_key}</td><td><Badge>{issue.state}</Badge></td><td>{issue.parent_id || "—"}</td><td>{new Date(issue.updated).toLocaleString()}</td></tr>)}
      </tbody></table>
      {!issues.length && <p className="table-empty">No Issues yet. Use + Issue to create the first one.</p>}
    </div>}
    <div className="pagination"><Button variant="outline" disabled={!history.length || loading} onClick={() => { const previous = history.at(-1)!; setHistory((items) => items.slice(0, -1)); void load(previous, projectFilter); }}>Previous</Button><Button variant="outline" disabled={!nextCursor || loading} onClick={() => { setHistory((items) => [...items, cursor]); void load(nextCursor, projectFilter); }}>Next</Button></div>
  </div>;
}

function parentError(cause: unknown, projectKey: string): string {
  if (!(cause instanceof APIError)) return cause instanceof Error ? cause.message : "Unable to save Issue";
  if (cause.status === 404) return "Parent issue does not exist.";
  if (cause.message.includes("must belong")) return `Parent issue must belong to Project ${projectKey}.`;
  if (cause.message.includes("own parent")) return "An Issue cannot be its own parent.";
  if (cause.message.includes("descendant")) return "Parent issue would create a hierarchy cycle.";
  return cause.message;
}

function CommentDialog({ comment, onClose, onSave }: { comment: Comment; onClose: () => void; onSave: (body: string) => Promise<void> }) {
  const [body, setBody] = useState(comment.body);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    try { await onSave(body); } catch (cause) { setError(cause instanceof Error ? cause.message : "Unable to edit Comment"); }
  }
  return <Dialog open onOpenChange={(value) => !value && onClose()}><DialogContent><form onSubmit={submit} className="form-stack">
    <DialogHeader><DialogTitle>Edit comment</DialogTitle><DialogDescription>Update the comment as Markdown.</DialogDescription></DialogHeader>
    <MarkdownEditor label="Comment body" value={body} onChange={setBody} size="compact" />
    {error && <p className="form-error" role="alert">{error}</p>}
    <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cancel</Button><Button type="submit" disabled={!body.trim()}>Save</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}

function ParentDialog({ issue, suggestions, onClose, onMoved, handleError }: { issue: Issue; suggestions: Issue[]; onClose: () => void; onMoved: (issue: Issue) => void; handleError: (cause: unknown) => boolean }) {
  const [parentID, setParentID] = useState(issue.parent_id);
  const [error, setError] = useState("");
  const [invalid, setInvalid] = useState(false);
  const [pending, setPending] = useState(false);
  async function move(value: string) {
    setPending(true); setError(""); setInvalid(false);
    try {
      const updated = await api.moveIssue(issue.id, value); onMoved(updated);
      toast.success(value ? "Parent updated" : "Issue detached");
    } catch (cause) {
      if (!handleError(cause)) { setError(parentError(cause, issue.project_key)); setInvalid(Boolean(value)); }
    } finally { setPending(false); }
  }
  function submit(event: FormEvent) { event.preventDefault(); void move(parentID); }
  return <Dialog open onOpenChange={(open) => !open && onClose()}><DialogContent><form onSubmit={submit} className="form-stack">
    <DialogHeader><DialogTitle>{issue.parent_id ? "Change" : "Set"} parent for {issue.id}</DialogTitle><DialogDescription>{issue.parent_id ? `Current parent: ${issue.parent_id}` : "Choose an Issue in the same Project."}</DialogDescription></DialogHeader>
    <ParentIssueInput value={parentID} onChange={(value) => { setParentID(value); setInvalid(false); }} issues={suggestions} excludeID={issue.id} invalid={invalid} />
    {error && <p className="form-error" role="alert">{error}</p>}
    <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>{issue.parent_id && <Button type="button" variant="outline" disabled={pending} onClick={() => void move("")}>Detach</Button>}<Button type="submit" disabled={pending || !parentID.trim()}>Save parent</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}

export function IssueView({ route, navigate, handleError, bootstrap }: { route: Extract<Route, { view: "issue" }>; navigate: (route: Route) => void; handleError: (cause: unknown) => boolean; bootstrap: AuthBootstrap }) {
  const create = "mode" in route;
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectKey, setProjectKey] = useState("");
  const [issue, setIssue] = useState<Issue>();
  const [suggestions, setSuggestions] = useState<Issue[]>([]);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState("");
  const [pageError, setPageError] = useState("");
  const [parentOpen, setParentOpen] = useState(false);
  const [confirmState, setConfirmState] = useState<"close" | "reopen" | null>(null);
  const [author, setAuthor] = useState(() => bootstrap.enabled ? "" : localStorage.getItem("tissues.comment-author") || "");
  const [comment, setComment] = useState("");
  const [commentError, setCommentError] = useState("");
  const [commentEditor, setCommentEditor] = useState(0);
  const [editingComment, setEditingComment] = useState<Comment>();
  const allSuggestions = useMemo(() => flatten(suggestions), [suggestions]);

  async function loadProjectIssues(key: string) {
    if (!key) { setSuggestions([]); return; }
    setSuggestions(await api.listProjectIssues(key));
  }
  async function loadExisting(id: string) {
    const loaded = await api.getIssue(id);
    setIssue(loaded); setProjectKey(loaded.project_key); setTitle(loaded.title); setDescription(loaded.description);
    await loadProjectIssues(loaded.project_key);
  }
  useEffect(() => { void (async () => {
    setLoading(true); setPageError("");
    try {
      const available = await listAllProjects(); setProjects(available);
      if (create) {
        const saved = localStorage.getItem(projectFilterKey) || "";
        const initial = available.some((project) => project.key === saved) ? saved : available[0]?.key || "";
        setProjectKey(initial);
      } else await loadExisting(route.issue);
    } catch (cause) {
      if (!handleError(cause)) setPageError(cause instanceof Error ? cause.message : "Unable to load Issue");
    } finally { setLoading(false); }
  })(); }, [create, create ? "" : route.issue]);

  async function changeProject(value: string) {
    setProjectKey(value);
  }
  async function save(event: FormEvent) {
    event.preventDefault(); setPending(true); setFormError("");
    try {
      if (create) {
        const created = await api.createIssue(projectKey, { title, description });
        toast.success(`${created.id} created`); navigate({ view: "issue", issue: created.id });
      } else if (issue) {
        const updated = await api.updateIssue(issue.id, { title, description });
        setIssue(updated); toast.success("Issue saved");
      }
    } catch (cause) {
      if (!handleError(cause)) setFormError(cause instanceof Error ? cause.message : "Unable to save Issue");
    } finally { setPending(false); }
  }
  async function changeState() {
    if (!issue || !confirmState) return;
    const action = confirmState; setPending(true);
    try {
      const updated = await api.state(issue.id, action); setIssue({ ...issue, state: updated.state, updated: updated.updated });
      toast.success(action === "close" ? "Issue closed" : "Issue reopened"); setConfirmState(null);
    } catch (cause) { if (!handleError(cause)) toast.error(cause instanceof Error ? cause.message : "Unable to change Issue state"); setConfirmState(null); }
    finally { setPending(false); }
  }
  async function addComment(event: FormEvent) {
    event.preventDefault(); if (!issue || !comment.trim()) return;
    setCommentError(""); setPending(true);
    try {
      if (!bootstrap.enabled) localStorage.setItem("tissues.comment-author", author);
      const added = await api.comment(issue.id, bootstrap.enabled ? "" : author, comment); setIssue({ ...issue, comments: [...issue.comments, added] });
      setComment(""); setCommentEditor((value) => value + 1); toast.success("Comment added");
    } catch (cause) { if (!handleError(cause)) setCommentError(cause instanceof Error ? cause.message : "Unable to add Comment"); }
    finally { setPending(false); }
  }
  async function saveComment(body: string) {
    if (!issue || !editingComment) return;
    const edited = await api.editComment(issue.id, editingComment.id, body); setIssue({ ...issue, comments: issue.comments.map((item) => item.id === edited.id ? edited : item) }); setEditingComment(undefined); toast.success("Comment updated");
  }

  if (loading) return <div className="form-view skeletons"><Skeleton /><Skeleton /><Skeleton /></div>;
  if (pageError) return <div className="error-state"><p>{pageError}</p><Button variant="outline" onClick={() => location.reload()}><RefreshCw /> Try again</Button></div>;
  if (create && !projects.length) return <div className="welcome"><h1>Create an Issue</h1><p>An Issue needs a Project. Create a Project first.</p><Button onClick={() => navigate({ view: "project", mode: "create" })}>Create project</Button></div>;
  if (!create && !issue) return <div className="error-state"><p>Unable to load Issue.</p><Button variant="outline" onClick={() => location.reload()}><RefreshCw /> Try again</Button></div>;
  return <div className="form-view issue-view"><header><p className="eyebrow">{create ? "New Issue" : issue?.id}</p><h1>{create ? "Create issue" : issue?.title}</h1>{!create && <Badge>{issue?.state}</Badge>}</header>
    <form onSubmit={save} className={`form-stack issue-form${create ? "" : " issue-form--existing"}`}>
      {create ? <label>Project ID<select aria-label="Project ID" required value={projectKey} onChange={(event) => void changeProject(event.target.value)}>{projects.map((project) => <option key={project.key}>{project.key}</option>)}</select></label> : <div className="immutable-row"><div><span>Project ID</span><strong>{issue?.project_key}</strong></div><div><span>Issue ID</span><strong>{issue?.id}</strong></div><div><span>State</span><strong>{issue?.state}</strong></div></div>}
      <label>Title<Input required value={title} onChange={(event) => setTitle(event.target.value)} /></label>
      <MarkdownEditor key={create ? `new-${projectKey}` : issue?.id} label="Description" value={description} onChange={setDescription} />
      {formError && <p className="form-error" role="alert">{formError}</p>}
      <div className="form-actions"><Button type="button" variant="outline" onClick={() => navigate({ view: "issues" })}>{create ? "Cancel" : "Back"}</Button><Button type="submit" disabled={pending || !projectKey || !title.trim() || !description.trim()}>{create ? "Create" : "Save"}</Button>{!create && <Button type="button" variant="outline" disabled={pending} onClick={() => setParentOpen(true)}>{issue?.parent_id ? "Change parent" : "Set parent"}</Button>}{!create && <Button type="button" variant="outline" disabled={pending} onClick={() => setConfirmState(issue?.state === "open" ? "close" : "reopen")}>{issue?.state === "open" ? "Close issue" : "Reopen issue"}</Button>}</div>
    </form>
    {!create && <div className="issue-relationship"><div><span>Parent</span><strong>{issue?.parent_id || "None"}</strong></div></div>}
    {!create && <><Separator className="issue-section-separator" /><section className="comments"><h2>Comments <span>{issue?.comments.length}</span></h2>{(issue?.comments ?? []).map((item) => <article key={item.id}><div><strong>{item.author}</strong><time>{new Date(item.created).toLocaleString()}{item.updated !== item.created ? " (edited)" : ""}</time><Button variant="ghost" size="sm" onClick={() => setEditingComment(item)}>Edit</Button></div><div className="markdown comment-body"><ReactMarkdown remarkPlugins={[remarkGfm]}>{item.body}</ReactMarkdown></div></article>)}
      <form onSubmit={addComment} className="comment-form"><h3><MessageSquarePlus /> Add comment</h3>{bootstrap.enabled ? <p className="comment-identity">Commenting as <strong>{bootstrap.author}</strong></p> : <Input aria-label="Comment author" required placeholder="Your name or email" value={author} onChange={(event) => setAuthor(event.target.value)} />}<MarkdownEditor key={`${issue?.id}-${commentEditor}`} label="Comment body" value={comment} onChange={setComment} size="compact" />{commentError && <p className="form-error" role="alert">{commentError}</p>}<Button type="submit" disabled={pending || !comment.trim()}>Comment</Button></form>
    </section></>}
    {editingComment && <CommentDialog key={editingComment.id} comment={editingComment} onClose={() => setEditingComment(undefined)} onSave={saveComment} />}
    {parentOpen && issue && <ParentDialog issue={issue} suggestions={allSuggestions} handleError={handleError} onClose={() => setParentOpen(false)} onMoved={(updated) => { setIssue(updated); setParentOpen(false); }} />}
    {!create && <AlertDialog open={confirmState !== null} onOpenChange={(open) => !open && setConfirmState(null)}><AlertDialogContent><AlertDialogHeader><AlertDialogTitle>{confirmState === "close" ? `Close ${issue?.id}?` : `Reopen ${issue?.id}?`}</AlertDialogTitle><AlertDialogDescription>{confirmState === "close" ? "This Issue will move to the closed state." : "This Issue will return to the open state."}</AlertDialogDescription></AlertDialogHeader><AlertDialogFooter><AlertDialogCancel asChild><Button variant="outline">Cancel</Button></AlertDialogCancel><AlertDialogAction asChild><Button disabled={pending} onClick={() => void changeState()}>{confirmState === "close" ? "Close issue" : "Reopen issue"}</Button></AlertDialogAction></AlertDialogFooter></AlertDialogContent></AlertDialog>}
  </div>;
}
