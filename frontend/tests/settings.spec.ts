import { test, expect, type Page } from "@playwright/test";

const screenshots = "../output";
const tab = (page: Page, name: string) => page.getByRole("tab", { name, exact: true });
const panels = ["local", "storage", "recognizer", "wallpaper"];
async function shown(page: Page, id: string) {
  for (const other of panels) await (other === id ? expect(page.locator("#settings-panel-" + other)).toBeVisible() : expect(page.locator("#settings-panel-" + other)).toBeHidden());
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}
async function theme(page: Page, value: "light" | "dark") {
  await page.evaluate(next => { document.documentElement.dataset.theme = next; document.documentElement.classList.toggle("dark", next === "dark"); }, value);
  await page.waitForTimeout(400); // let colour transitions finish before a screenshot
}
async function collapse(page: Page, collapsed: boolean) {
  if ((await page.locator("#sidebar-brand").getAttribute("aria-expanded") === "false") !== collapsed) await page.locator("#sidebar-brand").click();
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", collapsed ? "false" : "true");
}

test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => {
    const data = await (await page.request.get("/api/photos?limit=1")).json(); return !data.status.scanning && !data.status.queued;
  }, { timeout: 60_000 }).toBe(true);
});

test("settings 01 opens on 本地图库; switching shows one card, keeps the address and adds no history", async ({ page }) => {
  const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
  await page.goto("/");
  await expect(page.locator(".sheet").first()).toBeVisible();
  await expect(page).toHaveTitle("照片 · Juen's");
  await page.locator("#nav-settings").click();
  await expect(page.locator("#page-title")).toHaveText("设置");
  await expect(page).toHaveTitle("设置 · Juen's");
  await expect(page.getByRole("tab")).toHaveText(["本地图库", "对象存储", "识别服务", "随机壁纸"]);
  await expect(tab(page, "本地图库")).toHaveAttribute("aria-selected", "true");
  await shown(page, "local");
  await expect(page.locator("#local-count")).toBeVisible();
  const before = await page.evaluate(() => history.length);
  for (const [name, id, visible] of [["对象存储", "storage", "#s3-add"], ["识别服务", "recognizer", "#recognizer-card"], ["随机壁纸", "wallpaper", "#wall-card"], ["本地图库", "local", "#local-count"]]) {
    await tab(page, name).click();
    await expect(tab(page, name)).toHaveAttribute("aria-selected", "true");
    await shown(page, id);
    await expect(page.locator(visible)).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get("tab")).toBe(id);
    expect(new URL(page.url()).searchParams.get("view")).toBe("settings");
  }
  await expect(page.locator("#page-title")).toHaveText("设置");
  await expect(page).toHaveTitle("设置 · Juen's");
  expect(await page.evaluate(() => history.length)).toBe(before);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator(".sheet").first()).toBeVisible();
  expect(errors).toEqual([]);
});

test("settings 02 the address opens a sub-page, survives a reload and falls back to 本地图库", async ({ page }) => {
  await page.goto("/?view=settings&tab=wallpaper");
  await expect(tab(page, "随机壁纸")).toHaveAttribute("aria-selected", "true");
  await shown(page, "wallpaper");
  await page.reload();
  await expect(tab(page, "随机壁纸")).toHaveAttribute("aria-selected", "true");
  await shown(page, "wallpaper");
  await page.goto("/?view=settings&tab=abc");
  await expect(tab(page, "本地图库")).toHaveAttribute("aria-selected", "true");
  await shown(page, "local");
});

