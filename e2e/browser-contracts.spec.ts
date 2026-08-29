import { expect, Page, test } from "@playwright/test";

const markdown = "# Heading\n\nThis is **bold** and *italic*.\n\n- one\n- two\n\n> quoted\n\n`inline`\n\n<script data-unsafe>bad()</script>";
const timestamp = "2026-01-01T00:00:00Z";
const opaqueID = "aaaaaaaaaaaaaaaaaaaaaaaaaa";
const issue = {
  id: "FLUENT-1", project_key: "FLUENT", number: 1, title: "Browser fixture", state: "open",
  created: timestamp, updated: timestamp, description: markdown, parent_id: "", children: [],
  comments: [{ id: "comment-1", author: "Ada", created: timestamp, updated: timestamp, body: markdown }],
};
const secondIssue = { ...issue, id: "FLUENT-2", number: 2, title: "Parent candidate", comments: [] };
const tissuesIssue = { ...issue, id: "TISSUES-4", project_key: "TISSUES", number: 4, title: "Tissues fixture", state: "closed", comments: [] };
type AssetDTO = { name: string; url: string; content_type: "image/png" | "image/jpeg"; width: number; height: number; size: number };
type AssetState = { assets: AssetDTO[]; uploadFailures?: number };
const existingAsset: AssetDTO = { name: "existing.png", url: "/api/tissues/v1/issues/FLUENT-1/assets/existing.png", content_type: "image/png", width: 40, height: 30, size: 2048 };
const replacementAsset: AssetDTO = { ...existingAsset, name: "example.png", url: "/api/tissues/v1/issues/FLUENT-1/assets/example.png" };
const tinyPNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");

type MockOptions = {
  observe?: (method: string, pathname: string, body: string) => void;
  projectPages?: Record<string, { projects: Array<{ key: string; created: string }>; next_cursor: string }>;
  issuePages?: Record<string, { issues: unknown[]; next_cursor: string }>;
  parentFailure?: () => { status: number; message: string } | undefined;
  state?: { value: "open" | "closed"; parentID?: string };
  createdIssueGETGate?: Promise<void>;
  createdIssueID?: string;
  projectGETGate?: Promise<void>;
  assetState?: AssetState;
};

