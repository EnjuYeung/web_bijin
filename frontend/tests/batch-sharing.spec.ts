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
async function manage(page: Page) {
  const toggle = page.getByRole("button", { name: "管理分享", exact: true });
  await toggle.click(); await expect(toggle).toHaveAttribute("aria-pressed", "true");
}
const recent = (page: Page) => page.getByRole("button", { name: "查看最近的批量任务", exact: true });
async function openByAPI(page: Page, albums: string[]) {
  const task = await (await page.request.post("/api/share-batches/prepare", { data: { mode: "open", albums } })).json();
  expect((await page.request.post(`/api/share-batches/${task.id}/start`, { data: { revision: task.revision } })).status()).toBe(200);
  return finish(page, task.id);
}
const cover = (page: Page, id: string) => page.locator(`.album-card[href="/?album=${encodeURIComponent(id)}"]`);
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
  await page.goto("/?view=albums"); await manage(page);
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

test("single album mobile controls, manual multiline links and results reopen after refresh", async ({ page, browserName }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { write: () => Promise.reject(new Error("blocked")), writeText: () => Promise.reject(new Error("blocked")) } }));
  await page.addInitScript(() => localStorage.setItem("juens-theme", "day"));
  await page.setViewportSize({ width: 320, height: 700 });
  await page.goto("/?album=misc&shared=1");
  await expect(page.getByRole("button", { name: "管理分享", exact: true })).toHaveAttribute("aria-pressed", "true");
  const task = await begin(page, "open"); expect(task.total).toBe(3);
  const done = await finish(page, task.id); expect(done.succeeded).toBe(3);
  await expect(page.locator(".batch-result")).toContainText("成功 3");
  await page.getByRole("button", { name: "复制全部分享链接", exact: true }).click();
  const field = page.getByLabel("全部分享地址", { exact: true }); await expect(field).toBeVisible();
  expect((await field.inputValue()).split("\n")).toHaveLength(3);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/batch-${browserName}-mobile-light.png` });
  // After a reload the finished result waits behind its button.
  await page.reload(); await expect(recent(page)).toBeVisible(); await expect(page.locator(".batch-result")).toHaveCount(0);
  await recent(page).click(); await expect(page.locator(".batch-result")).toContainText("成功 3");
  await page.evaluate(() => { document.documentElement.dataset.theme = "dark"; document.documentElement.classList.add("dark"); });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "批量停止分享", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/batch-${browserName}-mobile-dark.png` });
});

test("lost start answer is read back, does not create a second attempt, and result can be reopened", async ({ page }) => {
  let starts = 0;
  await page.route("**/api/share-batches/*/start", async route => { starts++; const response = await route.fetch(); expect(response.status()).toBe(200); await route.fulfill({ status: 200, contentType: "application/json", body: "{" }); });
  await page.goto("/?album=misc"); await manage(page); const task = await begin(page, "open");
  const done = await finish(page, task.id); expect(done.revision).toBe(1); expect(starts).toBe(1);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator(".batch-result")).toContainText("成功 3");
  await page.goto("/?view=albums"); await manage(page); await expect(recent(page)).toBeVisible(); await expect(page.locator(".batch-result")).toHaveCount(0);
  await recent(page).click(); await expect(page.locator(".batch-result")).toContainText("成功 3");
});

test("a refused confirmation shows its reason inside the dialog and going back clears it", async ({ page }) => {
  await page.goto("/?album=misc"); await manage(page);
  const prepared = page.waitForResponse(r => r.url().endsWith("/api/share-batches/prepare"));
  await page.getByRole("button", { name: "批量开启分享", exact: true }).click();
  const task = await (await prepared).json();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText(`共 ${task.total} 张`);
  // Another tab already runs a task: the server refuses this one with 409.
  await page.route("**/api/share-batches/*/start", route => route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "任务或图片状态已改变，请重新核对" }) }));
  await page.getByRole("button", { name: "确认开启分享", exact: true }).click();
  await expect(dialog.locator("#batch-confirm-error")).toContainText("任务或图片状态已改变，请重新核对");
  await expect(page.getByRole("button", { name: "确认开启分享", exact: true })).toBeEnabled();
  expect((await (await page.request.get(`/api/share-batches/${task.id}`)).json()).status).toBe("prepared");
  await page.getByRole("button", { name: "返回", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.locator(".batch-sharing [role=alert]")).toHaveCount(0);
  await page.unroute("**/api/share-batches/*/start");
  const started = await begin(page, "open");
  await finish(page, started.id);
});

