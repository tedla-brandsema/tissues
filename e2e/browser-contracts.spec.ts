import { expect, Page, test } from "@playwright/test";

const markdown = "# Heading\n\nThis is **bold** and *italic*.\n\n- one\n- two\n\n> quoted\n\n`inline`\n\n<script data-unsafe>bad()</script>";
const timestamp = "2026-01-01T00:00:00Z";
const issue = {
  id: "parent", title: "Browser fixture", state: "open", created: timestamp, updated: timestamp,
  description: markdown, parent_id: "", children: [],
  comments: [{ id: "comment-1", author: "Ada", created: timestamp, updated: timestamp, body: markdown }],
};

async function mockTissuesAPI(page: Page, observe?: (method: string, pathname: string, body: string) => void) {
  await page.route("**/api/tissues/v1/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const method = request.method();
    observe?.(method, pathname, request.postData() || "");
    if (pathname.endsWith("/issues") && method === "GET") return route.fulfill({ json: { issues: [issue] } });
    if (pathname.endsWith("/issues") && method === "POST") return route.fulfill({ status: 201, json: { ...issue, id: "created" } });
    if (pathname.endsWith("/issues/parent") && method === "PATCH") return route.fulfill({ json: issue });
    if (pathname.endsWith("/comments/comment-1") && method === "PATCH") return route.fulfill({ json: issue.comments[0] });
    return route.fulfill({ json: issue });
  });
}

async function expectContained(box: { x: number; y: number; width: number; height: number } | null, viewport: { width: number; height: number } | null) {
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
}

async function expectInactiveCrepeChrome(editor: ReturnType<Page["locator"]>) {
  for (const selector of [".milkdown-toolbar", ".milkdown-slash-menu", ".milkdown-link-preview", ".milkdown-link-edit"]) {
    const surface = editor.locator(selector);
    await expect(surface).toHaveAttribute("data-show", "false");
    expect(await surface.evaluate((element) => ({ display: getComputedStyle(element).display, position: getComputedStyle(element).position }))).toEqual({ display: "none", position: "absolute" });
  }
  const blockHandle = editor.locator(".milkdown-block-handle");
  await expect(blockHandle).toHaveAttribute("data-show", "false");
  expect(await blockHandle.evaluate((element) => ({ opacity: getComputedStyle(element).opacity, pointerEvents: getComputedStyle(element).pointerEvents, position: getComputedStyle(element).position }))).toEqual({ opacity: "0", pointerEvents: "none", position: "absolute" });
  for (const label of ["Text", "List", "Advanced", "Heading 1", "Heading 2", "Heading 3"]) {
    const labels = editor.getByText(label, { exact: true });
    expect(await labels.count()).toBeGreaterThan(0);
    expect(await labels.evaluateAll((elements) => elements.every((element) => element.getClientRects().length === 0))).toBe(true);
  }
}

test("native auth form posts every credential and continuation field", async ({ page }) => {
  let posted = "";
  await page.route("**/auth/login**", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    posted = route.request().postData() || "";
    await route.fulfill({ status: 200, body: "ok" });
  });
  await page.goto("/auth/login?next=%2F%3Fview%3Dopen&next_exp=12345&next_sig=test-signature");
  await page.getByLabel("Email").fill("browser@example.test");
  await page.getByLabel("Password").fill("fake-browser-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect.poll(() => posted).not.toBe("");
  const form = new URLSearchParams(posted);
  expect(Object.fromEntries(form)).toEqual({
    next: "/?view=open", next_exp: "12345", next_sig: "test-signature",
    email: "browser@example.test", password: "fake-browser-password",
  });
});

test("workspace has one functional chrome and an opaque shared dialog", async ({ page }) => {
  await mockTissuesAPI(page);
  await page.goto("/?view=open");
  await expect(page.getByText("Shared workspace")).toHaveCount(0);
  await expect(page.locator("header")).toHaveCount(0);
  await expect(page.locator(".profile-message")).toHaveCount(0);
  await expect(page.locator(".navigator .app-identity")).toHaveText("🤧 tissues");
  const create = page.locator(".navigator").getByRole("button", { name: "New issue" });
  await expect(create).toBeVisible();
  await create.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const dialogBackground = await dialog.evaluate((element) => getComputedStyle(element).backgroundColor);
  const selectBackground = await dialog.locator("select").evaluate((element) => getComputedStyle(element).backgroundColor);
  const overlayBackground = await dialog.evaluate((element) => getComputedStyle(element.previousElementSibling!).backgroundColor);
  expect(dialogBackground).toBe("rgb(255, 255, 255)");
  expect(selectBackground).toBe("rgb(255, 255, 255)");
  expect(overlayBackground).toBe("oklab(0 0 0 / 0.4)");
  await expect(dialog.locator(".ProseMirror")).toBeEditable();
});