async function mockTissuesAPI(page: Page, options: MockOptions = {}) {
  const projectPages = options.projectPages || { "": { projects: [{ key: "FLUENT", created: timestamp }, { key: "TISSUES", created: timestamp }], next_cursor: "" } };
  const issuePages = options.issuePages || { "": { issues: [
    { project_key: "FLUENT", number: 1, id: "FLUENT-1", title: issue.title, state: "open", parent_id: "", updated: timestamp },
    { project_key: "TISSUES", number: 4, id: "TISSUES-4", title: tissuesIssue.title, state: "closed", parent_id: "FLUENT-1", updated: timestamp },
  ], next_cursor: "" } };
  const assetState = options.assetState || { assets: [] };
  await page.route("**/api/tissues/v1/**", async (route) => {
    const request = route.request(); const url = new URL(request.url()); const pathname = url.pathname; const method = request.method(); const body = request.postData() || "";
    options.observe?.(method, pathname, body);
    if (pathname.endsWith("/projects") && method === "GET") return route.fulfill({ json: projectPages[url.searchParams.get("cursor") || ""] || { projects: [], next_cursor: "" } });
    if (pathname.endsWith("/projects") && method === "POST") return route.fulfill({ status: 201, json: { key: "FLUENT", created: timestamp } });
    if (/\/projects\/[^/]+$/.test(pathname) && method === "GET") { await options.projectGETGate; return route.fulfill({ json: { key: pathname.split("/").at(-1), created: timestamp } }); }
    if (pathname.endsWith("/projects/FLUENT/issues") && method === "GET") return route.fulfill({ json: { issues: [issue, secondIssue] } });
    if (pathname.endsWith("/projects/TISSUES/issues") && method === "GET") return route.fulfill({ json: { issues: [tissuesIssue] } });
    if (pathname.endsWith("/projects/TELOS/issues") && method === "GET") return route.fulfill({ json: { issues: [] } });
    if (/\/projects\/[^/]+\/issues$/.test(pathname) && method === "POST") {
      const payload = JSON.parse(body); const id = options.createdIssueID || "FLUENT-3"; const projectKey = id.split("-")[0];
      return route.fulfill({ status: 201, json: { ...issue, id, project_key: projectKey, number: Number(id.split("-")[1]), title: payload.title, description: payload.description, comments: [] } });
    }
    if (pathname.endsWith("/issues") && method === "GET") {
      const result = issuePages[url.searchParams.get("cursor") || ""] || { issues: [], next_cursor: "" }; const project = url.searchParams.get("project");
      return route.fulfill({ json: project ? { ...result, issues: result.issues.filter((entry: any) => entry.project_key === project) } : result });
    }
    if (pathname.endsWith("/issues/FLUENT-1/assets") && method === "GET") return route.fulfill({ json: { assets: assetState.assets } });
    if (pathname.endsWith("/issues/FLUENT-1/assets") && method === "POST") {
      if ((assetState.uploadFailures || 0) > 0) { assetState.uploadFailures = (assetState.uploadFailures || 0) - 1; return route.fulfill({ status: 409, json: { error: { kind: "conflict", message: "Image changed; retry upload" } } }); }
      const uploaded: AssetDTO = { name: "example.png", url: "/api/tissues/v1/issues/FLUENT-1/assets/example.png", content_type: "image/png", width: 1200, height: 800, size: 483221 };
      assetState.assets = [...assetState.assets.filter((asset) => asset.name !== uploaded.name), uploaded];
      return route.fulfill({ status: 201, json: uploaded });
    }
    if (/\/issues\/FLUENT-1\/assets\/[^/]+$/.test(pathname) && method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: tinyPNG });
    if (pathname.endsWith("/issues/FLUENT-1/parent") && method === "PUT") {
      const failure = options.parentFailure?.(); if (failure) return route.fulfill({ status: failure.status, json: { error: { kind: failure.status === 404 ? "not_found" : "invalid", message: failure.message } } });
      const payload = JSON.parse(body); if (options.state) options.state.parentID = payload.parent_id; return route.fulfill({ json: { ...issue, parent_id: payload.parent_id, state: options.state?.value || issue.state } });
    }
    if (pathname.endsWith("/issues/FLUENT-1") && method === "PATCH") {
      const payload = JSON.parse(body); return route.fulfill({ json: { ...issue, title: payload.title, description: payload.description, parent_id: options.state?.parentID || "", state: options.state?.value || issue.state } });
    }
    if (pathname.endsWith("/issues/FLUENT-1/close") && method === "POST") { if (options.state) options.state.value = "closed"; return route.fulfill({ json: { ...issue, state: "closed" } }); }
    if (pathname.endsWith("/issues/FLUENT-1/reopen") && method === "POST") { if (options.state) options.state.value = "open"; return route.fulfill({ json: { ...issue, state: "open" } }); }
    if (pathname.endsWith("/comments/comment-1") && method === "PATCH") return route.fulfill({ json: issue.comments[0] });
    if (pathname.endsWith("/comments") && method === "POST") return route.fulfill({ status: 201, json: issue.comments[0] });
    if (pathname.endsWith("/issues/FLUENT-1") && method === "GET") return route.fulfill({ json: { ...issue, parent_id: options.state?.parentID || "", state: options.state?.value || issue.state } });
    if (pathname.endsWith("/issues/FLUENT-2") && method === "GET") return route.fulfill({ json: secondIssue });
    if (pathname.endsWith(`/issues/${options.createdIssueID || "FLUENT-3"}`) && method === "GET") {
      await options.createdIssueGETGate;
      const id = options.createdIssueID || "FLUENT-3"; return route.fulfill({ json: { ...issue, id, project_key: id.split("-")[0], number: Number(id.split("-")[1]), title: "Created in browser", comments: [] } });
    }
    if (pathname.endsWith("/issues/TISSUES-4") && method === "GET") return route.fulfill({ json: tissuesIssue });
    return route.fulfill({ status: 404, json: { error: { kind: "not_found", message: "resource not found" } } });
  });
}

async function expectContained(box: { x: number; y: number; width: number; height: number } | null, viewport: { width: number; height: number } | null) {
  expect(box).not.toBeNull(); expect(viewport).not.toBeNull(); expect(box!.x).toBeGreaterThanOrEqual(0); expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width); expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
}

async function expectInactiveCrepeChrome(editor: ReturnType<Page["locator"]>) {
  for (const selector of [".milkdown-toolbar", ".milkdown-slash-menu", ".milkdown-link-preview", ".milkdown-link-edit"]) {
    const surface = editor.locator(selector); await expect(surface).toHaveAttribute("data-show", "false");
    expect(await surface.evaluate((element) => ({ display: getComputedStyle(element).display, position: getComputedStyle(element).position }))).toEqual({ display: "none", position: "absolute" });
  }
  const blockHandle = editor.locator(".milkdown-block-handle"); await expect(blockHandle).toHaveAttribute("data-show", "false");
  expect(await blockHandle.evaluate((element) => ({ opacity: getComputedStyle(element).opacity, pointerEvents: getComputedStyle(element).pointerEvents, position: getComputedStyle(element).position }))).toEqual({ opacity: "0", pointerEvents: "none", position: "absolute" });
}