test("the shared-only gallery waits for a running task to end, then reloads once", async ({ page }) => {
  await openByAPI(page, ["misc"]);
  await page.goto("/?album=misc&shared=1");
  await expect(page.locator(".sheet")).toHaveCount(3);
  let lists = 0;
  page.on("request", r => { if (r.url().includes("/api/photos?") && r.url().includes("shared=1")) lists++; });
  // The real task ends at once; its first three progress reads say it is still running.
  let polls = 0;
  await page.route(/\/api\/share-batches\/[A-Za-z0-9_-]{32}$/, async route => {
    const response = await route.fetch(); const task = await response.json();
    if (route.request().method() === "GET" && ++polls <= 3) Object.assign(task, { status: "running", done: polls, succeeded: polls, items: [] });
    await route.fulfill({ response, json: task });
  });
  const started = await begin(page, "stop");
  await expect.poll(() => polls, { timeout: 15_000 }).toBe(3);
  await expect(page.locator(".batch-result")).toContainText("正在处理");
  expect(lists).toBe(0);
  await expect(page.locator(".batch-result")).toContainText("已完成", { timeout: 10_000 });
  await expect(page.getByText("还没有分享的照片", { exact: true })).toBeVisible();
  expect(lists).toBe(1);
  await finish(page, started.id);
});

test("select all only selects filtered albums and a filter change clears hidden selections", async ({ page }) => {
  await page.route("**/api/albums", async route => {
    const response = await route.fetch(); const data = await response.json();
    for (const a of data.albums) { a.author = a.id === "batch" ? { id: 900, name: "筛选测试作者" } : null; a.models = []; }
    await route.fulfill({ response, json: data });
  });
  await page.goto("/?view=albums"); await manage(page);
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
  await page.goto("/?album=misc"); await manage(page);
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toBeDisabled();
  await page.getByRole("combobox", { name: "查看批量任务", exact: true }).click();
  await page.getByRole("option", { name: /停止/ }).first().click();
  await expect(page.locator(".batch-result")).toContainText("停止分享");
  hold = false;
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toBeEnabled();
});