test("settings 03 keyboard and screen readers use the standard tab pattern", async ({ page }) => {
  await page.goto("/?view=settings");
  await expect(page.getByRole("tablist", { name: "设置分类" })).toBeVisible();
  await expect(tab(page, "本地图库")).toHaveAttribute("aria-controls", "settings-panel-local");
  await expect(page.getByRole("tabpanel", { name: "本地图库" })).toBeVisible();
  await expect(page.getByRole("tab", { selected: false })).toHaveCount(3);
  await tab(page, "本地图库").focus();
  await page.keyboard.press("ArrowDown");
  await expect(tab(page, "对象存储")).toBeFocused(); await expect(tab(page, "对象存储")).toHaveAttribute("aria-selected", "true"); await shown(page, "storage");
  await page.keyboard.press("ArrowRight");
  await expect(tab(page, "识别服务")).toBeFocused(); await shown(page, "recognizer");
  await page.keyboard.press("End");
  await expect(tab(page, "随机壁纸")).toBeFocused(); await shown(page, "wallpaper");
  await page.keyboard.press("ArrowDown");
  await expect(tab(page, "本地图库")).toBeFocused(); await shown(page, "local");
  await page.keyboard.press("ArrowUp");
  await expect(tab(page, "随机壁纸")).toBeFocused();
  await page.keyboard.press("Home");
  await expect(tab(page, "本地图库")).toBeFocused();
  await expect.poll(() => new URL(page.url()).searchParams.get("tab")).toBe("local");
  // Only the chosen tab is in the Tab order; the next Tab goes into its card.
  await expect(tab(page, "对象存储")).toHaveAttribute("tabindex", "-1");
  await page.keyboard.press("Tab");
  expect(await page.evaluate(() => !!document.activeElement?.closest("#settings-panel-local"))).toBe(true);
  expect(await page.evaluate(() => getComputedStyle(document.activeElement!).outlineStyle !== "none" || getComputedStyle(document.activeElement!).boxShadow !== "none")).toBe(true);
});

test("settings 04 wide screens: a column of buttons beside one card, centred, styled like the side rail", async ({ page }) => {
  for (const width of [1100, 1470]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/?view=settings&tab=storage");
    for (const collapsed of [false, true]) {
      await collapse(page, collapsed);
      await expect(page.locator("#s3-add")).toBeVisible();
      const layout = await page.evaluate(() => {
        const box = (selector: string) => document.querySelector(selector)!.getBoundingClientRect();
        const tabs = [...document.querySelectorAll('[role="tab"]')].map(el => el.getBoundingClientRect());
        const card = box("#settings-panel-storage"), main = box("#main-content"), shell = box(".settings-shell");
        const padding = parseFloat(getComputedStyle(document.querySelector("#main-content")!).paddingLeft);
        return { column: tabs.every((t, i) => i === 0 || (t.top > tabs[i - 1].top && Math.abs(t.left - tabs[0].left) < 1)), beside: tabs[0].right <= card.left, cardWidth: card.width, tabWidth: tabs[0].width,
          left: shell.left - main.left - padding, right: main.right - padding - shell.right, height: tabs[0].height };
      });
      expect(layout.column).toBe(true); expect(layout.beside).toBe(true);
      expect(layout.cardWidth).toBeLessThanOrEqual(961); expect(layout.tabWidth).toBeLessThan(152);
      expect(Math.abs(layout.left - layout.right)).toBeLessThanOrEqual(1); expect(layout.height).toBeGreaterThanOrEqual(44);
      // The chosen sub-page looks like the chosen side rail page, once its short colour transition ends.
      await expect.poll(() => page.evaluate(() => getComputedStyle(document.querySelector('[role="tab"][aria-selected="true"]')!).backgroundColor === getComputedStyle(document.querySelector("#nav-settings")!).backgroundColor)).toBe(true);
      const look = await page.evaluate(() => {
        const chosen = document.querySelector('[role="tab"][aria-selected="true"]')!, rail = document.querySelector("#nav-settings")!;
        const a = getComputedStyle(chosen), b = getComputedStyle(rail), mark = getComputedStyle(chosen, "::before"), railMark = getComputedStyle(rail, "::before");
        return { background: [a.backgroundColor, b.backgroundColor], border: [a.borderTopColor, b.borderTopColor], mark: [mark.width, mark.rotate, mark.backgroundColor], railMark: [railMark.width, railMark.rotate, railMark.backgroundColor] };
      });
      expect(look.background[0]).toBe(look.background[1]);
      expect(look.border[0]).toBe(look.border[1]);
      expect(look.mark).toEqual(look.railMark);
      await noOverflow(page);
      if (width === 1470) for (const mode of ["dark", "light"] as const) { await theme(page, mode); await page.screenshot({ path: `${screenshots}/settings-${width}-${collapsed ? "collapsed" : "expanded"}-${mode}.png` }); }
    }
  }
});

