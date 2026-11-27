// Component tests for stations: the real pages, player, music engine and
// generated client against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { music } from "./audio";
import { localDate } from "./stations";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label);
const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id: uid(id),
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  modifiedAt: ts,
});
const me = (active: number) => ({
  device: { id: uid(1) },
  account: acct(1, "Heidi"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Heidi", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Heidi"), acct(2, "Jax", "kid"), acct(3, "Sam")],
});
const page = (items: unknown[]) => ({ items });
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  [`GET /profiles/${uid(1)}/watch-state`]: page([]),
  [`GET /profiles/${uid(2)}/watch-state`]: page([]),
  [`GET /profiles/${uid(1)}/show-progress`]: page([]),
  [`GET /profiles/${uid(2)}/show-progress`]: page([]),
  "GET /genres": page([]),
  "GET /items": page([]),
  "GET /youtube/videos": page([]),
  "GET /media-files": page([]),
  [`GET /profiles/${uid(1)}/playlists/${uid(9)}/entries`]: page([]),
  [`GET /profiles/${uid(1)}/playlists/${uid(9)}/play-order`]: page([]),
  [`GET /profiles/${uid(1)}/playlists/${uid(9)}`]: { id: uid(9), kind: "movies", access: "owner" },
});

const later = (min: number) => new Date(Date.now() + min * 60000).toISOString();
const item = (id: number, name: string) => ({
  id: uid(id),
  libraryId: uid(1),
  kind: "movie",
  parsedTitle: name,
  addedAt: ts,
  createdAt: ts,
  modifiedAt: ts,
});
const program = (id: number, name: string, startsAt: string, endsAt = later(60)) => ({
  itemId: uid(id),
  editionId: uid(id * 10),
  kind: "movie",
  startsAt,
  endsAt,
  durationMs: Date.parse(endsAt) - Date.parse(startsAt),
  item: item(id, name),
});
const tune = (id: number, title: string, albumId = 5) => ({
  id: uid(id),
  title,
  artistId: uid(4),
  artistName: "The Hollow Pines",
  libraryId: uid(3),
  albumArtistId: uid(4),
  lossless: true,
  albumId: uid(albumId),
  albumTitle: "Low Country",
  durationMs: 200_000,
  addedAt: ts,
});
const songProgram = (id: number, title: string, startsAt: string, endsAt = later(3)) => ({
  itemId: uid(id),
  editionId: uid(id * 10),
  kind: "song",
  startsAt,
  endsAt,
  durationMs: 200_000,
  song: tune(id, title),
});
const onNow = (
  now: object = program(30, "The Lantern Keeper", later(-30)),
  next: object = program(31, "Salt and Ember", later(40)),
) => ({
  status: "on_air",
  now,
  positionMs: 1_800_000,
  next,
  upNext: [next],
});
const station = (id: number, name: string, extra: Record<string, unknown> = {}) => ({
  id: uid(id),
  name,
  kind: "movies",
  ownerId: uid(1),
  householdId: uid(1),
  playlistId: uid(9),
  order: "playlist",
  visibility: "household",
  timing: "hour",
  days: [],
  times: [],
  onAir: onNow(),
  createdAt: ts,
  modifiedAt: ts,
  ...extra,
});
const playlist = (id: number, name: string, kind = "movies") => ({
  id: uid(id),
  profileId: uid(1),
  access: "owner",
  name,
  kind,
  itemCount: 2,
  durationMs: 0,
  createdAt: ts,
  modifiedAt: ts,
});
const stream = (sessionId: number) => ({
  sessionId: uid(sessionId),
  entry: {
    position: 0,
    verdict: "direct_play",
    videoAction: "copy",
    audioAction: "copy",
    subtitleAction: "none",
    bad: false,
  },
  streamUrl: `/api/v2/streams/k${sessionId}/0/file`,
  streamKind: "file",
  positionMs: 1_800_000,
  negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
  rungs: [],
  sidecars: [],
});

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  setToken("tok");
  document.body.replaceChildren();
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  HTMLMediaElement.prototype.play = async () => {};
  HTMLMediaElement.prototype.pause = () => {};
  Object.defineProperty(navigator, "mediaCapabilities", {
    configurable: true,
    value: { decodingInfo: async () => ({ supported: false }) },
  });
});