test("sharing controls stay hidden until 管理分享 is pressed; closing clears the selection and the shared filter", async ({ page, browserName }) => {
  const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
  const toggle = page.getByRole("button", { name: "管理分享", exact: true });
  await page.goto("/?view=albums");
  await expect(page.locator(".album-card").first()).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByRole("checkbox")).toHaveCount(0);
  await expect(page.locator(".album-meta [data-slot=badge]")).toHaveCount(0);
  await expect(page.locator("#sharing-panel")).toHaveCount(0);
  await expect(page.locator(".album-share-mark")).toHaveCount(0);
  await page.screenshot({ path: `../output/manage-${browserName}-albums-closed.png` });
  // A batch that ends while the controls are closed is reflected once they open.
  // This page did not start it, so its result waits behind the button.
  await openByAPI(page, ["misc"]);
  await manage(page);
  const misc = page.locator(".album-select-card").filter({ has: page.getByRole("checkbox", { name: "选择相册「misc」", exact: true }) });
  await expect(misc).toContainText("全部已分享 · 3/3");
  await expect(misc.locator(".album-share-mark")).toHaveCount(0);
  await expect(recent(page)).toBeVisible();
  await expect(page.locator(".batch-result")).toHaveCount(0);
  // The whole 44 px chip selects, even at its corner, without opening the album.
  const chip = misc.locator(".album-checkbox"); const size = (await chip.boundingBox())!;
  expect(size.width).toBeGreaterThanOrEqual(44); expect(size.height).toBeGreaterThanOrEqual(44);
  await chip.click({ position: { x: 3, y: 3 } });
  await expect(page.getByRole("checkbox", { name: "选择相册「misc」", exact: true })).toBeChecked();
  expect(new URL(page.url()).searchParams.get("view")).toBe("albums");
  await expect(page.getByText("已选 1 本相册", { exact: true })).toBeVisible();
  await page.screenshot({ path: `../output/manage-${browserName}-albums-open.png` });
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByRole("checkbox")).toHaveCount(0);
  await expect(page.locator(".batch-result")).toHaveCount(0);
  await expect(cover(page, "misc").locator(".album-share-mark")).toHaveAttribute("data-state", "full");
  await manage(page);
  await expect(page.getByRole("checkbox", { name: "选择相册「misc」", exact: true })).not.toBeChecked();
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toHaveCount(0);

  // The photos page is a plain gallery: no 管理分享, and an old shared=1 link shows every photo.
  for (const path of ["/", "/?shared=1"]) {
    await page.goto(path);
    await expect(page.locator(".sheet").first()).toBeVisible();
    await expect(toggle).toHaveCount(0);
    await expect(page.locator("#sharing-panel")).toHaveCount(0);
    await expect(page.getByRole("checkbox", { name: "只看已分享" })).toHaveCount(0);
    await expect.poll(async () => Number(await page.locator("#count").innerText())).toBeGreaterThan(3);
  }
  await page.screenshot({ path: `../output/manage-${browserName}-photos.png` });

  await page.goto("/?album=misc");
  await expect(page.locator(".sheet").first()).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByRole("checkbox", { name: "只看已分享" })).toHaveCount(0);
  await expect(page.locator(".batch-result")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "批量开启分享", exact: true })).toHaveCount(0);
  await manage(page);
  await expect(recent(page)).toBeVisible();
  await expect(page.locator(".batch-result")).toHaveCount(0);
  await page.getByRole("checkbox", { name: "只看已分享" }).check();
  await expect(page.locator("#count")).toHaveText("3");
  await expect.poll(() => new URL(page.url()).searchParams.get("shared")).toBe("1");
  await toggle.click();
  await expect(page.getByRole("checkbox", { name: "只看已分享" })).toHaveCount(0);
  await expect.poll(() => new URL(page.url()).searchParams.get("shared")).toBeNull();

  await page.setViewportSize({ width: 320, height: 700 });
  for (const path of ["/?view=albums", "/?album=misc"]) {
    await page.goto(path);
    await expect(page.locator("#page-title")).toBeVisible();
    const box = (await toggle.boundingBox())!; expect(box.width).toBeLessThanOrEqual(48); expect(box.height).toBeGreaterThanOrEqual(44);
    await manage(page);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  await page.screenshot({ path: `../output/manage-${browserName}-album-mobile.png` });
  expect(errors).toEqual([]);
});

