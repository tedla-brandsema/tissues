import { FormEvent, useEffect, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Menu, MessageSquarePlus, Pencil, Plus, RefreshCw } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { toast } from "sonner";
import { Badge } from "@tissues/frontend/components/ui/badge";
import { Button } from "@tissues/frontend/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@tissues/frontend/components/ui/dialog";
import { Input } from "@tissues/frontend/components/ui/input";
import { ScrollArea } from "@tissues/frontend/components/ui/scroll-area";
import { Separator } from "@tissues/frontend/components/ui/separator";
import { Skeleton } from "@tissues/frontend/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@tissues/frontend/components/ui/tabs";
import { Toaster } from "@tissues/frontend/components/ui/sonner";
import { api, Comment, Issue } from "./api";
import { AuthBootstrap, readAuthBootstrap, recoverExpiredSession } from "./auth";
import { MarkdownEditor } from "./MarkdownEditor";

type Filter = "open" | "closed" | "all";
const issueIDFromURL = () => new URLSearchParams(location.search).get("issue") || "";
const filterFromURL = (): Filter => { const value = new URLSearchParams(location.search).get("view"); return value === "closed" || value === "all" ? value : "open"; };
function updateURL(issue: string, view: Filter) { const query = new URLSearchParams(); if (issue) query.set("issue", issue); query.set("view", view); history.replaceState(null, "", `?${query}`); }

function flatten(issues: Issue[]): Issue[] { return issues.flatMap((issue) => [issue, ...flatten(issue.children)]); }
function filteredTree(issues: Issue[], filter: Filter, query: string): Issue[] {
  return issues.flatMap((issue) => {
    const children = filteredTree(issue.children, filter, query);
    const matches = (filter === "all" || issue.state === filter) && issue.title.toLowerCase().includes(query.toLowerCase());
    return matches || children.length ? [{ ...issue, children }] : [];
  });
}

function IssueTree({ issues, selected, onSelect }: { issues: Issue[]; selected: string; onSelect: (id: string) => void }) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  return <ul className="issue-tree">{issues.map((issue) => <li key={issue.id}>
    <div className={`tree-row ${selected === issue.id ? "selected" : ""}`}>
      {issue.children.length ? <button aria-label={`${collapsed[issue.id] ? "Expand" : "Collapse"} ${issue.title}`} onClick={() => setCollapsed((old) => ({ ...old, [issue.id]: !old[issue.id] }))}>{collapsed[issue.id] ? <ChevronRight /> : <ChevronDown />}</button> : <span className="tree-spacer" />}
      <button className="tree-title" onClick={() => onSelect(issue.id)}>{issue.title}</button>
      {issue.state === "closed" && <Badge>closed</Badge>}
    </div>
    {!collapsed[issue.id] && issue.children.length > 0 && <IssueTree issues={issue.children} selected={selected} onSelect={onSelect} />}
  </li>)}</ul>;
}

