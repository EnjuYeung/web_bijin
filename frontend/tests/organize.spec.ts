import { test, expect, type Locator, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

const screenshots = "../output";
interface ApiAlbum { id: string; name: string; added: number; author: { name: string } | null; models: { name: string }[] }
interface ApiPerson { id: number; name: string; albums: number }

async function signIn(page: Page) {
  const response = await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } });
  expect(response.status()).toBe(200);
}
async function waitForScan(page: Page) {
  await expect.poll(async () => {
    const data = await (await page.request.get("/api/health")).json();
    return !data.status.scanning && !data.status.queued;
  }, { timeout: 60_000 }).toBe(true);
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}
async function albums(page: Page): Promise<ApiAlbum[]> {
  return (await (await page.request.get("/api/albums")).json()).albums;
}
async function setPeople(page: Page, albumIds: string[], data: { author?: string; models?: string[] }) {
  const response = await page.request.put("/api/album-people", { data: { albums: albumIds, ...data } });
  expect(response.status(), await response.text()).toBe(200);
}
// Every test starts and ends with no names and no album people.
async function resetPeople(page: Page) {
  await setPeople(page, (await albums(page)).map(album => album.id), { author: "", models: [] });
  const people = await (await page.request.get("/api/people")).json() as { authors: ApiPerson[]; models: ApiPerson[] };
  for (const person of [...people.authors, ...people.models]) expect((await page.request.delete("/api/people/" + person.id)).status()).toBe(200);
}
function row(page: Page, name: string) {
  return page.locator(".organize-row").filter({ has: page.locator(".organize-album > strong", { hasText: name }) });
}
function saved(page: Page) {
  return page.waitForResponse(response => new URL(response.url()).pathname === "/api/album-people" && response.request().method() === "PUT");
}
async function choose(page: Page, trigger: Locator, option: string) {
  await trigger.click();
  await page.getByRole("option", { name: option, exact: true }).click();
}
async function rowNames(page: Page) {
  return (await page.locator(".organize-row .organize-album > strong").allTextContents()).sort();
}
// The chosen option shows its check mark on the same line as its name.
async function checkOnNameLine(page: Page) {
  const chosen = page.locator(".person-option[data-selected]").first();
  await expect(chosen).toBeVisible();
  const check = (await chosen.locator(".person-check").boundingBox())!;
  const name = (await chosen.locator(".person-option-name").boundingBox())!;
  expect(Math.abs(check.y + check.height / 2 - (name.y + name.height / 2))).toBeLessThan(4);
  expect(check.x + check.width).toBeLessThanOrEqual(name.x);
  expect(name.height).toBeLessThan(30);
}
// Filters keep their list open for more picks; close it before the next control.
async function filterBy(page: Page, box: Locator, option: RegExp) {
  await box.click();
  await page.getByRole("option", { name: option }).click();
  await page.keyboard.press("Escape");
}
async function pick(page: Page, box: Locator, text: string, option: string | RegExp) {
  await box.click();
  await box.fill(text);
  await page.getByRole("option", { name: option }).click();
}

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await mkdir(screenshots, { recursive: true });
  await signIn(page);
  await waitForScan(page);
  await resetPeople(page);
  (page as Page & { uiErrors: string[] }).uiErrors = errors;
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { uiErrors: string[] }).uiErrors).toEqual([]);
  await resetPeople(page);
});

