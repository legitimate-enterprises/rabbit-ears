import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Snapshot } from "../src/viewer/capabilities";
import { api, leavePlayer, quiet, scanSoon, seed, signIn } from "./support";

test.describe.configure({ mode: "serial" });

test.beforeEach(({ page }) => page.addInitScript(quiet));

test.afterEach(leavePlayer);

async function openMovie(page: Page, name: RegExp) {
  await page
    .getByRole("navigation", { name: "Library" })
    .getByRole("link", { name: "Movies" })
    .click();
  // Home, still on screen for a moment, lists a movie in progress a second time.
  await page.locator("a.poster").and(page.getByRole("link", { name })).click();
}

const pill = (page: Page) => page.getByRole("button", { name: /Playback quality/ });
const played = async (page: Page, seconds: number) => {
  await expect
    .poll(() => page.evaluate(() => document.querySelector("video")?.currentTime ?? 0), {
      timeout: 30_000,
    })
    .toBeGreaterThan(seconds);
  // Two seconds into playing the controls fade and take no click; the pointer brings them back.
  await page.mouse.move(400, 300);
};

test("an adult plays a fixture, by direct play where the browser decodes it in hardware, and the position is saved", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const movies = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const itemId = movies.find((m: { parsedTitle: string }) => /h264-high41/.test(m.parsedTitle)).id;
  // A position an earlier spec or browser left near the 10 s fixture's end would resume into watched.
  await request.delete(`${api}/profiles/${me.account.id}/watch-state/${itemId}`, { headers });
  await signIn(page);
  // The server copies only what the client reports this browser decodes in hardware; one
  // that decodes H.264 in software, as on a machine without a GPU decoder, is given a
  // conversion. The report is the client's own, read from the module the page loaded.
  const { decoder } = (await page.evaluate(
    'import("/src/viewer/device.ts").then((m) => m.deviceSnapshot())',
  )) as Snapshot;
  const hardware = decoder.videoCodecs.some((c) => c.codec === "h264" && c.hardware);
  await openMovie(page, /h264-high41/);
  await expect(
    page.getByText(hardware ? "Direct play on this device" : "Converted for this device"),
  ).toBeVisible();
  await page.getByRole("link", { name: "Play", exact: true }).click();

  await expect(pill(page)).toContainText(hardware ? "Direct play" : "Video transcode");
  await played(page, 1);
  await page.getByRole("button", { name: "Pause" }).click();
  const at = await page.evaluate(() => document.querySelector("video")!.currentTime);

  await expect(async () => {
    const w = await request.get(`${api}/profiles/${me.account.id}/watch-state/${itemId}`, {
      headers,
    });
    expect((await w.json()).positionMs).toBeGreaterThan(Math.max(0, at * 1000 - 2000));
  }).toPass({ timeout: 10_000 });

  await page.getByRole("button", { name: "Back", exact: true }).click();
  await expect(page.getByRole("link", { name: "Resume" })).toBeVisible();
});

