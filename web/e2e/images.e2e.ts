import { expect, test } from "@playwright/test";
import { quiet, seed, signIn } from "./support";

test.beforeEach(({ page }) => page.addInitScript(quiet));

// A 1x1 red PNG.
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC",
  "base64",
);

test("a playlist's owner uploads its image, sees it, and removes it", async ({ page, request }) => {
  const headers = await seed(request);
  const me = await (await request.get("/api/v1/me", { headers })).json();
  const made = await request.post(`/api/v1/profiles/${me.account.id}/playlists`, {
    headers,
    data: { name: "Covers", kind: "movies" },
  });
  expect(made.ok()).toBeTruthy();
  const list = await made.json();
  await signIn(page);
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate((id) => (location.hash = `#/playlist/${id}`), list.id);
  await expect(page.getByRole("heading", { name: "Covers" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Remove image" })).toBeHidden();

  await page.getByRole("button", { name: "Set image" }).click();
  const dlg = page.getByRole("dialog", { name: "Set image" });
  await expect(dlg.getByText("Nothing in it has artwork to pick.")).toBeVisible();
  const shown = page.waitForResponse(
    (r) => /\/playlists\/\d+\/image\?v=\d+$/.test(r.url()) && r.status() === 200,
  );
  await dlg.getByLabel("Upload a file").setInputFiles({
    name: "red.png",
    mimeType: "image/png",
    buffer: png,
  });
  await expect(dlg).toBeHidden();
  expect((await shown).headers()["content-type"]).toBe("image/jpeg");

  await page.getByRole("button", { name: "Remove image" }).click();
  await expect(page.getByRole("button", { name: "Remove image" })).toBeHidden();
  const after = await (
    await request.get(`/api/v1/profiles/${me.account.id}/playlists/${list.id}`, { headers })
  ).json();
  expect(after.imageUrl).toBeUndefined();
});