test("organize 01 new names, reuse from the list, persistence and album covers", async ({ page }) => {
  await page.goto("/?view=organize");
  await expect(page.locator("#page-title")).toHaveText("整理");
  await expect(page.locator("#nav-organize")).toHaveAttribute("aria-current", "page");
  await expect(page.locator(".organize-row")).toHaveCount(4);
  await expect(page.locator("#organize-count")).toHaveText("共 4 本");

  // A new author: typed, then picked from 「添加」.
  const sub = row(page, "子目录");
  let save = saved(page);
  await pick(page, sub.getByRole("combobox", { name: "「子目录」的作者" }), "LEEHEE EXPRESS", "添加「LEEHEE EXPRESS」");
  expect((await save).status()).toBe(200);
  await expect(sub.locator(".organize-status")).toHaveText("已保存");
  // Two new models, the second added with Enter.
  const subModels = sub.getByRole("combobox", { name: "「子目录」的模特" });
  save = saved(page);
  await pick(page, subModels, "G.su", "添加「G.su」");
  expect((await save).status()).toBe(200);
  save = saved(page);
  await subModels.fill("Min.E (민이)");
  await page.keyboard.press("Enter");
  expect((await save).status()).toBe(200);
  await expect(sub.locator(".person-chip")).toHaveText(["G.su", "Min.E (민이)"]);
  await expect(subModels).toHaveValue("");

  // The next album picks the same names from the list, without retyping them.
  const misc = row(page, "misc");
  await misc.getByRole("combobox", { name: "「misc」的作者" }).click();
  await expect(page.getByRole("option", { name: /^LEEHEE EXPRESS/ })).toBeVisible();
  save = saved(page);
  await page.getByRole("option", { name: /^LEEHEE EXPRESS/ }).click();
  expect((await save).status()).toBe(200);
  // A different letter case finds the existing model instead of adding one.
  save = saved(page);
  await misc.getByRole("combobox", { name: "「misc」的模特" }).fill("g.su");
  await expect(page.getByRole("option", { name: /^添加/ })).toHaveCount(0);
  await page.getByRole("option", { name: /^G\.su/ }).click();
  expect((await save).status()).toBe(200);

  // Saved on the server: a reload shows the same, and there are only three names.
  await page.reload();
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["G.su", "Min.E (민이)"]);
  await expect(row(page, "子目录").getByRole("combobox", { name: "「子目录」的作者" })).toHaveValue("LEEHEE EXPRESS");
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["G.su"]);
  const people = await (await page.request.get("/api/people")).json() as { authors: ApiPerson[]; models: ApiPerson[] };
  expect(people.authors.map(person => [person.name, person.albums])).toEqual([["LEEHEE EXPRESS", 2]]);
  expect(people.models.map(person => [person.name, person.albums])).toEqual([["G.su", 2], ["Min.E (민이)", 1]]);
  await expect(page.locator("#people-content")).toHaveCount(0);
  await expect(page.locator("#people-toggle")).toHaveText("展开名单");
  await expect(page.locator("#people-card")).toContainText("作者 1 位，模特 2 位");
  await page.locator("#people-toggle").click();
  await expect(page.locator("#people-toggle")).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("#people-card .people-name")).toHaveText(["LEEHEE EXPRESS", "G.su", "Min.E (민이)"]);

  // Clearing the author with its X button saves an empty author.
  save = saved(page);
  await row(page, "misc").getByRole("button", { name: "清除「misc」的作者" }).click();
  expect((await save).status()).toBe(200);
  await expect(row(page, "misc").getByRole("combobox", { name: "「misc」的作者" })).toHaveValue("");

  // Album covers show the people; the accessible name keeps its old beginning.
  await page.locator("#nav-albums").click();
  await expect(page.getByRole("link", { name: "子目录，1 张照片，LEEHEE EXPRESS · G.su、Min.E (민이)" })).toBeVisible();
  await expect(page.locator(".album-card", { hasText: "子目录" }).locator(".album-people")).toHaveText("LEEHEE EXPRESS · G.su、Min.E (민이)");
  await expect(page.locator(".album-card", { hasText: "misc" }).locator(".album-people")).toHaveText("G.su");
  await expect(page.locator(".album-card", { hasText: "batch" }).locator(".album-people")).toHaveCount(0);
  await page.screenshot({ path: screenshots + "/organize-albums-desktop.png" });
});