describe("Stations lists", () => {
  const list = (_: unknown, u: URL) =>
    page(
      u.searchParams.get("kind") === "music"
        ? [
            station(5, "Kitchen Radio", {
              kind: "music",
              onAir: onNow(
                songProgram(40, "Paper Lanterns", later(-1)),
                songProgram(41, "Slow Tide", later(2)),
              ),
            }),
          ]
        : [
            station(4, "The Big Screen"),
            station(6, "Small Wonders", {
              onAir: {
                status: "between_programs",
                next: program(32, "Moon Pie", later(30)),
                upNext: [program(32, "Moon Pie", later(30))],
              },
            }),
            station(7, "Creature Double", { onAir: { status: "off_schedule", upNext: [] } }),
          ],
    );

  it("lists a kind's stations as 16:9 tiles drawn from what is on, for an adult who can make one", async () => {
    await at("#/movies?view=stations");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), "GET /stations": list }) });
    await until(() => r.querySelector(".grid .wide"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual([
      "Movies",
      "Collections",
      "Playlists",
      "Stations",
    ]);
    expect(calls.find((c) => c.key === "GET /stations")!.query).toContain("kind=movies");
    expect(text(r.querySelectorAll(".grid .wide b"))).toEqual([
      "The Big Screen",
      "Small Wonders",
      "Creature Double",
    ]);
    const tiles = [...r.querySelectorAll(".grid .wide")];
    expect(tiles[0].getAttribute("href")).toBe(`#/station/${uid(4)}`);
    expect(tiles[0].querySelector(".art")!.getAttribute("data-art")).toBe(`${uid(30)}/backdrop`);
    expect(text(tiles[0].querySelectorAll(".sub"))[0]).toBe("On now: The Lantern Keeper");
    expect(tiles[1].querySelector(".art")!.getAttribute("data-art")).toBe(`${uid(32)}/backdrop`);
    expect(text(tiles[1].querySelectorAll(".sub"))[0]).toMatch(/^Up next at .+: Moon Pie$/);
    // Nothing on or next: the station's own tile, carrying its name.
    expect(tiles[2].querySelector(".art")!.hasAttribute("data-art")).toBe(false);
    expect(tiles[2].querySelector(".art")!.textContent).toBe("Creature Double");
    expect(button(r, "New station")!.getAttribute("href")).toBe("#/station-new?kind=movies");
  });

  it("lists them for a kid, with no Playlists and no way to make one", async () => {
    await at("#/movies?view=stations");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(2), "GET /stations": list }) });
    await until(() => r.querySelector(".grid .wide"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual(["Movies", "Collections", "Stations"]);
    expect(button(r, "New station")).toBeUndefined();
  });

  it("gives TV a Stations tab", async () => {
    await at("#/tv?view=stations");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), "GET /stations": list }) });
    await until(() => r.querySelector(".grid .wide"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual(["Shows", "Playlists", "Stations"]);
    expect(r.querySelector(".vt [aria-current=page]")!.getAttribute("href")).toBe(
      "#/tv?view=stations",
    );
    expect(calls.find((c) => c.key === "GET /stations")!.query).toContain("kind=tv");
  });

  it("lists music stations as square tiles, the next song with no time", async () => {
    await at("#/music?view=stations");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), "GET /stations": list }) });
    await until(() => r.querySelector(".grid.sq .poster"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual([
      "Albums",
      "Artists",
      "Songs",
      "Playlists",
      "Stations",
    ]);
    expect(r.querySelector(".grid .art.sq")!.getAttribute("data-art")).toBe(`${uid(5)}/poster`);
    expect(text(r.querySelectorAll(".grid .poster .sub"))).toEqual([
      "On now: Paper Lanterns",
      "Next: Slow Tide",
    ]);
  });
});

describe("Home's On now row", () => {
  it("leads Home without a hero and lists only stations with something on, kinds mixed", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": (_: unknown, u: URL) =>
          page(u.searchParams.get("kind") === "movie" ? [item(30, "Dune")] : []),
        "GET /stations": page([
          station(4, "The Big Screen"),
          station(6, "Small Wonders", {
            onAir: {
              status: "between_programs",
              next: program(32, "Moon Pie", later(30)),
              upNext: [program(32, "Moon Pie", later(30))],
            },
          }),
          station(5, "Kitchen Radio", {
            kind: "music",
            onAir: onNow(
              songProgram(40, "Paper Lanterns", later(-1)),
              songProgram(41, "Slow Tide", later(2)),
            ),
          }),
        ]),
      }),
    });
    await until(() => r.querySelector("section[aria-label='On now']"));
    const labels = [...r.querySelectorAll("main > section")].map((s) =>
      s.getAttribute("aria-label"),
    );
    expect(labels[0]).toBe("On now");
    const row = r.querySelector("section[aria-label='On now']")!;
    expect(text(row.querySelectorAll(".strip > a > b"))).toEqual([
      "Movies: The Big Screen",
      "Music: Kitchen Radio",
    ]);
    // 30 of the movie's 90 minutes are gone.
    expect(row.querySelector(".strip > a .art .bar > div")!.getAttribute("style")).toMatch(
      /^width: 33\.3/,
    );
    expect(row.querySelectorAll(".art .bar")).toHaveLength(2);
    // A tile tunes in and says so; its arrow shows over the art.
    const tiles = [...row.querySelectorAll(".strip > a")];
    expect(tiles.map((t) => t.getAttribute("href"))).toEqual([
      `#/tune/${uid(4)}`,
      `#/tune/${uid(5)}`,
    ]);
    expect(tiles.map((t) => t.getAttribute("aria-label"))).toEqual([
      "Watch Movies: The Big Screen",
      "Listen to Music: Kitchen Radio",
    ]);
    expect(row.querySelectorAll(".art .playmark")).toHaveLength(2);
    expect(calls.find((c) => c.key === "GET /stations")!.query).not.toContain("kind=");
  });

  it("sits before Recently added, and New from your channels comes last", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": (_: unknown, u: URL) =>
          page(u.searchParams.get("kind") === "movie" ? [item(30, "Dune")] : []),
        "GET /youtube/videos": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("unwatched")
              ? [
                  {
                    id: uid(50),
                    libraryId: uid(5),
                    channelId: uid(40),
                    channelTitle: "Workbench Weekly",
                    title: "Shelves",
                    durationMs: 60000,
                    publishedOn: ts.slice(0, 10),
                    watched: false,
                    positionMs: 0,
                    addedAt: ts,
                  },
                ]
              : [],
          ),
        "GET /stations": page([station(4, "The Big Screen")]),
      }),
    });
    await until(() => r.querySelector("section[aria-label='New from your channels']"));
    const labels = [...r.querySelectorAll("main > section")].map((s) =>
      s.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["On now", "Recently added", "New from your channels"]);
  });

  it("is absent when nothing is on", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": (_: unknown, u: URL) =>
          page(u.searchParams.get("kind") === "movie" ? [item(30, "Dune")] : []),
        "GET /stations": page([
          station(7, "Creature Double", { onAir: { status: "off_schedule", upNext: [] } }),
        ]),
      }),
    });
    await until(() => r.querySelector("section[aria-label='Recently added']"));
    expect(r.querySelector("section[aria-label='On now']")).toBeNull();
  });
});

