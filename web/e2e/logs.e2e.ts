import { expect, test } from "@playwright/test";
import { api, seed, signIn } from "./support";

test("an error the viewer did not catch reaches the admin's Logs as a client's", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await signIn(page);
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  const message = `Uncaught e2e error ${Date.now()}`;
  await page.evaluate((m) => setTimeout(() => Promise.reject(new Error(m))), message);
  // The report rides on the next request that succeeds.
  await page.evaluate(() => (location.hash = "#/movies"));
  await expect
    .poll(async () => {
      const res = await request.get(`${api}/logs?source=client&q=${encodeURIComponent(message)}`, {
        headers,
      });
      return (await res.json()).items.map(
        (e: { client?: { platform: string } }) => e.client?.platform,
      );
    })
    .toEqual(["web"]);

  await page.goto("/admin/#/logs");
  await page.getByLabel("Source").selectOption("client");
  await page.getByLabel("Platform").selectOption("web");
  const row = page.getByRole("row", { name: new RegExp(message) });
  await expect(row).toContainText("Web app");
  await row.getByRole("button", { name: "Show details" }).click();
  await expect(page.locator("tr.detail")).toContainText("In unhandled");
  await expect(page.locator("tr.detail")).toContainText("Profile: Ada");
});
