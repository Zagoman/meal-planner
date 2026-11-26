import { test, expect } from "@playwright/test";

// The entry screen (design handover): once per browser session, on a full
// page load only; it fades out and stops taking taps, then is removed.
test("the splash shows once per session and gets out of the way", async ({ browser }) => {
  const ctx = await browser.newContext({ serviceWorkers: "block" });
  const page = await ctx.newPage();
  await page.goto("/login");
  const splash = page.locator("#splash");
  await expect(splash).toBeAttached();
  await expect(splash).toContainText("En madplan, der kender jer.");
  // Gone after the 2.5 s animation; the page underneath takes taps.
  await expect(splash).not.toBeAttached({ timeout: 5_000 });
  await page.getByText("Sign in with a password").click();

  // A reload in the same session: no splash at all.
  await page.reload();
  await expect(page.locator("#splash")).toHaveCount(0);

  // A new session (new tab context) sees it again.
  const other = await (await browser.newContext({ serviceWorkers: "block" })).newPage();
  await other.goto("/login");
  await expect(other.locator("#splash")).toBeAttached();
});

test("reduced motion: the splash fades after a second", async ({ browser }) => {
  const ctx = await browser.newContext({ serviceWorkers: "block", reducedMotion: "reduce" });
  const page = await ctx.newPage();
  const start = Date.now();
  await page.goto("/login");
  await expect(page.locator(".hsv-splash__name")).toHaveCSS("opacity", "1");
  await expect(page.locator("#splash")).not.toBeAttached({ timeout: 4_000 });
  expect(Date.now() - start).toBeLessThan(3_000);
});