describe("Station page", () => {
  const lantern = program(30, "The Lantern Keeper", later(-30));
  const entry = (id: number, itemId: number, name: string) => ({
    id: uid(id),
    playlistId: uid(9),
    position: id - 1,
    kind: "movie",
    itemId: uid(itemId),
    item: item(itemId, name),
    itemCount: 1,
    durationMs: 0,
  });
  let entries = [entry(1, 30, "The Lantern Keeper"), entry(2, 31, "Salt and Ember")];
  const routes = (extra: Record<string, unknown> = {}) => ({
    [`GET /profiles/${uid(1)}/playlists/${uid(9)}/entries`]: () => page(entries),
    [`GET /profiles/${uid(1)}/playlists/${uid(9)}/play-order`]: page([]),
    [`GET /stations/${uid(4)}`]: station(4, "The Big Screen", { onAir: onNow(lantern), ...extra }),
    [`GET /stations/${uid(4)}/schedule`]: page([
      program(29, "Northbound", later(-120), later(-30)),
      lantern,
      program(31, "Salt and Ember", later(40)),
    ]),
  });

  it("shows type, rating, visibility, Tune in and today's schedule, and Edit for its owner", async () => {
    await at(`#/station/${uid(4)}`);
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), ...routes() }) });
    await until(() => r.querySelector("h1")?.textContent === "The Big Screen");
    expect(text(r.querySelectorAll(".info .meta > *"))).toEqual([
      "Movies station",
      "None",
      "Household",
    ]);
    expect(button(r, "Tune in")!.getAttribute("href")).toBe(`#/tune/${uid(4)}`);
    expect(button(r, "Edit station")!.getAttribute("href")).toBe(`#/station-edit/${uid(4)}`);
    expect(calls.find((c) => c.key === `GET /stations/${uid(4)}/schedule`)!.query).toContain(
      `date=${localDate()}`,
    );
    const rows = [...r.querySelectorAll("section[aria-label='Today'] li")];
    expect(rows.map((li) => li.querySelector("b")!.textContent)).toEqual([
      "Northbound",
      "The Lantern Keeper",
      "Salt and Ember",
    ]);
    expect(rows[1].getAttribute("aria-current")).toBe("true");
    expect(rows[1].textContent).toContain("On now");
  });

  it("lists what the station plays and edits it like a playlist, for its owner", async () => {
    entries = [entry(1, 30, "The Lantern Keeper"), entry(2, 31, "Salt and Ember")];
    await at(`#/station/${uid(4)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...routes(),
        [`DELETE /profiles/${uid(1)}/playlists/${uid(9)}/entries/${uid(1)}`]: () => {
          entries = [entries[1]];
          return none();
        },
        [`PUT /profiles/${uid(1)}/playlists/${uid(9)}/entries/${uid(2)}`]: none,
      }),
    });
    const contents = "section[aria-label='Contents']";
    await until(() => r.querySelector(`${contents} li`));
    expect(text(r.querySelectorAll(`${contents} li .tr b`))).toEqual([
      expect.stringContaining("The Lantern Keeper"),
      expect.stringContaining("Salt and Ember"),
    ]);
    r.querySelectorAll(`${contents} li .tr`)[1].dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", altKey: true, bubbles: true }),
    );
    await until(() =>
      calls.some((c) => c.key === `PUT /profiles/${uid(1)}/playlists/${uid(9)}/entries/${uid(2)}`),
    );
    expect(
      calls.find((c) => c.key === `PUT /profiles/${uid(1)}/playlists/${uid(9)}/entries/${uid(2)}`)!
        .body,
    ).toEqual({
      itemId: uid(31),
      position: 0,
    });
    r.querySelector<HTMLElement>(`${contents} [aria-label='Remove The Lantern Keeper']`)!.click();
    await until(() => r.querySelectorAll(`${contents} li`).length === 1);
  });

  it("shows the rating and offers no Edit to another adult of the household", async () => {
    await at(`#/station/${uid(4)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(3),
        ...routes({ rating: "PG-13" }),
        [`GET /profiles/${uid(3)}/watch-state`]: page([]),
      }),
    });
    await until(() => r.querySelector("h1")?.textContent === "The Big Screen");
    expect(text(r.querySelectorAll(".info .meta > *"))).toEqual([
      "Movies station",
      "PG-13",
      "Household",
    ]);
    expect(button(r, "Edit station")).toBeUndefined();
    expect(r.querySelector("section[aria-label='Contents']")).toBeNull();
    expect(calls.some((c) => c.key.includes("/playlists/"))).toBe(false);
  });
});

