// Component tests for the watched bar on what a play would resume: the real pages and
// generated client against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { at, mockFetch, root, until } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

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
const me = {
  device: { id: uid(1) },
  account: acct(1, "Helga"),
  activeProfileId: uid(1),
  household: { id: uid(1), name: "Helga", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Helga")],
};
const page = (items: unknown[]) => ({ items });
const item = (id: number, kind: string, extra = {}) => ({
  id: uid(id),
  libraryId: uid(1),
  kind,
  parsedTitle: `T${id}`,
  addedAt: `2026-01-0${id % 9}T00:00:00Z`,
  createdAt: ts,
  modifiedAt: ts,
  ...extra,
});
const runtime = (runtimeMinutes: number) => ({ metadata: { status: "matched", runtimeMinutes } });
const state = (id: number, positionMs: number, watched = false, at = ts) => ({
  itemId: uid(id),
  positionMs,
  watched,
  playCount: watched ? 1 : 0,
  changedAt: at,
  lastPlayedAt: at,
});
const video = (id: number, positionMs: number) => ({
  id: uid(id),
  libraryId: uid(5),
  channelId: uid(40),
  channelTitle: "Workbench Weekly",
  title: `V${id}`,
  durationMs: 40 * 60000,
  watched: false,
  positionMs,
  addedAt: ts,
});

// Movie 30 is a quarter in, 31 finished, 32 unstarted. Show 10's season 11 resumes episode 22
// halfway; its season 12 plays episode 24 from the beginning.
const movies = [
  item(30, "movie", runtime(100)),
  item(31, "movie", runtime(100)),
  item(32, "movie", runtime(100)),
];
const show = item(10, "show");
const seasons = [
  item(11, "season", { parentId: uid(10), seasonNumber: 1 }),
  item(12, "season", { parentId: uid(10), seasonNumber: 2 }),
];
const episodes = [
  item(21, "episode", { parentId: uid(11), seasonNumber: 1, episodeNumber: 1, ...runtime(40) }),
  item(22, "episode", { parentId: uid(11), seasonNumber: 1, episodeNumber: 2, ...runtime(40) }),
  item(23, "episode", { parentId: uid(12), seasonNumber: 2, episodeNumber: 1, ...runtime(40) }),
  item(24, "episode", { parentId: uid(12), seasonNumber: 2, episodeNumber: 2, ...runtime(40) }),
];
const states = [
  state(30, 25 * 60000),
  state(31, 0, true),
  state(21, 0, true, "2026-01-02T00:00:00Z"),
  state(22, 20 * 60000, false, "2026-01-03T00:00:00Z"),
  state(23, 0, true, "2026-01-01T00:00:00Z"),
];
const routes = {
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me,
  [`GET /profiles/${uid(1)}/watch-state`]: (_: unknown, u: URL) => {
    const parent = u.searchParams.get("parentId");
    return parent === uid(11)
      ? page(states.filter((s) => [uid(21), uid(22)].includes(s.itemId)))
      : page(states);
  },
  [`GET /profiles/${uid(1)}/show-progress`]: page([
    {
      showId: uid(10),
      seasons: 2,
      episodes: 4,
      unwatched: 2,
      inProgress: true,
      nextEpisodeId: uid(22),
    },
  ]),
  "GET /genres": page([]),
  "GET /media-files": page([]),
  [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] },
  [`GET /items/${uid(30)}/credits`]: { cast: [], crew: [] },
  [`GET /items/${uid(30)}/related`]: { collections: [] },
  [`GET /profiles/${uid(1)}/occasions`]: page([]),
  ...Object.fromEntries(
    [...movies, show, ...seasons, ...episodes].map((i) => [`GET /items/${i.id}`, i]),
  ),
  "GET /items": (_: unknown, u: URL) => {
    const kind = u.searchParams.get("kind");
    const parent = u.searchParams.get("parentId");
    const all = { movie: movies, show: [show], season: seasons, episode: episodes }[kind!] ?? [];
    return page(all.filter((i) => !parent || ("parentId" in i && i.parentId === parent)));
  },
  "GET /youtube/videos": page([]),
};

const LABEL = "[role=img][aria-label$='percent watched']";
const labels = (el: ParentNode) => [...el.querySelectorAll(LABEL)].map((b) => b.ariaLabel);
const tileOf = (r: ParentNode, sel: string, id: number) =>
  [...r.querySelectorAll<HTMLElement>(sel)].find((a) => a.getAttribute("href")?.includes(uid(id)))!;

