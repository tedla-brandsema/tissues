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
import { Textarea } from "@tissues/frontend/components/ui/textarea";
import { Toaster } from "@tissues/frontend/components/ui/sonner";
import { api, Issue } from "./api";
import { AuthBootstrap, readAuthBootstrap, recoverExpiredSession } from "./auth";

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

function IssueDialog({ open, issue, issues, onClose, onSave }: { open: boolean; issue?: Issue; issues: Issue[]; onClose: () => void; onSave: (value: { title: string; description: string; parent_id: string }) => Promise<void> }) {
  const [title, setTitle] = useState(""); const [description, setDescription] = useState(""); const [parent, setParent] = useState("");
  useEffect(() => { if (open) { setTitle(issue?.title || ""); setDescription(issue?.description || ""); setParent(issue?.parent_id || ""); } }, [open, issue]);
  async function submit(event: FormEvent) { event.preventDefault(); await onSave({ title, description, parent_id: parent }); }
  return <Dialog open={open} onOpenChange={(value) => !value && onClose()}><DialogContent><form onSubmit={submit} className="form-stack">
    <DialogHeader><DialogTitle>{issue ? "Edit issue" : "Create issue"}</DialogTitle><DialogDescription>{issue ? "Update its title and Markdown description." : "Add work to the shared issue tree."}</DialogDescription></DialogHeader>
    <label>Title<Input required value={title} onChange={(e) => setTitle(e.target.value)} /></label>
    <label>Description<Textarea required rows={8} value={description} onChange={(e) => setDescription(e.target.value)} /></label>
    {!issue && <label>Parent<select value={parent} onChange={(e) => setParent(e.target.value)}><option value="">No parent</option>{issues.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label>}
    <DialogFooter><Button type="button" variant="outline" onClick={onClose}>Cancel</Button><Button type="submit">Save</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}

type AppProps = { bootstrap?: AuthBootstrap; recoverSession?: (cause: unknown) => boolean };

export function App({ bootstrap = readAuthBootstrap(), recoverSession = recoverExpiredSession }: AppProps = {}) {
  const [roots, setRoots] = useState<Issue[]>([]); const [selectedID, setSelectedID] = useState(issueIDFromURL()); const [selected, setSelected] = useState<Issue>();
  const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [filter, setFilter] = useState<Filter>(filterFromURL); const [query, setQuery] = useState(""); const [pending, setPending] = useState(false);
  const [navigator, setNavigator] = useState(false); const [dialog, setDialog] = useState<"create" | "edit" | null>(null); const [moving, setMoving] = useState(false);
  const [author, setAuthor] = useState(() => bootstrap.enabled ? "" : localStorage.getItem("tissues.comment-author") || ""); const [comment, setComment] = useState("");
  const all = useMemo(() => flatten(roots), [roots]); const visible = useMemo(() => filteredTree(roots, filter, query), [roots, filter, query]);
  const message = document.querySelector<HTMLMetaElement>('meta[name="tissues-message"]')?.content;
  async function load(preferred = selectedID) { setError(""); try { const next = await api.list(); setRoots(next); if (preferred) { const detail = await api.get(preferred); setSelected(detail); } } catch (cause) { if (!recoverSession(cause)) setError(cause instanceof Error ? cause.message : "Unable to load issues"); } finally { setLoading(false); } }
  useEffect(() => { void load(); }, []);
  async function choose(id: string) { setSelectedID(id); updateURL(id, filter); setNavigator(false); try { setSelected(await api.get(id)); } catch (cause) { if (!recoverSession(cause)) toast.error(String(cause)); } }
  async function mutate(action: () => Promise<unknown>, success: string) { setPending(true); try { await action(); await load(selectedID); toast.success(success); } catch (cause) { if (!recoverSession(cause)) toast.error(cause instanceof Error ? cause.message : "Request failed"); } finally { setPending(false); } }
  async function saveIssue(value: { title: string; description: string; parent_id: string }) { if (dialog === "edit" && selected) await mutate(() => api.update(selected.id, value), "Issue updated"); else { let created: Issue | undefined; await mutate(async () => { created = await api.create(value); setSelectedID(created.id); updateURL(created.id, filter); }, "Issue created"); if (created) setSelected(created); } setDialog(null); }
  async function addComment(event: FormEvent) { event.preventDefault(); if (!selected) return; if (!bootstrap.enabled) localStorage.setItem("tissues.comment-author", author); await mutate(() => api.comment(selected.id, bootstrap.enabled ? "" : author, comment), "Comment added"); setComment(""); }
  return <><div className="app-shell">
    <header><div><span className="eyebrow">Shared workspace</span><h1>tissues</h1></div><div className="header-actions"><Button className="mobile-only" variant="outline" size="icon" aria-label="Open issue navigator" onClick={() => setNavigator(true)}><Menu /></Button><Button onClick={() => setDialog("create")}><Plus /> New issue</Button></div></header>
    {message && <div className="profile-message">{message}</div>}
    <main>
      <aside className={navigator ? "navigator open" : "navigator"} aria-label="Issue navigator">
        <div className="navigator-heading"><strong>Issues</strong><Button className="mobile-only" variant="ghost" onClick={() => setNavigator(false)}>Close</Button></div>
        <Tabs value={filter} onValueChange={(value) => { const next = value as Filter; setFilter(next); updateURL(selectedID, next); }}><TabsList><TabsTrigger value="open">Open</TabsTrigger><TabsTrigger value="closed">Closed</TabsTrigger><TabsTrigger value="all">All</TabsTrigger></TabsList></Tabs>
        <Input aria-label="Filter issues" placeholder="Filter by title…" value={query} onChange={(e) => setQuery(e.target.value)} />
        <ScrollArea className="tree-scroll">{loading ? <div className="skeletons"><Skeleton /><Skeleton /><Skeleton /></div> : visible.length ? <IssueTree issues={visible} selected={selectedID} onSelect={choose} /> : <p className="empty">No matching issues.</p>}</ScrollArea>
      </aside>
      <section className="detail">
        {error ? <div className="error-state"><p>{error}</p><Button variant="outline" onClick={() => void load()}><RefreshCw /> Try again</Button></div> : !selected ? <div className="welcome"><h2>Choose an issue</h2><p>Select work from the navigator, or create the first issue.</p></div> : <>
          <div className="detail-heading"><div><Badge>{selected.state}</Badge><h2>{selected.title}</h2><p>{selected.parent_id ? `Attached beneath ${all.find((item) => item.id === selected.parent_id)?.title || "another issue"} · ` : ""}Updated {new Date(selected.updated).toLocaleString()}</p></div><div className="detail-actions"><Button disabled={pending} variant="outline" onClick={() => setDialog("edit")}><Pencil /> Edit</Button><Button disabled={pending} variant="outline" onClick={() => setMoving(true)}>Move</Button>{selected.parent_id && <Button disabled={pending} variant="outline" onClick={() => void mutate(() => api.move(selected.id, ""), "Issue detached")}>Detach</Button>}<Button disabled={pending} variant="outline" onClick={() => void mutate(() => api.state(selected.id, selected.state === "open" ? "close" : "reopen"), selected.state === "open" ? "Issue closed" : "Issue reopened")}>{selected.state === "open" ? "Close" : "Reopen"}</Button></div></div>
          <article className="markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{selected.description}</ReactMarkdown></article><Separator />
          <section className="comments"><h3>Comments <span>{selected.comments.length}</span></h3>{selected.comments.map((item) => <article key={item.id}><div><strong>{item.author}</strong><time>{new Date(item.created).toLocaleString()}{item.updated !== item.created ? " (edited)" : ""}</time><Button variant="ghost" size="sm" onClick={() => { const body = prompt("Edit comment", item.body); if (body) void mutate(() => api.editComment(selected.id, item.id, body), "Comment updated"); }}>Edit</Button></div><div className="markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{item.body}</ReactMarkdown></div></article>)}
            <form onSubmit={addComment} className="comment-form"><h4><MessageSquarePlus /> Add comment</h4>{bootstrap.enabled ? <p className="comment-identity">Commenting as <strong>{bootstrap.author}</strong></p> : <Input aria-label="Comment author" required placeholder="Your name or email" value={author} onChange={(e) => setAuthor(e.target.value)} />}<Textarea aria-label="Comment body" required placeholder="Write Markdown…" value={comment} onChange={(e) => setComment(e.target.value)} /><Button type="submit">Comment</Button></form>
          </section>
        </>}
      </section>
    </main>
  </div>
  <IssueDialog open={dialog !== null} issue={dialog === "edit" ? selected : undefined} issues={all} onClose={() => setDialog(null)} onSave={saveIssue} />
  <Dialog open={moving} onOpenChange={setMoving}><DialogContent><DialogHeader><DialogTitle>Move issue</DialogTitle><DialogDescription>Current parent: {all.find((item) => item.id === selected?.parent_id)?.title || "none"}. Choose another issue as parent, or detach it.</DialogDescription></DialogHeader><div className="move-list"><Button variant="outline" onClick={() => { setMoving(false); void mutate(() => api.move(selected!.id, ""), "Issue detached"); }}>Detach (no parent)</Button>{all.filter((item) => item.id !== selected?.id).map((item) => <Button key={item.id} variant="outline" onClick={() => { setMoving(false); void mutate(() => api.move(selected!.id, item.id), "Issue moved"); }}>{item.title}</Button>)}</div></DialogContent></Dialog>
  <Toaster /></>;
}
