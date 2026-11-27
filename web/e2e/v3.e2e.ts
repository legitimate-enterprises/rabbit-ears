import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { api, email, quiet, seed, seedAdult, seedMusic, seedTV, signIn } from "./support";

test.describe.configure({ mode: "serial" });

test.beforeEach(({ page }) => page.addInitScript(quiet));

const tab = (page: Page, name: string) =>
  page.getByRole("navigation", { name: "Library" }).getByRole("link", { name, exact: true });
const mini = (page: Page) => page.getByRole("region", { name: "Music player" });

async function openAlbum(page: Page) {
  await tab(page, "Music").click();
  await page.getByRole("link", { name: /First Light/ }).click();
  await expect(page.getByRole("heading", { name: "First Light" })).toBeVisible();
}

test("an adult plays an album through to its end and it is recently played", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await seedMusic(request, headers);
  await signIn(page);
  await openAlbum(page);
  await page.getByRole("main").getByRole("button", { name: "Play", exact: true }).click();

  // Each fixture song is 3 s; the queue moves on only when one ends.
  await expect(mini(page)).toContainText("Opening");
  await expect(mini(page)).toContainText("Second Wind", { timeout: 20_000 });
  await expect(mini(page)).toContainText("Last Call", { timeout: 20_000 });
  await expect(mini(page)).toBeHidden({ timeout: 20_000 });

  const me = await (await request.get(`${api}/me`, { headers })).json();
  const recent = await (
    await request.get(`${api}/profiles/${me.account.id}/music/recently-played?kind=album`, {
      headers,
    })
  ).json();
  expect(recent.items[0]?.album?.title).toBe("First Light");
});

test("Now playing keeps the stream's details behind the art, and a song goes into a playlist that plays", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await seedMusic(request, headers);
  await signIn(page);
  await openAlbum(page);
  await page.getByRole("main").getByRole("button", { name: "Play", exact: true }).click();
  await expect(mini(page)).toContainText("Opening");
  await mini(page).getByRole("button", { name: "Pause" }).click();
  await mini(page).getByTitle("Now playing").click();

  // The stream is not narrated; its details sit behind the art.
  const np = page.getByRole("region", { name: "Now playing" });
  await expect(np).not.toContainText("Direct play");
  await np.getByRole("button", { name: "File details" }).click();

  // The server's answer: a FLAC file the browser plays as it is.
  const details = np.getByRole("region", { name: "Audio details" });
  await expect(details.getByText("Lossless", { exact: true })).toBeVisible();
  await expect(details).toContainText("FLAC");
  await expect(details).toContainText("Direct play");
  await np.getByRole("button", { name: "File details" }).click();
  await expect(details).toBeHidden();

  await np.getByRole("button", { name: "Add to playlist" }).click();
  const add = page.getByRole("dialog", { name: "Add to playlist" });
  await add.getByLabel("New playlist").fill("Road trip");
  await add.getByRole("button", { name: "Create and add" }).click();
  await expect(np.getByRole("button", { name: "Added to Road trip" })).toBeVisible();

  await tab(page, "Music").click();
  await page
    .getByRole("navigation", { name: "View" })
    .getByRole("link", { name: "Playlists" })
    .click();
  await page.getByRole("link", { name: /Road trip/ }).click();
  await expect(page.getByRole("heading", { name: "Road trip" })).toBeVisible();
  const entries = page.getByRole("region", { name: "Entries" });
  await expect(entries).toContainText("Opening");
  await page.getByRole("main").getByRole("button", { name: "Play", exact: true }).click();
  await expect(mini(page)).toContainText("Opening");

  // Playing from a playlist stamps it, so Recently played can list it.
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const pls = await (
    await request.get(`${api}/profiles/${me.account.id}/playlists?kind=music`, { headers })
  ).json();
  const road = pls.items.find((p: { name: string }) => p.name === "Road trip");
  expect(road.itemCount).toBe(1);
  await expect
    .poll(
      async () =>
        (
          await (
            await request.get(`${api}/profiles/${me.account.id}/playlists/${road.id}`, {
              headers,
            })
          ).json()
        ).lastPlayedAt,
    )
    .toBeTruthy();

  await entries.getByRole("button", { name: "Remove Opening" }).click();
  await expect(entries).toContainText("Nothing here yet.");
  await page.getByRole("main").getByRole("button", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("link", { name: /Road trip/ })).toHaveCount(0);
});

