import { test, expect, type Browser, type Page } from "@playwright/test";

// Seeded by webfake.Seed (test data only).
const PASSWORD = "correct horse";

async function login(browser: Browser, email: string, serviceWorkers: "allow" | "block" = "allow"): Promise<Page> {
  const ctx = await browser.newContext({ serviceWorkers });
  const page = await ctx.newPage();
  await page.goto("/");
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("heading", { name: "Shopping list" })).toBeVisible();
  return page;
}

function item(page: Page, name: string) {
  return page.locator("#list li.item", { has: page.locator(".name", { hasText: new RegExp(`^${name}$`) }) });
}

test.beforeEach(async ({ request }) => {
  expect((await request.post("/__e2e/reset")).status()).toBe(204);
});

test("a tick on one phone shows up live on the other, also after being offline", async ({ browser }) => {
  // Playwright's setOffline does not cut off a page controlled by a service
  // worker, so Anna (who goes offline) runs without one; Ben checks the
  // service worker's offline cache.
  const anna = await login(browser, "anna@example.test", "block");
  const ben = await login(browser, "ben@example.test");

  // Both phones hold an open SSE stream (the server sends the list on connect).
  for (const p of [anna, ben]) {
    await expect(p.locator("#list-live")).toHaveAttribute("sse-connect", "/list/stream");
    await expect(item(p, "onion")).toBeVisible();
    await expect(item(p, "onion")).not.toHaveClass(/checked/);
  }

  // Anna ticks: her item swaps in place, greyed but still listed.
  await item(anna, "onion").getByRole("button").first().click();
  await expect(item(anna, "onion")).toHaveClass(/checked/);
  await expect(item(anna, "onion")).toBeVisible();

  // Ben's phone gets it over SSE without reloading.
  await expect(item(ben, "onion")).toHaveClass(/checked/, { timeout: 10_000 });
  await expect(ben.locator("#progress")).toContainText("1 of");

  // Check at home: Anna answers Buy for a pantry item; it joins its section
  // on both phones, and progress counts it.
  const home = (p: Page) => p.locator("#check-home li", { hasText: "rice" });
  await expect(home(anna)).toBeVisible();
  await expect(item(ben, "rice")).toHaveCount(0);
  const before = await ben.locator("#progress").getAttribute("data-total");
  await home(anna).getByRole("button", { name: "Buy" }).click();
  await expect(home(anna).getByRole("button", { name: "Buy" })).toHaveAttribute("aria-pressed", "true");
  await expect(item(ben, "rice")).toBeVisible({ timeout: 10_000 });
  await expect(ben.locator("#progress")).toHaveAttribute("data-total", String(Number(before) + 1));
  await home(anna).getByRole("button", { name: "Have it" }).click();
  await expect(item(ben, "rice")).toHaveCount(0, { timeout: 10_000 });

  // Offline: Anna's tick is queued locally and the badge says so.
  await anna.context().setOffline(true);
  await anna.evaluate(() => window.dispatchEvent(new Event("offline")));
  await item(anna, "potato").getByRole("button").first().click();
  await expect(item(anna, "potato")).toHaveClass(/checked/);
  await expect(anna.locator("#offline-badge")).toBeVisible();
  await expect(anna.locator("#offline-badge")).toContainText("1 to sync");
  await expect(item(ben, "potato")).not.toHaveClass(/checked/);

  // Back online: the queue is replayed and Ben sees it.
  await anna.context().setOffline(false);
  await anna.evaluate(() => window.dispatchEvent(new Event("online")));
  await expect(item(ben, "potato")).toHaveClass(/checked/, { timeout: 15_000 });
  await expect(anna.locator("#offline-badge")).toBeHidden({ timeout: 10_000 });
  expect(await anna.evaluate(() => localStorage.getItem("mp:tickQueue"))).toBeNull();

  // Ben's service worker controls the page and keeps the last list for
  // offline use (network-first, one entry per path).
  await ben.reload();
  await expect.poll(() => ben.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 10_000 }).toBe(true);
  await ben.reload();
  const cached = await ben.evaluate(async () => {
    const res = await caches.match("/", { cacheName: "mp-pages-v2" });
    return res ? res.text() : "";
  });
  expect(cached).toContain("Shopping list");
  expect(cached).toMatch(/class="item checked"><button[^>]*>.*?<span class="name">potato</s);
});