test("a started movie's poster and details show how much of it is watched", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  // The shared fixtures are all under a minute, which a scan gives no runtime; this movie runs 2.
  const dir = mkdtempSync(join(tmpdir(), "rabbit-ears-watched-"));
  execFileSync("ffmpeg", [
    ...["-v", "error", "-f", "lavfi", "-i", "color=size=64x36:rate=1:duration=120"],
    ...["-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p"],
    join(dir, "Long Walk (2003).mp4"),
  ]);
  const lib = await (
    await request.post(`${api}/libraries`, { headers, data: { name: "Walks", kind: "movies" } })
  ).json();
  try {
    await request.post(`${api}/libraries/${lib.id}/folders`, { headers, data: { path: dir } });
    let movie: { id: string } | undefined;
    await expect(async () => {
      await scanSoon(request, headers, lib.id);
      const items = (await (await request.get(`${api}/items?kind=movie`, { headers })).json())
        .items;
      movie = items.find(
        (i: { parsedTitle: string; metadata?: { runtimeMinutes?: number } }) =>
          i.parsedTitle === "Long Walk" && i.metadata?.runtimeMinutes === 2,
      );
      expect(movie).toBeTruthy();
    }).toPass({ timeout: 80_000, intervals: [2000] });
    await request.put(`${api}/profiles/${me.account.id}/watch-state/${movie!.id}`, {
      headers,
      data: { positionMs: 30_000, watched: false },
    });
    await signIn(page);
    await openMovie(page, /Long Walk/);
    await expect(page.getByRole("link", { name: "Resume" })).toBeVisible();
    await expect(
      page.locator(".detail").getByRole("img", { name: "25 percent watched" }),
    ).toBeVisible();
    await page.goBack();
    const poster = page.locator("a.poster").and(page.getByRole("link", { name: /Long Walk/ }));
    await expect(poster.getByRole("img", { name: "25 percent watched" })).toBeVisible();
  } finally {
    await request.delete(`${api}/libraries/${lib.id}`, { headers });
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a lower quality is decided by the server as a transcode and plays through HLS", async ({
  page,
  request,
}) => {
  await seed(request);
  await signIn(page);
  await openMovie(page, /hdr10-main10/);
  await page.getByRole("link", { name: /^(Play|Resume)$/ }).click();
  await pill(page).waitFor();
  await played(page, 1);

  await pill(page).click();
  const lower = page.getByRole("menuitemradio").filter({ hasText: "Transcode" }).first();
  await expect(lower).toBeVisible();
  await lower.click();
  await expect(pill(page)).toContainText(/Video transcode/);
  await played(page, 1);
});

test("copied video resumes, skips and scrubs on one timeline without falling back", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const me = await (await request.get(`${api}/me`, { headers })).json();
  const items = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
  const toy = items.find((i: { parsedTitle: string }) => i.parsedTitle === "Toy Story");
  await request.put(`${api}/profiles/${me.account.id}/watch-state/${toy.id}`, {
    headers,
    data: { positionMs: 25_890, watched: false },
  });
  const failures: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/events")) failures.push(r.url());
  });
  await signIn(page);
  await openMovie(page, /Toy Story/);
  await page
    .getByRole("link", { name: /^Resume/ })
    .first()
    .click();
  await played(page, 27);

  // Each seek must land near its target and keep playing from there.
  const seek = async (act: () => Promise<void>, near: number) => {
    await page.mouse.move(400, 300);
    await act();
    await expect
      .poll(() => page.evaluate(() => document.querySelector("video")?.currentTime ?? 0), {
        timeout: 30_000,
      })
      .toBeGreaterThan(near + 1);
    expect(await page.evaluate(() => document.querySelector("video")!.currentTime)).toBeLessThan(
      near + 8,
    );
  };
  await seek(() => page.getByRole("button", { name: "Forward 10 seconds" }).click(), 37);
  await seek(() => page.getByRole("button", { name: "Back 10 seconds" }).click(), 28);
  const bar = (await page.locator(".seek").boundingBox())!;
  await seek(() => page.mouse.click(bar.x + bar.width * 0.2, bar.y + bar.height / 2), 10);

  // A player anchored on a misplaced segment reports the wrong length.
  await expect
    .poll(() => page.evaluate(() => document.querySelector("video")!.duration), { timeout: 30_000 })
    .toBeCloseTo(51.1, 0);
  expect(failures).toEqual([]);
});

test("an AVI with MPEG-4 Part 2 video and MP3 audio plays through HLS", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  // The scan may still be on the TV folder.
  let guild: string | undefined;
  await expect(async () => {
    const items = (await (await request.get(`${api}/items?kind=movie`, { headers })).json()).items;
    for (const i of items) {
      const files = (
        await (await request.get(`${api}/media-files?itemId=${i.id}`, { headers })).json()
      ).items;
      if (files[0]?.path.includes("The Guild - S01E01")) guild = i.id;
    }
    expect(guild).toBeDefined();
  }).toPass({ timeout: 60_000, intervals: [2000] });
  await signIn(page);
  await expect(page.getByRole("navigation", { name: "Library" })).toBeVisible();
  await page.evaluate((id) => (location.hash = `#/play/${id}?pos=0`), guild);
  await pill(page).waitFor();
  await played(page, 2);
  await expect(page.getByText("No way to play this on this device.")).toBeHidden();
});