function IssueDialog({ issue, issues, onClose, onSave }: { issue?: Issue; issues: Issue[]; onClose: () => void; onSave: (value: { title: string; description: string; parent_id: string }) => Promise<void> }) {
  const [title, setTitle] = useState(issue?.title || ""); const [description, setDescription] = useState(issue?.description || ""); const [parent, setParent] = useState(issue?.parent_id || "");
  async function submit(event: FormEvent) { event.preventDefault(); await onSave({ title, description, parent_id: parent }); }
  return <Dialog open onOpenChange={(value) => !value && onClose()}><DialogContent><form onSubmit={submit} className="form-stack">
    <DialogHeader><DialogTitle>{issue ? "Edit issue" : "Create issue"}</DialogTitle><DialogDescription>{issue ? "Update its title and Markdown description." : "Add work to the shared issue tree."}</DialogDescription></DialogHeader>
    <label>Title<Input required value={title} onChange={(event) => setTitle(event.target.value)} /></label>
    <MarkdownEditor label="Description" value={description} onChange={setDescription} />
    {!issue && <label>Parent<select value={parent} onChange={(event) => setParent(event.target.value)}><option value="">No parent</option>{issues.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label>}
    <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cancel</Button><Button type="submit" disabled={!title.trim() || !description.trim()}>Save</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}

function CommentDialog({ comment, onClose, onSave }: { comment: Comment; onClose: () => void; onSave: (body: string) => Promise<void> }) {
  const [body, setBody] = useState(comment.body);
  async function submit(event: FormEvent) { event.preventDefault(); await onSave(body); }
  return <Dialog open onOpenChange={(value) => !value && onClose()}><DialogContent><form onSubmit={submit} className="form-stack">
    <DialogHeader><DialogTitle>Edit comment</DialogTitle><DialogDescription>Update the comment as Markdown.</DialogDescription></DialogHeader>
    <MarkdownEditor label="Comment body" value={body} onChange={setBody} size="compact" />
    <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cancel</Button><Button type="submit" disabled={!body.trim()}>Save</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}

type AppProps = { bootstrap?: AuthBootstrap; recoverSession?: (cause: unknown) => boolean };

export function App({ bootstrap = readAuthBootstrap(), recoverSession = recoverExpiredSession }: AppProps = {}) {
  const [roots, setRoots] = useState<Issue[]>([]); const [selectedID, setSelectedID] = useState(issueIDFromURL()); const [selected, setSelected] = useState<Issue>();
  const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [filter, setFilter] = useState<Filter>(filterFromURL); const [query, setQuery] = useState(""); const [pending, setPending] = useState(false);
  const [navigator, setNavigator] = useState(false); const [dialog, setDialog] = useState<"create" | "edit" | null>(null); const [moving, setMoving] = useState(false); const [editingComment, setEditingComment] = useState<Comment>();
  const [author, setAuthor] = useState(() => bootstrap.enabled ? "" : localStorage.getItem("tissues.comment-author") || ""); const [comment, setComment] = useState(""); const [commentEditor, setCommentEditor] = useState(0);
  const all = useMemo(() => flatten(roots), [roots]); const visible = useMemo(() => filteredTree(roots, filter, query), [roots, filter, query]);
  async function load(preferred = selectedID) { setError(""); try { const next = await api.list(); setRoots(next); if (preferred) { const detail = await api.get(preferred); setSelected(detail); } } catch (cause) { if (!recoverSession(cause)) setError(cause instanceof Error ? cause.message : "Unable to load issues"); } finally { setLoading(false); } }
  useEffect(() => { void load(); }, []);
  async function choose(id: string) { setSelectedID(id); updateURL(id, filter); setNavigator(false); try { setSelected(await api.get(id)); } catch (cause) { if (!recoverSession(cause)) toast.error(String(cause)); } }
  async function mutate(action: () => Promise<unknown>, success: string) { setPending(true); try { await action(); await load(selectedID); toast.success(success); } catch (cause) { if (!recoverSession(cause)) toast.error(cause instanceof Error ? cause.message : "Request failed"); } finally { setPending(false); } }
  async function saveIssue(value: { title: string; description: string; parent_id: string }) { if (dialog === "edit" && selected) await mutate(() => api.update(selected.id, value), "Issue updated"); else { let created: Issue | undefined; await mutate(async () => { created = await api.create(value); setSelectedID(created.id); updateURL(created.id, filter); }, "Issue created"); if (created) setSelected(created); } setDialog(null); }
  async function addComment(event: FormEvent) { event.preventDefault(); if (!selected || !comment.trim()) return; if (!bootstrap.enabled) localStorage.setItem("tissues.comment-author", author); await mutate(() => api.comment(selected.id, bootstrap.enabled ? "" : author, comment), "Comment added"); setComment(""); setCommentEditor((value) => value + 1); }
  async function saveComment(body: string) { if (!selected || !editingComment) return; await mutate(() => api.editComment(selected.id, editingComment.id, body), "Comment updated"); setEditingComment(undefined); }
  return <><div className="app-shell">
    <div className="mobile-toolbar" aria-label="Workspace actions"><Button variant="outline" size="icon" aria-label="Open issue navigator" onClick={() => setNavigator(true)}><Menu /></Button><strong>🤧 tissues</strong><Button aria-label="New issue" onClick={() => setDialog("create")}><Plus /> Issue</Button></div>
    <main>
      <aside className={navigator ? "navigator open" : "navigator"} aria-label="Issue navigator">
        <div className="navigator-heading"><strong className="app-identity">🤧 tissues</strong><div className="navigator-actions"><Button aria-label="New issue" size="sm" onClick={() => setDialog("create")}><Plus /> Issue</Button><Button className="mobile-only" variant="ghost" onClick={() => setNavigator(false)}>Close</Button></div></div>
        <Tabs value={filter} onValueChange={(value) => { const next = value as Filter; setFilter(next); updateURL(selectedID, next); }}><TabsList><TabsTrigger value="open">Open</TabsTrigger><TabsTrigger value="closed">Closed</TabsTrigger><TabsTrigger value="all">All</TabsTrigger></TabsList></Tabs>
        <Input aria-label="Filter issues" placeholder="Filter by title…" value={query} onChange={(event) => setQuery(event.target.value)} />
        <ScrollArea className="tree-scroll">{loading ? <div className="skeletons"><Skeleton /><Skeleton /><Skeleton /></div> : visible.length ? <IssueTree issues={visible} selected={selectedID} onSelect={choose} /> : <p className="empty">No matching issues.</p>}</ScrollArea>
      </aside>
      <section className="detail">
        {error ? <div className="error-state"><p>{error}</p><Button variant="outline" onClick={() => void load()}><RefreshCw /> Try again</Button></div> : !selected ? <div className="welcome"><h2>Choose an issue</h2><p>Select work from the navigator, or create the first issue.</p></div> : <>
          <div className="detail-heading"><div><Badge>{selected.state}</Badge><h2>{selected.title}</h2><p>{selected.parent_id ? `Attached beneath ${all.find((item) => item.id === selected.parent_id)?.title || "another issue"} · ` : ""}Updated {new Date(selected.updated).toLocaleString()}</p></div><div className="detail-actions"><Button disabled={pending} variant="outline" onClick={() => setDialog("edit")}><Pencil /> Edit</Button><Button disabled={pending} variant="outline" onClick={() => setMoving(true)}>Move</Button>{selected.parent_id && <Button disabled={pending} variant="outline" onClick={() => void mutate(() => api.move(selected.id, ""), "Issue detached")}>Detach</Button>}<Button disabled={pending} variant="outline" onClick={() => void mutate(() => api.state(selected.id, selected.state === "open" ? "close" : "reopen"), selected.state === "open" ? "Issue closed" : "Issue reopened")}>{selected.state === "open" ? "Close" : "Reopen"}</Button></div></div>
          <article className="markdown issue-description"><ReactMarkdown remarkPlugins={[remarkGfm]}>{selected.description}</ReactMarkdown></article><Separator />
          <section className="comments"><h3>Comments <span>{selected.comments.length}</span></h3>{selected.comments.map((item) => <article key={item.id}><div><strong>{item.author}</strong><time>{new Date(item.created).toLocaleString()}{item.updated !== item.created ? " (edited)" : ""}</time><Button variant="ghost" size="sm" onClick={() => setEditingComment(item)}>Edit</Button></div><div className="markdown comment-body"><ReactMarkdown remarkPlugins={[remarkGfm]}>{item.body}</ReactMarkdown></div></article>)}
            <form onSubmit={addComment} className="comment-form"><h4><MessageSquarePlus /> Add comment</h4>{bootstrap.enabled ? <p className="comment-identity">Commenting as <strong>{bootstrap.author}</strong></p> : <Input aria-label="Comment author" required placeholder="Your name or email" value={author} onChange={(event) => setAuthor(event.target.value)} />}<MarkdownEditor key={`${selected.id}-${commentEditor}`} label="Comment body" value={comment} onChange={setComment} size="compact" /><Button type="submit" disabled={!comment.trim()}>Comment</Button></form>
          </section>
        </>}
      </section>
    </main>
  </div>
  {dialog && <IssueDialog key={`${dialog}-${selected?.id || "new"}`} issue={dialog === "edit" ? selected : undefined} issues={all} onClose={() => setDialog(null)} onSave={saveIssue} />}
  {editingComment && <CommentDialog key={editingComment.id} comment={editingComment} onClose={() => setEditingComment(undefined)} onSave={saveComment} />}
  <Dialog open={moving} onOpenChange={setMoving}><DialogContent><DialogHeader><DialogTitle>Move issue</DialogTitle><DialogDescription>Current parent: {all.find((item) => item.id === selected?.parent_id)?.title || "none"}. Choose another issue as parent, or detach it.</DialogDescription></DialogHeader><div className="move-list"><Button variant="outline" onClick={() => { setMoving(false); void mutate(() => api.move(selected!.id, ""), "Issue detached"); }}>Detach (no parent)</Button>{all.filter((item) => item.id !== selected?.id).map((item) => <Button key={item.id} variant="outline" onClick={() => { setMoving(false); void mutate(() => api.move(selected!.id, item.id), "Issue moved"); }}>{item.title}</Button>)}</div></DialogContent></Dialog>
  <Toaster /></>;
}
