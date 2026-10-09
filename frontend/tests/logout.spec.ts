import { test, expect, type Page } from "@playwright/test";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { resolve } from "node:path";

const output = resolve("../output");
const base = "http://127.0.0.1:18092";

async function signIn(page: Page) {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => (await (await page.request.get("/api/health")).json()).status.scanning, { timeout: 90_000 }).toBe(false);
}
async function signedIn(page: Page) {
  return (await page.context().cookies()).some(cookie => cookie.name === "bijin");
}
// Ask this browser for a thumbnail the normal way: a cached copy answers 200
// without asking the server, which turns away a signed-out browser with 401.
async function thumbStatus(page: Page, src: string) {
  return page.evaluate(url => fetch(url).then(response => response.status), src);
}
async function firstThumb(page: Page) {
  const img = page.locator('main img[src^="/thumb/"]').first();
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((element: HTMLImageElement) => element.complete && element.naturalWidth > 0)).toBe(true);
  return (await img.getAttribute("src"))!;
}

test.describe.configure({ mode: "serial" });
test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  (page as Page & { logoutErrors: string[] }).logoutErrors = errors;
  await mkdir(output, { recursive: true });
  await signIn(page);
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { logoutErrors: string[] }).logoutErrors).toEqual([]);
});

test("logout 01 sits at the bottom of the sidebar on desktop, collapsed and phone", async ({ page, browserName }) => {
  await page.goto("/?view=settings");
  const button = page.locator("#nav-logout");
  await expect(button).toBeVisible();
  await expect(button).toHaveText("退出登录");
  await expect(button).toHaveAttribute("title", "退出登录");
  const settings = (await page.locator("#nav-settings").boundingBox())!;
  const logout = (await button.boundingBox())!;
  const foot = (await page.locator(".sidebar-foot").boundingBox())!;
  // Kept apart from the five pages, near the bottom, above the footer line.
  expect(logout.y).toBeGreaterThan(settings.y + settings.height + 200);
  expect(logout.y + logout.height).toBeLessThanOrEqual(foot.y);
  expect(await page.locator(".sidebar-logout").evaluate(form => getComputedStyle(form).borderTopStyle)).toBe("solid");
  await page.screenshot({ path: resolve(output, `logout-sidebar-desktop-${browserName}.png`) });

  if (await page.locator("html").getAttribute("data-sidebar") !== "collapsed") await page.locator("#sidebar-brand").click();
  await expect(page.locator("html")).toHaveAttribute("data-sidebar", "collapsed");
  await expect(button).toBeVisible();
  await expect(button.locator("span")).toBeHidden();
  await expect(button).toHaveAccessibleName("退出登录");

  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(button).toBeVisible();
  const phone = (await button.boundingBox())!;
  expect(phone.y + phone.height).toBeLessThanOrEqual(844);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: resolve(output, `logout-sidebar-mobile-${browserName}.png`) });
  await page.setViewportSize({ width: 1440, height: 1000 });
});

test("logout 02 signs this browser out, shows the notice, and keeps preferences", async ({ page, browserName }) => {
  // Set the preference on a page with no album code, so no photo list is
  // still loading when the test moves on.
  await page.goto("/api/health");
  await page.evaluate(() => localStorage.setItem("juens-theme", "night"));
  await page.goto("/?view=albums");
  await expect(page.locator("html")).toHaveAttribute("data-mode", "night");
  await firstThumb(page);

  // A slow answer, like Chrome pausing to clear its cache, shows the wait.
  // Playwright cannot look into a page that is being left, so the page notes
  // what the button shows in sessionStorage, which the next page can read.
  await page.evaluate(() => {
    const button = document.getElementById("nav-logout") as HTMLButtonElement;
    new MutationObserver(() => sessionStorage.setItem("logout-button", `${button.textContent}|${button.disabled}`))
      .observe(button, { subtree: true, childList: true, characterData: true, attributes: true });
  });
  await page.route("**/logout", async route => { await new Promise(done => setTimeout(done, 1_500)); await route.continue(); });
  await page.locator("#nav-logout").click();
  await expect(page).toHaveURL(base + "/login?out=1");
  expect(await page.evaluate(() => sessionStorage.getItem("logout-button"))).toBe("正在退出…|true");
  await page.unrouteAll();
  await expect(page.locator("#gate-out")).toHaveText("已退出登录。");
  await expect(page.locator("#gate-err")).toHaveCount(0);
  await page.screenshot({ path: resolve(output, `logout-login-page-${browserName}.png`) });
  expect(await signedIn(page)).toBe(false);
  expect((await page.request.get("/api/photos")).status()).toBe(401);

  // Back does not bring the albums back.
  await page.goBack();
  await expect(page).toHaveURL(/\/login\?/);
  await expect(page.locator("#gate")).toBeVisible();
  await expect(page.locator("#count")).toHaveCount(0);

  await page.goto("/login?out=1");
  await page.locator("#login-user").fill("ui-test");
  await page.locator("#login-password").fill("fixture-password");
  await page.getByRole("button", { name: "进入相册" }).click();
  await expect(page).toHaveURL(base + "/");
  await expect(page.locator("#page-title")).toHaveText("照片");
  await expect(page.locator("#count")).toHaveText(/^\d/);
  await expect(page.locator("html")).toHaveAttribute("data-mode", "night");
  await page.evaluate(() => localStorage.removeItem("juens-theme"));
});

