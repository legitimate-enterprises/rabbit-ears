import { expect, type APIRequestContext, type Page } from "@playwright/test";
import { copyFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { API_BASE } from "../src/api/client";

export const fixtures = resolve(import.meta.dirname, "../../fixtures");
export const email = "ada@example.com";
export const password = "correct horse battery";
export const api = API_BASE;

// Audio still decodes and plays, at 1% so a run is not heard across the room.
export const quiet = () => {
  const play = HTMLMediaElement.prototype.play;
  HTMLMediaElement.prototype.play = function () {
    this.volume = 0.01;
    return play.call(this);
  };
};

type Headers = { Authorization: string };

/** Makes a library's scan task due now, once, if it has never run. */
export async function scanSoon(request: APIRequestContext, headers: Headers, libraryId: string) {
  const tasks = (await (await request.get(`${api}/tasks`, { headers })).json()).items;
  const scan = tasks.find((t: { kind: string }) => t.kind === `scan:${libraryId}`);
  if (scan && !scan.lastRun)
    await request.put(`${api}/tasks/${scan.id}`, {
      headers,
      data: { name: scan.name, kind: scan.kind, nextRunAt: new Date().toISOString() },
    });
}

// The admin spec may already have set the server up; either way end with an
// admin, a Movies library over the fixtures, and every file scanned.
export async function seed(request: APIRequestContext) {
  const health = await (await request.get(`${api}/health`)).json();
  const body = { email, password, deviceName: "e2e", platform: "web" };
  const signed = health.setupRequired
    ? await request.post(`${api}/setup`, { data: { ...body, serverName: "Den", name: "Ada" } })
    : await request.post(`${api}/auth/sign-in`, { data: body });
  expect(signed.ok()).toBeTruthy();
  const headers = { Authorization: `Bearer ${(await signed.json()).token}` };
  const libs = (await (await request.get(`${api}/libraries`, { headers })).json()).items;
  let movies = libs.find((l: { kind: string }) => l.kind === "movies");
  if (!movies) {
    movies = await (
      await request.post(`${api}/libraries`, { headers, data: { name: "Movies", kind: "movies" } })
    ).json();
    await request.post(`${api}/libraries/${movies.id}/folders`, {
      headers,
      data: { path: fixtures },
    });
  }
  await expect(async () => {
    await scanSoon(request, headers, movies.id);
    const items = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
    expect(items.length).toBeGreaterThanOrEqual(2);
    for (const i of items) {
      const files = (
        await (await request.get(`${api}/media-files?itemId=${i.id}`, { headers })).json()
      ).items;
      expect(files[0]?.tracks.length).toBeGreaterThan(0);
    }
  }).toPass({ timeout: 80_000, intervals: [2000] });
  return headers;
}

export async function seedMusic(request: APIRequestContext, headers: Headers) {
  const libs = (await (await request.get(`${api}/libraries`, { headers })).json()).items;
  let lib = libs.find((l: { kind: string }) => l.kind === "music");
  if (!lib) {
    lib = await (
      await request.post(`${api}/libraries`, { headers, data: { name: "Music", kind: "music" } })
    ).json();
    await request.post(`${api}/libraries/${lib.id}/folders`, {
      headers,
      data: { path: `${fixtures}/music` },
    });
  }
  await expect(async () => {
    await scanSoon(request, headers, lib.id);
    const songs = (await (await request.get(`${api}/music/songs`, { headers })).json()).items;
    expect(songs.length).toBe(4);
  }).toPass({ timeout: 60_000, intervals: [2000] });
}

/**
 * A TV library of one two-episode show. The files are copies: the Movies library already owns
 * the fixture paths, and a second library scanning them would take them from it.
 */
export async function seedTV(request: APIRequestContext, headers: Headers) {
  const root = join(tmpdir(), "rabbit-ears-e2e-tv");
  const season = join(root, "Signal Hour", "Season 01");
  mkdirSync(season, { recursive: true });
  for (const n of ["S01E01", "S01E02"])
    copyFileSync(
      join(fixtures, "library/Movies/Toy Story (1995)/Toy Story (1995).mp4"),
      join(season, `Signal Hour - ${n}.mp4`),
    );
  const libs = (await (await request.get(`${api}/libraries`, { headers })).json()).items;
  let lib = libs.find((l: { kind: string }) => l.kind === "tv");
  if (!lib) {
    lib = await (
      await request.post(`${api}/libraries`, { headers, data: { name: "TV", kind: "tv" } })
    ).json();
    await request.post(`${api}/libraries/${lib.id}/folders`, {
      headers,
      data: { path: root },
    });
  }
  await expect(async () => {
    await scanSoon(request, headers, lib.id);
    const eps = (await (await request.get(`${api}/items?kind=episode`, { headers })).json()).items;
    expect(eps.length).toBeGreaterThanOrEqual(2);
  }).toPass({ timeout: 80_000, intervals: [2000] });
}

/**
 * A Movies library of one movie in two editions. The files are copies, as for the TV library;
 * its name sorts after the fixtures' library.
 */
export async function seedEditions(request: APIRequestContext, headers: Headers) {
  const root = join(tmpdir(), "rabbit-ears-e2e-editions");
  const folder = join(root, "Paper Lanterns (2011)");
  mkdirSync(folder, { recursive: true });
  for (const name of ["Paper Lanterns (2011)", "Paper Lanterns (2011) {edition-Extended}"])
    copyFileSync(
      join(fixtures, "library/Movies/Toy Story (1995)/Toy Story (1995).mp4"),
      join(folder, `${name}.mp4`),
    );
  const libs = (await (await request.get(`${api}/libraries`, { headers })).json()).items;
  let lib = libs.find((l: { name: string }) => l.name === "Movies 2");
  if (!lib) {
    lib = await (
      await request.post(`${api}/libraries`, {
        headers,
        data: { name: "Movies 2", kind: "movies" },
      })
    ).json();
    await request.post(`${api}/libraries/${lib.id}/folders`, { headers, data: { path: root } });
  }
  let movie: { id: string; editions: { id: string; name: string }[] } | undefined;
  await expect(async () => {
    await scanSoon(request, headers, lib.id);
    const items = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
    const found = items.find((i: { parsedTitle: string }) => i.parsedTitle === "Paper Lanterns");
    expect(found).toBeTruthy();
    movie = await (await request.get(`${api}/items/${found.id}`, { headers })).json();
    expect(movie!.editions.map((e) => e.name)).toEqual(["Standard", "Extended"]);
  }).toPass({ timeout: 80_000, intervals: [2000] });
  return movie!;
}

/** A second adult in the owner's household, who has accepted their code. */
export async function seedAdult(
  request: APIRequestContext,
  headers: Headers,
  who: { name: string; email: string },
) {
  const added = await request.post(`${api}/household/adults`, { headers, data: who });
  if (added.status() === 409) return;
  expect(added.ok()).toBeTruthy();
  const { joinCode } = await added.json();
  const joined = await request.post(`${api}/join/${joinCode.code}`, {
    data: { name: who.name, password, deviceName: "e2e", platform: "web" },
  });
  expect(joined.ok()).toBeTruthy();
}

export async function signIn(page: Page, as = { email, profile: "Ada" }) {
  await page.goto("/");
  await page.getByLabel("Email").fill(as.email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Who's watching?" })).toBeVisible();
  await page.getByRole("link", { name: as.profile }).click();
}

// Closing a page skips the player's teardown, leaving its transcode holding
// one of the server's few slots until it is reaped; leave the player instead.
export async function leavePlayer({ page }: { page: Page }) {
  if (!page.url().includes("#/play/")) return;
  const ended = page
    .waitForResponse(
      (r) => r.request().method() === "DELETE" && r.url().includes("/playback/sessions/"),
      { timeout: 5_000 },
    )
    .catch(() => {});
  await page.evaluate(() => (location.hash = "#/"));
  await ended;
}