test("organize 02 filters by fill status, authors and models, then batch setting", async ({ page }) => {
  await setPeople(page, ["misc"], { author: "Pure Media" });
  await setPeople(page, ["batch"], { models: ["Woo"] });
  await setPeople(page, ["子目录"], { author: "LEEHEE EXPRESS", models: ["G.su", "Min.E"] });
  await page.goto("/?view=organize");
  const fill = page.locator("#organize-fill");
  const authors = page.getByRole("combobox", { name: "按作者筛选" });
  const models = page.getByRole("combobox", { name: "按模特筛选" });
  expect(await fill.evaluate(element => element.tagName)).toBe("BUTTON");

  await page.locator("#organize-filter").fill("目录");
  await expect.poll(() => rowNames(page)).toEqual(["子目录", "根目录"].sort());
  await expect(page.locator("#organize-count")).toHaveText("显示 2 / 4 本");
  await page.locator("#organize-filter").fill("");
  // Fill status: no author, no model, neither.
  await choose(page, fill, "没填作者");
  await expect.poll(() => rowNames(page)).toEqual(["batch", "根目录"].sort());
  await choose(page, fill, "没填模特");
  await expect.poll(() => rowNames(page)).toEqual(["misc", "根目录"].sort());
  await choose(page, fill, "作者和模特都没填");
  await expect.poll(() => rowNames(page)).toEqual(["根目录"]);
  await choose(page, fill, "全部相册");
  await expect(page.locator("#organize-count")).toHaveText("共 4 本");

  // One author, then a second one: either matches.
  await filterBy(page, authors, /^Pure Media/);
  await expect.poll(() => rowNames(page)).toEqual(["misc"]);
  await filterBy(page, authors, /^LEEHEE EXPRESS/);
  await expect.poll(() => rowNames(page)).toEqual(["misc", "子目录"].sort());
  // Filters only offer names in use; nothing new can be typed in.
  await authors.fill("Nobody");
  await expect(page.getByText("没有匹配的名字")).toBeVisible();
  await expect(page.getByRole("option")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await page.locator("#organize-clear").click();
  await expect(page.locator(".organize-row")).toHaveCount(4);
  await expect(page.locator(".organize-filters .person-chip")).toHaveCount(0);

  // Several models, and authors and models together.
  await filterBy(page, models, /^G\.su/);
  await expect.poll(() => rowNames(page)).toEqual(["子目录"]);
  await filterBy(page, models, /^Woo/);
  await expect.poll(() => rowNames(page)).toEqual(["batch", "子目录"].sort());
  await filterBy(page, authors, /^LEEHEE EXPRESS/);
  await expect.poll(() => rowNames(page)).toEqual(["子目录"]);
  await page.locator("#organize-clear").click();

  // Batch: only the albums without an author get one; their models stay.
  await choose(page, fill, "没填作者");
  await page.getByRole("checkbox", { name: "全选当前列表" }).click();
  await expect(page.locator("#organize-batch")).toContainText("已选 2 本相册");
  await page.locator("#organize-apply").click();
  await expect(page.locator("#organize-batch-message")).toHaveText("先选择作者或模特。");
  await pick(page, page.getByRole("combobox", { name: "批量设置作者" }), "Fantasy Factory", "添加「Fantasy Factory」");
  const save = saved(page);
  await page.locator("#organize-apply").click();
  expect((await save).status()).toBe(200);
  await expect(page.locator("#organize-batch-message")).toHaveText("已设置 2 本相册。");
  // The rows stay listed while the filter is on, now with an author.
  await expect.poll(() => rowNames(page)).toEqual(["batch", "根目录"].sort());
  for (const name of ["batch", "根目录"]) await expect(row(page, name).getByRole("combobox", { name: `「${name}」的作者` })).toHaveValue("Fantasy Factory");
  const data = await albums(page);
  const byId = (id: string) => data.find(album => album.id === id)!;
  expect([byId("batch").author?.name, byId(".").author?.name, byId("misc").author?.name, byId("子目录").author?.name]).toEqual(["Fantasy Factory", "Fantasy Factory", "Pure Media", "LEEHEE EXPRESS"]);
  expect(byId("batch").models.map(model => model.name)).toEqual(["Woo"]);
  // Choosing the status again finds no album without an author.
  await choose(page, fill, "全部相册");
  await choose(page, fill, "没填作者");
  await expect(page.locator(".organize-row")).toHaveCount(0);
  await expect(page.getByText("没有符合条件的相册。")).toBeVisible();
});

test("organize 03 rename, merge and delete names; bad input stays in the row", async ({ page }) => {
  await setPeople(page, ["子目录"], { models: ["Gsu", "Nara"] });
  await setPeople(page, ["misc"], { models: ["G.su"] });
  await page.goto("/?view=organize");
  await page.locator("#people-toggle").click();
  const card = page.locator("#people-card");
  await card.getByRole("button", { name: "改名「Nara」" }).click();
  await card.getByRole("textbox", { name: "「Nara」的新名字" }).fill("NARA");
  await card.getByRole("button", { name: "保存新名字" }).click();
  await expect(page.locator("#people-message")).toHaveText("已改名为「NARA」。");
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["Gsu", "NARA"]);

  // Renaming to a name already on the list asks first, then merges.
  page.once("dialog", dialog => { expect(dialog.message()).toContain("已经在名单里"); void dialog.accept(); });
  await card.getByRole("button", { name: "改名「Gsu」" }).click();
  await card.getByRole("textbox", { name: "「Gsu」的新名字" }).fill("g.su");
  await card.getByRole("button", { name: "保存新名字" }).click();
  await expect(page.locator("#people-message")).toHaveText("已合并为「g.su」。");
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["g.su", "NARA"]);
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["g.su"]);
  await expect(card.locator(".people-list li")).toHaveCount(2);

  // Cancelling a delete keeps the name; confirming removes it everywhere.
  page.once("dialog", dialog => void dialog.dismiss());
  await card.getByRole("button", { name: "删除「NARA」" }).click();
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["g.su", "NARA"]);
  page.once("dialog", dialog => { expect(dialog.message()).toContain("照片不受影响"); void dialog.accept(); });
  await card.getByRole("button", { name: "删除「NARA」" }).click();
  await expect(page.locator("#people-message")).toHaveText("已删除「NARA」。");
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["g.su"]);

  // An empty name is refused before sending; Escape leaves the editor.
  await card.getByRole("button", { name: "改名「g.su」" }).click();
  await card.getByRole("textbox", { name: "「g.su」的新名字" }).fill("   ");
  await card.getByRole("button", { name: "保存新名字" }).click();
  await expect(page.locator("#people-message")).toHaveText("名字不能为空。");
  await card.getByRole("textbox", { name: "「g.su」的新名字" }).press("Escape");
  await expect(card.getByRole("textbox")).toHaveCount(0);

  // A name the server refuses shows its reason in the row and changes nothing.
  const save = saved(page);
  const models = row(page, "misc").getByRole("combobox", { name: "「misc」的模特" });
  await models.fill("名".repeat(41));
  await page.keyboard.press("Enter");
  expect((await save).status()).toBe(400);
  await expect(row(page, "misc").locator(".organize-status")).toHaveText("名字最多 40 个字");
  await expect(row(page, "misc").locator(".person-chip")).toHaveText(["g.su"]);
  expect((await albums(page)).find(album => album.id === "misc")!.models.map(model => model.name)).toEqual(["g.su"]);
});

