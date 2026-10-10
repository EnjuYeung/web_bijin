import { test, expect, type Page, type Route } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const photos = resolve("../photos"), output = resolve("../output");
const prefix = () => "upload-recovery-" + Date.now() + "-" + Math.random().toString(16).slice(2, 7);
const image = () => readFile(resolve(photos, "batch/01.jpg"));
async function open(page: Page, directory: string) {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => (await (await page.request.get("/api/health")).json()).status.scanning, { timeout: 90_000 }).toBe(false);
  await page.goto("/?view=upload"); await page.locator("#upload-target").click();
  await page.getByRole("option", { name: "本地 · 本地照片目录", exact: true }).click();
  await page.locator("#upload-directory").fill(directory);
}
async function add(page: Page, ...names: string[]) {
  const buffer = await image();
  await page.locator("#upload-file-input").setInputFiles(names.map(name => ({ name, mimeType: "image/jpeg", buffer })));
}
function counts(page: Page) {
  const count = { prepare: 0, put: 0, verify: 0, complete: 0 };
  page.on("request", request => {
    const path = new URL(request.url()).pathname;
    if (path === "/api/uploads" && request.method() === "POST") count.prepare++;
    if (request.method() === "PUT") count.put++;
    if (path.endsWith("/verify")) count.verify++;
    if (path.endsWith("/complete")) count.complete++;
  }); return count;
}
// Browser interception does not consistently expose File request bodies. Forward
// the exact selected fixture bytes to the real Go handler, then lose its reply.
async function loseSavedPUT(route: Route, bytes: Buffer) {
  const response = await route.fetch({ postData: bytes });
  expect(response.status()).toBe(200);
  expect((await response.json()).task.saved).toBe(true);
  await route.abort("failed");
}
test.beforeEach(async ({ page }) => {
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  (page as Page & { uploadErrors: string[] }).uploadErrors = errors;
});
test.afterEach(async ({ page }) => { expect((page as Page & { uploadErrors: string[] }).uploadErrors).toEqual([]); });

test("recovery 01 saved local PUT with lost response finishes using its original task", async ({ page }) => {
  const directory = prefix(), buffer = await image(); await open(page, directory);
  const count = counts(page);
  await page.route("**/api/uploads/*/local", route => loseSavedPUT(route, buffer));
  await add(page, "已保存.jpg"); await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
  expect(count).toEqual({ prepare: 1, put: 1, verify: 1, complete: 0 });
  expect(await readFile(resolve(photos, directory, "已保存.jpg"))).toEqual(buffer);
  await page.screenshot({ path: resolve(output, "upload-recovery-saved-" + test.info().project.name + ".png") });
});

for (const loss of ["put", "complete"]) test(`recovery S3 ${loss} response loss confirms the object without retransmission`, async ({ page }) => {
  const directory = prefix(), buffer = await image(); await open(page, directory);
  const response = await page.request.post("/api/storages", { data: { name: "恢复测试 " + directory, endpoint: "http://127.0.0.1:18093", bucket: "family", prefix: directory, accessKey: "test-ak", secretKey: "test-sk", region: "us-east-1", addressing: "path" } });
  expect(response.status()).toBe(201); const storage = (await response.json()).storage;
  try {
    await page.reload(); await page.locator("#upload-target").click();
    await page.getByRole("option", { name: "S3 · " + storage.name, exact: true }).click();
    await page.locator("#upload-directory").fill(directory);
    const count = counts(page);
    if (loss === "put") await page.route("http://127.0.0.1:18093/**", async route => {
      if (route.request().method() !== "PUT") return route.continue();
      const reply = await route.fetch({ postData: buffer }); expect(reply.status()).toBe(200); await route.abort("failed");
    });
    else await page.route("**/api/uploads/*/complete", async route => {
      const reply = await route.fetch(); expect(reply.status()).toBe(200); expect((await reply.json()).task.saved).toBe(true); await route.abort("failed");
    });
    await add(page, "云端恢复.jpg"); await page.locator("#upload-start").click();
    await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
    expect(count.prepare).toBe(1); expect(count.put).toBe(1); expect(count.verify).toBe(1);
    expect(count.complete).toBe(loss === "put" ? 0 : 1);
    const result = await (await page.request.get("/api/photos?album=" + encodeURIComponent(directory))).json();
    const photo = result.photos.find((item: { name: string }) => item.name === directory + "/云端恢复.jpg");
    expect(photo).toBeTruthy(); expect(await (await page.request.get(photo.src)).body()).toEqual(buffer);
  } finally { expect((await page.request.delete("/api/storages/" + storage.id)).status()).toBe(200); }
});