describe("music station page", () => {
  it("lists no day of songs, only what is on and next", async () => {
    await at(`#/station/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(5)}`]: station(5, "Kitchen Radio", {
          kind: "music",
          onAir: onNow(
            songProgram(40, "Paper Lanterns", later(-1)),
            songProgram(41, "Slow Tide", later(2)),
          ),
        }),
      }),
    });
    await until(() => r.querySelector("h1")?.textContent === "Kitchen Radio");
    expect(r.querySelector(".info .sub")!.textContent).toBe(
      "On now: Paper Lanterns · Next: Slow Tide",
    );
    expect(r.querySelector("section[aria-label='Today']")).toBeNull();
    expect(calls.some((c) => c.key === `GET /stations/${uid(5)}/schedule`)).toBe(false);
  });
});

describe("Station editor", () => {
  const editorRoutes = {
    [`GET /profiles/${uid(1)}/playlists`]: page([
      playlist(9, "Movie night"),
      playlist(10, "Creature features"),
    ]),
    [`GET /profiles/${uid(1)}/playlists/${uid(10)}`]: playlist(10, "Creature features"),
    [`GET /profiles/${uid(1)}/playlists/${uid(10)}/entries`]: page([]),
    [`GET /profiles/${uid(1)}/playlists/${uid(10)}/play-order`]: page([]),
    [`GET /stations/${uid(8)}`]: station(8, "Creature Double"),
    [`GET /stations/${uid(8)}/schedule`]: page([]),
  };

  it("makes a station of its own: name, kind chosen once, order, visibility and schedule", async () => {
    await at("#/movies?view=stations");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...editorRoutes,
        "GET /stations": page([]),
        "POST /stations": station(8, "Creature Double", { kind: "tv" }),
      }),
    });
    await until(() => button(r, "New station"));
    button(r, "New station")!.click();
    await until(() => r.querySelector("form.station"));
    const f = r.querySelector("form.station")!;
    expect(f.querySelector("select[name=playlistId]")).toBeNull();
    expect(calls.some((c) => c.key === `GET /profiles/${uid(1)}/playlists`)).toBe(false);
    const kind = f.querySelector<HTMLSelectElement>("select[name=kind]")!;
    expect(kind.value).toBe("movies");
    expect(text(kind.querySelectorAll("option"))).toEqual(["Movies", "TV", "Music"]);
    expect(text(f.querySelectorAll("select[name=order] option"))).toEqual(["In order", "Shuffled"]);
    kind.value = "tv";
    kind.dispatchEvent(new Event("change"));
    expect(f.querySelector<HTMLSelectElement>("select[name=timing]")!.value).toBe("half_hour");
    f.querySelector<HTMLInputElement>("input[name=name]")!.value = "Creature Double";
    f.querySelector<HTMLSelectElement>("select[name=order]")!.value = "random";
    f.querySelector("select[name=order]")!.dispatchEvent(new Event("change"));
    // Chosen days and times: Friday and Saturday, 10 PM to 2 AM.
    f.querySelector<HTMLInputElement>("input[value=chosen]")!.click();
    button(f, "Fri")!.click();
    button(f, "Sat")!.click();
    button(f, "Add a time range")!.click();
    const [from, to] = f.querySelectorAll<HTMLInputElement>("input[type=time]");
    from.value = "22:00";
    from.dispatchEvent(new Event("input"));
    to.value = "02:00";
    to.dispatchEvent(new Event("input"));
    (f as HTMLFormElement).requestSubmit();
    await until(() => location.hash === `#/station/${uid(8)}`);
    expect(calls.find((c) => c.key === "POST /stations")!.body).toEqual({
      name: "Creature Double",
      kind: "tv",
      order: "random",
      visibility: "private",
      timing: "half_hour",
      days: ["friday", "saturday"],
      times: [{ from: "22:00", to: "02:00" }],
    });
  });

  it("offers no Make a station on a playlist", async () => {
    await at(`#/playlist/${uid(10)}`);
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), ...editorRoutes }) });
    await until(() => button(r, "Rename"));
    expect(button(r, "Make a station")).toBeUndefined();
  });

  it("shows a 400's field errors on their fields", async () => {
    await at("#/station-new?kind=movies");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...editorRoutes,
        "POST /stations": json(
          {
            code: "bad_request",
            message: "invalid",
            fields: [
              { field: "kind", message: "must be movies, tv or music" },
              { field: "times[0].to", message: "must be HH:MM" },
            ],
          },
          400,
        ),
      }),
    });
    await until(() => r.querySelector("form.station"));
    const f = r.querySelector<HTMLFormElement>("form.station")!;
    f.querySelector<HTMLInputElement>("input[name=name]")!.value = "X";
    f.requestSubmit();
    await until(() => f.querySelector("[data-for=kind]:not([hidden])"));
    expect(f.querySelector("[data-for=kind]")!.textContent).toBe("must be movies, tv or music");
    expect(f.querySelector("[data-for=times]")!.textContent).toBe("must be HH:MM");
    expect(f.querySelector("[data-for=name]")!.hasAttribute("hidden")).toBe(true);
  });

  it("edits a server station without its visibility, asks to reschedule and deletes on confirmation", async () => {
    await at(`#/station-edit/${uid(8)}`);
    const r = root();
    const server = station(8, "Westerns", { visibility: "server", rating: "PG-13" });
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...editorRoutes,
        [`GET /stations/${uid(8)}`]: server,
        [`PUT /stations/${uid(8)}`]: server,
        [`POST /stations/${uid(8)}/reschedule`]: server,
        [`DELETE /stations/${uid(8)}`]: none,
        "GET /stations": page([]),
      }),
    });
    await until(() => r.querySelector("form.station"));
    const f = r.querySelector<HTMLFormElement>("form.station")!;
    expect(f.querySelector<HTMLInputElement>("input[name=visibility]")!.readOnly).toBe(true);
    expect(f.querySelector<HTMLInputElement>("input[name=visibility]")!.value).toBe("Server");
    expect(f.textContent).toContain("Recalculated daily");
    expect(f.querySelector(".rating .tag")!.textContent).toBe("PG-13");
    f.querySelector<HTMLSelectElement>("select[name=timing]")!.value = "continuous";
    f.requestSubmit();
    await until(() => document.querySelector("[role=dialog]"));
    const ask = document.querySelector("[role=dialog]")!;
    expect(ask.textContent).toContain("Reschedule now?");
    expect(calls.some((c) => c.key === `POST /stations/${uid(8)}/reschedule`)).toBe(false);
    button(ask, "Now")!.click();
    await until(() => location.hash === `#/station/${uid(8)}`);
    const put = calls.find((c) => c.key === `PUT /stations/${uid(8)}`)!;
    expect(put.body).toHaveProperty("timing", "continuous");
    expect(put.body).not.toHaveProperty("visibility");
    expect(put.body).not.toHaveProperty("playlistId");
    expect(f.querySelector("select[name=kind]")).toBeNull();
    expect(calls.findIndex((c) => c.key === `PUT /stations/${uid(8)}`)).toBeLessThan(
      calls.findIndex((c) => c.key === `POST /stations/${uid(8)}/reschedule`),
    );

    await at(`#/station-edit/${uid(8)}`);
    await until(() => button(r, "Delete station"));
    button(r, "Delete station")!.click();
    const dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.textContent).toContain("Delete Westerns?");
    button(dlg, "Delete")!.click();
    await until(() => location.hash === "#/movies?view=stations");
    expect(calls.some((c) => c.key === `DELETE /stations/${uid(8)}`)).toBe(true);
  });

  it("is not offered to a kid", async () => {
    await at(`#/station-edit/${uid(8)}`);
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(2), ...editorRoutes }) });
    await until(() => r.querySelector("main")!.textContent !== "Loading");
    expect(r.querySelector("form.station")).toBeNull();
  });
});

