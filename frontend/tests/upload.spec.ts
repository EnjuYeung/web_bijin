import { test, expect, type Page } from "@playwright/test";
import { copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { resolve } from "node:path";

const photos = resolve("../photos");
const output = resolve("../output");
const imagePath = resolve(photos, "batch/01.jpg");
const prefix = () => "upload-ui-" + Date.now() + "-" + Math.random().toString(16).slice(2, 7);
async function login(page: Page) {
  expect((await page.request.post("/api/login", { data: { user: "ui-test", pass: "fixture-password" } })).status()).toBe(200);
  await expect.poll(async () => (await (await page.request.get("/api/health")).json()).status.scanning, { timeout: 90_000 }).toBe(false);
}
async function openLocal(page: Page, directory: string) {
  await page.goto("/?view=upload");
  await expect(page.locator("#page-title")).toHaveText("上传");
  await expect(page.locator("#nav-upload")).toHaveAttribute("aria-current", "page");
  await page.locator("#upload-target").selectOption("local");
  await page.locator("#upload-directory").fill(directory);
}
async function verifyPhoto(page: Page, directory: string, name: string, original: Buffer) {
  const result = await (await page.request.get("/api/photos?album=" + encodeURIComponent(directory))).json();
  const photo = result.photos.find((item: { name: string }) => item.name === (directory === "." ? name : directory + "/" + name));
  expect(photo).toBeTruthy();
  const thumb = await page.request.get(photo.thumb);
  expect(thumb.status()).toBe(200); expect((await thumb.body()).length).toBeGreaterThan(100);
  const source = await page.request.get(photo.src);
  expect(source.status()).toBe(200); expect(await source.body()).toEqual(original);
}
test.beforeEach(async ({ page }) => {
  await login(page);
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  (page as Page & { uploadErrors: string[] }).uploadErrors = errors;
});
test.afterEach(async ({ page }) => {
  expect((page as Page & { uploadErrors: string[] }).uploadErrors).toEqual([]);
});

test("upload 01 single and multiple photos save locally and enter gallery", async ({ page }) => {
  const dir = prefix(), buffer = await readFile(imagePath);
  await openLocal(page, dir);
  await page.locator("#upload-file-input").setInputFiles({ name: "单张 空格.JPG", mimeType: "image/jpeg", buffer });
  await expect(page.locator("#upload-list li")).toHaveCount(1);
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
  await verifyPhoto(page, dir, "单张 空格.JPG", buffer);
  await page.locator("#upload-clear").click();
  await page.locator("#upload-file-input").setInputFiles([
    { name: "一.jpg", mimeType: "image/jpeg", buffer }, { name: "二.jpg", mimeType: "image/jpeg", buffer },
  ]);
  await expect(page.locator("#upload-list li")).toHaveCount(2);
  await page.locator("#upload-start").click();
  await expect(page.locator('#upload-list li[data-phase="done"]')).toHaveCount(2, { timeout: 45_000 });
  await verifyPhoto(page, dir, "二.jpg", buffer);
  await mkdir(output, { recursive: true });
  await page.screenshot({ path: resolve(output, "upload-local-desktop.png") });
});

test("upload 02 folder picker keeps direct pictures and skips descendants", async ({ page }) => {
  const dir = prefix(), fixture = resolve(output, "folder-fixtures", dir, "旅行");
  await mkdir(resolve(fixture, "杭州"), { recursive: true });
  await copyFile(imagePath, resolve(fixture, "直属.jpg"));
  await copyFile(imagePath, resolve(fixture, "杭州", "不上传.jpg"));
  await copyFile(imagePath, resolve(fixture, ".隐藏.jpg"));
  await openLocal(page, dir);
  await page.locator("#upload-folder-input").setInputFiles(fixture);
  await expect(page.locator("#upload-list li")).toHaveCount(1);
  await expect(page.locator("#upload-list")).toContainText("旅行/直属.jpg");
  await expect(page.locator("#upload-note")).toContainText("子文件夹");
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
  await expect.poll(async () => { try { await readFile(resolve(photos, dir, "旅行", "直属.jpg")); return true; } catch { return false; } }).toBe(true);
  await expect(readFile(resolve(photos, dir, "旅行", "杭州", "不上传.jpg"))).rejects.toThrow();
  await verifyPhoto(page, dir + "/旅行", "直属.jpg", await readFile(imagePath));
});

test("upload 03 multiple dragged folders never enumerate child folders", async ({ page }) => {
  const dir = prefix(), bytes = Array.from(await readFile(imagePath));
  await openLocal(page, dir);
  await page.evaluate(bytes => {
    function file(name: string) {
      return { isFile: true, isDirectory: false, name, file: (success: (file: File) => void) => success(new File([new Uint8Array(bytes)], name, { type: "image/jpeg" })) };
    }
    const child = { isFile: false, isDirectory: true, name: "不会读取", createReader: () => { throw new Error("descendant directory was read"); } };
    function folder(name: string, children: unknown[]) {
      return { isFile: false, isDirectory: true, name, createReader: () => { let first = true; return { readEntries: (success: (entries: unknown[]) => void) => { const batch = first ? children : []; first = false; queueMicrotask(() => success(batch)); } }; } };
    }
    const entries = [folder("家人", [file("甲.jpg"), child]), folder("朋友", [file("乙.jpg")])];
    const transfer = new DataTransfer();
    Object.defineProperty(transfer, "items", { value: entries.map(entry => ({ kind: "file", webkitGetAsEntry: () => entry })) });
    document.getElementById("upload-drop")!.dispatchEvent(new DragEvent("drop", { dataTransfer: transfer, bubbles: true, cancelable: true }));
  }, bytes);
  await expect(page.locator("#upload-list li")).toHaveCount(2);
  await expect(page.locator("#upload-list")).toContainText("家人/甲.jpg");
  await expect(page.locator("#upload-list")).toContainText("朋友/乙.jpg");
  await expect(page.locator("#upload-note")).toContainText("跳过 1 个子文件夹");
  await page.locator("#upload-start").click();
  await expect(page.locator('#upload-list li[data-phase="done"]')).toHaveCount(2, { timeout: 45_000 });
  await verifyPhoto(page, dir + "/家人", "甲.jpg", await readFile(imagePath));
  await verifyPhoto(page, dir + "/朋友", "乙.jpg", await readFile(imagePath));
});

test("upload 04 interrupted transfer can retry and existing files stay unchanged", async ({ page }) => {
  const dir = prefix(), buffer = await readFile(imagePath);
  await openLocal(page, dir);
  let interrupt = true;
  await page.route("**/api/uploads/*/local", route => {
    if (interrupt) { interrupt = false; return route.abort("failed"); }
    return route.continue();
  });
  await page.locator("#upload-file-input").setInputFiles({ name: "重试.jpg", mimeType: "image/jpeg", buffer });
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "error");
  await page.getByRole("button", { name: "重试 重试.jpg", exact: true }).click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
  await verifyPhoto(page, dir, "重试.jpg", buffer);
  await page.locator("#upload-clear").click();
  await page.locator("#upload-file-input").setInputFiles({ name: "重试.jpg", mimeType: "image/jpeg", buffer: Buffer.from("different and invalid") });
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "skipped");
  expect(await readFile(resolve(photos, dir, "重试.jpg"))).toEqual(buffer);
});

