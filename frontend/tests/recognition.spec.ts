import { test, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

// Recognition suggestions against tests/recognizer_fixture.py, a local
// OpenAI-format service on 127.0.0.1:18095.
const screenshots = "../output";
const fixture = "http://127.0.0.1:18095";
const service = { provider: "custom", endpoint: fixture + "/v1", apiKey: "sk-fixture", model: "fixture-model", reasoning: "" };
interface ApiAlbum { id: string; name: string; author: { name: string } | null; models: { name: string }[] }
interface FixtureRequest { model: string; authorization: string; reasoning: string | null; format: string | null; temperature: boolean; albums: string[]; roster: { authors: string[]; models: string[] } }

async function signIn(page: Page) {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
}
async function waitForScan(page: Page) {
  await expect.poll(async () => {
    const data = await (await page.request.get("/api/photos?limit=1")).json();
    return !data.status.scanning && !data.status.queued;
  }, { timeout: 60_000 }).toBe(true);
}
async function albums(page: Page): Promise<ApiAlbum[]> {
  return (await (await page.request.get("/api/albums")).json()).albums;
}
async function album(page: Page, id: string) {
  return (await albums(page)).find(item => item.id === id)!;
}
async function organize(page: Page, change: Record<string, unknown>) {
  const response = await page.request.post("/api/organize", { data: { operationId: `${Date.now()}-${crypto.randomUUID()}`, ...change } });
  expect(response.status(), await response.text()).toBe(200);
}
async function resetPeople(page: Page) {
  await organize(page, { kind: "assign", albums: (await albums(page)).map(item => item.id), author: "", models: [] });
  const people = await (await page.request.get("/api/people")).json() as { authors: { id: number }[]; models: { id: number }[] };
  for (const person of [...people.authors, ...people.models]) await organize(page, { kind: "remove", personId: person.id });
}
async function configure(page: Page, values: Partial<typeof service> = {}) {
  const response = await page.request.put("/api/recognizer", { data: { ...service, ...values } });
  expect(response.status(), await response.text()).toBe(200);
}
async function fixtureRequests(page: Page): Promise<FixtureRequest[]> {
  return (await page.request.get(fixture + "/requests")).json();
}
// Upload tests may leave more albums behind; counts come from the server.
async function unfilled(page: Page) {
  return Math.min(100, (await albums(page)).filter(item => item.id !== "." && (!item.author || !item.models.length)).length);
}
async function asked(page: Page) {
  return (await fixtureRequests(page)).flatMap(request => request.albums);
}
function row(page: Page, name: string) {
  return page.locator(".organize-row").filter({ has: page.locator(".organize-album > strong", { hasText: name }) });
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await mkdir(screenshots, { recursive: true });
  await signIn(page);
  await waitForScan(page);
  await resetPeople(page);
  await page.request.delete("/api/recognizer");
  await page.request.post(fixture + "/reset");
  (page as Page & { uiErrors: string[] }).uiErrors = errors;
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { uiErrors: string[] }).uiErrors).toEqual([]);
  await resetPeople(page);
  await page.request.delete("/api/recognizer");
});