const pick = (page: Page, name: RegExp | string) =>
  page.getByRole("main").getByRole("link", { name }).first();

/**
 * Makes the playlist the first time; a movie's or show's Add to playlist is in its More menu.
 */
async function addToList(page: Page, list: string, inMore = true) {
  if (inMore) {
    await page.getByRole("button", { name: "More" }).click();
    await page.getByRole("menuitem", { name: "Add to playlist" }).click();
  } else await page.getByRole("button", { name: "Add to playlist" }).click();
  const dlg = page.getByRole("dialog", { name: "Add to playlist" });
  await expect(dlg.getByText("Loading")).toBeHidden();
  const existing = dlg.getByRole("button", { name: list, exact: true });
  if (await existing.count()) await existing.click();
  else {
    await dlg.getByLabel("New playlist").fill(list);
    await dlg.getByRole("button", { name: "Create and add" }).click();
  }
  await expect(dlg).toBeHidden();
}

async function openList(page: Page, tabName: string, list: string) {
  await tab(page, tabName).click();
  await page
    .getByRole("navigation", { name: "View" })
    .getByRole("link", { name: "Playlists" })
    .click();
  await page.getByRole("link", { name: new RegExp(list) }).click();
  await expect(page.getByRole("heading", { name: list })).toBeVisible();
}

/** Plays the playlist's first row, runs it to its end and takes Up next; true for the second row. */
async function advances(page: Page) {
  await page.getByRole("main").getByRole("button", { name: "Play", exact: true }).click();
  await expect(page).toHaveURL(/#\/play\/[0-9a-f-]{36}\?playlist=[0-9a-f-]{36}&n=0$/);
  const video = () => page.evaluate(() => document.querySelector("video")?.duration ?? 0);
  await expect.poll(video, { timeout: 30_000 }).toBeGreaterThan(10);
  await page.evaluate(() => {
    const v = document.querySelector("video")!;
    v.currentTime = v.duration - 0.5;
  });
  await page.getByRole("button", { name: "Play now" }).click();
  await expect(page).toHaveURL(/&n=1$/);
  await expect(page.locator("video")).toBeVisible();
}

async function deleteList(
  request: APIRequestContext,
  headers: { Authorization: string },
  name: string,
) {
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const base = `${api}/profiles/${me.account.id}/playlists`;
  const all = await (await request.get(base, { headers })).json();
  for (const p of all.items.filter((x: { name: string }) => x.name === name))
    await request.delete(`${base}/${p.id}`, { headers });
}

test("a movies playlist plays its movies in order and moves on when one ends", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await signIn(page);
  await tab(page, "Movies").click();
  await pick(page, /Toy Story/).click();
  await addToList(page, "Double feature");
  await tab(page, "Movies").click();
  await pick(page, /h264-high41/).click();
  await addToList(page, "Double feature");

  await openList(page, "Movies", "Double feature");
  await expect(page.getByRole("region", { name: "Entries" }).locator(".qrow")).toHaveCount(2);
  await advances(page);

  await deleteList(request, headers, "Double feature");
});

test("a tv playlist holds a show as one row, plays its episodes and moves on", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await seedTV(request, headers);
  await signIn(page);
  await tab(page, "TV").click();
  await pick(page, /Signal Hour/).click();
  await addToList(page, "Binge");

  await openList(page, "TV", "Binge");
  const entries = page.getByRole("region", { name: "Entries" });
  await expect(entries.locator(".qrow")).toHaveCount(1);
  await expect(entries).toContainText("Signal Hour");
  await expect(entries).toContainText("2 episodes");
  await advances(page);

  await deleteList(request, headers, "Binge");
});