test("logout 03 removes the thumbnails a browser profile cached", async ({ playwright, browserName }) => {
  // Playwright's WebKit for Linux never acts on Clear-Site-Data, not even for
  // a plain page over HTTPS (checked against a bare test server), so there the
  // photo stays. Apple's WebKit, as in Safari, is checked separately.
  const afterLogout = browserName === "webkit" ? 200 : 401;
  if (browserName === "webkit") test.info().annotations.push({ type: "known limitation", description: "Playwright's Linux WebKit ignores Clear-Site-Data" });
  // A real profile keeps its cache on disk, as a phone or computer does.
  const profile = await mkdtemp(resolve(output, `profile-${browserName}-`));
  const context = await playwright[browserName].launchPersistentContext(profile, {
    baseURL: base, viewport: { width: 1440, height: 1000 }, args: browserName === "chromium" ? ["--no-sandbox"] : [],
  });
  try {
    const page = context.pages()[0] ?? await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    await signIn(page);
    await page.goto("/?view=albums");
    const src = await firstThumb(page);
    // The health check is a page with no album code that could ask the
    // server for anything else.
    await page.goto("/api/health");
    expect(await thumbStatus(page, src)).toBe(200);

    // Control: removing only the cookie leaves the cached photo readable,
    // which is what the cache clearing has to fix.
    await context.clearCookies();
    expect(await thumbStatus(page, src)).toBe(200);

    await signIn(page);
    await page.goto("/?view=albums");
    await firstThumb(page);
    await page.locator("#nav-logout").click();
    await expect(page).toHaveURL(base + "/login?out=1");
    expect(await thumbStatus(page, src)).toBe(afterLogout);
    const fresh = await context.newPage();
    await fresh.goto("/api/health");
    expect(await thumbStatus(fresh, src)).toBe(afterLogout);
    expect(errors).toEqual([]);
  } finally {
    await context.close();
    await rm(profile, { recursive: true, force: true });
  }
});

test("logout 04 sends the other open tabs to the login page", async ({ page }) => {
  const other = await page.context().newPage();
  await other.goto("/?view=albums");
  await expect(other.locator("#page-title")).toHaveText("相册");
  await page.goto("/?view=settings");
  await page.locator("#nav-logout").click();
  await expect(page).toHaveURL(base + "/login?out=1");
  await expect(other).toHaveURL(base + "/login?out=1");
  await expect(other.locator("#gate-out")).toBeVisible();

  // A tab opened after signing in again is not sent away.
  await signIn(page);
  const later = await page.context().newPage();
  await later.goto("/?view=albums");
  await expect(later.locator("#page-title")).toHaveText("相册");
  await page.waitForTimeout(1_000);
  await expect(later).toHaveURL(base + "/?view=albums");
  await later.close();
  await other.close();
});

test("logout 05 during an upload asks first, and staying keeps the upload signed in", async ({ page }) => {
  // Hold the upload body so the upload stays in progress.
  await page.route("**/api/uploads/*/local", () => {});
  await page.goto("/?view=upload");
  await page.locator("#upload-target").click();
  await page.getByRole("option", { name: "本地 · 本地照片目录", exact: true }).click();
  await page.locator("#upload-directory").fill("logout-ui-" + Date.now());
  await page.locator("#upload-file-input").setInputFiles({ name: "held.jpg", mimeType: "image/jpeg", buffer: await readFile(resolve("../photos/batch/01.jpg")) });
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "uploading");

  const asked: string[] = [];
  page.once("dialog", dialog => { asked.push(dialog.type()); void dialog.dismiss(); });
  await page.locator("#nav-logout").click();
  await expect.poll(() => asked).toEqual(["beforeunload"]);
  await expect(page).toHaveURL(base + "/?view=upload");
  await expect(page.locator("#nav-logout")).toBeEnabled();
  await expect(page.locator("#nav-logout")).toHaveText("退出登录");
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "uploading");
  expect(await signedIn(page)).toBe(true);
  expect((await page.request.get("/api/photos?limit=1")).status()).toBe(200);

  page.once("dialog", dialog => { asked.push(dialog.type()); void dialog.accept(); });
  await page.locator("#nav-logout").click();
  await expect(page).toHaveURL(base + "/login?out=1");
  expect(asked).toEqual(["beforeunload", "beforeunload"]);
  expect(await signedIn(page)).toBe(false);
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("logout 06 works when already signed out, and other sites cannot sign anyone out", async ({ page }) => {
  // The upload page asks the server nothing more once it has loaded.
  await page.goto("/?view=upload");
  await expect(page.locator("#upload-target")).toBeEnabled();
  await page.context().clearCookies(); // as if the 30 days ran out meanwhile
  await page.locator("#nav-logout").click();
  await expect(page).toHaveURL(base + "/login?out=1");
  await expect(page.locator("#gate-out")).toBeVisible();

  await signIn(page);
  // localhost is another site to the browser, though the same test server.
  await page.goto("http://localhost:18092/login");
  await expect(page.locator("#gate")).toBeVisible();
  const refused = page.waitForResponse(response => response.url() === base + "/logout");
  await page.evaluate(target => {
    const form = document.createElement("form");
    form.method = "post"; form.action = target;
    document.body.append(form); form.submit();
  }, base + "/logout");
  expect((await refused).status()).toBe(403);
  expect(await signedIn(page)).toBe(true);
  expect((await page.request.get(base + "/api/photos?limit=1")).status()).toBe(200);
});
