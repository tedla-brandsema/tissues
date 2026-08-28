import { ReactNode, useState } from "react";
import { Menu } from "lucide-react";
import { Button } from "@tissues/frontend/components/ui/button";
import { Route } from "./routes";

export function AppShell({ route, navigate, children }: { route: Route; navigate: (route: Route) => void; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const area = route.view === "project" || route.view === "projects" ? "projects" : "issues";
  const go = (next: Route) => { setOpen(false); navigate(next); };
  return <div className="app-shell">
    <div className="mobile-toolbar"><Button variant="outline" size="icon" aria-label="Open navigation" onClick={() => setOpen(true)}><Menu /></Button><strong>🤧 tissues</strong></div>
    <main className="workbench">
      <aside className={open ? "side-navigation open" : "side-navigation"} aria-label="Product navigation">
        <div className="navigation-heading"><strong className="app-identity">🤧 tissues</strong><Button className="mobile-only" variant="ghost" onClick={() => setOpen(false)}>Close</Button></div>
        <nav>
          <button aria-current={area === "projects" ? "page" : undefined} onClick={() => go({ view: "projects" })}>Projects</button>
          <button aria-current={area === "issues" ? "page" : undefined} onClick={() => go({ view: "issues" })}>Issues</button>
        </nav>
      </aside>
      <section className="main-view">{children}</section>
    </main>
  </div>;
}
