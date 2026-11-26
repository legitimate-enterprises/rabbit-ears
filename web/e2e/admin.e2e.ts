import { expect, test } from "@playwright/test";
import { resolve } from "node:path";

const fixtures = resolve(import.meta.dirname, "../../fixtures");

test("first run, add a library, scan it, add an adult and see the join code", async ({ page }) => {
  await page.goto("/admin/");
  await expect(page.getByRole("heading", { name: "Create the admin account" })).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("Ada");
  await page.getByLabel("Email").fill("ada@example.com");
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
  await page.getByLabel("Confirm password").fill("correct horse battery");
  await page.getByRole("button", { name: "Create admin account" }).click();

  await page.getByLabel("Server name").fill("Den");
  await page.getByRole("button", { name: "Continue" }).click();

  await page.getByLabel("Folder path").fill(fixtures);
  await page.getByRole("button", { name: "Add library" }).click();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();

  await page.getByRole("link", { name: "Libraries" }).click();
  const card = page.getByRole("region", { name: "Movies library" });
  await expect(card.getByText(fixtures)).toBeVisible();
  // The scan task appears on the runner's next tick.
  const scan = card.getByRole("button", { name: "Scan now" });
  await expect(async () => {
    await page.reload();
    await expect(scan).toBeEnabled({ timeout: 500 });
  }).toPass({ timeout: 40_000 });
  await scan.click();

  await page.getByRole("link", { name: "Tasks" }).click();
  await expect(page.getByRole("row", { name: /Scan Movies/ }).getByText("OK")).toBeVisible({
    timeout: 60_000,
  });

  await page.getByRole("link", { name: "Users & profiles" }).click();
  await page.getByRole("button", { name: "Add adult" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Heidi");
  await page.getByLabel("Email").fill("heidi@example.com");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("dialog", { name: "Heidi's account is ready" })).toBeVisible();
  await expect(page.locator(".code")).toHaveText(/^[A-Z0-9]{4}-[A-Z0-9]{4}$/);
  await expect(page.getByLabel("Join link")).toHaveValue(/\/join\/[A-Z0-9]{4}-[A-Z0-9]{4}$/);

  await page.goto("/admin/#/libraries");
  await page.reload();
  await card.getByRole("button", { name: "Edit" }).click();
  await page.getByLabel("Folder path").fill(`${fixtures}/elsewhere`);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(card.getByText(`${fixtures}/elsewhere`)).toBeVisible();

  await card.getByRole("button", { name: "Edit" }).click();
  const edit = page.getByRole("dialog", { name: "Edit library" });
  await edit.getByRole("button", { name: "Delete library" }).click();
  await expect(edit.getByText("stay on disk")).toBeVisible();
  await edit.getByRole("button", { name: "Delete library" }).last().click();
  await expect(page.getByText("No libraries yet.")).toBeVisible();
});