test("recognition 01 settings card saves, tests without saving and never shows the key", async ({ page }) => {
  await page.goto("/?view=settings&tab=recognizer");
  const card = page.locator("#recognizer-card");
  await expect(card.locator("#recognizer-state")).toHaveText("未配置");
  await card.locator("#recognizer-add").click();
  await card.locator("#recognizer-provider").click();
  await page.getByRole("option", { name: "其他兼容服务", exact: true }).click();
  // Required fields are named before anything is sent.
  await card.locator("#recognizer-save").click();
  await expect(card.locator("#recognizer-endpoint-error")).toHaveText("请填写接口地址。");
  await expect(card.locator("#recognizer-apiKey-error")).toHaveText("请填写 API Key。");
  await expect(card.locator("#recognizer-model-error")).toHaveText("请填写模型名。");
  await card.locator("#recognizer-endpoint").fill(fixture + "/v1");
  await card.locator("#recognizer-apiKey").fill("sk-fixture");
  await card.locator("#recognizer-model").fill("fixture-model");
  await card.locator("#recognizer-reasoning").click();
  await page.getByRole("option", { name: "高", exact: true }).click();

  await card.locator("#recognizer-test").click();
  await expect(card.locator("#recognizer-msg")).toContainText("测试成功");
  expect((await (await page.request.get("/api/recognizer")).json()).configured).toBe(false);
  let sent = await fixtureRequests(page);
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ model: "fixture-model", authorization: "Bearer sk-fixture", reasoning: "high", format: "json_schema", temperature: false });

  // A model that answers in words fails the test with the reason.
  await card.locator("#recognizer-model").fill("fixture-bad");
  await card.locator("#recognizer-test").click();
  await expect(card.locator("#recognizer-msg")).toContainText("不是要求的 JSON 格式");
  await card.locator("#recognizer-model").fill("fixture-model");

  await card.locator("#recognizer-save").click();
  await expect(card.locator("#recognizer-state")).toHaveText("已配置");
  await expect(card.locator("#recognizer-provider-value")).toContainText("其他兼容服务");
  await expect(card.locator("#recognizer-provider-value")).toContainText(fixture + "/v1");
  await expect(card.locator("#recognizer-model-value")).toHaveText("fixture-model");
  await expect(card.locator("#recognizer-reasoning-value")).toHaveText("高");
  const raw = await (await page.request.get("/api/recognizer")).text();
  expect(raw).not.toContain("sk-fixture");
  expect(JSON.parse(raw)).toMatchObject({ configured: true, provider: "custom", model: "fixture-model", reasoning: "high", hasKey: true });
  await page.screenshot({ path: `${screenshots}/recognition-settings.png`, fullPage: true });

  // Reloaded, the key stays hidden; an empty key keeps it for the same service.
  await page.reload();
  await card.locator("#recognizer-edit").click();
  await expect(card.locator("#recognizer-apiKey")).toHaveValue("");
  await expect(card.locator("#recognizer-apiKey-hint")).toHaveText("已保存，留空表示不修改");
  await card.locator("#recognizer-reasoning").click();
  await page.getByRole("option", { name: "默认（由模型决定）", exact: true }).click();
  await card.locator("#recognizer-test").click();
  await expect(card.locator("#recognizer-msg")).toContainText("测试成功");
  sent = await fixtureRequests(page);
  expect(sent.at(-1)).toMatchObject({ authorization: "Bearer sk-fixture", reasoning: null });
  // A compatible service at another address is another company too: nothing is sent.
  await card.locator("#recognizer-endpoint").fill("http://127.0.0.1:9/v1");
  await expect(card.locator("#recognizer-apiKey-hint")).toHaveText("只保存在服务器，保存后不会返回");
  await card.locator("#recognizer-test").click();
  await expect(card.locator("#recognizer-apiKey-error")).toHaveText("换了接口地址，请重新填写 API Key。");
  expect(await fixtureRequests(page)).toHaveLength(sent.length);
  await card.locator("#recognizer-endpoint").fill(fixture + "/v1/");
  await expect(card.locator("#recognizer-apiKey-hint")).toHaveText("已保存，留空表示不修改");
  // Another company needs its own key.
  await card.locator("#recognizer-provider").click();
  await page.getByRole("option", { name: "Grok（xAI）", exact: true }).click();
  await card.locator("#recognizer-save").click();
  await expect(card.locator("#recognizer-apiKey-error")).toHaveText("换了识别服务，请填写这家服务的 API Key。");
  await card.locator("#recognizer-cancel").click();
  await expect(card.locator("#recognizer-reasoning-value")).toHaveText("高");

  page.once("dialog", dialog => dialog.accept());
  await card.locator("#recognizer-delete").click();
  await expect(card.locator("#recognizer-state")).toHaveText("未配置");
  expect((await (await page.request.get("/api/recognizer")).json()).configured).toBe(false);

  // A phone opens the page with the navigation folded away.
  await page.setViewportSize({ width: 320, height: 800 });
  await page.reload();
  await card.locator("#recognizer-add").click();
  await noOverflow(page);
  await page.screenshot({ path: `${screenshots}/recognition-settings-320.png`, fullPage: true });
});

