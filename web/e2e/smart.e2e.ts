import { expect, test } from "@playwright/test";
import { api, seed, signIn } from "./support";

test("an adult makes a smart playlist and a smart collection whose titles follow their rules", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const name = (m: { metadata?: { title?: string }; parsedTitle: string }) =>
    m.metadata?.title || m.parsedTitle;
  // A title no other movie shares, as other specs' libraries hold some twice.
  const names: string[] = movies.map(name);
  const first = names.sort().find((n) => names.filter((m) => m === n).length === 1)!;
  const rest = names.filter((n) => n !== first);
  await signIn(page);
  // The pick boots the page again onto Home; a hash set before that is lost.
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate(() => (location.hash = "#/movies?view=playlists"));

  await page.getByRole("button", { name: "New smart playlist" }).click();
  let sheet = page.getByRole("dialog", { name: "New smart playlist" });
  await sheet.getByLabel("Name").fill("Only one");
  await sheet.getByLabel("Field").selectOption("Title");
  await sheet.getByLabel("Value").fill(first);
  await sheet.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Only one" })).toBeVisible();
  await expect(page.getByRole("img", { name: "Smart" })).toBeVisible();
  const entries = page.getByRole("region", { name: "Entries" });
  await expect(entries.getByRole("listitem")).toHaveCount(1);
  await expect(entries).toContainText(first);
  await expect(entries.getByRole("button", { name: /^Remove / })).toHaveCount(0);

  await page.getByRole("button", { name: "Edit rules…" }).click();
  sheet = page.getByRole("dialog", { name: "Edit rules" });
  await expect(sheet.getByLabel("Value")).toHaveValue(first);
  await sheet.getByLabel("Operator").selectOption("is not");
  await sheet.getByRole("button", { name: "Save" }).click();
  await expect(sheet).toBeHidden();
  await expect(entries.getByRole("listitem")).toHaveCount(rest.length);
  await expect(entries).not.toContainText(first);
  const playlistId = page.url().split("#/playlist/")[1];

  await page.evaluate(() => (location.hash = "#/movies?view=collections"));
  await page.getByRole("button", { name: "New smart collection" }).click();
  sheet = page.getByRole("dialog", { name: "New smart collection" });
  await sheet.getByLabel("Name").fill("Just the one");
  await sheet.getByLabel("Value").fill(first);
  await sheet.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Just the one" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Movies" }).getByRole("listitem")).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Edit order" })).toHaveCount(0);

  await page.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(/view=collections/);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  expect(
    (
      await request.delete(`${api}/profiles/${me.account.id}/playlists/${playlistId}`, { headers })
    ).ok(),
  ).toBeTruthy();
});
