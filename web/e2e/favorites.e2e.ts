import { expect, test } from "@playwright/test";
import { api, seed, signIn } from "./support";

test("an adult stars a movie and finds it in Favorites and under the Favorites chip", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const [movie] = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const name = movie.metadata?.title || movie.parsedTitle;
  const favorite = `${api}/profiles/${me.account.id}/favorites/${movie.id}`;
  await request.delete(favorite, { headers });
  await signIn(page);
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate((id) => (location.hash = `#/movie/${id}`), movie.id);
  const star = page.getByRole("button", { name: "Favorite" });
  await expect(star).toHaveAttribute("aria-pressed", "false");
  await expect(star).toBeEnabled();
  await star.click();
  await expect(star).toHaveAttribute("aria-pressed", "true");

  await page
    .getByRole("navigation", { name: "Library" })
    .getByRole("link", { name: "Favorites" })
    .click();
  const movies = page.getByRole("region", { name: "Movies" });
  await expect(movies.getByRole("link", { name: new RegExp(name) })).toBeVisible();

  await page
    .getByRole("navigation", { name: "Library" })
    .getByRole("link", { name: "Movies" })
    .click();
  await page
    .getByRole("group", { name: "Filter" })
    .getByRole("button", { name: "Favorites" })
    .click();
  await expect(page.locator("main .grid a.poster")).toHaveCount(1);
  await expect(page.locator("main .grid a.poster")).toContainText(name);

  expect((await request.delete(favorite, { headers })).status()).toBe(204);
});
