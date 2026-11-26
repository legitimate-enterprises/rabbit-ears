import { expect, test } from "@playwright/test";
import { leavePlayer, quiet, seed, seedEditions, signIn } from "./support";

test.beforeEach(({ page }) => page.addInitScript(quiet));

test.afterEach(leavePlayer);

test("a movie of two editions adds and plays the edition chosen", async ({ page, request }) => {
  const headers = await seed(request);
  const movie = await seedEditions(request, headers);
  const extended = movie.editions.find((e) => e.name === "Extended")!;
  await signIn(page);
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate((id) => (location.hash = `#/movie/${id}`), movie.id);

  const choice = page.getByLabel("Edition");
  await expect(choice.locator("option:checked")).toHaveText(/^Standard/);
  await choice.selectOption(String(extended.id));

  await page.getByRole("button", { name: "More" }).click();
  await page.getByRole("menuitem", { name: "Add to playlist" }).click();
  const dlg = page.getByRole("dialog", { name: "Add to playlist" });
  await expect(dlg.getByText("Loading")).toBeHidden();
  await dlg.getByLabel("New playlist").fill("Cuts");
  await dlg.getByRole("button", { name: "Create and add" }).click();
  await expect(dlg).toBeHidden();

  const start = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/playback/sessions"),
  );
  await page.getByRole("link", { name: "Play", exact: true }).click();
  expect((await start).postDataJSON().editionId).toBe(extended.id);
  await page.evaluate(() => (location.hash = "#/movies?view=playlists"));
  await page.getByRole("link", { name: /Cuts/ }).click();
  await expect(
    page.getByRole("main").getByRole("button", { name: /^Paper Lanterns · Extended/ }),
  ).toBeVisible();
});