test("organize 04 album sorting by author, model and date, unfilled last, remembered", async ({ page }) => {
  await setPeople(page, ["."], { author: "Zeta Studio", models: ["Ann"] });
  await setPeople(page, ["子目录"], { author: "alpha", models: ["Zoe", "Ann"] });
  await setPeople(page, ["misc"], { models: ["Mia"] });
  await setPeople(page, ["batch"], { author: "Beta" });
  await page.goto("/?view=albums");
  const names = () => page.locator(".album-card strong").allTextContents();
  const collated = (list: { id: string; name: string; added: number }[], newest: boolean) => page.evaluate(({ list, newest }) => {
    const collator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
    const byName = (a: { id: string; name: string }, b: { id: string; name: string }) => collator.compare(a.name, b.name) || collator.compare(a.id, b.id);
    return [...list].sort((a, b) => newest ? (b.added - a.added) || byName(a, b) : byName(a, b)).map(album => album.name);
  }, { list, newest });
  const data = (await albums(page)).map(({ id, name, added }) => ({ id, name, added }));

  const sortBy = (label: string) => choose(page, page.locator("#album-sort"), label);
  expect(await page.locator("#album-sort").evaluate(element => element.tagName)).toBe("BUTTON");
  await expect(page.locator("#album-sort")).toHaveText("名称");
  await expect.poll(names).toEqual(await collated(data, false));
  await sortBy("作者");
  await expect.poll(names).toEqual(["子目录", "batch", "根目录", "misc"]);
  await page.locator("#album-sort-direction").click();
  await expect.poll(names).toEqual(["根目录", "batch", "子目录", "misc"]);
  await sortBy("模特");
  await expect.poll(names).toEqual(["根目录", "misc", "子目录", "batch"]);
  await page.locator("#album-sort-direction").click();
  await expect.poll(names).toEqual(["子目录", "misc", "根目录", "batch"]);
  await sortBy("添加日期");
  await expect(page.locator("#album-sort-direction")).toHaveAttribute("aria-label", "顺序：从新到旧，点击改为从旧到新");
  await expect.poll(names).toEqual(await collated(data, true));

  // The choice is remembered after a reload.
  await sortBy("模特");
  await page.locator("#album-sort-direction").click();
  await page.reload();
  await expect(page.locator("#album-sort")).toHaveText("模特");
  await expect.poll(names).toEqual(["子目录", "misc", "根目录", "batch"]);
});