test("settings 05 narrow screens: one row of four equal buttons, icon over name, no sideways scroll", async ({ page }) => {
  for (const [width, height] of [[320, 700], [375, 812], [768, 1024]]) {
    await page.setViewportSize({ width, height });
    await page.goto("/?view=settings");
    await expect(page.locator("#local-count")).toBeVisible();
    const row = await page.evaluate(() => [...document.querySelectorAll('[role="tab"]')].map(el => {
      const box = el.getBoundingClientRect(), icon = el.querySelector("svg")!.getBoundingClientRect(), name = el.querySelector("span")!.getBoundingClientRect();
      return { top: box.top, width: box.width, height: box.height, iconAbove: icon.bottom <= name.top + 1, fits: name.width <= box.width };
    }));
    expect(row).toHaveLength(4);
    for (const item of row) {
      expect(Math.abs(item.top - row[0].top)).toBeLessThan(1); expect(Math.abs(item.width - row[0].width)).toBeLessThan(1);
      expect(item.height).toBeGreaterThanOrEqual(44); expect(item.iconAbove).toBe(true); expect(item.fits).toBe(true);
    }
    // The row sits above the card.
    const card = (await page.locator("#settings-panel-local").boundingBox())!;
    expect(row[0].top + row[0].height).toBeLessThanOrEqual(card.y);
    await noOverflow(page);
    for (const mode of ["light", "dark"] as const) { await theme(page, mode); await page.screenshot({ path: `${screenshots}/settings-${width}-${mode}.png` }); }
  }
});

test("settings 06 the buttons stay in view while scrolling; switching starts at the top", async ({ page }) => {
  for (const [width, height] of [[1440, 600], [375, 600]]) {
    await page.setViewportSize({ width, height });
    await page.goto("/?view=settings&tab=storage");
    await page.locator("#s3-add").click();
    await expect(page.locator("#s3-endpoint")).toBeFocused();
    await page.evaluate(() => scrollTo(0, document.documentElement.scrollHeight));
    await expect.poll(() => page.evaluate(() => scrollY)).toBeGreaterThan(200);
    const header = (await page.locator(".top").boundingBox())!;
    const tabs = (await page.locator('[role="tablist"]').boundingBox())!;
    expect(tabs.y).toBeGreaterThanOrEqual(header.y + header.height - 1);
    expect(tabs.y).toBeLessThan(header.y + header.height + 24);
    await expect(tab(page, "随机壁纸")).toBeInViewport();
    if (width === 375) await page.screenshot({ path: `${screenshots}/settings-sticky-${width}.png` });
    await tab(page, "随机壁纸").click();
    await expect.poll(() => page.evaluate(() => scrollY)).toBe(0);
    await tab(page, "对象存储").click();
  }
});