test("native auth form posts every credential and continuation field", async ({ page }) => {
  let posted = "";
  await page.route("**/auth/login**", async (route) => { if (route.request().method() !== "POST") return route.continue(); posted = route.request().postData() || ""; await route.fulfill({ status: 200, body: "ok" }); });
  await page.goto("/auth/login?next=%2F%3Fview%3Dissue%26issue%3DFLUENT-1&next_exp=12345&next_sig=test-signature");
  await page.getByLabel("Email").fill("browser@example.test"); await page.getByLabel("Password").fill("fake-browser-password"); await page.getByRole("button", { name: "Sign in" }).click();
  await expect.poll(() => posted).not.toBe("");
  expect(Object.fromEntries(new URLSearchParams(posted))).toEqual({ next: "/?view=issue&issue=FLUENT-1", next_exp: "12345", next_sig: "test-signature", email: "browser@example.test", password: "fake-browser-password" });
});

test("side navigation has exactly two product areas and no object controls", async ({ page }) => {
  await mockTissuesAPI(page); await page.goto("/?view=projects");
  const side = page.getByLabel("Product navigation"); const nav = side.getByRole("navigation");
  await expect(nav.getByRole("button")).toHaveCount(2); await expect(nav.getByRole("button", { name: "Projects" })).toHaveAttribute("aria-current", "page"); await expect(nav.getByRole("button", { name: "Issues" })).toBeVisible();
  for (const forbidden of ["Create project", "Create issue", "New issue", "Open", "Closed", "All", "Filter issues", "Project selector"]) await expect(side.getByText(forbidden, { exact: true })).toHaveCount(0);
  await expect(side.locator("select, input, .issue-tree")).toHaveCount(0); await expect(page.locator("body")).not.toContainText("Shared workspace"); await expect(page.locator(".profile-message")).toHaveCount(0);
  await nav.getByRole("button", { name: "Issues" }).click(); await expect(page).toHaveURL(/view=issues/); await expect(nav.getByRole("button", { name: "Issues" })).toHaveAttribute("aria-current", "page");
});

test("Projects overview pages, navigates rows, and has one creation action", async ({ page }) => {
  const pageErrors: Error[] = []; page.on("pageerror", (error) => pageErrors.push(error));
  await mockTissuesAPI(page, { projectPages: {
    "": { projects: [{ key: "FLUENT", created: timestamp }, { key: "TISSUES", created: timestamp }], next_cursor: "page-2" },
    "page-2": { projects: [{ key: "TELONAUTICS", created: timestamp }], next_cursor: "" },
  } });
  await page.goto("/?view=projects"); const main = page.locator(".main-view");
  await expect(main.getByRole("button", { name: "Project", exact: true })).toHaveCount(1); await expect(main.getByRole("table")).toContainText("FLUENT"); await expect(main.getByRole("table")).toContainText("TISSUES");
  await main.getByRole("button", { name: "Next" }).click(); await expect(main.getByRole("table")).toContainText("TELONAUTICS"); await expect(main.getByRole("button", { name: "Next" })).toBeDisabled();
  await main.getByRole("button", { name: "Previous" }).click(); await main.getByRole("button", { name: "FLUENT" }).click(); await expect(page).toHaveURL(/view=project.*project=FLUENT/); await page.goBack(); await expect(page).toHaveURL(/view=projects/); await page.goForward(); await expect(page).toHaveURL(/view=project.*project=FLUENT/); await expect(page.getByRole("heading", { name: "FLUENT" })).toBeVisible(); expect(pageErrors).toEqual([]);
});

test("zero Projects has one overview action and blocks Issue creation honestly", async ({ page }) => {
  await mockTissuesAPI(page, { projectPages: { "": { projects: [], next_cursor: "" } }, issuePages: { "": { issues: [], next_cursor: "" } } });
  await page.goto("/?view=projects"); const main = page.locator(".main-view"); await expect(main.getByText("No Projects yet.")).toBeVisible(); await expect(main.getByRole("button", { name: "Project", exact: true })).toHaveCount(1);
  await page.getByLabel("Product navigation").getByRole("button", { name: "Issues" }).click(); await page.locator(".main-view").getByRole("button", { name: "Issue", exact: true }).click();
  await expect(page.getByText("An Issue needs a Project. Create a Project first.")).toBeVisible(); await page.getByRole("button", { name: "Create project" }).click(); await expect(page).toHaveURL(/view=project.*mode=create/);
});

