import { expect, test, type APIRequestContext } from "@playwright/test";
import { email, password, seed, signIn } from "./support";

const api = "/api/v1";
type Headers = { Authorization: string };

/** An adult the admin adds, who joins with their code into a household of their own. */
async function joined(request: APIRequestContext, headers: Headers, name: string) {
  const made = await request.post(`${api}/accounts`, {
    headers,
    data: { kind: "adult", name, email: `${name.toLowerCase()}@example.com`, isAdmin: false },
  });
  expect(made.ok()).toBeTruthy();
  const acct = await made.json();
  const code = await (
    await request.post(`${api}/join-codes`, {
      headers,
      data: { accountId: acct.id, expiresInDays: 7 },
    })
  ).json();
  const join = await request.post(`${api}/join/${code.code}`, {
    data: { name, password, deviceName: "e2e", platform: "web" },
  });
  expect(join.ok()).toBeTruthy();
  return { Authorization: `Bearer ${(await join.json()).token}` };
}

test("an admin connects two adults, one shares a kid and the other accepts it", async ({
  browser,
  request,
}) => {
  const headers = await seed(request);
  const nora = await joined(request, headers, "Nora");
  await joined(request, headers, "Marcus");
  const kid = await request.post(`${api}/profiles`, {
    headers: nora,
    data: { name: "Pip", birthday: "2018-04-02", filterMode: "ratings" },
  });
  expect(kid.ok()).toBeTruthy();

  const admin = await (await browser.newContext()).newPage();
  await admin.goto("/admin/");
  await admin.getByLabel("Email").fill(email);
  await admin.getByLabel("Password").fill(password);
  await admin.getByRole("button", { name: "Sign in" }).click();
  await admin.getByRole("link", { name: "Users & profiles" }).click();
  const connected = admin.getByRole("region", { name: "Connected adults" });
  await connected.getByRole("button", { name: "Connect adults" }).click();
  const connect = admin.getByRole("dialog", { name: "Connect adults" });
  await connect.getByLabel("Adult", { exact: true }).selectOption({ label: "Nora" });
  await connect.getByLabel("Other adult").selectOption({ label: "Marcus" });
  await connect.getByRole("button", { name: "Connect" }).click();
  await expect(connected.getByRole("row", { name: /Nora.*Marcus|Marcus.*Nora/ })).toBeVisible();

  const offering = await (await browser.newContext()).newPage();
  await signIn(offering, { email: "nora@example.com", profile: "Nora" });
  await offering.getByRole("button", { name: "Nora" }).click();
  await offering.getByRole("menuitem", { name: "Manage household" }).click();
  const pip = offering.getByRole("listitem").filter({ hasText: "Pip" });
  await pip.getByRole("button", { name: "Share with another household…" }).click();
  const sheet = offering.getByRole("dialog", { name: "Share Pip" });
  await expect(sheet.getByRole("button", { name: "Send offer" })).toBeDisabled();
  await sheet.getByRole("radio", { name: "Marcus" }).check();
  await sheet.getByRole("button", { name: "Send offer" }).click();
  await expect(offering.getByText("Offered to Marcus, waiting")).toBeVisible();

  const accepting = await (await browser.newContext()).newPage();
  await signIn(accepting, { email: "marcus@example.com", profile: "Marcus" });
  await accepting.getByRole("button", { name: "Marcus" }).click();
  await accepting.getByRole("menuitem", { name: "Manage household" }).click();
  await expect(accepting.getByText("Nora offers to share Pip with your household")).toBeVisible();
  await accepting.getByRole("button", { name: "Accept" }).click();
  await expect(accepting.getByText("In 2 households")).toBeVisible();
  await accepting.evaluate(() => (location.hash = "#/who"));
  await expect(accepting.getByRole("link", { name: "Pip" })).toBeVisible();
  // The offering household hears the offer go, with no reload.
  await expect(offering.getByText("Offered to Marcus, waiting")).toBeHidden({ timeout: 20_000 });
});