test("an episode's Next opens the next episode in place of it, so Back returns to the season", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await seedTV(request, headers);
  await signIn(page);
  await tab(page, "TV").click();
  await pick(page, /Signal Hour/).click();
  await pick(page, /Season 1/).click();
  await expect(page).toHaveURL(/#\/season\//);
  // An earlier spec may have watched it, which leads its name with Watched.
  await page.getByRole("region", { name: "Episodes" }).getByRole("link").first().click();
  await expect(page.getByRole("heading", { name: /^1\. / })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Previous episode/ })).toHaveCount(0);
  await page.getByRole("link", { name: "Next episode, S1 E2" }).click();
  await expect(page.getByRole("heading", { name: /^2\. / })).toBeVisible();
  await expect(page.getByRole("link", { name: "Previous episode, S1 E1" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Next episode/ })).toHaveCount(0);
  await page.goBack();
  await expect(page).toHaveURL(/#\/season\//);
});

test("a custom collection holds a movie, shows on its details, is one row in a movies playlist, and rows its other films", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await signIn(page);
  await tab(page, "Movies").click();
  await pick(page, /h264-high41/).click();
  await page.getByRole("button", { name: "More" }).click();
  await page.getByRole("menuitem", { name: "Add to collection" }).click();
  const dlg = page.getByRole("dialog", { name: "Add to collection" });
  await dlg.getByLabel("New collection").fill("Rainy day picks");
  await dlg.getByRole("button", { name: "Create and add" }).click();
  await page.getByRole("button", { name: "More" }).click();
  await expect(page.getByRole("menuitem", { name: "Added to Rainy day picks" })).toBeVisible();

  await tab(page, "Movies").click();
  await pick(page, /h264-high41/).click();
  await page.getByRole("main").getByRole("link", { name: "Rainy day picks" }).click();
  await expect(page.getByRole("heading", { name: "Rainy day picks" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Movies" }).locator(".qrow")).toHaveCount(1);
  await addToList(page, "Collected", false);

  await openList(page, "Movies", "Collected");
  const entries = page.getByRole("region", { name: "Entries" });
  await expect(entries.locator(".qrow")).toHaveCount(1);
  await expect(entries).toContainText("Rainy day picks");
  await expect(entries).toContainText("1 movie");

  await deleteList(request, headers, "Collected");
  const cols = await (await request.get(`${api}/collections`, { headers })).json();
  const rainy = cols.items.find((x: { name: string }) => x.name === "Rainy day picks");
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const other = movies.find((m: { parsedTitle: string }) => m.parsedTitle !== "h264-high41");
  await request.put(`${api}/collections/${rainy.id}/movies/${other.id}`, { headers });
  await tab(page, "Movies").click();
  await pick(page, /h264-high41/).click();
  await expect(
    page.getByRole("region", { name: "Rainy day picks" }).locator("a.poster"),
  ).toHaveAttribute("href", `#/movie/${other.id}`);

  for (const c of cols.items.filter((x: { name: string }) => x.name === "Rainy day picks"))
    await request.delete(`${api}/collections/${c.id}`, { headers });
});

test("two adults watch a movie together: join, play, pause and leave apply to both", async ({
  page,
  browser,
  request,
}) => {
  const headers = await seed(request);
  const helga = { name: "Helga", email: "helga.e2e@example.com" };
  await seedAdult(request, headers, helga);
  const other = await browser.newContext();
  await other.addInitScript(quiet);
  const guest = await other.newPage();
  try {
    await signIn(page);
    await signIn(guest, { email: helga.email, profile: "Helga" });

    const open = async (p: Page) => {
      await tab(p, "Movies").click();
      await p
        .locator("a.poster")
        .and(p.getByRole("link", { name: /h264-high41/ }))
        .click();
    };
    await open(page);
    await page.getByRole("button", { name: "Watch together" }).click();
    await expect(page.getByText(/Waiting for someone to join · \d:\d\d/)).toBeVisible();

    // The guest's details poll every 5 s until the invitation shows.
    await open(guest);
    await guest.getByRole("button", { name: /^Join Ada · \d:\d\d$/ }).click();
    await expect(guest.getByText("In sync with Ada")).toBeVisible();
    await expect(page.getByText("In sync with Helga")).toBeVisible({ timeout: 10_000 });

    // Play together on the host's details plays for both: each player opens
    // and starts at the one instant the server set.
    await page.getByRole("link", { name: /Play together/ }).click();
    await expect(page.getByText("In sync with Helga")).toBeVisible();
    await expect(guest).toHaveURL(/#\/play\//, { timeout: 10_000 });
    const time = (p: Page) => p.evaluate(() => document.querySelector("video")?.currentTime ?? 0);
    const paused = (p: Page) => p.evaluate(() => document.querySelector("video")?.paused ?? true);
    await expect.poll(() => time(guest), { timeout: 30_000 }).toBeGreaterThan(1);
    await expect.poll(() => time(page), { timeout: 30_000 }).toBeGreaterThan(1);

    // Two seconds into playing the controls fade and take no click; the pointer brings them back.
    await page.mouse.move(400, 300);
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect.poll(() => paused(guest), { timeout: 10_000 }).toBe(true);
    await expect(guest.getByText("Ada paused")).toBeVisible();
    // Both paused at one instant, where the pause was sent.
    await expect.poll(() => paused(page), { timeout: 10_000 }).toBe(true);
    expect(Math.abs((await time(guest)) - (await time(page)))).toBeLessThan(1);

    await page.getByRole("button", { name: "Leave" }).click();
    await expect(guest.getByText("Ada left")).toBeVisible({ timeout: 10_000 });
  } finally {
    // Leaving the players ends both sessions before the next spec runs.
    for (const p of [page, guest]) await p.evaluate(() => (location.hash = "#/")).catch(() => {});
    await guest.waitForTimeout(500);
    await other.close();
  }
});

test("a kid opens Watch together on a movie on their allow list and an adult joins", async ({
  page,
  browser,
  request,
}) => {
  const headers = await seed(request);
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const movie = movies.find((m: { parsedTitle: string }) => /h264-high41/.test(m.parsedTitle));
  const made = await request.post(`${api}/profiles`, {
    headers,
    data: { name: "Wren", birthday: "2018-05-01", filterMode: "allow_list" },
  });
  expect(made.ok()).toBeTruthy();
  const kid = await made.json();
  await request.put(`${api}/profiles/${kid.id}/allowed-items/${movie.id}`, { headers });
  const other = await browser.newContext();
  await other.addInitScript(quiet);
  const tablet = await other.newPage();
  try {
    await signIn(page);
    await signIn(tablet, { email, profile: "Wren" });
    const open = async (p: Page) => {
      await tab(p, "Movies").click();
      await p
        .locator("a.poster")
        .and(p.getByRole("link", { name: /h264-high41/ }))
        .click();
    };
    await open(tablet);
    await tablet.getByRole("button", { name: "Watch together" }).click();
    await expect(tablet.getByText(/Waiting for someone to join · \d:\d\d/)).toBeVisible();

    await open(page);
    await page.getByRole("button", { name: /^Join Wren · \d:\d\d$/ }).click();
    await expect(page.getByText("In sync with Wren")).toBeVisible();
    await expect(tablet.getByText("In sync with Ada")).toBeVisible({ timeout: 10_000 });

    await tablet.getByRole("button", { name: "Leave" }).click();
    await expect(page.getByRole("button", { name: "Watch together" })).toBeVisible({
      timeout: 10_000,
    });
  } finally {
    await other.close();
    await request.delete(`${api}/profiles/${kid.id}`, { headers });
  }
});
