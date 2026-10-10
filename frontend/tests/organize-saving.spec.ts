import { test, expect, type Page, type Route } from "@playwright/test";
import { mkdir } from "node:fs/promises";

type Wire = { operationId: string; kind: string; albums?: string[]; author?: string; models?: string[]; name?: string };
const defer = () => {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
};
const row = (page: Page, name: string) => page.locator(".organize-row").filter({ has: page.locator(".organize-album > strong", { hasText: name }) });
const modelBox = (page: Page) => row(page, "misc").getByRole("combobox", { name: "「misc」的模特" });
async function pick(page: Page, label: string, name: string) {
  const box = page.getByRole("combobox", { name: label, exact: true });
  await box.fill(name); await box.press("Enter");
}
async function actualModels(page: Page) {
  const { albums } = await (await page.request.get("/api/albums")).json();
  return albums.find((album: { id: string }) => album.id === "misc").models.map((person: { name: string }) => person.name);
}
async function reset(page: Page) {
  const { albums } = await (await page.request.get("/api/albums")).json();
  expect((await page.request.put("/api/album-people", { data: { albums: albums.map((album: { id: string }) => album.id), author: "", models: [] } })).status()).toBe(200);
  const people = await (await page.request.get("/api/people")).json();
  for (const person of [...people.authors, ...people.models]) expect((await page.request.delete("/api/people/" + person.id)).status()).toBe(200);
}
async function holdAnswers(page: Page) {
  const held: { wire: Wire; release: () => void; status: number }[] = [];
  await page.route("**/api/organize", async (route: Route) => {
    const response = await route.fetch(); // Real Go/SQLite write; delay only the answer.
    const completion = defer();
    held.push({ wire: route.request().postDataJSON(), release: completion.release, status: response.status() });
    await completion.promise;
    await route.fulfill({ response });
  });
  return held;
}
test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  (page as Page & { saveErrors: string[] }).saveErrors = errors;
  await mkdir("../output", { recursive: true });
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => !(await (await page.request.get("/api/health")).json()).status.scanning, { timeout: 60_000 }).toBe(true);
  await reset(page);
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { saveErrors: string[] }).saveErrors).toEqual([]);
  await reset(page);
});

test("saving 01 real delayed responses retain A/B/C and never show saved early", async ({ page, browserName }) => {
  const held = await holdAnswers(page);
  await page.goto("/?view=organize");
  await pick(page, "「misc」的模特", "A");
  await expect.poll(() => held.length).toBe(1);
  await pick(page, "「misc」的模特", "B");
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A", "B"]);
  held[0].release();
  await expect.poll(() => held.length).toBe(2);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A", "B"]);
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("保存中");
  await pick(page, "「misc」的模特", "C");
  held[1].release();
  await expect.poll(() => held.length).toBe(3);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A", "B", "C"]);
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("保存中");
  await page.screenshot({ path: `../output/organize-saving-latest-${browserName}.png`, fullPage: true });
  held[2].release();
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("已保存");
  expect(held.map(item => item.wire.models)).toEqual([["A"], ["A", "B"], ["A", "B", "C"]]);
  expect(await actualModels(page)).toEqual(["A", "B", "C"]);
});

test("saving 02 batch, row and draft-name rename share order and immediate preview", async ({ page }) => {
  const held = await holdAnswers(page);
  await page.goto("/?view=organize");
  await row(page, "misc").getByRole("checkbox").check();
  await row(page, "batch").getByRole("checkbox").check();
  await pick(page, "批量设置作者", "Studio A");
  await page.locator("#organize-apply").click();
  await expect.poll(() => held.length).toBe(1);
  await pick(page, "「misc」的作者", "Studio B");
  await page.locator("#people-toggle").click();
  await page.getByRole("button", { name: "改名「Studio A」" }).click();
  await page.getByRole("textbox", { name: "「Studio A」的新名字" }).fill("Studio C");
  // Confirming the newly created name while its editor is open must not
  // discard the text merely because its temporary ID becomes a saved ID.
  held[0].release(); await expect.poll(() => held.length).toBe(2);
  await expect(page.getByRole("textbox", { name: "「Studio A」的新名字" })).toHaveValue("Studio C");
  await page.getByRole("button", { name: "保存新名字" }).click();
  await expect(row(page, "misc").getByRole("combobox", { name: "「misc」的作者" })).toHaveValue("Studio B");
  await expect(row(page, "batch").getByRole("combobox", { name: "「batch」的作者" })).toHaveValue("Studio C");
  expect(held.length).toBe(2);
  held[1].release(); await expect.poll(() => held.length).toBe(3);
  expect(held.map(item => item.wire.kind)).toEqual(["assign", "assign", "rename"]);
  expect(held[2].status).toBe(200);
  held[2].release();
  await expect(page.locator("#people-message")).toHaveText("已改名为「Studio C」。");
  const { albums } = await (await page.request.get("/api/albums")).json();
  expect(albums.find((album: { id: string }) => album.id === "misc").author.name).toBe("Studio B");
  expect(albums.find((album: { id: string }) => album.id === "batch").author.name).toBe("Studio C");
});

