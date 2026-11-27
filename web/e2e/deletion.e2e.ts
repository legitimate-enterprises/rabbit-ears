import { expect, test } from "@playwright/test";
import { api, password, seed, seedAdult, signIn } from "./support";

test("an adult deletes their own account and every device of theirs signs out", async ({
  page,
  browser,
  request,
}) => {
  const headers = await seed(request);
  const gil = { name: "Gil", email: "gil@example.com" };
  await seedAdult(request, headers, gil);
  const second = await browser.newContext();
  const other = await second.newPage();
  try {
    await signIn(page, { email: gil.email, profile: "Gil" });
    await signIn(other, { email: gil.email, profile: "Gil" });
    await expect(other.getByRole("button", { name: "Gil" })).toBeVisible();

    await page.evaluate(() => (location.hash = "#/preferences"));
    await page.getByRole("button", { name: "Delete account…" }).click();
    const ask = page.getByRole("dialog", { name: "Delete your account?" });
    await expect(ask).toContainText("It cannot be undone. The household stays as it is.");
    await ask.getByLabel("Password").fill("not the password");
    await ask.getByRole("button", { name: "Delete account" }).click();
    await expect(ask.getByRole("alert")).toHaveText("The password is wrong.");
    await ask.getByLabel("Password").fill(password);
    await ask.getByRole("button", { name: "Delete account" }).click();

    await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
    // The other device hears it on its held poll, with no reload.
    await expect(other.getByRole("button", { name: "Sign in" })).toBeVisible({ timeout: 20_000 });
    const back = await request.post(`${api}/auth/sign-in`, {
      data: { email: gil.email, password, deviceName: "e2e", platform: "web" },
    });
    expect(back.status()).toBe(401);
    const left = await (await request.get(`${api}/profiles`, { headers })).json();
    expect(left.items.map((p: { name: string }) => p.name)).not.toContain("Gil");
  } finally {
    await second.close();
  }
});
