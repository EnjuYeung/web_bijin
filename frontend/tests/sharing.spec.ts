import { test, expect, type Page } from "@playwright/test";

const ids = new Set<number>();
const baseURL = "http://127.0.0.1:18092";
interface Share { active: boolean; generation: number; version: string; path: string }

async function current(page: Page, id: number): Promise<Share> {
  const res = await page.request.get(`/api/photos/${id}/share`);
  expect(res.status()).toBe(200);
  return res.json();
}

async function openFirst(page: Page, path = "/") {
  await page.goto(path);
  const card = page.locator(".sheet").first();
  await expect(card).toBeVisible();
  const id = Number(await card.getAttribute("data-id")); ids.add(id);
  await card.click();
  await expect(page.getByRole("button", { name: "复制分享链接", exact: true })).toBeEnabled();
  return id;
}

test.beforeEach(async ({ page }) => {
  ids.clear();
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => {
    const data = await (await page.request.get("/api/photos?limit=1")).json();
    return !data.status.scanning && !data.status.queued;
  }, { timeout: 60_000 }).toBe(true);
});

test.afterEach(async ({ page }) => {
  for (const id of ids) {
    const share = await current(page, id);
    if (share.active) expect((await page.request.delete(`/api/photos/${id}/share`, { data: { version: share.version, generation: share.generation } })).status()).toBe(200);
  }
});

test("share copies the actual original URL, guest and external page can view it, stop rejects it", async ({ page, browser, browserName }) => {
  if (browserName === "chromium") await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  const errors: string[] = []; page.on("pageerror", err => errors.push(err.message));
  const id = await openFirst(page);
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByRole("button", { name: "已复制", exact: true })).toBeVisible();
  const shared = await current(page, id);
  expect(shared.active).toBe(true);
  const address = baseURL + shared.path;
  if (browserName === "chromium") expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(address);
  const original = await page.request.get(`/original/${id}`);
  expect(original.status()).toBe(200);
  const bytes = await original.body();
  const guest = await browser.newContext();
  try {
    expect((await guest.request.get(baseURL + "/api/photos")).status()).toBe(401);
    const res = await guest.request.get(address);
    expect(res.status()).toBe(200); expect(await res.body()).toEqual(bytes);
    const outside = await guest.newPage();
    await outside.setContent(`<img alt="外站引用" src="${address}">`);
    await expect.poll(() => outside.getByRole("img", { name: "外站引用" }).evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
    await page.screenshot({ path: `../output/share-${browserName}-desktop.png` });
    await page.getByRole("button", { name: "停止分享", exact: true }).click();
    await expect(page.getByRole("button", { name: "停止分享", exact: true })).toHaveCount(0);
    expect((await guest.request.get(address)).status()).toBe(404);
    await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
    await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeVisible();
    expect((await current(page, id)).path).not.toBe(shared.path);
    expect((await guest.request.get(address)).status()).toBe(404);
  } finally { await guest.close(); }
  expect(errors).toEqual([]);
});

test("clipboard rejection offers a real manual address and read-only arrow keys keep the same photo", async ({ page }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
    write: () => Promise.reject(new Error("clipboard blocked")), writeText: () => Promise.reject(new Error("clipboard blocked")),
  } }));
  const id = await openFirst(page);
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  const field = page.getByLabel("图片地址", { exact: true });
  await expect(field).toBeVisible();
  const shared = await current(page, id);
  await expect(field).toHaveValue(baseURL + shared.path);
  expect((await page.request.get(shared.path)).status()).toBe(200);
  await field.focus(); await page.keyboard.press("ArrowLeft");
  await expect(page).toHaveURL(new RegExp(`#p/${id}$`));
  await expect(page.getByRole("button", { name: "已复制", exact: true })).toHaveCount(0);
});

test("lost or malformed creation answer is verified without creating another share or copying a bad URL", async ({ page }) => {
  let posts = 0;
  await page.route("**/api/photos/*/share", async route => {
    if (route.request().method() !== "POST") return route.continue();
    posts++;
    const answer = await route.fetch(); expect(answer.status()).toBe(200);
    await route.fulfill({ status: 200, headers: { "Content-Type": "application/json" }, body: "{" });
  });
  const id = await openFirst(page);
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByLabel("图片地址", { exact: true })).toBeVisible();
  const shared = await current(page, id);
  await expect(page.getByLabel("图片地址", { exact: true })).toHaveValue(baseURL + shared.path);
  expect(shared.active).toBe(true); expect(posts).toBe(1);
  expect((await page.request.get(shared.path)).status()).toBe(200);
  await expect(page.getByRole("button", { name: "已复制", exact: true })).toHaveCount(0);
  await page.unrouteAll({ behavior: "wait" });
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeVisible();
  expect((await current(page, id)).path).toBe(shared.path);
});

test("shared filter works in an album, stopping removes it and resets paging", async ({ page }) => {
  const id = await openFirst(page, "/?album=misc");
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeVisible();
  await page.locator("#lb-close").click();
  await page.getByRole("button", { name: "管理分享", exact: true }).click();
  await page.getByRole("checkbox", { name: "只看已分享" }).check();
  await expect(page.locator("#count")).toHaveText("1");
  await expect(page.locator(".sheet")).toHaveCount(1);
  await expect(page.locator(".sheet")).toHaveAttribute("data-id", String(id));
  await page.locator(".sheet").click();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "停止分享", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator("#count")).toHaveText("0");
  await expect(page.getByText("还没有分享的照片", { exact: true })).toBeVisible();
  await page.getByRole("checkbox", { name: "只看已分享" }).uncheck();
  await expect(page.locator(".sheet").first()).toBeVisible();
  expect(Number(await page.locator("#count").innerText())).toBeGreaterThan(1);
  await page.goto("/?album=__missing_fixture_album__&shared=1");
  await expect(page.getByText("还没有分享的照片", { exact: true })).toBeVisible();
});

test("mobile share controls stay visible with info hidden and reopening remains usable", async ({ page, browserName }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const id = await openFirst(page);
  await expect(page.locator("#lb-meta")).toHaveCount(0);
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeInViewport();
  await page.setViewportSize({ width: 320, height: 650 });
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/share-${browserName}-mobile.png` });
  await page.locator("#lb-close").click();
  await page.locator(`.sheet[data-id="${id}"]`).click();
  await expect(page.getByRole("button", { name: "复制分享链接", exact: true })).toBeEnabled();
});

test("closing during a pending copy and reopening the same photo does not leave actions stuck", async ({ page }) => {
  let release!: () => void;
  let created!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  const reached = new Promise<void>(resolve => { created = resolve; });
  let hold = true;
  await page.route("**/api/photos/*/share", async route => {
    if (route.request().method() !== "POST" || !hold) return route.continue();
    hold = false;
    const answer = await route.fetch();
    created();
    await held;
    try { await route.fulfill({ response: answer }); } catch { /* closing aborts the old request */ }
  });
  const id = await openFirst(page);
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await reached;
  await page.locator("#lb-close").click();
  await page.locator(`.sheet[data-id="${id}"]`).click();
  await expect(page.getByRole("button", { name: "复制分享链接", exact: true })).toBeEnabled();
  release();
  await page.getByRole("button", { name: "复制分享链接", exact: true }).click();
  await expect(page.getByRole("button", { name: "停止分享", exact: true })).toBeEnabled();
});