test("Crepe editor chrome stays styled, hidden at rest, and within the viewport", async ({ page }, testInfo) => {
  await mockTissuesAPI(page);
  await page.goto("/?view=open");
  await page.locator(".navigator").getByRole("button", { name: "New issue" }).click();

  const createDialog = page.getByRole("dialog");
  const createEditor = createDialog.getByRole("group", { name: "Description" });
  await expect(createEditor.locator(".ProseMirror")).toBeEditable();
  await expectContained(await createDialog.boundingBox(), page.viewportSize());
  const createBox = await createEditor.boundingBox();
  expect(createBox!.height).toBeGreaterThanOrEqual(180);
  expect(createBox!.height).toBeLessThanOrEqual(322);
  await expectInactiveCrepeChrome(createEditor);
  if (testInfo.project.name === "chromium") await expect(createDialog).toHaveScreenshot("create-issue-dialog.png", { animations: "disabled" });
  await createDialog.getByRole("button", { name: "Cancel" }).click();

  await page.goto("/?issue=parent&view=open");
  await page.getByRole("button", { name: "Edit", exact: true }).first().click();
  const editDialog = page.getByRole("dialog");
  const editEditor = editDialog.getByRole("group", { name: "Description" });
  await expect(editEditor.locator(".ProseMirror h1")).toHaveText("Heading");
  await expectContained(await editDialog.boundingBox(), page.viewportSize());
  expect((await editEditor.boundingBox())!.height).toBeLessThanOrEqual(322);
  await expectInactiveCrepeChrome(editEditor);
  if (testInfo.project.name === "chromium") await expect(editDialog).toHaveScreenshot("edit-issue-dialog.png", { animations: "disabled" });
  await editDialog.getByRole("button", { name: "Cancel" }).click();

  const commentForm = page.locator(".comment-form");
  const commentEditor = commentForm.getByRole("group", { name: "Comment body" });
  await expect(commentEditor.locator(".ProseMirror")).toBeEditable();
  const commentBox = await commentEditor.boundingBox();
  expect(commentBox!.height).toBeGreaterThanOrEqual(90);
  expect(commentBox!.height).toBeLessThanOrEqual(194);
  expect(commentBox!.height).toBeLessThan(page.viewportSize()!.height * 0.35);
  await expectInactiveCrepeChrome(commentEditor);
  if (testInfo.project.name === "chromium") await expect(commentForm).toHaveScreenshot("comment-create-editor.png", { animations: "disabled" });

  await page.getByRole("button", { name: "Edit", exact: true }).last().click();
  const commentDialog = page.getByRole("dialog");
  const commentEditEditor = commentDialog.getByRole("group", { name: "Comment body" });
  await expect(commentEditEditor.locator(".ProseMirror h1")).toHaveText("Heading");
  await expectContained(await commentDialog.boundingBox(), page.viewportSize());
  expect((await commentEditEditor.boundingBox())!.height).toBeLessThanOrEqual(194);
  await expectInactiveCrepeChrome(commentEditEditor);
});

test("browser renders Markdown semantics and keeps raw HTML disabled", async ({ page }) => {
  await mockTissuesAPI(page);
  await page.goto("/?issue=parent&view=open");
  for (const surface of [page.locator(".issue-description"), page.locator(".comment-body")]) {
    await expect(surface.locator("h1")).toHaveText("Heading");
    await expect(surface.locator("strong")).toHaveText("bold");
    await expect(surface.locator("em")).toHaveText("italic");
    await expect(surface.locator("ul li")).toHaveCount(2);
    await expect(surface.locator("blockquote")).toContainText("quoted");
    await expect(surface.locator("code")).toContainText("inline");
    await expect(surface.locator("script")).toHaveCount(0);
    await expect(surface.locator("[data-unsafe]")).toHaveCount(0);
  }
  await expect(page.locator(".comment-form .ProseMirror")).toBeEditable();
  await page.getByRole("button", { name: "Edit", exact: true }).first().click();
  await expect(page.getByRole("dialog").locator(".ProseMirror h1")).toHaveText("Heading");
});

test("WYSIWYG issue creation sends canonical Markdown", async ({ page }) => {
  let payload = "";
  await mockTissuesAPI(page, (method, pathname, body) => {
    if (method === "POST" && pathname.endsWith("/issues")) payload = body;
  });
  await page.goto("/?view=open");
  await page.locator(".navigator").getByRole("button", { name: "New issue" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Title").fill("Created in browser");
  const editor = dialog.locator(".ProseMirror");
  await editor.pressSequentially("Browser ");
  await editor.press("Control+B");
  await editor.pressSequentially("bold");
  await editor.press("Control+B");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => payload).not.toBe("");
  const body = JSON.parse(payload);
  expect(body.description).toContain("Browser **bold**");
  expect(body.description).not.toContain("<strong>");
  expect(body.description).not.toContain("{");
});

test("comment editing uses a dialog editor and sends Markdown", async ({ page }) => {
  let payload = "";
  await mockTissuesAPI(page, (method, pathname, body) => {
    if (method === "PATCH" && pathname.endsWith("/comments/comment-1")) payload = body;
  });
  await page.addInitScript(() => { window.prompt = () => { throw new Error("prompt must not be called"); }; });
  await page.goto("/?issue=parent&view=open");
  await page.getByRole("button", { name: "Edit", exact: true }).last().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Edit comment" })).toBeVisible();
  const editor = dialog.locator(".ProseMirror");
  await editor.press("Control+End");
  await editor.pressSequentially(" Edited ");
  await editor.press("Control+B");
  await editor.pressSequentially("bold");
  await editor.press("Control+B");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => payload).not.toBe("");
  const body = JSON.parse(payload);
  expect(body.body).toContain("# Heading");
  expect(body.body).toContain("- one");
  expect(body.body).toContain("**bold**");
  expect(body.body).not.toContain("<strong>");
});