test("recovery 04 uncertain item does not block another photo and continues with no PUT", async ({ page }) => {
  const directory = prefix(), buffer = await image(); await open(page, directory);
  let canVerify = false; const count = counts(page);
  await page.route("**/api/uploads/*/local", async route => {
    const current = (await (await page.request.get(route.request().url().replace(/\/local$/, ""))).json()).task;
    if (current.path.endsWith("/待确认.jpg")) return loseSavedPUT(route, buffer);
    return route.continue();
  });
  await page.route("**/api/uploads/*/verify", route => canVerify ? route.continue() : route.abort("failed"));
  await add(page, "待确认.jpg", "继续上传.jpg"); await page.locator("#upload-start").click();
  await expect(page.locator('#upload-list li[data-phase="uncertain"]')).toHaveCount(1);
  await expect(page.locator('#upload-list li[data-phase="done"]')).toHaveCount(1, { timeout: 45_000 });
  expect(count.verify).toBe(1);
  const asked: string[] = []; page.once("dialog", dialog => { asked.push(dialog.type()); void dialog.dismiss(); });
  await page.locator('.upload-actions a[href="/?view=albums"]').click();
  await expect.poll(() => asked).toEqual(["beforeunload"]); await expect(page).toHaveURL(/view=upload/);
  canVerify = true; await page.getByRole("button", { name: "继续确认 待确认.jpg", exact: true }).click();
  await expect(page.locator('#upload-list li[data-phase="done"]')).toHaveCount(2, { timeout: 45_000 });
  expect(count.prepare).toBe(2); expect(count.put).toBe(2); expect(count.verify).toBe(2);
});

test("recovery 05 stopping four active transfers starts no fifth and still guards leaving", async ({ page }) => {
  await open(page, prefix()); const count = counts(page), releases: (() => void)[] = [];
  await page.route("**/api/uploads/*/local", async route => {
    await new Promise<void>(resolve => releases.push(resolve));
    try { await route.abort("failed"); } catch { /* The user already cancelled it. */ }
  });
  try {
    await add(page, "1.jpg", "2.jpg", "3.jpg", "4.jpg", "5.jpg"); await page.locator("#upload-start").click();
    await expect.poll(() => releases.length).toBe(4); await page.locator("#upload-cancel").click();
    await expect(page.locator('#upload-list li[data-phase="uncertain"]')).toHaveCount(4);
    await expect(page.locator('#upload-list li[data-phase="pending"]')).toHaveCount(1);
    expect(count.prepare).toBe(4); expect(count.put).toBe(4); expect(count.verify).toBe(0);
    const asked: string[] = []; page.once("dialog", dialog => { asked.push(dialog.type()); void dialog.dismiss(); });
    await page.locator("#nav-logout").click(); await expect.poll(() => asked).toEqual(["beforeunload"]);
    await expect(page).toHaveURL(/view=upload/); await expect(page.locator("#nav-logout")).toBeEnabled();
    expect((await page.request.get("/api/upload-targets")).status()).toBe(200);
  } finally { releases.forEach(release => release()); await page.unrouteAll({ behavior: "ignoreErrors" }); }
});

test("recovery 06 selected but unsent photos warn before navigating or logging out", async ({ page }) => {
  await open(page, prefix()); await add(page, "未上传.jpg");
  const asked: string[] = [];
  let expected = 0;
  for (const locator of [page.locator('.upload-actions a[href="/?view=albums"]'), page.locator("#nav-logout")]) {
    page.once("dialog", dialog => { asked.push(dialog.type()); void dialog.dismiss(); }); await locator.click();
    await expect.poll(() => asked.length).toBe(++expected);
    await expect(page).toHaveURL(/view=upload/);
  }
  expect(asked).toEqual(["beforeunload", "beforeunload"]);
  await page.locator("#upload-clear").click();
  await page.locator("#nav-albums").click(); await expect(page).toHaveURL(/view=albums/);
});

for (const saved of [false, true]) test(`recovery expired task preserves known saved=${saved} and never recreates itself`, async ({ page }) => {
  const directory = prefix(), buffer = await image(); await open(page, directory); const count = counts(page);
  await page.route("**/api/uploads/*/local", async route => {
    if (!saved) return loseSavedPUT(route, buffer);
    const reply = await route.fetch({ postData: buffer }); const data = await reply.json();
    expect(data.task.saved).toBe(true);
    // Preserve a saved receipt while simulating loss of the memory task.
    data.task.phase = "processing"; await route.fulfill({ status: 200, json: data });
  });
  await page.route(/\/api\/uploads\/[a-f0-9]+(?:\/verify)?$/, route => route.fulfill({ status: 404, json: { error: "任务已失效" } }));
  await add(page, "失效.jpg"); await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "expired");
  await expect(page.locator("#upload-list li")).toContainText(saved ? "原图已保存" : "原图保存结果仍待确认");
  expect(count.prepare).toBe(1); expect(count.put).toBe(1);
  expect(await readFile(resolve(photos, directory, "失效.jpg"))).toEqual(buffer);
  await expect(page.locator("#upload-start")).toBeDisabled();
  await page.getByRole("button", { name: "移除 失效.jpg", exact: true }).click();
  await expect(page.locator("#upload-directory")).toBeEnabled();
});