beforeEach(async () => {
  window.onhashchange = null;
  setToken("tok");
  document.body.replaceChildren();
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
});

describe("the watched bar", () => {
  it("marks a started movie in the Movies grid, and no finished or unstarted one", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => r.querySelectorAll(".grid a.poster").length === 3);
    expect(labels(tileOf(r, ".grid a", 30))).toEqual(["25 percent watched"]);
    expect(tileOf(r, ".grid a", 30).querySelector<HTMLElement>(".bar div")!.style.width).toBe(
      "25%",
    );
    expect(labels(tileOf(r, ".grid a", 31))).toEqual([]);
    expect(labels(tileOf(r, ".grid a", 32))).toEqual([]);
  });

  it("marks a show's poster and the season whose play resumes, not the one starting fresh", async () => {
    await at(`#/show/${uid(10)}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => r.querySelector("a.poster.season"));
    expect(r.querySelector(".btn.p")!.textContent).toBe("Resume S1 E2");
    expect(labels(r.querySelector(".detail")!)).toEqual(["50 percent watched"]);
    expect(labels(tileOf(r, "a.season", 11))).toEqual(["50 percent watched"]);
    expect(labels(tileOf(r, "a.season", 12))).toEqual([]);
  });

  it("marks only the started episode on a season's page", async () => {
    await at(`#/season/${uid(11)}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => r.querySelectorAll("a.ep").length === 2);
    expect(labels(r.querySelector(".detail")!)).toEqual(["50 percent watched"]);
    expect(labels(tileOf(r, "a.ep", 21))).toEqual([]);
    expect(labels(tileOf(r, "a.ep", 22))).toEqual(["50 percent watched"]);
    expect(tileOf(r, "a.ep", 21).querySelector(".bar")).toBeNull();
  });

  it("marks a started movie's poster where its button reads Resume", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        [`GET /profiles/${uid(1)}/watch-state/${uid(30)}`]: state(30, 25 * 60000),
      }),
    });
    await until(() => r.querySelector(".detail"));
    expect(r.querySelector(".btn.p")!.textContent).toBe("Resume");
    expect(labels(r.querySelector(".detail")!)).toEqual(["25 percent watched"]);
  });

  it("hides a movie poster's bar while an edition other than the saved one is picked", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    const edition = (id: number, isDefault: boolean) => ({
      id: uid(id),
      name: `E${id}`,
      isDefault,
      copies: [],
    });
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        [`GET /items/${uid(30)}`]: {
          ...movies[0],
          editions: [edition(401, true), edition(402, false)],
        },
        [`GET /profiles/${uid(1)}/watch-state/${uid(30)}`]: {
          ...state(30, 25 * 60000),
          editionId: uid(402),
        },
      }),
    });
    await until(() => r.querySelector(".detail select"));
    const bar = r.querySelector<HTMLElement>(`.detail ${LABEL}`)!;
    const pick = r.querySelector<HTMLSelectElement>(".detail select")!;
    expect([r.querySelector(".btn.p")!.textContent, bar.hidden]).toEqual(["Resume", false]);
    pick.value = uid(401);
    pick.dispatchEvent(new Event("change"));
    expect([r.querySelector(".btn.p")!.textContent, bar.hidden]).toEqual(["Play", true]);
  });

  it("marks the hero's episode and the movie in progress beside it on Home", async () => {
    await at("#/");
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => r.querySelector(".also"));
    expect(r.querySelector(".hero .btn.p")!.textContent).toBe("Resume");
    expect(labels(r.querySelector(".hero-tile")!)).toEqual(["50 percent watched"]);
    expect(labels(r.querySelector(".also")!)).toEqual(["25 percent watched"]);
  });

  it("marks a started YouTube video's tile, not an unstarted one", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        "GET /youtube/channels": page([
          {
            id: uid(40),
            libraryId: uid(5),
            title: "W",
            videoCount: 2,
            unwatchedCount: 2,
            addedAt: ts,
          },
        ]),
        "GET /youtube/videos": page([video(60, 10 * 60000), video(61, 0)]),
      }),
    });
    await until(() => r.querySelectorAll("a.vid").length === 2);
    expect(labels(tileOf(r, "a.vid", 60))).toEqual(["25 percent watched"]);
    expect(labels(tileOf(r, "a.vid", 61))).toEqual([]);
  });
});