test("Issues overview pages across Projects without exposing opaque IDs", async ({ page }) => {
  const pageErrors: Error[] = []; page.on("pageerror", (error) => pageErrors.push(error));
  await mockTissuesAPI(page, { issuePages: {
    "": { issues: [{ project_key: "FLUENT", number: 1, id: "FLUENT-1", title: "Browser fixture", state: "open", parent_id: "", updated: timestamp }], next_cursor: "page-2" },
    "page-2": { issues: [{ project_key: "TISSUES", number: 4, id: "TISSUES-4", title: "Tissues fixture", state: "closed", parent_id: "TISSUES-2", updated: timestamp }], next_cursor: "" },
  } });
  await page.goto("/?view=issues"); const main = page.locator(".main-view");
  await expect(main.getByRole("button", { name: "Issue", exact: true })).toHaveCount(1); await expect(main.getByRole("table")).toContainText("FLUENT-1"); await expect(page.getByText(opaqueID)).toHaveCount(0);
  await expect(main.getByRole("columnheader", { name: "Issue ID" })).toBeVisible(); await main.getByRole("button", { name: "Next" }).click(); await expect(main.getByRole("table")).toContainText("TISSUES-4"); await expect(main.getByRole("table")).toContainText("TISSUES-2");
  await main.getByRole("button", { name: "TISSUES-4" }).click(); await expect(page).toHaveURL(/view=issue.*issue=TISSUES-4/); await page.goBack(); await expect(page).toHaveURL(/view=issues/); await page.goForward(); await expect(page).toHaveURL(/view=issue.*issue=TISSUES-4/); await expect(page.locator(".eyebrow")).toHaveText("TISSUES-4"); expect(pageErrors).toEqual([]);
});

test("Issue Project filter persists, resets paging, and defaults creation", async ({ page }) => {
  await mockTissuesAPI(page, { issuePages: {
    "": { issues: [{ project_key: "FLUENT", number: 1, id: "FLUENT-1", title: issue.title, state: "open", parent_id: "", updated: timestamp }, { project_key: "TISSUES", number: 4, id: "TISSUES-4", title: tissuesIssue.title, state: "closed", parent_id: "", updated: timestamp }], next_cursor: "page-2" },
    "page-2": { issues: [{ project_key: "TISSUES", number: 5, id: "TISSUES-5", title: "Later", state: "open", parent_id: "", updated: timestamp }], next_cursor: "" },
  } });
  await page.goto("/?view=issues"); const filter = page.locator(".main-view").getByLabel("Project"); await expect(filter).toHaveValue("");
  await page.getByRole("button", { name: "Next" }).click(); await expect(page.getByRole("button", { name: "Previous" })).toBeEnabled();
  await filter.selectOption("FLUENT"); await expect(page.getByRole("button", { name: "Previous" })).toBeDisabled(); await expect(page.getByRole("table")).not.toContainText("TISSUES-4");
  await page.reload(); await expect(filter).toHaveValue("FLUENT"); await page.getByLabel("Product navigation").getByRole("button", { name: "Projects" }).click(); await page.getByLabel("Product navigation").getByRole("button", { name: "Issues" }).click(); await expect(filter).toHaveValue("FLUENT");
  await page.getByRole("button", { name: "Issue", exact: true }).click(); await expect(page.getByLabel("Project ID")).toHaveValue("FLUENT");
  await page.getByLabel("Project ID").selectOption("TISSUES"); expect(await page.evaluate(() => localStorage.getItem("tissues.issues-project-filter"))).toBe("FLUENT");
});

