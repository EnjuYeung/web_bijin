import { test, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

const screenshots = "../output";
async function signIn(page: Page) {
  const response = await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } });
  expect(response.status()).toBe(200);
}
async function waitForScan(page: Page) {
  await expect.poll(async () => {
    const response = await page.request.get("/api/health");
    const data = await response.json();
    return !data.status.scanning && !data.status.queued;
  }).toBe(true);
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}
async function setMode(page: Page, mode: "day" | "night") {
  for (let i = 0; i < 3; i++) {
    if (await page.locator("html").getAttribute("data-mode") === mode) break;
    await page.locator("#theme-btn").click();
  }
  await expect(page.locator("html")).toHaveAttribute("data-theme", mode === "night" ? "dark" : "light");
  await expect.poll(() => page.locator("#sidebar-brand").evaluate(button => getComputedStyle(button).color === getComputedStyle(document.body).color)).toBe(true);
}
async function whiteButtonContrast(page: Page) {
  const result = await page.locator("#s3-add").evaluate(button => {
    const style = getComputedStyle(button);
    const canvas = document.createElement("canvas"); canvas.width = canvas.height = 1;
    const context = canvas.getContext("2d")!;
    function rgb(color: string) { context.clearRect(0, 0, 1, 1); context.fillStyle = color; context.fillRect(0, 0, 1, 1); return Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3); }
    function luminance(channels: number[]) { return channels.map(value => { const s = value / 255; return s <= .04045 ? s / 12.92 : ((s + .055) / 1.055) ** 2.4; }).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0); }
    const foreground = rgb(style.color), background = rgb(style.backgroundColor);
    const a = luminance(foreground), b = luminance(background);
    return { foreground, background, ratio: (Math.max(a, b) + .05) / (Math.min(a, b) + .05) };
  });
  expect(result.foreground).toEqual([255, 255, 255]);
  expect(result.ratio).toBeGreaterThanOrEqual(4.5);
  console.log("Add storage contrast", result);
}

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await mkdir(screenshots, { recursive: true });
  await signIn(page);
  await waitForScan(page);
  (page as Page & { uiErrors: string[] }).uiErrors = errors;
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { uiErrors: string[] }).uiErrors).toEqual([]);
});

test("01 login errors, safe return URL and access gate", async ({ page }) => {
  await page.context().clearCookies();
  await page.goto("/?view=settings");
  await expect(page).toHaveURL(/\/login\?next=/);
  await page.locator("#login-user").fill("ui-test");
  await page.locator("#login-password").fill("wrong-password");
  await page.getByRole("button", { name: "进入相册" }).click();
  await expect(page.locator("#gate-err")).toContainText("用户名或密码不对");
  await expect(page.locator("#login-password")).toBeEnabled();
  await page.locator("#login-password").fill("fixture-password");
  await page.getByRole("button", { name: "进入相册" }).click();
  await expect(page.locator("#page-title")).toHaveText("设置");
  await expect(page.locator("#local-count")).toContainText("58 张照片");
  await page.context().clearCookies();
  for (const path of ["/api/photos", "/api/albums", "/api/settings", "/thumb/1", "/original/1"]) {
    expect((await page.request.get(path)).status()).toBe(401);
  }
  await page.goto("/login?next=%2F%2Fevil.example");
  await page.locator("#login-user").fill("ui-test"); await page.locator("#login-password").fill("fixture-password");
  await page.getByRole("button", { name: "进入相册" }).click();
  await expect(page).toHaveURL("http://127.0.0.1:18092/");
  await expect(page.locator("#count")).toHaveText("58");
});