test("organize 05 keyboard use, phones to desktops and both themes", async ({ page }) => {
  await setPeople(page, ["子目录"], { author: "LEEHEE EXPRESS", models: ["G.su", "Min.E (민이)", "Jdabyeol (정다별이)"] });
  await page.goto("/?view=organize");
  // Keyboard only: type part of a name, take the highlighted match with Enter.
  const author = row(page, "misc").getByRole("combobox", { name: "「misc」的作者" });
  await author.focus();
  await page.keyboard.type("LEE");
  await expect(page.getByRole("option", { name: /^LEEHEE EXPRESS/ })).toBeVisible();
  let save = saved(page);
  await page.keyboard.press("Enter");
  expect((await save).status()).toBe(200);
  await expect(author).toHaveValue("LEEHEE EXPRESS");
  // Escape closes the list without saving anything.
  const models = row(page, "misc").getByRole("combobox", { name: "「misc」的模特" });
  await models.focus();
  await page.keyboard.type("Ne");
  await expect(page.getByRole("option", { name: "添加「Ne」" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("option")).toHaveCount(0);
  // Left arrow from the empty model box reaches the last model; Backspace removes it.
  save = saved(page);
  const subModels = row(page, "子目录").getByRole("combobox", { name: "「子目录」的模特" });
  await subModels.focus();
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.press("Backspace");
  expect((await save).status()).toBe(200);
  await expect(row(page, "子目录").locator(".person-chip")).toHaveText(["G.su", "Min.E (민이)"]);

  for (const mode of ["night", "day"] as const) {
    await page.addInitScript(value => localStorage.setItem("juens-theme", value), mode);
    for (const [width, height] of [[320, 900], [375, 900], [414, 900], [768, 900], [1024, 900], [1280, 800], [1440, 900], [1470, 956]]) {
      await page.setViewportSize({ width, height });
      await page.goto("/?view=organize");
      await expect(page.locator(".organize-row")).toHaveCount(4);
      await expect(page.locator("html")).toHaveAttribute("data-mode", mode);
      await noOverflow(page);
      for (const selector of [".person-field", ".organize-check", "#organize-filter", "#people-card [data-slot=\"button\"]"]) {
        const box = await page.locator(selector).first().boundingBox();
        expect(box!.height, selector + " at " + width).toBeGreaterThanOrEqual(44);
      }
      // A chosen author or model keeps its check mark on its own line (13-inch MacBook widths included).
      await row(page, "子目录").getByRole("combobox", { name: "「子目录」的作者" }).click();
      await checkOnNameLine(page);
      if (width === 1470) await page.screenshot({ path: `${screenshots}/organize-author-open-${width}-${mode}.png` });
      await page.keyboard.press("Escape");
      await row(page, "子目录").getByRole("combobox", { name: "「子目录」的模特" }).click();
      await checkOnNameLine(page);
      await page.keyboard.press("Escape");
      if (width === 375 || width === 1440) await page.screenshot({ path: `${screenshots}/organize-${width}-${mode}.png`, fullPage: true });
      await page.goto("/?view=albums");
      await expect(page.locator(".album-card")).toHaveCount(4);
      await noOverflow(page);
      const sort = await page.locator("#album-sort").boundingBox();
      expect(sort!.height).toBeGreaterThanOrEqual(44);
      await page.locator("#album-sort").click();
      await checkOnNameLine(page);
      if (width === 1470 || width === 375) await page.screenshot({ path: `${screenshots}/albums-sort-open-${width}-${mode}.png` });
      await page.keyboard.press("Escape");
      if (width === 375) await page.screenshot({ path: `${screenshots}/organize-albums-${width}-${mode}.png` });
    }
  }
  // An open list on a phone stays inside the screen.
  await page.setViewportSize({ width: 320, height: 700 });
  await page.goto("/?view=organize");
  await row(page, "misc").getByRole("combobox", { name: "「misc」的模特" }).click();
  const popup = await page.locator(".person-popup").boundingBox();
  expect(popup!.x).toBeGreaterThanOrEqual(0);
  expect(popup!.x + popup!.width).toBeLessThanOrEqual(320);
  // Names keep their own column and stay on one line, selected or not.
  const options = page.locator(".person-option > span");
  await expect(options).toHaveText(["G.su", "Jdabyeol (정다별이)", "Min.E (민이)"]);
  for (const option of await options.all()) expect((await option.boundingBox())!.height).toBeLessThan(30);
  await page.screenshot({ path: `${screenshots}/organize-popup-320.png` });
});

test("organize 06 album page filters by authors and models and keeps them for the visit", async ({ page }) => {
  await setPeople(page, ["子目录"], { author: "LEEHEE EXPRESS", models: ["G.su", "Min.E"] });
  await setPeople(page, ["misc"], { author: "LEEHEE EXPRESS", models: ["Woo"] });
  await setPeople(page, ["batch"], { author: "Pure Media", models: ["G.su"] });
  await page.goto("/?view=albums");
  const cards = async () => (await page.locator(".album-card strong").allTextContents()).sort();
  const authors = page.getByRole("combobox", { name: "按作者筛选" });
  const models = page.getByRole("combobox", { name: "按模特筛选" });
  await expect(page.locator(".album-card")).toHaveCount(4);
  await expect(page.locator("#album-filter-note")).toHaveCount(0);

  await filterBy(page, authors, /^LEEHEE EXPRESS/);
  await expect.poll(cards).toEqual(["misc", "子目录"].sort());
  await expect(page.locator("#album-filter-note")).toContainText("筛选后 2 / 4 本");
  await filterBy(page, authors, /^Pure Media/);
  await expect.poll(cards).toEqual(["batch", "misc", "子目录"].sort());
  await filterBy(page, models, /^G\.su/);
  await expect.poll(cards).toEqual(["batch", "子目录"].sort());
  await page.screenshot({ path: screenshots + "/albums-filtered-desktop.png" });

  // The filter stays while an album is opened and left again.
  await page.getByRole("link", { name: /^子目录，1 张照片/ }).click();
  await expect(page.locator("#page-title")).toHaveText("子目录");
  await page.locator("#album-back").click();
  await expect.poll(cards).toEqual(["batch", "子目录"].sort());
  await expect(page.locator(".album-filters .person-chip")).toHaveText(["LEEHEE EXPRESS", "Pure Media", "G.su"]);

  // A combination without albums says so; clearing shows every album again.
  await page.getByRole("button", { name: "移除LEEHEE EXPRESS" }).click();
  await page.getByRole("button", { name: "移除G.su" }).click();
  await filterBy(page, models, /^Min\.E/);
  await expect(page.getByText("没有符合条件的相册")).toBeVisible();
  await page.getByRole("button", { name: "清除筛选" }).click();
  await expect(page.locator(".album-card")).toHaveCount(4);
  await expect(page.locator("#album-filter-note")).toHaveCount(0);

  // A new tab starts without a filter.
  await filterBy(page, authors, /^Pure Media/);
  const fresh = await page.context().newPage();
  await fresh.goto("/?view=albums");
  await expect(fresh.locator(".album-card")).toHaveCount(4);
  await fresh.close();
});
