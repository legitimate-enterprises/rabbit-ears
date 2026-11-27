import { expect, test, type Page } from "@playwright/test";
import { api, quiet, seed, signIn } from "./support";

test.beforeEach(async ({ context }) => {
  await context.addInitScript(quiet);
});

test("one profile on two devices: live rows, Move here and Watch together", async ({
  page,
  browser,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const movie = movies.find((m: { parsedTitle: string }) => /h264-high41/.test(m.parsedTitle));
  await request.delete(`${api}/profiles/${me.account.id}/watch-state/${movie.id}`, { headers });
  const second = await browser.newContext();
  await second.addInitScript(quiet);
  const other = await second.newPage();
  const time = (p: Page) => p.evaluate(() => document.querySelector("video")?.currentTime ?? 0);
  const title = new RegExp(movie.parsedTitle);
  try {
    await signIn(page);
    await signIn(other);
    await expect(other.getByRole("heading", { name: "Recently added" })).toBeVisible();
    const resume = other.locator(".hero", { hasText: title }).getByRole("link", { name: "Resume" });
    await expect(resume).toHaveCount(0);

    // The fixture is 10 s long: paused, its session stays open on that device.
    const hold = async (p: Page) => {
      await expect.poll(() => time(p), { timeout: 30_000 }).toBeGreaterThan(1);
      await p.evaluate(() => document.querySelector("video")!.pause());
    };
    await page.evaluate((id) => (location.hash = `#/play/${id}`), movie.id);
    await hold(page);
    // Home on the other device learns of it with no reload.
    await expect(resume).toBeVisible({ timeout: 20_000 });

    await resume.click();
    const ask = other.getByRole("dialog", { name: /^Playing on .+ at \d+:\d\d$/ });
    await expect(ask).toBeVisible();
    await expect(ask.getByRole("button", { name: "Move here" })).toBeFocused();
    await ask.getByRole("button", { name: "Move here" }).click();
    await hold(other);
    await expect(page.getByText(/^Moved to /)).toBeVisible({ timeout: 10_000 });
    await expect(page).not.toHaveURL(/#\/play\//);

    await page.evaluate((id) => (location.hash = `#/play/${id}`), movie.id);
    const again = page.getByRole("dialog", { name: /^Playing on / });
    await again.getByRole("button", { name: "Watch together" }).click();
    await expect(page.getByText(/^In sync with /)).toBeVisible({ timeout: 20_000 });
    await expect(other.getByText(/^In sync with /)).toBeVisible({ timeout: 20_000 });
  } finally {
    for (const p of [page, other]) await p.evaluate(() => (location.hash = "#/")).catch(() => {});
    await other.waitForTimeout(500);
    await second.close();
  }
});