test("02 photos, virtual scrolling, lightbox and folder albums", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#count")).toHaveText("58");
  await expect(page.locator(".sheet").first()).toBeVisible();
  await expect.poll(() => page.locator(".sheet img").first().evaluate(image => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  await page.evaluate(() => scrollTo(0, 1600));
  await expect.poll(() => page.locator(".sheet").evaluateAll(nodes => nodes.some(node => { const box = node.getBoundingClientRect(); return box.top >= 80 && box.bottom < innerHeight; }))).toBe(true);
  const id = await page.locator(".sheet").evaluateAll(nodes => (nodes.find(node => { const box = node.getBoundingClientRect(); return box.top >= 80 && box.bottom < innerHeight; }) as HTMLElement).dataset.id);
  const photo = page.locator(`.sheet[data-id="${id}"]`);
  await expect(photo).toBeInViewport();
  const scrollBefore = await page.evaluate(() => scrollY);
  await photo.click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page).toHaveURL(new RegExp("#p/" + id + "$"));
  await expect.poll(() => page.locator("#lb-img").evaluate(image => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  await expect(page.locator("#lb-meta")).toBeVisible();
  const before = await page.locator("#lb-img").getAttribute("src");
  await page.keyboard.press("ArrowRight");
  await expect(page.locator("#lb-img")).not.toHaveAttribute("src", before!);
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator("#lb-img")).toHaveAttribute("src", before!);
  for (let i = 0; i < 9; i++) {
    await page.keyboard.press("Tab");
    await expect.poll(() => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => scrollY)).toBeCloseTo(scrollBefore, 0);
  await expect(photo).toBeFocused();
  expect(await page.locator(".sheet").count()).toBeLessThan(58);
  await noOverflow(page);
  await setMode(page, "night");
  await page.screenshot({ path: screenshots + "/after-gallery-desktop.png" });
  await page.locator("#nav-albums").click();
  await expect(page.locator("#count")).toHaveText("4");
  await expect(page.locator(".album-card")).toHaveCount(4);
  await page.getByRole("link", { name: "子目录，1 张照片" }).click();
  await expect(page.locator("#page-title")).toHaveText("子目录");
  await expect(page.locator("#count")).toHaveText("1");
  await page.locator(".sheet").click();
  await expect(page.locator("#lb-next")).toBeDisabled();
  await expect(page.locator("#lb-prev")).toBeDisabled();
  await page.locator("#lb-img").click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.locator("#album-back").click();
  await expect(page.locator(".album-card")).toHaveCount(4);
});

test("03 settings form validation, test, save, edit and delete", async ({ page }) => {
  await page.goto("/?view=settings");
  await expect(page.locator("#events-note")).toContainText("自动扫描");
  await expect(page.locator("#local-count")).toContainText("58 张照片");
  await expect(page.locator("#local-count")).toContainText("1 个文件无法显示");
  await page.locator("#s3-add").click();
  await page.locator("#s3-save").click();
  await expect(page.locator("#s3-endpoint")).toBeFocused();
  await expect(page.locator('[data-slot="field-error"]')).toHaveCount(4);
  await page.locator("#s3-name").fill("云端家庭照片");
  await page.locator("#s3-endpoint").fill("http://127.0.0.1:18093");
  await page.locator("#s3-bucket").fill("family");
  await page.locator("#s3-accessKey").fill("wrong-key");
  await page.locator("#s3-secretKey").fill("test-sk");
  await page.locator("#s3-test").click();
  await expect(page.locator("#s3-msg")).toContainText("Access Key");
  await page.locator("#s3-accessKey").fill("test-ak");
  await page.locator("#s3-test").click();
  await expect(page.locator("#s3-msg")).toContainText("找到 2 张图片");
  await page.locator("#s3-form summary").click();
  await page.locator("#s3-addressing").selectOption("path");
  await page.locator("#s3-save").click();
  await expect(page.locator("#s3-form")).toHaveCount(0);
  await expect(page.locator(".storage-row")).toContainText("云端家庭照片");
  await expect(page.locator(".storage-count")).toContainText("2 张照片");
  await page.reload();
  await expect(page.locator(".storage-count")).toContainText("2 张照片");
  const settings = await (await page.request.get("/api/settings")).json();
  expect(settings.storages[0].secretKey).toBeUndefined();
  await page.getByRole("button", { name: "编辑 云端家庭照片" }).click();
  await expect(page.locator("#s3-secretKey")).toHaveValue("");
  await expect(page.locator("#s3-addressing")).toBeVisible();
  await expect(page.locator("#s3-addressing")).toHaveValue("path");
  await page.locator("#s3-name").fill("家庭照片备份");
  await expect(page.locator("#s3-publicEndpoint")).toHaveCount(0);
  await page.getByRole("checkbox", { name: "大图由浏览器直接从存储读取" }).click();
  await page.locator("#s3-publicEndpoint").fill("http://127.0.0.1:18093");
  await page.locator("#s3-test").click();
  await expect(page.locator("#s3-msg")).toContainText("找到 2 张图片");
  await page.locator("#s3-save").click();
  await expect(page.locator(".storage-row")).toContainText("家庭照片备份");
  await expect(page.locator(".storage-location")).toContainText("大图直连 127.0.0.1:18093");
  const saved = await (await page.request.get("/api/settings")).json();
  expect(saved.storages[0].directOriginal).toBe(true);
  expect(saved.storages[0].publicEndpoint).toBe("http://127.0.0.1:18093");
  await waitForScan(page);
  await page.locator("#nav-albums").click();
  await expect(page.getByRole("link", { name: "子目录，2 张照片" })).toBeVisible();
  // The cloud original is fetched by the browser from storage, not via bijin.
  await page.getByRole("link", { name: "子目录，2 张照片" }).click();
  const direct = page.waitForResponse(response => response.url().startsWith("http://127.0.0.1:18093/family/") && response.url().includes("X-Amz-Signature="));
  await page.getByRole("button", { name: "云端 日落.jpg" }).click();
  const original = await direct;
  expect(original.status()).toBe(200);
  expect(original.headers()["cache-control"]).toBe("private, max-age=43200, immutable");
  await expect.poll(() => page.locator("#lb-img").evaluate(image => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  await page.locator("#lb-img").click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.locator("#nav-albums").click();
  await expect(page.getByRole("link", { name: "子目录，2 张照片" })).toBeVisible();
  await page.locator("#nav-photos").click();
  await expect(page.locator("#count")).toHaveText("60");
  await page.locator("#nav-settings").click();
  page.once("dialog", dialog => dialog.dismiss());
  await page.getByRole("button", { name: "删除 家庭照片备份" }).click();
  await expect(page.locator(".storage-row")).toHaveCount(1);
  await setMode(page, "night");
  await page.screenshot({ path: screenshots + "/after-settings-desktop.png" });
  page.once("dialog", dialog => dialog.accept());
  await page.getByRole("button", { name: "删除 家庭照片备份" }).click();
  await expect(page.locator(".storage-row")).toHaveCount(0);
  await waitForScan(page);
  await page.locator("#nav-photos").click();
  await expect(page.locator("#count")).toHaveText("58");
});

test("04 brand keyboard toggle, preference persistence and white text contrast", async ({ page }) => {
  await page.goto("/?view=settings");
  await expect(page.locator("#s3-add")).toBeVisible();
  await expect(page.locator("#sidebar-toggle")).toHaveCount(0);
  await page.locator("#sidebar-brand").focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", "false");
  await page.reload();
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", "false");
  await page.locator("#sidebar-brand").focus(); await page.keyboard.press("Space");
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", "true");
  for (const mode of ["night", "day"] as const) {
    await setMode(page, mode);
    await page.mouse.move(800, 50);
    await expect.poll(() => page.locator("#s3-add").evaluate(button => getComputedStyle(button).backgroundColor)).toBe("oklch(0.51 0.18 350)");
    await whiteButtonContrast(page);
    await page.locator("#s3-add").hover();
    await expect.poll(() => page.locator("#s3-add").evaluate(button => getComputedStyle(button).backgroundColor)).toBe("oklch(0.46 0.18 350)");
    await whiteButtonContrast(page);
  }
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-mode", "day");
  await expect(page.locator("#s3-add")).toBeVisible();
  await page.screenshot({ path: screenshots + "/after-settings-light.png" });
  await page.locator("#s3-add").click();
  await page.keyboard.press("Escape");
  await expect(page.locator("#s3-form")).toHaveCount(0);
  await expect(page.locator("#s3-add")).toBeFocused();
});

test("05 responsive layout, mobile navigation, metadata and reduced motion", async ({ page }) => {
  await page.addInitScript(() => { localStorage.removeItem("juens-sidebar"); localStorage.setItem("juens-theme", "night"); });
  for (const width of [320, 375, 414, 768, 1280, 1440]) {
    await page.setViewportSize({ width, height: 812 });
    await page.goto("/?view=settings");
    await expect(page.locator("#s3-add")).toBeVisible();
    await noOverflow(page);
    const box = await page.locator("#s3-add").boundingBox(); expect(box!.height).toBeGreaterThanOrEqual(44);
    await page.locator("#s3-add").click();
    await expect(page.locator("#s3-endpoint")).toBeVisible();
    const input = await page.locator("#s3-endpoint").boundingBox(); expect(input!.height).toBeGreaterThanOrEqual(44);
    await noOverflow(page);
    await page.locator("#s3-cancel").click();
  }
  await page.setViewportSize({ width: 375, height: 812 }); await page.goto("/?view=settings");
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", "false");
  const margin = await page.locator(".app-main").evaluate(element => getComputedStyle(element).marginLeft);
  await page.locator("#sidebar-brand").click();
  await expect(page.locator("#nav-scrim")).toBeVisible();
  expect(await page.locator(".app-main").evaluate(element => getComputedStyle(element).marginLeft)).toBe(margin);
  await page.keyboard.press("Escape");
  await expect(page.locator("#sidebar-brand")).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#sidebar-brand")).toBeFocused();
  await page.screenshot({ path: screenshots + "/after-settings-mobile.png", fullPage: true });
  await page.locator("#s3-add").click();
  await page.screenshot({ path: screenshots + "/after-form-mobile.png", fullPage: true });
  await page.locator("#s3-cancel").click();
  await page.locator("#nav-photos").click();
  await expect(page.locator("#count")).toHaveText("58");
  await page.locator(".sheet").first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator("#lb-meta")).toHaveCount(0);
  await page.locator("#lb-meta-toggle").click(); await expect(page.locator("#lb-meta")).toBeVisible(); await noOverflow(page);
  await page.locator("#lb-close").click(); await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.locator(".sheet").first().hover();
  expect(await page.locator(".sheet img").first().evaluate(image => getComputedStyle(image).scale)).toBe("1");
  await page.screenshot({ path: screenshots + "/after-gallery-mobile.png" });
  await page.setViewportSize({ width: 812, height: 375 });
  await page.goto("/?view=settings"); await expect(page.locator("#s3-add")).toBeVisible(); await noOverflow(page);
});

test("06 mobile photo bookmarks, browser back and touch swipe", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  const response = await page.request.get("/api/photos?album=batch&limit=1");
  const data = await response.json();
  await page.goto("/?album=batch#p/" + data.photos[0].id);
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator("#lb-meta")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => !!document.activeElement?.closest("#main-content"))).toBe(true);
  await expect(page).toHaveURL("http://127.0.0.1:18092/?album=batch");
  await page.locator(".sheet").first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.goBack();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.goForward();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator("#lb-meta")).toHaveCount(0);
  const before = await page.locator("#lb-img").getAttribute("src");
  const session = await page.context().newCDPSession(page);
  await session.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 280, y: 360 }] });
  await session.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 120, y: 360 }] });
  await session.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await expect(page.locator("#lb-img")).not.toHaveAttribute("src", before!);
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.locator("#lb-close").click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
