import { expect, test } from "@playwright/test";
import { quiet, seed, signIn } from "./support";

test.beforeEach(({ page }) => page.addInitScript(quiet));

test("a playlist's owner shares it with their household and it shows as Shared", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get("/api/v1/me", { headers })).json();
  const base = `/api/v1/profiles/${me.account.id}`;
  const made = await request.post(`${base}/playlists`, {
    headers,
    data: { name: "Family night", kind: "movies" },
  });
  expect(made.ok()).toBeTruthy();
  const list = await made.json();
  const [home] = (await (await request.get(`${base}/share-households`, { headers })).json()).items;
  await signIn(page);
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate((id) => (location.hash = `#/playlist/${id}`), list.id);
  await expect(page.getByRole("heading", { name: "Family night" })).toBeVisible();
  await expect(page.getByRole("img", { name: "Shared" })).toBeHidden();

  await page.getByRole("button", { name: "Share" }).click();
  const sheet = page.getByRole("dialog", { name: "Share" });
  const households = sheet.getByRole("region", { name: "Households" });
  await expect(households.getByLabel(`Access for ${home.name}`)).toHaveValue("");
  const shared = page.waitForResponse(
    (r) => r.url().endsWith("/household-shares") && r.request().method() === "POST",
  );
  await households.getByLabel(`Access for ${home.name}`).selectOption("Can view");
  expect((await shared).status()).toBe(201);
  await sheet.getByRole("button", { name: "Done" }).click();
  await expect(sheet).toBeHidden();
  await expect(page.getByRole("img", { name: "Shared" })).toBeVisible();

  await page.evaluate(() => (location.hash = "#/movies?view=playlists"));
  await expect(
    page.getByRole("link", { name: /Family night/ }).getByRole("img", { name: "Shared" }),
  ).toBeVisible();

  expect((await request.delete(`${base}/playlists/${list.id}`, { headers })).ok()).toBeTruthy();
});