describe("deleting a playlist a station plays", () => {
  it("says a station plays it", async () => {
    await at(`#/playlist/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /profiles/${uid(1)}/playlists/${uid(10)}`]: playlist(10, "Creature features"),
        [`GET /profiles/${uid(1)}/playlists/${uid(10)}/entries`]: page([]),
        [`GET /profiles/${uid(1)}/playlists/${uid(10)}/play-order`]: page([]),
        [`DELETE /profiles/${uid(1)}/playlists/${uid(10)}`]: json(
          { code: "conflict", message: "in use" },
          409,
        ),
      }),
    });
    await until(() => button(r, "Delete"));
    button(r, "Delete")!.click();
    const dlg = document.querySelector("[role=dialog]")!;
    button(dlg, "Delete")!.click();
    await until(() => dlg.querySelector(".err:not([hidden])"));
    expect(dlg.querySelector(".err")!.textContent).toBe(
      "A station plays this playlist. Delete the station or give it another playlist first.",
    );
  });
});

describe("tuned in to a video station", () => {
  it("starts on a click or Space when the browser refused to autoplay, and never pauses", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: station(4, "The Big Screen", { onAir: onNow() }),
        [`POST /stations/${uid(4)}/playback`]: { stream: stream(7), onAir: onNow() },
        [`PUT /playback/sessions/${uid(7)}/position`]: none,
        [`DELETE /playback/sessions/${uid(7)}`]: none,
      }),
    });
    await until(() => r.querySelector(".player video"));
    const video = r.querySelector("video")!;
    const play = vi.spyOn(video, "play").mockResolvedValue();
    const pause = vi.spyOn(video, "pause");
    const space = () =>
      document.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    video.click();
    space();
    expect(play).toHaveBeenCalledTimes(2);
    Object.defineProperty(video, "paused", { configurable: true, get: () => false });
    video.click();
    space();
    expect(play).toHaveBeenCalledTimes(2);
    expect(pause).not.toHaveBeenCalled();
  });

  it("plays the live stream with no pause, seek or skip, and asks again when the stream ends", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    let n = 0;
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: () =>
          station(4, "The Big Screen", {
            onAir:
              n < 2
                ? onNow()
                : {
                    status: "between_programs",
                    next: program(31, "Salt and Ember", later(40)),
                    upNext: [program(31, "Salt and Ember", later(40))],
                  },
          }),
        [`POST /stations/${uid(4)}/playback`]: () => {
          n++;
          return n === 1
            ? { stream: { ...stream(7), live: true }, onAir: onNow() }
            : json({ code: "off_air", message: "off", resumesAt: later(40) }, 409);
        },
        [`PUT /playback/sessions/${uid(7)}/position`]: none,
        [`DELETE /playback/sessions/${uid(7)}`]: none,
      }),
    });
    await until(() => r.querySelector(".player video"));
    const p = r.querySelector(".player")!;
    expect(p.querySelector(".ph .t")!.textContent).toBe("The Big Screen");
    expect(p.querySelector(".ph .sub")!.textContent).toBe("On now: The Lantern Keeper");
    expect(p.textContent).not.toContain("Next: ");
    for (const gone of ["Play", "Pause", "Back 10 seconds", "Forward 10 seconds", "Next"])
      expect(p.querySelector(`[aria-label='${gone}']`)).toBeNull();
    expect(p.querySelector("button[aria-label=Subtitles]")).toBeNull();
    expect(p.querySelector("[role=slider]")).toBeNull();
    expect(p.querySelector(".seek .thumb")).toBeNull();
    for (const kept of ["Full screen", "Picture in picture", "Volume"])
      expect(p.querySelector(`[aria-label='${kept}']`)).not.toBeNull();
    expect(calls.find((c) => c.key === `POST /stations/${uid(4)}/playback`)!.body).toMatchObject({
      live: true,
    });
    // Half an hour into an hour and a half.
    expect(p.querySelector("[role=progressbar] .fill")!.getAttribute("style")).toMatch(
      /width: 33\.\d+%/,
    );

    // The stream ends, for a player a little ahead of the schedule: once the program is over
    // by the schedule, an hour on, the session ends and the station is asked again.
    const waits = vi.spyOn(globalThis, "setTimeout");
    p.querySelector("video")!.dispatchEvent(new Event("ended"));
    const [ask, wait] = waits.mock.calls.at(-1)!;
    waits.mockRestore();
    expect(wait).toBeGreaterThan(3_500_000);
    expect(n).toBe(1);
    (ask as () => void)();
    await until(() => r.textContent?.includes("Up next at"));
    expect(calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`)).toBe(true);
    expect(n).toBe(2);
  });

  it("follows what is on through the programs and idents of one stream", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    // The program on ends in a moment; the ident then runs up to the next.
    const ending = program(30, "The Lantern Keeper", later(-30), later(0.005));
    const next = program(31, "Salt and Ember", later(40));
    let asked = 0;
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: () =>
          station(4, "The Big Screen", {
            onAir:
              asked++ === 0
                ? onNow(ending, next)
                : { status: "between_programs", next, upNext: [next] },
          }),
        [`POST /stations/${uid(4)}/playback`]: {
          stream: { ...stream(8), live: true },
          onAir: onNow(ending, next),
        },
        [`PUT /playback/sessions/${uid(8)}/position`]: none,
        [`DELETE /playback/sessions/${uid(8)}`]: none,
      }),
    });
    await until(() => r.querySelector(".player video"));
    const sub = r.querySelector(".player .ph .sub")!;
    expect(sub.textContent).toBe("On now: The Lantern Keeper");
    await until(() => sub.textContent !== "On now: The Lantern Keeper", 3000);
    expect(sub.textContent).toMatch(/^Up next at .+: Salt and Ember$/);
    expect(calls.filter((c) => c.key === `POST /stations/${uid(4)}/playback`)).toHaveLength(1);
    expect(calls.some((c) => c.key === `DELETE /playback/sessions/${uid(8)}`)).toBe(false);
  });

  it("stays on its stream when an event tells of the station", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    let told = 0;
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        // The first poll from a cursor tells of the station; the next is held.
        "GET /events": (_b: unknown, url: URL) =>
          !url.searchParams.has("after")
            ? { cursor: "c0", resync: false, events: [] }
            : told++ === 0
              ? { cursor: "c1", resync: false, events: [{ kind: "station", stationId: uid(4) }] }
              : new Promise(() => {}),
        [`GET /stations/${uid(4)}`]: station(4, "The Big Screen", { onAir: onNow() }),
        [`POST /stations/${uid(4)}/playback`]: {
          stream: { ...stream(7), live: true },
          onAir: onNow(),
        },
        [`PUT /playback/sessions/${uid(7)}/position`]: none,
        [`DELETE /playback/sessions/${uid(7)}`]: none,
      }),
    });
    await until(() => r.querySelector(".player video"));
    const video = r.querySelector("video");
    await until(() => told === 2);
    // Past the wait a page's redraw is held for.
    await new Promise((done) => setTimeout(done, 400));
    expect(r.querySelector("video")).toBe(video);
    expect(calls.filter((c) => c.key === `POST /stations/${uid(4)}/playback`)).toHaveLength(1);
    expect(calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`)).toBe(false);
  });

  it("tuned in between programs plays the ident and says what is next", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    const next = program(31, "Salt and Ember", later(40));
    const between = { status: "between_programs", next, upNext: [next] };
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: station(4, "The Big Screen", { onAir: between }),
        [`POST /stations/${uid(4)}/playback`]: {
          stream: { ...stream(7), live: true },
          onAir: between,
        },
        [`PUT /playback/sessions/${uid(7)}/position`]: none,
        [`DELETE /playback/sessions/${uid(7)}`]: none,
      }),
    });
    await until(() => r.querySelector(".player video"));
    expect(r.querySelector(".player .ph .sub")!.textContent).toMatch(
      /^Up next at .+: Salt and Ember$/,
    );
    expect(r.querySelector(".offair")).toBeNull();
  });

  it("ends a session that finishes tuning in after the viewer has left", async () => {
    let started!: () => void;
    const gate = new Promise<void>((r) => (started = r));
    await at(`#/tune/${uid(4)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: station(4, "The Big Screen"),
        [`POST /stations/${uid(4)}/playback`]: async () => {
          await gate;
          return json({ stream: stream(7), onAir: onNow() }, 201);
        },
        [`DELETE /playback/sessions/${uid(7)}`]: none,
      }),
    });
    await until(() => calls.some((c) => c.key === `POST /stations/${uid(4)}/playback`));
    await at("#/");
    started();
    await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
  });

  it("between programs shows the station's tile with what is next, and tunes in at resumesAt", async () => {
    await at(`#/tune/${uid(6)}`);
    const r = root();
    let n = 0;
    // Starts in a moment, so the call at resumesAt comes within the test.
    const next = program(32, "Moon Pie", new Date(Date.now() + 300).toISOString());
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(6)}`]: station(6, "Small Wonders", {
          onAir: { status: "between_programs", next, upNext: [next] },
        }),
        [`POST /stations/${uid(6)}/playback`]: () => {
          n++;
          return json({ code: "off_air", message: "off", resumesAt: next.startsAt }, 409);
        },
      }),
    });
    await until(() => r.querySelector(".offair h1"));
    expect(r.querySelector(".offair h1")!.textContent).toBe("Small Wonders");
    expect(r.querySelector(".offair p")!.textContent).toMatch(/^Up next at .+: Moon Pie$/);
    expect(n).toBe(1);
    await until(() => n === 2, 3000);
    // The server still answers off air: the next call waits, so a clock ahead of the server's cannot spin.
    await new Promise((r) => setTimeout(r, 300));
    expect(n).toBe(2);
  });

  it("left while tuning in to a station off the air, redraws nothing at resumesAt", async () => {
    await at(`#/tune/${uid(6)}`);
    const r = root();
    const next = program(32, "Moon Pie", new Date(Date.now() + 200).toISOString());
    let answer!: () => void;
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(6)}`]: station(6, "Small Wonders", {
          onAir: { status: "between_programs", next, upNext: [next] },
        }),
        [`POST /stations/${uid(6)}/playback`]: () =>
          new Promise(
            (done) =>
              (answer = () =>
                done(json({ code: "off_air", message: "off", resumesAt: next.startsAt }, 409))),
          ),
      }),
    });
    await until(() => answer);
    await at("#/stations");
    const redrawn = vi.fn();
    window.addEventListener("hashchange", redrawn);
    answer();
    await new Promise((r) => setTimeout(r, 1300));
    window.removeEventListener("hashchange", redrawn);
    expect(redrawn).not.toHaveBeenCalled();
  });

  it("back in view after the next program's start, asks at once", async () => {
    await at(`#/tune/${uid(6)}`);
    const r = root();
    let n = 0;
    const next = program(32, "Moon Pie", later(40));
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(6)}`]: station(6, "Small Wonders", {
          onAir: { status: "between_programs", next, upNext: [next] },
        }),
        [`POST /stations/${uid(6)}/playback`]: () => {
          n++;
          return json({ code: "off_air", message: "off", resumesAt: next.startsAt }, 409);
        },
      }),
    });
    await until(() => r.querySelector(".offair h1"));
    document.dispatchEvent(new Event("visibilitychange"));
    await new Promise((r) => setTimeout(r, 50));
    expect(n).toBe(1);
    // The tab slept through the start.
    const now = Date.now() + 41 * 60000;
    const clock = vi.spyOn(Date, "now").mockReturnValue(now);
    document.dispatchEvent(new Event("visibilitychange"));
    await until(() => n === 2);
    clock.mockRestore();
  });

  it("off schedule says when it is back", async () => {
    await at(`#/tune/${uid(7)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(7)}`]: station(7, "Creature Double", {
          onAir: {
            status: "off_schedule",
            next: program(33, "Them", later(3 * 1440)),
            upNext: [program(33, "Them", later(3 * 1440))],
          },
        }),
        [`POST /stations/${uid(7)}/playback`]: json(
          { code: "off_air", message: "off", resumesAt: later(3 * 1440) },
          409,
        ),
      }),
    });
    await until(() => r.querySelector(".offair p"));
    expect(r.querySelector(".offair p")!.textContent).toMatch(/^Back \w+ at /);
  });

  it("says so when nothing this device plays can carry the station", async () => {
    await at(`#/tune/${uid(4)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(4)}`]: station(4, "The Big Screen", { onAir: onNow() }),
        [`POST /stations/${uid(4)}/playback`]: json({ code: "unplayable", message: "no" }, 409),
      }),
    });
    await until(() => r.querySelector(".player .fail"));
    expect(r.querySelector(".fail .err")!.textContent).toBe("No way to play this on this device.");
    expect(r.querySelector(".fail a")!.getAttribute("href")).toBe("#/");
  });
});

