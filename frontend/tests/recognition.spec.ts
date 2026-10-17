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
  await page.goto("/?view=settings");
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

test("recognition 02 organize page suggests only empty fields; accept, ignore and accept all", async ({ page }) => {
  await configure(page);
  // batch already has an author; its author suggestion must not show.
  await organize(page, { kind: "assign", albums: ["batch"], author: "Existing Studio" });
  await page.goto("/?view=organize");
  const start = page.locator("#recognize-start");
  let count = await unfilled(page);
  await expect(start).toHaveText(`识别未填写的相册（${count} 本）`);
  await start.click();
  // Only the fixture's three albums get answers; batch's author is already filled.
  await expect(page.locator("#recognize-status")).toHaveText(`识别 ${count} 本，给出 4 条建议，跳过 1 条。`);

  const sent = await fixtureRequests(page);
  const albumsAsked = await asked(page);
  expect(albumsAsked).toHaveLength(count);
  expect(albumsAsked).toEqual(expect.arrayContaining(["batch", "misc", "子目录"]));
  expect(albumsAsked).not.toContain(".");
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

  // A field filled by hand hides its suggestion.
  count = await unfilled(page);
  await expect(start).toHaveText(`识别未填写的相册（${count} 本）`);
  await start.click();
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toContainText("Misc Model");
  const models = misc.getByRole("combobox", { name: "「misc」的模特" });
  await models.click();
  await models.fill("Hand Picked");
  await page.getByRole("option", { name: "添加「Hand Picked」" }).click();
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toHaveCount(0);
  await expect(misc.locator(".organize-status")).toHaveText("已保存");

  await expect(misc.locator(".person-chip")).toHaveText(["Hand Picked"]);
  expect((await album(page, "misc")).models.map(model => model.name)).toEqual(["Hand Picked"]);

  // On a phone the suggestion fits under its picker.
  await organize(page, { kind: "assign", albums: ["misc"], models: [] });
  await page.setViewportSize({ width: 320, height: 800 });
  await page.reload();
  count = await unfilled(page);
  await page.locator("#recognize-start").click();
  await expect(page.locator("#recognize-status")).toHaveText(`识别 ${count} 本，给出 1 条建议，跳过 2 条。`);
  await expect(misc.getByRole("group", { name: "「misc」的模特建议" })).toBeVisible();
  await noOverflow(page);
  await misc.scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${screenshots}/recognition-organize-320.png` });
});

test("recognition 03 without a service the button waits for settings", async ({ page }) => {
  await page.goto("/?view=organize");
  await expect(page.locator("#recognize-start")).toBeDisabled();
  await expect(page.locator("#recognize-status")).toContainText("还没有配置识别服务");
  await page.locator("#recognize-status a").click();
  await expect(page).toHaveURL(/view=settings/);
  await expect(page.locator("#recognizer-state")).toHaveText("未配置");
});

test("recognition 04 progress while waiting, and a failed batch changes nothing", async ({ page }) => {
  await configure(page, { model: "fixture-slow" });
  await page.goto("/?view=organize");
  const count = await unfilled(page);
  await page.locator("#recognize-start").click();
  await expect(page.locator("#recognize-status")).toHaveText(`识别中 0 / ${count}`);
  await expect(page.locator("#recognize-start")).toBeDisabled();
  // misc and batch get an author and models each, 子目录 an author.
  await expect(page.locator("#recognize-status")).toHaveText(`识别 ${count} 本，给出 5 条建议，跳过 0 条。`, { timeout: 30_000 });

  await configure(page, { model: "fixture-bad", apiKey: "" });
  await page.reload();
  await page.locator("#recognize-start").click();
  await expect(page.locator("#recognize-failures")).toContainText(`第 1 批（${Math.min(count, 20)} 本）：识别服务的回答不是要求的 JSON 格式`);
  await expect(page.locator("#recognize-status")).toHaveText(`识别 0 本，给出 0 条建议，跳过 0 条，${count} 本失败。`);
  await expect(page.locator(".suggestion")).toHaveCount(0);
  for (const item of await albums(page)) expect([item.author, item.models.length]).toEqual([null, 0]);
});