// Recognition starts from an album's button: a selected album's button asks
// about every selected album after a confirmation.
function aiButton(page: Page, name: string) {
  return row(page, name).locator(".organize-ai-button");
}
async function recognizeAllSelected(page: Page, from = "misc") {
  await page.getByRole("checkbox", { name: "全选当前列表" }).check();
  await aiButton(page, from).click();
  await page.getByRole("dialog").getByRole("button", { name: "开始识别" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
}
// The organize page lists albums by name, A to Z, in the browser's collation.
async function byName(page: Page, ids: string[]) {
  const list = (await albums(page)).filter(item => ids.includes(item.id)).map(({ id, name }) => ({ id, name }));
  return page.evaluate(list => {
    const collator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
    return [...list].sort((a, b) => collator.compare(a.name, b.name) || collator.compare(a.id, b.id)).map(item => item.id);
  }, list);
}

test("recognition 02 the selection after a confirmation: only empty fields; accept, ignore and accept all", async ({ page }) => {
  await configure(page);
  // batch already has an author; its author suggestion must not show.
  await organize(page, { kind: "assign", albums: ["batch"], author: "Existing Studio" });
  await page.goto("/?view=organize");
  const total = (await albums(page)).length;
  let count = await unfilled(page);
  // No separate button any more; the hint line waits until there is something to say.
  await expect(page.locator("#recognize-start")).toHaveCount(0);
  await expect(page.locator("#organize-recognize")).toBeHidden();
  await expect(page.locator(".organize-head-ai")).toHaveAttribute("title", "识别建议");

  await page.getByRole("checkbox", { name: "全选当前列表" }).check();
  await expect(aiButton(page, "misc")).toHaveAttribute("title", `识别所选的 ${total} 本相册`);
  await aiButton(page, "misc").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("[data-slot=dialog-title]")).toHaveText(`识别所选的 ${total} 本相册？`);
  await expect(dialog.locator("[data-slot=dialog-description]")).toHaveText(`其中 ${count} 本有空着的作者或模特，会发给识别服务；1 本已填满或是根目录，跳过。`);
  await page.screenshot({ path: `${screenshots}/recognition-confirm.png`, animations: "disabled" });
  await dialog.getByRole("button", { name: "开始识别" }).click();
  // Only the fixture's three albums get answers; batch's author is already filled.
  await expect(page.locator("#recognize-status")).toHaveText(`识别 ${count} 本，给出 4 条建议，跳过 1 条。`);
  // The page says 识别 and never AI.
  expect(await page.locator("#organize").innerText()).not.toMatch(/AI/i);
  // The selection stays for a batch edit afterwards.
  await expect(page.locator("#organize-selected")).toHaveText(`已选 ${total} 本`);

  const sent = await fixtureRequests(page);
  const albumsAsked = await asked(page);
  // In the page's order, without the root album.
  expect(albumsAsked).toEqual(await byName(page, (await albums(page)).filter(item => item.id !== "." && (!item.author || !item.models.length)).map(item => item.id)));
  expect(albumsAsked).toEqual(expect.arrayContaining(["batch", "misc", "子目录"]));
  expect(sent[0].roster).toEqual({ authors: ["Existing Studio"], models: [] });

  const misc = row(page, "misc");
  const miscAuthor = misc.getByRole("group", { name: "「misc」的作者建议" });
  await expect(miscAuthor).toContainText("Fixture Studio");
  await expect(miscAuthor).toContainText("新名字");
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toContainText("Misc Model");
  const sub = row(page, "子目录");
  const subAuthor = sub.getByRole("group", { name: "「子目录」的作者建议" });
  await expect(subAuthor).toHaveText(/Existing Studio/);
  await expect(subAuthor).not.toContainText("新名字");
  await expect(sub.getByRole("group", { name: "「子目录」的模特建议" })).toHaveCount(0);
  const batch = row(page, "batch");
  await expect(batch.getByRole("group", { name: "「batch」的作者建议" })).toHaveCount(0);
  await expect(batch.getByRole("group", { name: "「batch」的模特建议" })).toContainText("Batch Girl");
  await expect(row(page, "根目录").locator(".suggestion")).toHaveCount(0);
  // Suggestions are not saved until accepted.
  expect((await album(page, "misc")).author).toBeNull();
  await page.screenshot({ path: `${screenshots}/recognition-organize.png`, fullPage: true });

  await miscAuthor.getByRole("button", { name: "接受作者建议「Fixture Studio」" }).click();
  await expect(misc.locator(".organize-status")).toHaveText("已保存");
  await expect(misc.getByRole("combobox", { name: "「misc」的作者" })).toHaveValue("Fixture Studio");
  await expect(miscAuthor).toHaveCount(0);
  await misc.getByRole("button", { name: "忽略模特建议「Misc Model」" }).click();
  await expect(misc.locator(".suggestion")).toHaveCount(0);
  expect((await album(page, "misc")).models).toEqual([]);

  await page.locator("#recognize-accept-all").click();
  await expect(page.locator("#recognize-accept-all")).toHaveCount(0);
  await expect(page.locator(".suggestion")).toHaveCount(0);
  await expect(sub.locator(".organize-status")).toHaveText("已保存");
  await expect(batch.locator(".organize-status")).toHaveText("已保存");
  const saved = Object.fromEntries((await albums(page)).map(item => [item.id, [item.author?.name ?? "", item.models.map(model => model.name).join(",")]]));
  expect(saved).toMatchObject({ misc: ["Fixture Studio", ""], "子目录": ["Existing Studio", ""], batch: ["Existing Studio", "Batch Girl"] });
  const people = await (await page.request.get("/api/people")).json() as { authors: { name: string }[]; models: { name: string }[] };
  expect(people.authors.map(person => person.name).sort()).toEqual(["Existing Studio", "Fixture Studio"]);
  // batch is full now; its button says so and cannot start anything.
  await expect(aiButton(page, "batch")).toBeDisabled();
  await expect(aiButton(page, "batch")).toHaveAttribute("title", "作者和模特都已填写");

  // With nothing selected one album's button asks about that album only;
  // a field filled by hand hides its suggestion.
  await page.locator("#organize-unselect").click();
  await page.request.post(fixture + "/reset");
  await expect(aiButton(page, "misc")).toHaveAttribute("title", "识别这本相册");
  await aiButton(page, "misc").click();
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toContainText("Misc Model");
  expect(await asked(page)).toEqual(["misc"]);
  await expect(page.locator("#recognize-status")).toHaveText("识别 1 本，给出 1 条建议，跳过 1 条。");
  const models = misc.getByRole("combobox", { name: "「misc」的模特" });
  await models.click();
  await models.fill("Hand Picked");
  await page.getByRole("option", { name: "添加「Hand Picked」" }).click();
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toHaveCount(0);
  await expect(misc.locator(".organize-status")).toHaveText("已保存");

  await expect(misc.locator(".person-chip")).toHaveText(["Hand Picked"]);
  expect((await album(page, "misc")).models.map(model => model.name)).toEqual(["Hand Picked"]);

  // On a phone the button sits beside the album name and the suggestion fits under its picker.
  await organize(page, { kind: "assign", albums: ["misc"], models: [] });
  await page.setViewportSize({ width: 320, height: 800 });
  await page.reload();
  const nameBox = (await misc.locator(".organize-album").boundingBox())!;
  const buttonBox = (await aiButton(page, "misc").boundingBox())!;
  expect(buttonBox.x).toBeGreaterThan(nameBox.x + nameBox.width - 1);
  expect(buttonBox.y).toBeLessThan(nameBox.y + nameBox.height);
  expect(buttonBox.height).toBeGreaterThanOrEqual(44);
  expect(nameBox.width).toBeGreaterThanOrEqual(64);
  await aiButton(page, "misc").click();
  await expect(page.locator("#recognize-status")).toHaveText("识别 1 本，给出 1 条建议，跳过 1 条。");
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toBeVisible();
  await noOverflow(page);
  await misc.scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${screenshots}/recognition-organize-320.png` });
});

test("recognition 03 without a service there is no recognize column and the hint leads to settings", async ({ page }) => {
  await page.goto("/?view=organize");
  await expect(page.locator(".organize-row").first()).toBeVisible();
  await expect(page.locator(".organize-ai-button")).toHaveCount(0);
  await expect(page.locator(".organize-head-ai")).toHaveCount(0);
  await expect(page.locator("#recognize-status")).toContainText("还没有配置识别服务");
  await page.locator("#recognize-status a").click();
  await expect(page).toHaveURL(/view=settings&tab=recognizer/);
  await expect(page.getByRole("tab", { name: "识别服务", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#recognizer-state")).toBeVisible();
  await expect(page.locator("#recognizer-state")).toHaveText("未配置");
});

test("recognition 04 progress while waiting, one run at a time, and a failed batch changes nothing", async ({ page }) => {
  await configure(page, { model: "fixture-slow" });
  await page.goto("/?view=organize");
  const count = await unfilled(page);
  await recognizeAllSelected(page);
  await expect(page.locator("#recognize-status")).toHaveText(`识别中 0 / ${count}`);
  // Albums waiting for their answer spin; no other recognition can start meanwhile.
  await expect(aiButton(page, "misc").locator("[data-slot=spinner]")).toBeVisible();
  await expect(aiButton(page, "misc")).toBeDisabled();
  await expect(aiButton(page, "batch")).toBeDisabled();
  // misc and batch get an author and models each, 子目录 an author.
  await expect(page.locator("#recognize-status")).toHaveText(`识别 ${count} 本，给出 5 条建议，跳过 0 条。`, { timeout: 30_000 });
  await expect(page.locator(".organize-ai-button [data-slot=spinner]")).toHaveCount(0);
  await expect(aiButton(page, "misc")).toBeEnabled();

  await configure(page, { model: "fixture-bad", apiKey: "" });
  await page.reload();
  await recognizeAllSelected(page);
  await expect(page.locator("#recognize-failures")).toContainText(`第 1 批（${Math.min(count, 20)} 本）：识别服务的回答不是要求的 JSON 格式`);
  await expect(page.locator("#recognize-status")).toHaveText(`识别 0 本，给出 0 条建议，跳过 0 条，${count} 本失败。`);
  await expect(page.locator(".suggestion")).toHaveCount(0);
  for (const item of await albums(page)) expect([item.author, item.models.length]).toEqual([null, 0]);
});

test("recognition 05 which albums a button covers, cancelling, and the 100-album note", async ({ page }) => {
  await configure(page);
  await organize(page, { kind: "assign", albums: ["子目录"], author: "Full Studio", models: ["Full Model"] });
  await page.goto("/?view=organize");
  // Filled and root albums cannot be recognized and say why.
  await expect(aiButton(page, "子目录")).toBeDisabled();
  await expect(aiButton(page, "子目录")).toHaveAttribute("title", "作者和模特都已填写");
  await expect(aiButton(page, "根目录")).toBeDisabled();
  await expect(aiButton(page, "根目录")).toHaveAttribute("title", "根目录相册不识别");

  // Nothing selected: that album only, no confirmation.
  await aiButton(page, "batch").click();
  await expect(page.locator("#recognize-status")).toHaveText("识别 1 本，给出 2 条建议，跳过 0 条。");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(await asked(page)).toEqual(["batch"]);
  // One album selected: its own button asks about it directly.
  await row(page, "misc").getByRole("checkbox").check();
  await expect(aiButton(page, "misc")).toHaveAttribute("title", "识别这本相册");
  await aiButton(page, "misc").click();
  await expect(page.locator("#recognize-status")).toHaveText("识别 1 本，给出 2 条建议，跳过 0 条。");
  expect(await asked(page)).toEqual(["batch", "misc"]);
  // Two selected: an album outside the selection still asks about itself only.
  await row(page, "根目录").getByRole("checkbox").check();
  await expect(page.locator("#organize-selected")).toHaveText("已选 2 本");
  await expect(aiButton(page, "batch")).toHaveAttribute("title", "识别这本相册");
  await aiButton(page, "batch").click();
  await expect(page.locator("#recognize-status")).toHaveText("识别 1 本，给出 2 条建议，跳过 0 条。");
  expect(await asked(page)).toEqual(["batch", "misc", "batch"]);
  await expect(page.locator("#organize-selected")).toHaveText("已选 2 本");
  // A selected album's button asks first; cancelling sends nothing.
  await aiButton(page, "misc").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("[data-slot=dialog-title]")).toHaveText("识别所选的 2 本相册？");
  await expect(dialog.locator("[data-slot=dialog-description]")).toHaveText("其中 1 本有空着的作者或模特，会发给识别服务；1 本已填满或是根目录，跳过。");
  await dialog.getByRole("button", { name: "取消" }).click();
  await expect(dialog).toHaveCount(0);
  expect(await asked(page)).toEqual(["batch", "misc", "batch"]);

  // Grid view: the same button on the cover's top-right corner, the suggestion under its picker.
  await page.locator("#organize-unselect").click();
  await page.locator("#organize-view-grid").click();
  const cover = (await row(page, "batch").locator(".organize-cover").boundingBox())!;
  const corner = (await aiButton(page, "batch").boundingBox())!;
  expect(cover.x + cover.width - (corner.x + corner.width)).toBeLessThan(12);
  expect(corner.y - cover.y).toBeLessThan(12);
  expect(corner.height).toBeGreaterThanOrEqual(44);
  await aiButton(page, "batch").click();
  await expect.poll(() => asked(page)).toEqual(["batch", "misc", "batch", "batch"]);
  await row(page, "batch").getByRole("button", { name: "接受作者建议「Someone Else」" }).click();
  await expect(row(page, "batch").locator(".organize-status")).toHaveText("已保存");
  expect((await album(page, "batch")).author?.name).toBe("Someone Else");
  await page.screenshot({ path: `${screenshots}/recognition-grid.png`, fullPage: true });
  await page.locator("#organize-view-list").click();

  // More than 100 selected albums: only the first 100 go (a made-up album list).
  await page.route("**/api/albums", async route => {
    const real = await (await route.fetch()).json();
    const base = real.albums.find((item: ApiAlbum) => item.id === "misc");
    real.albums = Array.from({ length: 105 }, (_, i) => ({ ...base, id: `many/${String(i).padStart(3, "0")}`, name: `many ${String(i).padStart(3, "0")}`, author: null, models: [] }));
    real.total = real.albums.length;
    await route.fulfill({ json: real });
  });
  await page.reload();
  await expect(page.locator("#organize-count")).toHaveText("共 105 本");
  await page.getByRole("checkbox", { name: "全选当前列表" }).check();
  await aiButton(page, "many 000").click();
  await expect(dialog.locator("[data-slot=dialog-title]")).toHaveText("识别所选的 105 本相册？");
  await expect(dialog.locator("[data-slot=dialog-description]")).toHaveText("这 105 本都有空着的作者或模特，会发给识别服务。每次最多 100 本，按当前顺序识别前 100 本。");
  await dialog.getByRole("button", { name: "取消" }).click();
  expect(await asked(page)).toEqual(["batch", "misc", "batch", "batch"]);
  await page.unroute("**/api/albums");
});