describe("tuned in to a music station", () => {
  it("left while the station loads, neither tunes in nor moves to Now playing", async () => {
    await at(`#/tune/${uid(5)}`);
    const r = root();
    const kitchen = station(5, "Kitchen Radio", { kind: "music" });
    let answer!: () => void;
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /stations": page([]),
        [`GET /stations/${uid(5)}`]: () => new Promise((done) => (answer = () => done(kitchen))),
        [`POST /stations/${uid(5)}/playback`]: { stream: stream(8), onAir: kitchen.onAir },
      }),
    });
    await until(() => answer);
    await at("#/stations");
    answer();
    await new Promise((r) => setTimeout(r, 50));
    expect(location.hash).toBe("#/stations");
    expect(calls.some((c) => c.key === `POST /stations/${uid(5)}/playback`)).toBe(false);
  });

  it("plays through the music player with only Stop, Up next read only, the queue kept until Stop", async () => {
    await at(`#/station/${uid(5)}`);
    const r = root();
    const now = songProgram(40, "Paper Lanterns", later(-1));
    const next = songProgram(41, "Slow Tide", later(2));
    const onAir = {
      ...onNow(now, next),
      upNext: [
        next,
        songProgram(42, "Tin Roof Weather", later(5)),
        songProgram(43, "Honeycomb", later(8)),
      ],
    };
    const kitchen = station(5, "Kitchen Radio", { kind: "music", onAir });
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(5)}`]: kitchen,
        [`GET /stations/${uid(5)}/schedule`]: page([]),
        [`POST /stations/${uid(5)}/playback`]: { stream: stream(8), onAir },
        "POST /playback/sessions": stream(9),
        [`PUT /playback/sessions/${uid(8)}/position`]: none,
        [`DELETE /playback/sessions/${uid(8)}`]: none,
        [`PUT /playback/sessions/${uid(9)}/position`]: none,
        [`DELETE /playback/sessions/${uid(9)}`]: none,
      }),
    });
    const m = music()!;
    await m.play([tune(50, "Mine"), tune(51, "Also mine")], 0, "My album", `#/album/${uid(5)}`);
    await until(() => button(r, "Tune in"));
    const entries = history.length;
    button(r, "Tune in")!.click();
    await until(() => location.hash === "#/now-playing");
    // Now playing took the entry of #/tune/5, so Back does not tune in again.
    expect(history.length).toBe(entries + 1);
    const mini = await vi.waitFor(() => {
      const el = r.querySelector<HTMLElement>(".mini");
      expect(el?.textContent).toContain("Paper Lanterns");
      return el!;
    });
    expect(mini.textContent).toContain("Kitchen Radio");
    const labels = [...mini.querySelectorAll(".mctl button")].map((b) =>
      b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["Stop"]);
    expect(mini.querySelector("input.seekbar")).toBeNull();
    await until(() =>
      r.querySelector("section[aria-label='Up next']")?.textContent?.includes("Slow Tide"),
    );
    const up = r.querySelector("section[aria-label='Up next']")!;
    expect(text(up.querySelectorAll("li b"))).toEqual([
      expect.stringContaining("Slow Tide"),
      expect.stringContaining("Tin Roof Weather"),
      expect.stringContaining("Honeycomb"),
    ]);
    expect(up.querySelector("button.pb")).toBeNull();
    expect(up.querySelector("[draggable=true]")).toBeNull();
    expect(text(r.querySelectorAll("section[aria-label='Now playing'] button"))).toEqual(["Stop"]);
    expect(m.q.current?.title).toBe("Mine");
    expect(m.q.upNext.map((s) => s.title)).toEqual(["Also mine"]);

    mini.querySelector<HTMLElement>("[aria-label=Stop]")!.click();
    await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(8)}`));
    expect(m.station).toBeUndefined();
    expect(m.q.current).toBeUndefined();
    expect(mini.hidden).toBe(true);
  });

  it("names the station when its stream cannot play, with nothing queued", async () => {
    await at(`#/station/${uid(5)}`);
    const r = root();
    const onAir = onNow(songProgram(40, "Paper Lanterns", later(-1)));
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /stations/${uid(5)}`]: station(5, "Kitchen Radio", { kind: "music", onAir }),
        [`GET /stations/${uid(5)}/schedule`]: page([]),
        [`POST /stations/${uid(5)}/playback`]: { stream: stream(8), onAir },
        [`POST /playback/sessions/${uid(8)}/events`]: () =>
          json({ code: "conflict", message: "no entry left" }, 409),
        [`PUT /playback/sessions/${uid(8)}/position`]: none,
        [`DELETE /playback/sessions/${uid(8)}`]: none,
      }),
    });
    await until(() => button(r, "Tune in"));
    button(r, "Tune in")!.click();
    const m = await vi.waitFor(() => {
      const m = music()!;
      expect(m.audio.getAttribute("src")).toBeTruthy();
      return m;
    });
    m.audio.dispatchEvent(new Event("error"));
    await until(() => m.error);
    expect(m.error).toBe("Kitchen Radio can't play on this device.");
  });
});
