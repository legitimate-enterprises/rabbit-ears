import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { quiet, seed, seedMusic, signIn } from "./support";

test.describe.configure({ mode: "serial" });

test.beforeEach(({ page }) => page.addInitScript(quiet));

const api = "/api/v1";
type Headers = { Authorization: string };
const made: number[] = [];

// Stations go with the playlists they own, so later specs see a Home without an On now row.
test.afterEach(async ({ page, request }) => {
  await page.evaluate(() => (location.hash = "#/")).catch(() => {});
  const headers = await seed(request);
  for (const id of made.splice(0)) await request.delete(`${api}/stations/${id}`, { headers });
});

/**
 * Makes a station in the editor; fills and schedules it over the API, as Add to station and
 * Reschedule now would.
 */
async function makeStation(
  page: Page,
  request: APIRequestContext,
  headers: Headers,
  kind: string,
  itemId: number,
  name: string,
) {
  // Signing in lands on Home; navigating before it has would be undone by it.
  await expect(page.getByRole("main").getByRole("region").first()).toBeVisible();
  await page.evaluate((k) => (location.hash = `#/station-new?kind=${k}`), kind);
  await page.getByLabel("Name").fill(name);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name })).toBeVisible();
  const id = Number(/#\/station\/(\d+)/.exec(page.url())![1]);
  made.push(id);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const st = await (await request.get(`${api}/stations/${id}`, { headers })).json();
  const entries = `${api}/profiles/${me.account.id}/playlists/${st.playlistId}/entries`;
  expect((await request.post(entries, { headers, data: { itemId } })).ok()).toBeTruthy();
  expect((await request.post(`${api}/stations/${id}/reschedule`, { headers })).ok()).toBeTruthy();
  await page.reload();
  await expect(page.getByRole("heading", { name })).toBeVisible();
  return id;
}

test("a music station is on now and tunes in live, with no pause or seek", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  await seedMusic(request, headers);
  const albums = (await (await request.get(`${api}/music/albums`, { headers })).json()).items;
  const album = albums.find((a: { title: string }) => a.title === "First Light");
  await signIn(page);
  const stationId = await makeStation(page, request, headers, "music", album.id, "Kitchen Radio");

  // A music station plays songs back to back from the moment it is made.
  await page.evaluate(() => (location.hash = "#/"));
  const onNow = page.getByRole("region", { name: "On now" });
  await expect(onNow.getByRole("link", { name: /Kitchen Radio/ })).toBeVisible();

  // Its On now tile tunes in at once rather than opening the station.
  const tuned = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith(`/stations/${stationId}/playback`),
  );
  await onNow.getByRole("link", { name: /Kitchen Radio/ }).click();
  expect((await tuned).postDataJSON()).toHaveProperty("snapshot");

  const mini = page.getByRole("region", { name: "Music player" });
  await expect(mini).toContainText("Kitchen Radio");
  await expect(mini.getByRole("button", { name: "Stop" })).toBeVisible();
  for (const gone of ["Pause", "Play", "Previous", "Next"])
    await expect(mini.getByRole("button", { name: gone, exact: true })).toHaveCount(0);
  await expect(mini.getByRole("slider", { name: "Seek" })).toHaveCount(0);
  await expect(
    page.getByRole("region", { name: "Now playing" }).getByRole("button", { name: "Stop" }),
  ).toBeVisible();

  // Each fixture song is 3 s: when one ends the station is asked again for the next.
  await page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith(`/stations/${stationId}/playback`),
    { timeout: 15_000 },
  );

  const ended = page.waitForResponse(
    (r) => r.request().method() === "DELETE" && r.url().includes("/playback/sessions/"),
  );
  await mini.getByRole("button", { name: "Stop" }).click();
  await ended;
});

test("a movie station plays as one live stream, its ident up to the next program", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const toy = movies.find((m: { parsedTitle: string }) => m.parsedTitle === "Toy Story");
  await signIn(page);
  const stationId = await makeStation(page, request, headers, "movies", toy.id, "The Big Screen");
  await expect(page.getByText(/Movies station/)).toBeVisible();

  const tuned = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith(`/stations/${stationId}/playback`),
  );
  const playlist = page.waitForResponse(
    (r) => /\/channels\/[^/]+\/live\.m3u8$/.test(r.url()) && r.ok(),
  );
  // An ident's segments are under i…, a program's under p….
  const segment = page.waitForResponse(
    (r) => /\/channels\/[^/]+\/[ip][^/]+\/\d+\.m4s$/.test(r.url()) && r.ok(),
    { timeout: 60_000 },
  );
  await page.getByRole("link", { name: "Tune in" }).click();
  await expect(page).toHaveURL(new RegExp(`#/tune/${stationId}$`));
  expect((await tuned).postDataJSON()).toMatchObject({ live: true });
  await playlist;
  await segment;
  // Off the hour the stream is the station's ident; on it, the movie. The
  // line fades with the controls, so the pointer brings it back.
  await page.mouse.move(400, 300);
  await expect(page.getByText(/^(Up next at .+|On now): Toy Story/)).toBeVisible();
  const video = page.locator("video");
  const time = () => video.evaluate((v: HTMLVideoElement) => v.currentTime);
  await expect
    .poll(() => video.evaluate((v: HTMLVideoElement) => v.videoWidth), { timeout: 60_000 })
    .toBeGreaterThan(0);
  const began = await time();
  await expect.poll(time, { timeout: 30_000 }).toBeGreaterThan(began + 3);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Pause" })).toHaveCount(0);
  await expect(page.getByRole("slider", { name: "Seek" })).toHaveCount(0);
});