test("upload 05 S3 direct PUT uses current storage and processes the object", async ({ page }) => {
  const dir = prefix(), buffer = await readFile(imagePath);
  const response = await page.request.post("/api/storages", { data: {
    name: "UI上传测试", endpoint: "http://127.0.0.1:18093", bucket: "family", prefix: dir,
    accessKey: "test-ak", secretKey: "test-sk", region: "us-east-1", addressing: "path",
  } });
  expect(response.status()).toBe(201);
  const saved = (await response.json()).storage;
  await page.goto("/?view=upload");
  await page.locator("#upload-target").selectOption("s3-" + saved.id);
  await page.locator("#upload-file-input").setInputFiles({ name: "对象存储.jpg", mimeType: "image/jpeg", buffer });
  await page.locator("#upload-start").click();
  await expect(page.locator("#upload-list li")).toHaveAttribute("data-phase", "done", { timeout: 45_000 });
  await verifyPhoto(page, ".", "对象存储.jpg", buffer);
  await page.screenshot({ path: resolve(output, "upload-s3-desktop.png") });
  expect((await page.request.delete("/api/storages/" + saved.id)).status()).toBe(200);
});

test("upload 06 both themes stay usable on narrow and wide screens", async ({ page }) => {
  await openLocal(page, prefix());
  const buffer = await readFile(imagePath);
  await page.locator("#upload-file-input").setInputFiles({ name: "很长的中文文件名称需要完整显示而且不能撑破手机屏幕的图片.jpg", mimeType: "image/jpeg", buffer });
  for (const width of [320, 375, 414, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    if (width < 720 && await page.locator("#nav-scrim").isVisible()) await page.locator("#nav-scrim").click();
    for (const mode of ["day", "night"]) {
      for (let i = 0; i < 3 && await page.locator("html").getAttribute("data-mode") !== mode; i++) await page.locator("#theme-btn").click();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      for (const id of ["upload-select-files", "upload-select-folder", "upload-start", "upload-target"]) {
        const box = await page.locator("#" + id).boundingBox();
        expect(box!.height).toBeGreaterThanOrEqual(44);
      }
      if (width === 375) await page.screenshot({ path: resolve(output, "upload-mobile-" + mode + ".png") });
    }
  }
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.locator("#upload-directory").focus();
  await expect(page.locator("#upload-directory")).toBeFocused();
});