test("settings 07 only a failed scan is flagged on its button", async ({ page }) => {
  let fail: "" | "local" | "storage" = "";
  await page.route("**/api/settings", async route => {
    const response = await route.fetch(); const data = await response.json();
    if (fail === "local") data.local.status = { at: new Date().toISOString(), seen: 0, err: "目录无法读取" };
    if (fail === "storage") data.storages = [{ id: 99, name: "坏掉的存储", endpoint: "http://127.0.0.1:1", region: "", bucket: "b", prefix: "", accessKey: "k", addressing: "auto", listV1: false, directOriginal: false, publicEndpoint: "", hasSecret: true, photos: 0, broken: 0, status: { at: new Date().toISOString(), seen: 0, err: "连接被拒绝" } }];
    await route.fulfill({ response, json: data });
  });
  await page.goto("/?view=settings");
  await expect(page.locator("#local-count")).toBeVisible();
  await expect(page.locator(".settings-tab-alert")).toHaveCount(0);
  // 识别服务 is not configured in the fixture, and that is not flagged either.
  await expect(page.locator("#recognizer-state")).toHaveText("未配置");
  fail = "storage"; await page.reload();
  const storage = page.getByRole("tab", { name: /^对象存储\s*，扫描失败$/ });
  await expect(storage).toBeVisible();
  await expect(storage.locator(".settings-tab-alert")).toBeVisible();
  await expect(storage).toHaveAttribute("title", "对象存储：扫描失败");
  await expect(page.locator(".settings-tab-alert")).toHaveCount(1);
  await page.setViewportSize({ width: 375, height: 812 });
  await collapse(page, true); await page.waitForTimeout(400);
  await expect(storage.locator(".settings-tab-alert")).toBeVisible();
  await page.screenshot({ path: `${screenshots}/settings-alert-375.png` });
  await page.setViewportSize({ width: 1440, height: 1000 });
  fail = "local"; await page.reload();
  await expect(page.getByRole("tab", { name: /^本地图库\s*，扫描失败$/ })).toBeVisible();
  await expect(page.locator(".settings-tab-alert")).toHaveCount(1);
  await page.screenshot({ path: `${screenshots}/settings-alert-1440.png` });
});

test("settings 08 switching keeps a half-filled form and a running connection test", async ({ page }) => {
  let release = () => {};
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/storages/test", async route => { await held; await route.fulfill({ json: { images: 5, checked: 5, more: false } }); });
  await page.goto("/?view=settings&tab=storage");
  await page.locator("#s3-add").click();
  await page.locator("#s3-name").fill("半途的存储");
  await page.locator("#s3-endpoint").fill("http://127.0.0.1:18093");
  await page.locator("#s3-bucket").fill("family");
  await page.locator("#s3-accessKey").fill("test-ak");
  await page.locator("#s3-secretKey").fill("test-sk");
  await page.locator("#s3-test").click();
  await expect(page.locator("#s3-msg")).toContainText("正在检查连接");
  await tab(page, "随机壁纸").click(); await shown(page, "wallpaper");
  release();
  await expect.poll(async () => (await page.locator("#s3-msg").textContent()) || "").toContain("连接成功");
  await tab(page, "对象存储").click();
  await expect(page.locator("#s3-name")).toHaveValue("半途的存储");
  await expect(page.locator("#s3-bucket")).toHaveValue("family");
  await expect(page.locator("#s3-msg")).toHaveText("连接成功，找到 5 张图片。");
  await page.locator("#s3-cancel").click();
});

test("settings 09 the buttons show while loading; a failed read is reported beside them", async ({ page }) => {
  let mode: "slow" | "fail" = "slow";
  let release = () => {};
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/settings", async route => {
    if (mode === "fail") return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "服务器暂时出错" }) });
    await held; await route.continue();
  });
  await page.goto("/?view=settings");
  await expect(page.getByRole("tab")).toHaveCount(4);
  await expect(page.getByRole("status", { name: "正在读取设置" })).toBeVisible();
  release();
  await expect(page.locator("#local-count")).toBeVisible();
  await expect(page.getByRole("status", { name: "正在读取设置" })).toHaveCount(0);
  mode = "fail"; await page.reload();
  const alert = page.locator(".settings-content").getByText("设置暂时读不出来", { exact: true });
  await expect(alert).toBeVisible();
  await expect(page.getByRole("tab")).toHaveCount(4);
  mode = "slow"; release();
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.locator("#local-count")).toBeVisible();
});
