import { test, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

// Grid layout differs between browser engines (Firefox once sized a subgrid row
// by the cover image's own pixel height), so this file runs in Chromium, WebKit
// and Firefox.
const screenshots = "../output";
const longName = "一本名字很长很长的相册，窄屏上要换成好几行才能放得下";

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  (page as Page & { uiErrors: string[] }).uiErrors = errors;
  await mkdir(screenshots, { recursive: true });
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => (await (await page.request.get("/api/photos?limit=1")).json()).status.scanning, { timeout: 90_000 }).toBe(false);
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { uiErrors: string[] }).uiErrors).toEqual([]);
  await page.request.delete("/api/recognizer");
});

async function cards(page: Page) {
  return page.locator(".organize-row").evaluateAll(rows => rows.map(row => {
    const box = (selector: string) => row.querySelector(selector)!.getBoundingClientRect();
    const cover = box(".organize-cover"), check = box(":scope > .organize-check"), ai = box(".organize-ai");
    return {
      cover: Math.round(cover.top), coverBottom: Math.round(cover.bottom), coverWidth: cover.width, coverHeight: cover.height,
      name: Math.round(box(".organize-album").top), author: Math.round(box(".organize-author").top), models: Math.round(box(".organize-models").top),
      // How far the check and the recognize button sit from the cover's top corners.
      checkLeft: check.left - cover.left, checkTop: check.top - cover.top, aiRight: cover.right - ai.right, aiTop: ai.top - cover.top,
    };
  }));
}

test("organize grid 01 covers sit right above their names, the boxes line up and the corner buttons stay on the cover", async ({ page, browserName }) => {
  // Like the real library: portrait covers of photos thousands of pixels tall,
  // one name long enough to wrap onto several lines, and a recognition service.
  expect((await page.request.put("/api/recognizer", { data: { provider: "custom", endpoint: "http://127.0.0.1:18095/v1", apiKey: "sk-fixture", model: "fixture-model", reasoning: "" } })).status()).toBe(200);
  const root = (await (await page.request.get("/api/photos?album=.&limit=50")).json()).photos as { name: string; thumb: string }[];
  const tall = root.find(photo => photo.name.endsWith("tall.jpg"))!;
  const sizes = [[4912, 5348], [750, 9504]];
  // Other specs leave albums behind, so the count comes from the server.
  let total = 0;
  await page.route("**/api/albums", async route => {
    const response = await route.fetch(), data = await response.json();
    total = data.albums.length;
    data.albums = data.albums.map((album: { id: string; name: string; cover: object }, i: number) => ({
      ...album, name: album.id === "misc" ? longName : album.name,
      cover: { ...album.cover, thumb: tall.thumb, w: sizes[i % 2][0], h: sizes[i % 2][1] },
    }));
    await route.fulfill({ response, json: data });
  });
  // Opened with the grid already chosen, as on a return visit.
  await page.addInitScript(() => localStorage.setItem("juens-organize-view", "grid"));
  for (const [width, ratio] of [[320, 3 / 4], [375, 3 / 4], [768, 4 / 3], [1024, 4 / 3], [1440, 4 / 3]]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/?view=organize");
    await expect(page.locator("#organize-view-grid")).toHaveAttribute("aria-pressed", "true");
    if (width < 720 && await page.locator("#nav-scrim").isVisible()) await page.locator("#nav-scrim").click();
    await expect.poll(() => total).toBeGreaterThanOrEqual(4);
    await expect(page.locator(".organize-row")).toHaveCount(total);
    // Before and after every cover has loaded.
    for (const phase of ["opened", "loaded"]) {
      if (phase === "loaded") {
        // Covers load lazily: bring every card into view once.
        for (const row of await page.locator(".organize-row").all()) await row.scrollIntoViewIfNeeded();
        await expect.poll(() => page.locator(".organize-cover img").evaluateAll(list => list.every(img => (img as HTMLImageElement).complete && (img as HTMLImageElement).naturalWidth > 0))).toBe(true);
      }
      const all = await cards(page);
      expect(all.some(card => all.filter(other => other.cover === card.cover).length > 1), `cards beside each other ${phase} at ${width}`).toBe(true);
      for (const card of all) {
        const at = `${phase} at ${width}`;
        expect(Math.abs(card.coverWidth / card.coverHeight - ratio), "cover shape " + at).toBeLessThan(0.02);
        expect(card.name - card.coverBottom, "space under the cover " + at).toBeGreaterThanOrEqual(0);
        expect(card.name - card.coverBottom, "space under the cover " + at).toBeLessThanOrEqual(12);
        // The check sits on the cover's top-left corner, the recognize button on its top-right.
        for (const [edge, value] of [["check left", card.checkLeft], ["check top", card.checkTop], ["recognize right", card.aiRight], ["recognize top", card.aiTop]] as const) {
          expect(value, edge + " " + at).toBeGreaterThanOrEqual(0);
          expect(value, edge + " " + at).toBeLessThan(12);
        }
        const line = all.filter(other => other.cover === card.cover);
        for (const part of ["name", "author", "models"] as const) expect(new Set(line.map(other => other[part])).size, part + " " + at).toBe(1);
      }
    }
    if (width === 375 || width === 1440) await page.screenshot({ path: `${screenshots}/organize-grid-${browserName}-${width}.png`, fullPage: true });
  }
});
