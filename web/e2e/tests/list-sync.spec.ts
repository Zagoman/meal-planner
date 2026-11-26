import { test, expect, type Browser, type Page } from "@playwright/test";

// Seeded by webfake.Seed (test data only).
const PASSWORD = "correct horse";

async function login(browser: Browser, email: string, serviceWorkers: "allow" | "block" = "allow"): Promise<Page> {
  const ctx = await browser.newContext({ serviceWorkers });
  // The entry splash covers the page for 2.5 s; splash.spec.ts tests it.
  await ctx.addInitScript(() => { try { sessionStorage.setItem("splash", "1"); } catch {} });
  const page = await ctx.newPage();
  await page.goto("/");
  await expect(page).toHaveURL(/\/login/);
  await page.getByText("Sign in with a password").click();
  await page.locator("details").getByLabel("Email").fill(email);
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
  // The queue (IndexedDB) is empty once replayed.
  const queued = await anna.evaluate(
    () =>
      new Promise<number>((resolve) => {
        const req = indexedDB.open("mp", 1);
        req.onsuccess = () => {
          const count = req.result.transaction("ticks", "readonly").objectStore("ticks").count();
          count.onsuccess = () => resolve(count.result);
        };
        req.onerror = () => resolve(-1);
      }),
  );
  expect(queued).toBe(0);

  // Ben's service worker controls the page and keeps the last list for
  // offline use (network-first, one entry per path).
  await ben.reload();
  await expect.poll(() => ben.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 10_000 }).toBe(true);
  await ben.reload();
  const cached = await ben.evaluate(async () => {
    const res = await caches.match("/", { cacheName: "mp-pages-v4" });
    return res ? res.text() : "";
  });
  expect(cached).toContain("Shopping list");
  expect(cached).toMatch(/class="item checked"><button[^>]*>.*?<span class="name">potato</s);
});

test("ticked items sink, focus stays, typing survives a live update, clear and undo", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test", "block");
  const ben = await login(browser, "ben@example.test", "block");

  // Anna starts typing; Ben ticks something; Anna's text survives the live update.
  await anna.locator("#add-name").fill("oat milk");
  const firstSection = anna.locator("#list section.sec").first();
  const names = await firstSection.locator(".item .name").allInnerTexts();
  await item(ben, names[1]).locator(".tick").click();
  await expect(item(anna, names[1])).toHaveClass(/checked/, { timeout: 3_000 });
  await expect(anna.locator("#add-name")).toHaveValue("oat milk");
  await anna.locator("#add-name").fill("");

  // Anna ticks the first item of the section: it moves to the end, focus stays on it.
  await item(anna, names[0]).locator(".tick").focus();
  await anna.keyboard.press("Enter");
  await expect(item(anna, names[0])).toHaveClass(/checked/);
  // Both ticked items (Ben's and Anna's) are now last, in alphabetical order.
  const ticked = [names[0], names[1]].sort((a, b) => a.localeCompare(b));
  await expect(firstSection.locator(".item .name")).toHaveText([...names.slice(2), ...ticked], { timeout: 3_000 });
  await expect(item(anna, names[0]).locator(".tick")).toBeFocused({ timeout: 3_000 });
  await expect(anna.locator("#announce")).toContainText(`${names[0]} in basket`);

  // Clear the ticked items, then undo.
  await anna.locator("#progress").getByRole("button", { name: /Clear \d+ ticked/ }).click();
  await expect(anna.locator("#flash")).toContainText("items cleared");
  await expect(item(anna, names[0])).toHaveCount(0);
  await expect(item(ben, names[0])).toHaveCount(0, { timeout: 3_000 }); // the other phone too
  await anna.locator("#flash").getByRole("button", { name: "Undo" }).click();
  await expect(item(anna, names[0])).toHaveClass(/checked/);

  // Scrolled to the bottom, the "+" brings the add field back with focus.
  await anna.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await anna.setViewportSize({ width: 375, height: 400 });
  await anna.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await expect(anna.locator("#add-jump")).toBeVisible();
  await anna.locator("#add-jump").click();
  await expect(anna.locator("#add-name")).toBeFocused();
});