test("Project create and existing views keep the key immutable", async ({ page }) => {
  let releaseGET = () => {}; const projectGETGate = new Promise<void>((resolve) => { releaseGET = resolve; }); const pageErrors: Error[] = []; page.on("pageerror", (error) => pageErrors.push(error));
  await mockTissuesAPI(page, { projectGETGate }); await page.goto("/?view=projects"); await page.locator(".main-view").getByRole("button", { name: "Project", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Create project" })).toBeVisible(); await expect(page.getByLabel("Project ID")).toHaveAttribute("placeholder", "Project ID"); await page.getByLabel("Project ID").fill("fluent"); await expect(page.getByLabel("Project ID")).toHaveValue("FLUENT"); await page.getByRole("button", { name: "Create" }).click();
  await expect(page).toHaveURL(/view=project.*project=FLUENT/); await expect(page.locator(".main-view .skeletons")).toBeVisible(); expect(pageErrors).toEqual([]); releaseGET(); await expect(page.getByRole("heading", { name: "FLUENT" })).toBeVisible(); await expect(page.getByText(new Date(timestamp).toLocaleString())).toBeVisible();
  await expect(page.getByText("Project ID", { exact: true })).toBeVisible(); await expect(page.getByRole("button", { name: "Save" })).toHaveCount(0); await expect(page.getByLabel("Project ID")).toHaveCount(0); expect(pageErrors).toEqual([]);
});

test("Issue create safely loads the canonical existing route", async ({ page }) => {
  let releaseGET = () => {};
  const createdIssueGETGate = new Promise<void>((resolve) => { releaseGET = resolve; });
  const pageErrors: Error[] = [];
  page.on("pageerror", (error) => pageErrors.push(error));
  await mockTissuesAPI(page, { createdIssueGETGate, createdIssueID: "TELOS-1", projectPages: { "": { projects: [{ key: "TELOS", created: timestamp }], next_cursor: "" } } });
  await page.goto("/?view=issue&mode=create");
  await expect(page.getByLabel("Project ID")).toHaveValue("TELOS");
  await expect(page.getByLabel("Parent issue ID")).toHaveCount(0);
  await page.getByLabel("Title").fill("Created in browser");
  const editor = page.getByRole("group", { name: "Description" }).locator(".ProseMirror");
  await editor.fill("Created body");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page).toHaveURL(/view=issue.*issue=TELOS-1/);
  await expect(page.locator(".main-view .skeletons")).toBeVisible();
  expect(pageErrors).toEqual([]);
  releaseGET();
  await expect(page.getByRole("heading", { name: "Created in browser" })).toBeVisible();
  await expect(page.locator(".eyebrow")).toHaveText("TELOS-1");
  expect(pageErrors).toEqual([]);
});

test("Issue content and hierarchy use separate workflows and failed parent input remains visible", async ({ page }) => {
  let patchBody = ""; let putBody = ""; let fail = false; const state: { value: "open" | "closed"; parentID?: string } = { value: "open" };
  await mockTissuesAPI(page, { state, observe: (method, pathname, body) => { if (method === "PATCH" && pathname.endsWith("/issues/FLUENT-1")) patchBody = body; if (method === "PUT" && pathname.endsWith("/parent")) putBody = body; }, parentFailure: () => fail ? { status: 404, message: "resource not found" } : undefined });
  await page.goto("/?view=issue&issue=FLUENT-1"); await expect(page.getByText("FLUENT", { exact: true }).last()).toBeVisible(); await expect(page.getByText("FLUENT-1", { exact: true }).last()).toBeVisible();
  await expect(page.getByText("Issue ID", { exact: true })).toBeVisible(); await expect(page.getByText("Project ID", { exact: true })).toBeVisible(); await expect(page.getByLabel("Parent issue ID")).toHaveCount(0); await expect(page.locator(".main-view")).not.toContainText(/Reference|\bRef\b/);
  const actions = await page.locator(".form-actions").boundingBox(); const separator = await page.locator(".issue-section-separator").last().boundingBox(); const commentsHeading = await page.getByRole("heading", { name: /Comments/ }).boundingBox(); expect(actions && separator && commentsHeading).toBeTruthy(); expect(separator!.y).toBeGreaterThan(actions!.y + actions!.height); expect(commentsHeading!.y).toBeGreaterThan(separator!.y + separator!.height);
  await page.getByLabel("Title").fill("Changed title"); const descriptionGroup = page.getByRole("group", { name: "Description" }); await expectInactiveCrepeChrome(descriptionGroup); const descriptionEditor = descriptionGroup.locator(".ProseMirror"); await descriptionEditor.locator("p").first().click(); await page.keyboard.press("End"); await page.keyboard.insertText(" Changed body"); await expect(descriptionEditor).toContainText("Changed body"); await page.waitForTimeout(1000); await page.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => patchBody).not.toBe(""); expect(JSON.parse(patchBody)).toEqual({ title: "Changed title", description: expect.stringContaining("Changed body") }); await expect(page.getByText("Issue saved")).toBeVisible();
  await page.getByRole("button", { name: "Set parent" }).click(); await page.getByLabel("Parent issue ID").fill("FLUENT-2"); await page.getByRole("button", { name: "Save parent" }).click(); await expect.poll(() => putBody).not.toBe(""); expect(JSON.parse(putBody)).toEqual({ parent_id: "FLUENT-2" }); await expect(page.getByText("Parent updated")).toBeVisible();
  putBody = ""; await page.getByRole("button", { name: "Change parent" }).click(); await page.getByRole("button", { name: "Detach" }).click(); await expect.poll(() => putBody).not.toBe(""); expect(JSON.parse(putBody)).toEqual({ parent_id: "" }); await expect(page.getByText("Issue detached")).toBeVisible();
  fail = true; putBody = ""; await page.getByRole("button", { name: "Set parent" }).click(); await page.getByLabel("Parent issue ID").fill("FLUENT-999"); await page.getByRole("button", { name: "Save parent" }).click();
  await expect(page.getByRole("alert")).toHaveText("Parent issue does not exist."); await expect(page.getByLabel("Parent issue ID")).toHaveValue("FLUENT-999"); await expect(page.getByLabel("Parent issue ID")).toHaveAttribute("aria-invalid", "true"); await expect(page).toHaveURL(/view=issue.*issue=FLUENT-1/);
});