test("saving 03 a committed write with a failed answer is recovered from its SQLite receipt", async ({ page, browserName }) => {
  const writes: Wire[] = [];
  const confirmed = defer();
  let checking = false;
  await page.route("**/api/organize", async route => {
    writes.push(route.request().postDataJSON());
    if (writes.length === 1) { await route.fetch(); await route.fulfill({ status: 503, json: { error: "fixture lost answer" } }); }
    else await route.continue();
  });
  await page.route("**/api/organize/receipts/*", async route => {
    const response = await route.fetch(); checking = true;
    await confirmed.promise; await route.fulfill({ response });
  });
  await page.goto("/?view=organize");
  await pick(page, "「misc」的模特", "A");
  await expect.poll(() => checking).toBe(true);
  await expect(page.locator("#organize-confirmation")).toBeVisible();
  await pick(page, "「misc」的模特", "B");
  expect(writes.length).toBe(1);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A", "B"]);
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("待确认");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `../output/organize-saving-confirm-${browserName}.png`, fullPage: true });
  confirmed.release();
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("已保存");
  await expect(page.locator("#organize-confirmation")).toHaveCount(0);
  expect(writes.length).toBe(2);
  expect(await actualModels(page)).toEqual(["A", "B"]);
  expect((await (await page.request.get("/api/organize/receipts/" + writes[0].operationId)).json()).state).toBe("committed");
});

test("saving 04 unsent request is retried with the same ID and content before newer edits", async ({ page }) => {
  const writes: Wire[] = [];
  await page.route("**/api/organize", async route => {
    writes.push(route.request().postDataJSON());
    if (writes.length === 1) await route.abort("connectionfailed");
    else await route.continue();
  });
  await page.goto("/?view=organize");
  await pick(page, "「misc」的模特", "A");
  await expect(page.locator("#organize-retry")).toBeVisible();
  await pick(page, "「misc」的模特", "B");
  expect(writes.length).toBe(1);
  await page.locator("#organize-retry").click();
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("已保存");
  expect(writes.length).toBe(3);
  expect(writes[1]).toEqual(writes[0]);
  expect(writes[2].models).toEqual(["A", "B"]);
  expect(await actualModels(page)).toEqual(["A", "B"]);
  // A duplicate of the earlier assignment answers its receipt and does not
  // overwrite B in the actual, newer album data.
  expect((await page.request.post("/api/organize", { data: writes[0] })).status()).toBe(200);
  expect(await actualModels(page)).toEqual(["A", "B"]);
});

test("saving 05 explicit refusal restores the invalid item while a newer model remains", async ({ page }) => {
  expect((await page.request.put("/api/album-people", { data: { albums: ["misc"], models: ["A"] } })).status()).toBe(200);
  const held = await holdAnswers(page);
  await page.goto("/?view=organize");
  await pick(page, "「misc」的模特", "名".repeat(41));
  await expect.poll(() => held.length).toBe(1);
  expect(held[0].status).toBe(400);
  await pick(page, "「misc」的模特", "C");
  held[0].release(); await expect.poll(() => held.length).toBe(2);
  expect(held[1].wire.models).toEqual(["A", "C"]);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A", "C"]);
  await expect(page.locator("#organize-rejection")).toContainText("名字最多 40 个字");
  held[1].release();
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("已保存");
  expect(await actualModels(page)).toEqual(["A", "C"]);
});

test("saving 06 pending edits guard page changes, tab close and logout; saved edits stop prompting", async ({ page }) => {
  const held = await holdAnswers(page);
  await page.goto("/?view=organize");
  await pick(page, "「misc」的模特", "A");
  await expect.poll(() => held.length).toBe(1);
  for (const selector of ["#nav-albums", "#nav-logout"]) {
    const dialogPromise = page.waitForEvent("dialog");
    const clicked = page.locator(selector).click({ noWaitAfter: true });
    const dialog = await dialogPromise;
    expect(dialog.type()).toBe("beforeunload");
    await dialog.dismiss(); await clicked;
    expect(page.url()).toContain("view=organize");
    await expect(page.locator("#nav-logout")).toBeEnabled();
    expect((await page.request.get("/api/people")).status()).toBe(200);
  }
  const closeDialog = page.waitForEvent("dialog");
  await page.close({ runBeforeUnload: true });
  const dialog = await closeDialog;
  expect(dialog.type()).toBe("beforeunload"); await dialog.dismiss();
  expect(page.isClosed()).toBe(false);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["A"]);
  held[0].release();
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("已保存");
  await page.locator("#nav-albums").click();
  await expect(page.locator("#page-title")).toHaveText("相册");
});
