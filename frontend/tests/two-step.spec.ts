import { test, expect, type Page } from "@playwright/test";
import { createHmac } from "node:crypto";
import { mkdir } from "node:fs/promises";

const screenshots = "../output";
const twoStep = "http://127.0.0.1:18094";
const passwordOnly = "http://127.0.0.1:18092";
// RFC 6238's SHA-1 test key, the same secret the 18094 server runs with.
const key = Buffer.from("12345678901234567890");

function codeAt(offset: number) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000) + offset));
  const sum = createHmac("sha1", key).update(msg).digest();
  const at = sum[sum.length - 1] & 0x0f;
  return String((sum.readUInt32BE(at) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}
async function fill(page: Page, code: string) {
  await page.locator("#login-user").fill("ui-test");
  await page.locator("#login-password").fill("fixture-password");
  await page.locator("#login-code").fill(code);
  await page.getByRole("button", { name: "进入相册" }).click();
}

test.describe.configure({ mode: "serial" });
let usedCode = "";

test("21 password-only server shows no code box", async ({ page }) => {
  await page.goto(passwordOnly + "/login");
  await expect(page.locator("#login-password")).toBeVisible();
  expect((await (await page.request.get(passwordOnly + "/api/login-options")).json()).twoStep).toBe(false);
  await expect(page.locator("#login-code")).toHaveCount(0);
});

test("22 two-step login asks for the code and rejects a wrong one", async ({ page }) => {
  await page.goto(twoStep + "/?view=settings");
  await expect(page).toHaveURL(/\/login\?next=/);
  const box = page.locator("#login-code");
  await expect(box).toBeVisible();
  await expect(box).toHaveAttribute("autocomplete", "one-time-code");
  await expect(box).toHaveAttribute("inputmode", "numeric");
  expect(await box.evaluate((el: HTMLInputElement) => el.required)).toBe(true);
  await mkdir(screenshots, { recursive: true });
  await page.screenshot({ path: `${screenshots}/two-step-login-desktop.png` });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: `${screenshots}/two-step-login-mobile.png` });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.setViewportSize({ width: 1440, height: 1000 });

  await fill(page, "000000");
  await expect(page.locator("#gate-err")).toContainText("用户名、密码或动态码不对");
  await expect(box).toBeEnabled();
  usedCode = codeAt(0);
  await fill(page, usedCode);
  await expect(page.locator("#page-title")).toHaveText("设置");
  await page.context().clearCookies();
  expect((await page.request.get(twoStep + "/api/photos")).status()).toBe(401);
});

test("23 a used code is refused, the next one works", async ({ page }) => {
  await page.goto(twoStep + "/login");
  // The code test 22 used stays refused even while it is still current.
  expect(usedCode).not.toBe("");
  await fill(page, usedCode);
  await expect(page.locator("#gate-err")).toContainText("用户名、密码或动态码不对");
  await fill(page, codeAt(1));
  await expect(page).toHaveURL(twoStep + "/");
  await expect(page.locator("#count")).toHaveText("58");
});

test("24 five misses lock one IP but not another", async ({ page, browser }) => {
  await page.setExtraHTTPHeaders({ "X-Real-IP": "203.0.113.9" });
  await page.goto(twoStep + "/login");
  for (let i = 0; i < 5; i++) {
    await fill(page, "000000");
    await expect(page.locator("#gate-err")).toContainText("用户名、密码或动态码不对");
    await expect(page.locator("#login-code")).toBeEnabled();
  }
  await fill(page, "111111");
  await expect(page.locator("#gate-err")).toContainText("输错次数太多");
  await page.screenshot({ path: `${screenshots}/two-step-locked.png` });

  const other = await browser.newPage({ extraHTTPHeaders: { "X-Real-IP": "198.51.100.7" } });
  await other.goto(twoStep + "/login");
  await fill(other, "000000");
  await expect(other.locator("#gate-err")).toContainText("用户名、密码或动态码不对");
  await other.close();
});