test("search finds a fixture by title as it is typed and opens its details", async ({
  page,
  request,
}) => {
  await seed(request);
  await signIn(page);
  const box = page.getByRole("searchbox", { name: "Search" });
  // Results come as typing pauses, and the field keeps the cursor.
  await box.fill("toy");
  const hit = page.getByRole("region", { name: "Movies" }).getByRole("link", { name: /Toy Story/ });
  await hit.waitFor();
  await expect(box).toBeFocused();
  await hit.click();
  await expect(page.getByRole("heading", { name: "Toy Story" })).toBeVisible();
  // The server's playability answer, not a guess in the browser.
  await expect(page.getByText(/^(Direct play on|Converted for) this device$/)).toBeVisible();
  // The fixtures carry no TMDB credits or collections, so nothing relates.
  await expect(page.locator(".detail + section")).toHaveAttribute("aria-label", "Details");
});

test("the owner adds a kid from Manage household, sets the kid's PIN and removes the kid", async ({
  page,
  request,
}, info) => {
  await seed(request);
  const name = `Robin ${info.project.name}`;
  await signIn(page);
  await page.getByRole("button", { name: "Ada" }).click();
  await page.getByRole("menuitem", { name: "Manage household" }).click();
  await page.getByRole("link", { name: "Add kid" }).click();
  await page.getByLabel("Name").fill(name);
  await page.getByLabel("Birthday", { exact: true }).fill("2016-05-01");
  await page.getByRole("button", { name: `Add ${name}` }).click();
  await expect(page.getByRole("heading", { name: "Household" })).toBeVisible();
  const row = page.getByRole("listitem").filter({ hasText: name });
  await expect(row).toContainText("Kid · Movies up to");
  await expect(row).not.toContainText("PIN");

  await row.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByRole("heading", { name: `Edit ${name}` })).toBeVisible();
  await page.getByRole("switch", { name: "Require a PIN" }).check();
  await page.getByLabel("PIN", { exact: true }).fill("2468");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(row).toContainText("PIN");

  await row.getByRole("button", { name: "Remove" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
  await expect(row).toHaveCount(0);
});

test("an occasion's wash fills the viewport, and the admin editor previews and saves it", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const path = `${api}/occasions/00000000-0000-7000-9000-000000000001`;
  const o = await (await request.get(path, { headers })).json();
  const put = await request.put(path, {
    headers,
    data: {
      name: o.name,
      holiday: o.holiday,
      daysBefore: o.daysBefore,
      daysAfter: o.daysAfter,
      keywordIds: o.keywordIds,
      colors: ["#1f6b45", "#b3263e", "#e0b252"],
      wash: { strength: 200, brightness: 15, sharpness: 0, reach: 100 },
    },
  });
  expect(put.ok()).toBeTruthy();
  await signIn(page);
  await page.getByRole("region", { name: "Recently added" }).waitFor();
  await page.evaluate((id) => (location.hash = `#/occasion/${id}`), o.id);
  const wash = page.locator("main .wash");
  // A browser that refused the gradient would compute none.
  await expect
    .poll(() =>
      wash.evaluate((e) => getComputedStyle(e).backgroundImage.split("radial-gradient").length - 1),
    )
    .toBe(3);
  expect(await wash.evaluate((e) => getComputedStyle(e).position)).toBe("fixed");
  const box = await wash.boundingBox();
  expect(box).toMatchObject({ x: 0, y: 0, ...page.viewportSize() });

  await page.goto(`/admin/#/occasions/${o.id}`);
  await page.getByLabel("Strength").fill("320");
  await expect
    .poll(() =>
      page
        .locator(".washpreview .wash")
        .evaluate((e) => getComputedStyle(e).backgroundImage.split("radial-gradient").length - 1),
    )
    .toBe(3);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Saved")).toBeVisible();
  const saved = await (await request.get(path, { headers })).json();
  expect(saved.wash).toEqual({ strength: 320, brightness: 15, sharpness: 0, reach: 100 });
});