test("Close and Reopen require Radix confirmation and provide feedback", async ({ page }) => {
  const state: { value: "open" | "closed" } = { value: "open" }; let closeCalls = 0; let reopenCalls = 0;
  await page.addInitScript(() => { window.confirm = () => { throw new Error("native confirm must not be called"); }; });
  await mockTissuesAPI(page, { state, observe: (method, pathname) => { if (method === "POST" && pathname.endsWith("/close")) closeCalls++; if (method === "POST" && pathname.endsWith("/reopen")) reopenCalls++; } });
  await page.goto("/?view=issue&issue=FLUENT-1"); await page.getByRole("button", { name: "Close issue" }).click(); expect(closeCalls).toBe(0); await expect(page.getByRole("alertdialog")).toContainText("Close FLUENT-1?");
  await page.getByRole("alertdialog").getByRole("button", { name: "Cancel" }).click(); expect(closeCalls).toBe(0); await page.getByRole("button", { name: "Close issue" }).click(); await page.getByRole("alertdialog").getByRole("button", { name: "Close issue" }).click();
  await expect.poll(() => closeCalls).toBe(1); await expect(page.getByText("Issue closed")).toBeVisible(); await expect(page.getByRole("button", { name: "Reopen issue" })).toBeVisible();
  await page.getByRole("button", { name: "Reopen issue" }).click(); expect(reopenCalls).toBe(0); await expect(page.getByRole("alertdialog")).toContainText("Reopen FLUENT-1?"); await page.getByRole("alertdialog").getByRole("button", { name: "Cancel" }).click(); expect(reopenCalls).toBe(0);
  await page.getByRole("button", { name: "Reopen issue" }).click(); await page.getByRole("alertdialog").getByRole("button", { name: "Reopen issue" }).click(); await expect.poll(() => reopenCalls).toBe(1); await expect(page.getByText("Issue reopened")).toBeVisible();
});

