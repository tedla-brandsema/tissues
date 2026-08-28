export type Route =
  | { view: "projects" }
  | { view: "project"; mode: "create" }
  | { view: "project"; project: string }
  | { view: "issues" }
  | { view: "issue"; mode: "create" }
  | { view: "issue"; issue: string };

export function readRoute(): Route {
  const query = new URLSearchParams(location.search);
  switch (query.get("view")) {
    case "project":
      return query.get("mode") === "create" ? { view: "project", mode: "create" } : query.get("project") ? { view: "project", project: query.get("project")! } : { view: "projects" };
    case "issues": return { view: "issues" };
    case "issue":
      return query.get("mode") === "create" ? { view: "issue", mode: "create" } : query.get("issue") ? { view: "issue", issue: query.get("issue")! } : { view: "issues" };
    default: return { view: "projects" };
  }
}

export function routeURL(route: Route): string {
  const query = new URLSearchParams({ view: route.view });
  if ("mode" in route) query.set("mode", route.mode);
  if ("project" in route) query.set("project", route.project);
  if ("issue" in route) query.set("issue", route.issue);
  return `?${query}`;
}