test("a finished result opens by itself only on the page that started it", async ({ page }) => {
  const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
  const toggle = page.getByRole("button", { name: "管理分享", exact: true });
  const earlier = await openByAPI(page, ["misc"]);
  await page.goto("/?view=albums"); await manage(page);
  await expect(recent(page)).toBeVisible();
  await expect(page.locator(".batch-result")).toHaveCount(0);
  // Started here: the result shows, also after closing and reopening 管理分享.
  await page.getByRole("checkbox", { name: "选择相册「misc」", exact: true }).check();
  // Tasks are ordered by the second they were created; a later second makes "most recent" unambiguous.
  await expect.poll(() => Math.floor(Date.now() / 1000), { timeout: 3000 }).toBeGreaterThan(earlier.created);
  const task = await begin(page, "stop"); await finish(page, task.id);
  await expect(page.locator(".batch-result")).toContainText("已完成");
  await expect(recent(page)).toHaveCount(0);
  await toggle.click(); await manage(page);
  await expect(page.locator(".batch-result")).toContainText("停止分享 · 1 本相册");
  // Leaving through the sidebar and coming back puts it behind the button.
  await page.locator("#nav-photos").click(); await expect(page.locator(".sheet").first()).toBeVisible();
  await page.locator("#nav-albums").click(); await expect(page.locator(".album-card").first()).toBeVisible();
  await manage(page);
  await expect(recent(page)).toBeVisible();
  await expect(page.locator(".batch-result")).toHaveCount(0);
  await recent(page).click();
  await expect(page.locator(".batch-result")).toContainText("停止分享 · 1 本相册");
  await expect(page.getByRole("combobox", { name: "查看批量任务", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("covers mark shared albums quietly and the album filter narrows by sharing state", async ({ page, browserName }) => {
  const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
  // misc fully shared, one photo of batch shared, every other album not shared.
  await openByAPI(page, ["misc"]);
  const photo = (await (await page.request.get("/api/photos?album=batch&limit=1")).json()).photos[0];
  const state = await (await page.request.get(`/api/photos/${photo.id}/share`)).json();
  expect((await page.request.post(`/api/photos/${photo.id}/share`, { data: { generation: state.generation, version: photo.version } })).status()).toBe(200);
  const total = (await (await page.request.get("/api/albums")).json()).total;

  await page.goto("/?view=albums");
  const full = cover(page, "misc").locator(".album-share-mark"), part = cover(page, "batch").locator(".album-share-mark");
  await expect(full).toHaveAttribute("data-state", "full"); await expect(full).toHaveText(""); await expect(full).toHaveAttribute("title", "全部已分享");
  await expect(part).toHaveAttribute("data-state", "partial"); await expect(part).toHaveText("1/50"); await expect(part).toHaveAttribute("title", "部分已分享，1 / 50 张");
  await expect(page.locator(".album-share-mark")).toHaveCount(2);
  await expect(cover(page, "misc")).toHaveAttribute("aria-label", /，全部已分享$/);
  await expect(cover(page, "batch")).toHaveAttribute("aria-label", /，部分已分享，1 \/ 50 张$/);
  // A small chip in the top-left corner, smaller than the 44 px selection check.
  const box = await cover(page, "batch").evaluate(card => { const c = card.getBoundingClientRect(), m = card.querySelector(".album-share-mark")!.getBoundingClientRect(); return { left: m.left - c.left, top: m.top - c.top, height: m.height }; });
  expect(box.left).toBeLessThanOrEqual(12); expect(box.top).toBeLessThanOrEqual(12); expect(box.height).toBeLessThan(32);
  await page.screenshot({ path: `../output/share-marks-${browserName}-desktop.png` });

  const filter = page.locator("#album-share-filter");
  const choose = async (label: string) => { await filter.click(); await page.getByRole("option", { name: label, exact: true }).click(); await expect(filter).toHaveText(label); };
  await expect(filter).toHaveText("全部分享状态");
  expect((await filter.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  await choose("全部已分享");
  await expect(page.locator(".album-card")).toHaveCount(1); await expect(cover(page, "misc")).toBeVisible();
  await expect(page.locator("#album-filter-note")).toContainText(`筛选后 1 / ${total} 本`);
  await choose("部分已分享");
  await expect(page.locator(".album-card")).toHaveCount(1); await expect(cover(page, "batch")).toBeVisible();
  await choose("未分享");
  await expect(page.locator(".album-card")).toHaveCount(total - 2); await expect(page.locator(".album-share-mark")).toHaveCount(0);
  // Kept for this visit like the author and model filters; 清除筛选 resets it too.
  await page.reload(); await expect(filter).toHaveText("未分享"); await expect(page.locator(".album-card")).toHaveCount(total - 2);
  await page.getByRole("button", { name: "清除筛选", exact: true }).click();
  await expect(filter).toHaveText("全部分享状态"); await expect(page.locator(".album-card")).toHaveCount(total);
  await expect(page.locator("#album-filter-note")).toHaveCount(0);

  // While 管理分享 is open the check takes the corner and the badge tells the state.
  await manage(page);
  await expect(page.locator(".album-share-mark")).toHaveCount(0);
  await expect(page.locator(".album-select-card").filter({ has: cover(page, "batch") })).toContainText("部分已分享 · 1/50");
  await page.getByRole("button", { name: "管理分享", exact: true }).click();
  await expect(page.locator(".album-share-mark")).toHaveCount(2);

  await page.setViewportSize({ width: 320, height: 700 });
  if (await page.locator("html").getAttribute("data-sidebar") === "expanded") await page.locator("#sidebar-brand").click();
  for (const theme of ["light", "dark"]) {
    await page.evaluate(value => { document.documentElement.dataset.theme = value; document.documentElement.classList.toggle("dark", value === "dark"); }, theme);
    await page.waitForTimeout(400); // let the side rail and colour transitions finish before the screenshot
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await expect(part).toBeVisible();
    await page.screenshot({ path: `../output/share-marks-${browserName}-mobile-${theme}.png` });
  }
  expect(errors).toEqual([]);
});
