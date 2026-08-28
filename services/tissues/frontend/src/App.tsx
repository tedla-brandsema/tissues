import { useEffect, useState } from "react";
import { Toaster } from "@tissues/frontend/components/ui/sonner";
import { AppShell } from "./AppShell";
import { AuthBootstrap, readAuthBootstrap, recoverExpiredSession } from "./auth";
import { IssuesOverview, IssueView } from "./IssuesViews";
import { ProjectsOverview, ProjectView } from "./ProjectsViews";
import { readRoute, Route, routeURL } from "./routes";

type AppProps = { bootstrap?: AuthBootstrap; recoverSession?: (cause: unknown) => boolean };

export function App({ bootstrap = readAuthBootstrap(), recoverSession = recoverExpiredSession }: AppProps = {}) {
  const [route, setRoute] = useState<Route>(readRoute);
  useEffect(() => {
    if (!location.search) history.replaceState(null, "", routeURL({ view: "projects" }));
    const pop = () => setRoute(readRoute());
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, []);
  function navigate(next: Route) {
    history.pushState(null, "", routeURL(next));
    setRoute(next);
  }
  const handleError = (cause: unknown) => recoverSession(cause);
  const routeKey = routeURL(route);
  let view;
  switch (route.view) {
    case "projects": view = <ProjectsOverview key={routeKey} navigate={navigate} handleError={handleError} />; break;
    case "project": view = <ProjectView key={routeKey} route={route} navigate={navigate} handleError={handleError} />; break;
    case "issues": view = <IssuesOverview key={routeKey} navigate={navigate} handleError={handleError} />; break;
    case "issue": view = <IssueView key={routeKey} route={route} navigate={navigate} handleError={handleError} bootstrap={bootstrap} />; break;
  }
  return <><AppShell route={route} navigate={navigate}>{view}</AppShell><Toaster /></>;
}
