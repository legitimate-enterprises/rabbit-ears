import { expect, test, type APIRequestContext } from "@playwright/test";
import { api, password, seed, signIn } from "./support";

type Headers = { Authorization: string };

/** An adult the admin adds, who joins with their code into a household of their own. */
async function joined(request: APIRequestContext, headers: Headers, name: string) {
  const made = await request.post(`${api}/accounts`, {
    headers,
    data: { kind: "adult", name, email: `${name.toLowerCase()}@example.com`, isAdmin: false },
  });
  expect(made.ok()).toBeTruthy();
  const code = await (
    await request.post(`${api}/join-codes`, {
      headers,
      data: { accountId: (await made.json()).id, expiresInDays: 7 },
    })
  ).json();
  const join = await request.post(`${api}/join/${code.code}`, {
    data: { name, password, deviceName: "e2e", platform: "web" },
  });
  expect(join.ok()).toBeTruthy();
  return { Authorization: `Bearer ${(await join.json()).token}` };
}

test("an adult asks two emails alike, is accepted by one, and removes them", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const wren = await joined(request, headers, "Wren");
  await signIn(page);
  await page.getByRole("button", { name: "Ada" }).click();
  await page.getByRole("menuitem", { name: "Friends" }).click();
  await expect(page.getByRole("heading", { name: "Friends", level: 1 })).toBeVisible();

  for (const email of ["wren@example.com", "nobody@example.com"]) {
    await page.getByRole("button", { name: "Add Friend" }).click();
    const sheet = page.getByRole("dialog", { name: "Add Friend" });
    await sheet.getByLabel("Email").fill(email);
    await sheet.getByRole("button", { name: "Send Request" }).click();
    await expect(sheet).toBeHidden();
    await expect(
      page.getByRole("status").filter({
        hasText: "If they're on this server, they'll get your friend request.",
      }),
    ).not.toHaveCount(0);
  }
  await page.getByText("Sent (2)").click();
  const rows = page.locator("details li");
  await expect(rows).toHaveCount(2);
  const html = await rows.evaluateAll((lis) =>
    lis.map((li) => li.innerHTML.replace(/wren@example\.com|nobody@example\.com/, "x")),
  );
  expect(html[0]).toBe(html[1]);

  const { incoming } = await (
    await request.get(`${api}/friend-requests`, { headers: wren })
  ).json();
  expect(incoming).toHaveLength(1);
  expect(
    (
      await request.post(`${api}/friend-requests/${incoming[0].id}/accept`, { headers: wren })
    ).status(),
  ).toBe(204);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  const friends = page.getByRole("region", { name: "Friends" });
  await expect(friends.getByText("Wren")).toBeVisible();
  await expect(page.getByText("Sent (1)")).toBeVisible();

  await friends.getByRole("button", { name: "More for Wren" }).click();
  await page.getByRole("menuitem", { name: "Remove Friend" }).click();
  const confirm = page.getByRole("dialog", { name: "Remove Wren?" });
  await confirm.getByRole("button", { name: "Remove" }).click();
  await expect(confirm).toBeHidden();
  await expect(friends.getByText("No friends yet.", { exact: false })).toBeVisible();

  // Sent stays open across the redraws since it was opened.
  await page.getByRole("button", { name: "Withdraw" }).click();
  await expect(page.locator("details")).toHaveCount(0);
});