test("image management is exclusive to existing Issues", async ({ page }) => {
  await mockTissuesAPI(page);
  await page.goto("/?view=issue&mode=create");
  await expect(page.getByRole("heading", { name: "Create issue" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Images" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Upload image" })).toHaveCount(0);
  await page.goto("/?view=issue&issue=FLUENT-1");
  await expect(page.getByRole("heading", { name: "Images" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Upload image" })).toBeVisible();
});

test("real file upload creates a private Issue attachment without changing Markdown", async ({ page }) => {
  let assetPosts = 0; let uploadContentType = ""; let patchCalls = 0; const observedPaths: string[] = []; const browserRequests: string[] = [];
  page.on("request", (request) => {
    browserRequests.push(request.url());
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/tissues/v1/issues/FLUENT-1/assets") uploadContentType = request.headers()["content-type"] || "";
  });
  await mockTissuesAPI(page, { observe: (method, pathname, body) => {
    observedPaths.push(pathname);
    if (method === "POST" && pathname === "/api/tissues/v1/issues/FLUENT-1/assets") assetPosts++;
    if (method === "PATCH" && pathname.endsWith("/issues/FLUENT-1")) patchCalls++;
  } });
  await page.goto("/?view=issue&issue=FLUENT-1");
  await page.getByRole("button", { name: "Upload image" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Image file").setInputFiles({ name: "Example.PNG", mimeType: "image/png", buffer: tinyPNG });
  await expect(dialog.getByText("Example.PNG", { exact: true })).toBeVisible();
  const localPreview = dialog.getByAltText("Selected image preview"); await expect(localPreview).toBeVisible(); await expect(localPreview).toHaveAttribute("src", /^blob:/);
  await expect(dialog.getByLabel("Image description / Alt text")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  await expect.poll(() => assetPosts).toBe(1); expect(uploadContentType).toMatch(/^multipart\/form-data; boundary=/); await expect(dialog.getByText("example.png", { exact: true })).toBeVisible(); await expect(dialog.getByText("1200 × 800 · 472 KB")).toBeVisible();
  const processed = dialog.locator(".upload-result img"); await expect(processed).toHaveAttribute("src", "/api/tissues/v1/issues/FLUENT-1/assets/example.png?preview=1");
  await expect.poll(() => processed.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  await expect(dialog.getByRole("button", { name: /Insert|Copy Markdown/ })).toHaveCount(0);
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("region", { name: "Images" }).getByRole("button", { name: "View example.png" })).toBeVisible();
  expect(patchCalls).toBe(0);
  expect(observedPaths.some((pathname) => pathname.includes("storage.googleapis.com") || pathname.includes("googleapis.com"))).toBe(false);
  expect(browserRequests.some((url) => url.includes("storage.googleapis.com") || url.includes("googleapis.com"))).toBe(false);
  await expect(page.locator("body")).not.toContainText("storage.googleapis.com");
});

test("an existing private attachment opens an accessible large preview", async ({ page }) => {
  const browserRequests: string[] = []; page.on("request", (request) => browserRequests.push(request.url()));
  await mockTissuesAPI(page, { assetState: { assets: [existingAsset] } });
  await page.goto("/?view=issue&issue=FLUENT-1"); const images = page.getByRole("region", { name: "Images" });
  await expect(images.getByText("existing.png", { exact: true })).toBeVisible(); await expect(images.getByText("40 × 30 · 2 KB")).toBeVisible();
  const thumbnail = images.locator('img[src="/api/tissues/v1/issues/FLUENT-1/assets/existing.png"]'); await expect.poll(() => thumbnail.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  const previewButton = images.getByRole("button", { name: "View existing.png" }); await previewButton.focus(); await page.keyboard.press("Enter");
  const preview = page.getByRole("dialog"); await expect(preview).toBeVisible(); await expect(preview.getByRole("heading", { name: "existing.png" })).toBeVisible(); await expect(preview.getByText("40 × 30 · 2 KB")).toBeVisible();
  const largeImage = preview.getByAltText("Preview of existing.png"); await expect(largeImage).toHaveAttribute("src", existingAsset.url); await expect.poll(() => largeImage.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  expect(browserRequests.some((url) => url.includes("storage.googleapis.com") || url.includes("googleapis.com"))).toBe(false); await expect(page.locator("body")).not.toContainText("storage.googleapis.com");
  await page.keyboard.press("Escape"); await expect(preview).toBeHidden(); await expect(previewButton).toBeFocused();
});

test("same-name replacement refreshes the list thumbnail without changing canonical identity", async ({ page }) => {
  const assetState: AssetState = { assets: [replacementAsset] }; const assetRequests: string[] = [];
  page.on("request", (request) => { if (new URL(request.url()).pathname === replacementAsset.url) assetRequests.push(request.url()); });
  await mockTissuesAPI(page, { assetState }); await page.goto("/?view=issue&issue=FLUENT-1");
  const images = page.getByRole("region", { name: "Images" }); const initialThumbnail = images.locator(".asset-card img");
  await expect(initialThumbnail).toHaveAttribute("src", replacementAsset.url);
  await expect.poll(() => initialThumbnail.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  expect(assetRequests.some((requestURL) => new URL(requestURL).search === "")).toBe(true);

  await images.getByRole("button", { name: "Upload image" }).click(); const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Image file").setInputFiles({ name: "example.png", mimeType: "image/png", buffer: tinyPNG });
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  await expect(dialog.getByText("1200 × 800 · 472 KB")).toBeVisible();
  await expect(dialog.locator(".upload-result img")).toHaveAttribute("src", `${replacementAsset.url}?preview=1`);
  await dialog.getByRole("button", { name: "Done" }).click();

  await expect(images.getByText("example.png", { exact: true })).toHaveCount(1);
  await expect(images.getByText("1200 × 800 · 472 KB")).toBeVisible();
  const refreshedThumbnail = images.locator(".asset-card img"); await expect(refreshedThumbnail).toHaveAttribute("src", `${replacementAsset.url}?preview=1`);
  await expect.poll(() => refreshedThumbnail.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  const refreshedRequest = assetRequests.find((requestURL) => new URL(requestURL).searchParams.get("preview") === "1");
  expect(refreshedRequest).toBeTruthy(); expect(new URL(refreshedRequest!).origin).toBe(new URL(page.url()).origin);
  const previewButton = images.getByRole("button", { name: "View example.png" }); await previewButton.click(); const preview = page.getByRole("dialog");
  const previewImage = preview.getByAltText("Preview of example.png"); await expect(previewImage).toHaveAttribute("src", `${replacementAsset.url}?preview=1`); await expect.poll(() => previewImage.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  await expect(preview.getByRole("heading", { name: "example.png" })).toBeVisible(); await expect(preview.getByText("1200 × 800 · 472 KB")).toBeVisible(); await preview.getByRole("button", { name: "Close" }).click(); await expect(preview).toBeHidden();
  expect(assetState.assets).toHaveLength(1); expect(assetState.assets[0].url).toBe(replacementAsset.url);
  expect(assetRequests.some((requestURL) => requestURL.includes("storage.googleapis.com") || requestURL.includes("googleapis.com"))).toBe(false);
  await expect(page.locator("body")).not.toContainText("storage.googleapis.com");
});

test("upload validation, retryable backend errors, and narrow dialog containment", async ({ page }) => {
  const assetState: AssetState = { assets: [], uploadFailures: 1 }; let assetPosts = 0;
  await mockTissuesAPI(page, { assetState, observe: (method, pathname) => { if (method === "POST" && pathname.endsWith("/issues/FLUENT-1/assets")) assetPosts++; } });
  await page.setViewportSize({ width: 375, height: 700 }); await page.goto("/?view=issue&issue=FLUENT-1"); await page.getByRole("button", { name: "Upload image" }).click();
  const dialog = page.getByRole("dialog"); const input = dialog.getByLabel("Image file");
  await input.setInputFiles({ name: "too-large.png", mimeType: "image/png", buffer: Buffer.alloc(6 * 1024 * 1024 + 1) }); await expect(dialog.getByRole("alert")).toContainText("exceeds the 6 MiB"); expect(assetPosts).toBe(0);
  await input.setInputFiles({ name: "notes.gif", mimeType: "image/gif", buffer: Buffer.from("gif") }); await expect(dialog.getByRole("alert")).toContainText("Choose a PNG or JPEG"); expect(assetPosts).toBe(0);
  await input.setInputFiles({ name: "example.png", mimeType: "image/png", buffer: tinyPNG }); await expect(dialog.getByAltText("Selected image preview")).toBeVisible();
  await expectContained(await dialog.boundingBox(), page.viewportSize()); expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "Upload", exact: true }).click(); await expect(dialog.getByRole("alert")).toContainText("Image changed; retry upload"); await expect(dialog).toBeVisible(); await expect(page.getByLabel("Title")).toBeEnabled();
  await dialog.getByRole("button", { name: "Upload", exact: true }).click(); await expect(dialog.getByText("example.png", { exact: true })).toBeVisible(); expect(assetPosts).toBe(2); await expectContained(await dialog.boundingBox(), page.viewportSize()); expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "Done" }).click(); const images = page.getByRole("region", { name: "Images" }); await images.getByRole("button", { name: "View example.png" }).click();
  const preview = page.getByRole("dialog"); await expectContained(await preview.boundingBox(), page.viewportSize()); expect(await preview.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await expect(preview.getByAltText("Preview of example.png")).toBeVisible(); await expect(preview.getByRole("button", { name: "Close" })).toBeVisible();
  expect(await page.locator("html").evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await preview.getByRole("button", { name: "Close" }).click(); await expect(preview).toBeHidden();
});

test("Crepe editors retain accepted styling and generated Markdown", async ({ page }, testInfo) => {
  let createPayload = ""; await mockTissuesAPI(page, { observe: (method, pathname, body) => { if (method === "POST" && pathname.endsWith("/projects/FLUENT/issues")) createPayload = body; } });
  await page.goto("/?view=issue&mode=create"); const createEditor = page.getByRole("group", { name: "Description" }); await expect(createEditor.locator(".ProseMirror")).toBeEditable(); await expectContained(await page.locator(".form-view").boundingBox(), page.viewportSize()); await expectInactiveCrepeChrome(createEditor);
  if (testInfo.project.name === "chromium") await expect(page.locator(".form-view")).toHaveScreenshot("create-issue-view.png", { animations: "disabled" });
  await page.getByLabel("Title").fill("Created in browser"); const prose = createEditor.locator(".ProseMirror"); await prose.pressSequentially("Browser "); await prose.press("Control+B"); await prose.pressSequentially("bold"); await prose.press("Control+B"); await page.getByRole("button", { name: "Create" }).click();
  await expect.poll(() => createPayload).not.toBe(""); expect(JSON.parse(createPayload).description).toContain("Browser **bold**");
  await page.goto("/?view=issue&issue=FLUENT-1"); const editEditor = page.getByRole("group", { name: "Description" }); await expect(editEditor.locator(".ProseMirror h1")).toHaveText("Heading"); await expectInactiveCrepeChrome(editEditor);
  if (testInfo.project.name === "chromium") await expect(page.locator(".form-view")).toHaveScreenshot("edit-issue-view.png", { animations: "disabled" });
});

test("Comments preserve semantic Markdown, raw HTML safety, and dialog editing", async ({ page }, testInfo) => {
  let payload = ""; await mockTissuesAPI(page, { observe: (method, pathname, body) => { if (method === "PATCH" && pathname.endsWith("/comments/comment-1")) payload = body; } }); await page.addInitScript(() => { window.prompt = () => { throw new Error("prompt must not be called"); }; });
  await page.goto("/?view=issue&issue=FLUENT-1"); const surface = page.locator(".comment-body"); await expect(surface.locator("h1")).toHaveText("Heading"); await expect(surface.locator("strong")).toHaveText("bold"); await expect(surface.locator("em")).toHaveText("italic"); await expect(surface.locator("ul li")).toHaveCount(2); await expect(surface.locator("script, [data-unsafe]")).toHaveCount(0);
  const commentForm = page.locator(".comment-form"); await expectInactiveCrepeChrome(commentForm.getByRole("group", { name: "Comment body" })); if (testInfo.project.name === "chromium") await expect(commentForm).toHaveScreenshot("comment-create-editor.png", { animations: "disabled" });
  await page.getByRole("button", { name: "Edit", exact: true }).click(); const dialog = page.getByRole("dialog"); const editor = dialog.getByRole("group", { name: "Comment body" }); await expect(editor.locator(".ProseMirror h1")).toHaveText("Heading"); await expectContained(await dialog.boundingBox(), page.viewportSize()); await expectInactiveCrepeChrome(editor); await editor.locator(".ProseMirror p").first().click(); await page.keyboard.press("End"); await page.keyboard.insertText(" Edited"); await expect(editor.locator(".ProseMirror")).toContainText("Edited"); await page.waitForTimeout(1000); await dialog.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => payload).not.toBe(""); expect(JSON.parse(payload).body).toContain("# Heading"); expect(JSON.parse(payload).body).toContain("Edited");
});
