import { test, expect, type Page } from "@playwright/test";

async function finish(page: Page, id: string) {
  await expect.poll(async () => (await (await page.request.get(`/api/share-batches/${id}`)).json()).status).toBe("completed");
  return (await (await page.request.get(`/api/share-batches/${id}`)).json());
}
async function cleanup(page: Page) {
  const albums = (await (await page.request.get("/api/albums")).json()).albums;
  const chosen = albums.filter((a: { shared: number }) => a.shared > 0).map((a: { id: string }) => a.id);
  if (!chosen.length) return;
  const task = await (await page.request.post("/api/share-batches/prepare", { data: { mode: "stop", albums: chosen } })).json();
  expect((await page.request.post(`/api/share-batches/${task.id}/start`, { data: { revision: task.revision } })).status()).toBe(200);
  await finish(page, task.id);
}
async function begin(page: Page, mode: "open" | "stop") {
  const prepared = page.waitForResponse(r => r.url().endsWith("/api/share-batches/prepare") && r.request().method() === "POST");
  await page.getByRole("button", { name: mode === "open" ? "批量开启分享" : "批量停止分享", exact: true }).click();
  const task = await (await prepared).json();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByRole("dialog")).toContainText(`共 ${task.total} 张`);
  await page.getByRole("button", { name: mode === "open" ? "确认开启分享" : "确认停止分享", exact: true }).click();
  return task;
}

test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => {
    const data = await (await page.request.get("/api/photos?limit=1")).json(); return !data.status.scanning && !data.status.queued;
  }, { timeout: 60_000 }).toBe(true);
  await cleanup(page);
});
test.afterEach(async ({ page }) => { await cleanup(page); });

test("album selection opens all pages, copies original addresses, reports state and stops all shares", async ({ page, browserName }) => {
  if (browserName === "chromium") await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
  await page.goto("/?view=albums");
  await page.getByRole("checkbox", { name: "选择相册「batch」", exact: true }).check();
  await page.getByRole("checkbox", { name: "选择相册「misc」", exact: true }).check();
  const task = await begin(page, "open"); expect(task.total).toBe(53);
  const done = await finish(page, task.id); expect(done.succeeded).toBe(53); expect(done.failed).toBe(0);
  await expect(page.locator(".batch-result")).toContainText("成功 53");
  await expect(page.locator(".album-select-card").filter({ has: page.getByRole("checkbox", { name: "选择相册「batch」", exact: true }) })).toContainText("全部已分享 · 50/50");
  await page.getByRole("button", { name: "复制全部分享链接", exact: true }).click();
  await expect(page.getByText("已复制 53 个分享链接。", { exact: true })).toBeVisible();
  if (browserName === "chromium") {
    const links = (await page.evaluate(() => navigator.clipboard.readText())).split("\n"); expect(links).toHaveLength(53);
    const original = await page.request.get(`/original/${done.items[0].id}`);
    const guest = await page.context().browser()!.newContext();
    try { const response = await guest.request.get(links[0]); expect(response.status()).toBe(200); expect(await response.body()).toEqual(await original.body()); } finally { await guest.close(); }
  }
  await expect(page.getByRole("checkbox", { name: "全选当前列表", exact: true })).toHaveAttribute("aria-checked", "mixed");
  await page.screenshot({ path: `../output/batch-${browserName}-desktop.png` });
  await page.setViewportSize({ width: 320, height: 700 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  if (await page.locator("html").getAttribute("data-sidebar") === "expanded") await page.locator("#sidebar-brand").click();
  const bounds = await page.locator(".album-select-card").first().evaluate(card => { const a = card.getBoundingClientRect(), b = card.querySelector(".album-checkbox")!.getBoundingClientRect(); return { inside: b.left >= a.left && b.right <= a.right, height: b.height }; });
  expect(bounds.inside).toBe(true); expect(bounds.height).toBeGreaterThanOrEqual(44);
  await page.screenshot({ path: `../output/batch-${browserName}-albums-mobile.png` });
  await page.setViewportSize({ width: 1440, height: 1000 });
  const stopped = await begin(page, "stop"); await finish(page, stopped.id);
  await expect(page.locator(".batch-result")).toContainText("成功 53");
  expect((await page.request.get(done.items[0].path)).status()).toBe(404);
  await expect(page.locator(".album-select-card").filter({ has: page.getByRole("checkbox", { name: "选择相册「batch」", exact: true }) })).toContainText("未分享 · 0/50");
  expect(errors).toEqual([]);
});

test("single album mobile controls, manual multiline links and results recover after refresh", async ({ page, browserName }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { write: () => Promise.reject(new Error("blocked")), writeText: () => Promise.reject(new Error("blocked")) } }));
  await page.addInitScript(() => localStorage.setItem("juens-theme", "day"));
  await page.setViewportSize({ width: 320, height: 700 });
  await page.goto("/?album=misc&shared=1");
  const task = await begin(page, "open"); expect(task.total).toBe(3);
  const done = await finish(page, task.id); expect(done.succeeded).toBe(3);
  await expect(page.locator(".batch-result")).toContainText("成功 3");
  await page.getByRole("button", { name: "复制全部分享链接", exact: true }).click();
  const field = page.getByLabel("全部分享地址", { exact: true }); await expect(field).toBeVisible();
  expect((await field.inputValue()).split("\n")).toHaveLength(3);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/batch-${browserName}-mobile-light.png` });
  await page.reload(); await expect(page.locator(".batch-result")).toContainText("成功 3");
  await page.evaluate(() => { document.documentElement.dataset.theme = "dark"; document.documentElement.classList.add("dark"); });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "批量停止分享", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/batch-${browserName}-mobile-dark.png` });
});

test("lost start answer is read back, does not create a second attempt, and result survives reopening", async ({ page }) => {
  let starts = 0;
  await page.route("**/api/share-batches/*/start", async route => { starts++; const response = await route.fetch(); expect(response.status()).toBe(200); await route.fulfill({ status: 200, contentType: "application/json", body: "{" }); });
  await page.goto("/?album=misc"); const task = await begin(page, "open");
  const done = await finish(page, task.id); expect(done.revision).toBe(1); expect(starts).toBe(1);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.goto("/?view=albums"); await expect(page.locator(".batch-result")).toContainText("成功 3");
});

test("select all only selects filtered albums and a filter change clears hidden selections", async ({ page }) => {
  await page.route("**/api/albums", async route => {
    const response = await route.fetch(); const data = await response.json();
    for (const a of data.albums) { a.author = a.id === "batch" ? { id: 900, name: "筛选测试作者" } : null; a.models = []; }
    await route.fulfill({ response, json: data });
  });
  await page.goto("/?view=albums");
  await page.getByRole("checkbox", { name: "全选当前列表", exact: true }).check();
  expect(await page.getByRole("checkbox", { name: /^选择相册/ }).count()).toBeGreaterThan(1);
  await page.getByRole("combobox", { name: "按作者筛选", exact: true }).click();
  await page.getByRole("option", { name: /^筛选测试作者/ }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("checkbox", { name: /^选择相册/ })).toHaveCount(1);
  await expect(page.getByRole("checkbox", { name: "选择相册「batch」", exact: true })).not.toBeChecked();
  await page.getByRole("checkbox", { name: "全选当前列表", exact: true }).check();
  const task = await begin(page, "open"); expect(task.albums).toEqual(["batch"]); await finish(page, task.id);
});


test("viewing an older result still observes the active task and releases batch controls when it finishes", async ({ page }) => {
  const previous = await (await page.request.post("/api/share-batches/prepare", { data: { mode: "stop", albums: ["misc"] } })).json();
  await page.request.post(`/api/share-batches/${previous.id}/start`, { data: { revision: previous.revision } }); await finish(page, previous.id);
  const next = await (await page.request.post("/api/share-batches/prepare", { data: { mode: "open", albums: ["misc"] } })).json();
  await page.request.post(`/api/share-batches/${next.id}/start`, { data: { revision: next.revision } }); await finish(page, next.id);
  let hold = true;
  await page.route("**/api/share-batches", async route => { const response = await route.fetch(); const data = await response.json(); if (hold) data.tasks.find((t: { id: string }) => t.id === next.id).status = "running"; await route.fulfill({ response, json: data }); });
  await page.route(`**/api/share-batches/${next.id}`, async route => { const response = await route.fetch(); const data = await response.json(); if (hold) data.status = "running"; await route.fulfill({ response, json: data }); });
  await page.goto("/?album=misc");
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toBeDisabled();
  await page.getByRole("combobox", { name: "查看批量任务", exact: true }).click();
  await page.getByRole("option", { name: /停止/ }).first().click();
  await expect(page.locator(".batch-result")).toContainText("停止分享");
  hold = false;
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toBeEnabled();
});
