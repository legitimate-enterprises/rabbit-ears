// Component tests for YouTube and the Movies views: the real pages and generated
// client against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { inProgress } from "./data";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id,
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  updatedAt: ts,
});
const me = (active: number) => ({
  device: { id: 1 },
  account: acct(1, "Heidi"),
  activeProfileId: active,
  household: { id: 1, name: "Heidi", adultId: 1, createdAt: ts },
  profiles: [acct(1, "Heidi"), acct(2, "Jax", "kid")],
});
const page = (items: unknown[]) => ({ items });
const channel = (id: number, title: string, extra = {}) => ({
  id,
  libraryId: 5,
  title,
  videoCount: 214,
  unwatchedCount: 12,
  lastVideoOn: new Date().toISOString().slice(0, 10),
  addedAt: ts,
  ...extra,
});
const video = (id: number, title: string, extra = {}) => ({
  id,
  libraryId: 5,
  channelId: 40,
  channelTitle: "Workbench Weekly",
  title,
  durationMs: 42 * 60000,
  publishedOn: new Date().toISOString().slice(0, 10),
  watched: false,
  positionMs: 0,
  addedAt: ts,
  ...extra,
});
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  "GET /profiles/1/watch-state": page([]),
  "GET /profiles/2/watch-state": page([]),
  "GET /profiles/1/show-progress": page([]),
  "GET /profiles/2/show-progress": page([]),
  "GET /genres": page([]),
  "GET /items": page([]),
  "GET /youtube/channels": page([channel(40, "Workbench Weekly"), channel(41, "Clockwork Corner")]),
  "GET /youtube/videos": page([video(60, "The dishwasher video")]),
});

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  localStorage.clear();
  history.replaceState(null, "", "/");
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
  setToken("tok");
});

describe("YouTube section", () => {
  it("counts channels in the header and lists newest videos with the unwatched dot", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".grid.vids a"));
    expect(r.querySelector(".titlerow")!.textContent).toBe("YouTube2 channels");
    const tile = r.querySelector(".grid.vids a")!;
    expect(tile.getAttribute("href")).toBe("#/video/60");
    expect(tile.textContent).toBe("The dishwasher videoWorkbench Weekly · 42m · today");
    expect(tile.querySelector(".dot")).toBeTruthy();
    expect(tile.querySelector("[data-art]")!.getAttribute("data-art")).toBe("60/poster");
    const q = calls.find((c) => c.key === "GET /youtube/videos")!.query;
    expect(q).toContain("sort=newest");
    expect(r.querySelector("nav a[aria-current=page]")!.textContent).toBe("YouTube");
  });

  it("filters on the server by Unwatched, Channel and Sort, kept in the address", async () => {
    await at("#/youtube?unwatched=1&channel=41&sort=length");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".grid.vids a"));
    const q = new URLSearchParams(calls.find((c) => c.key === "GET /youtube/videos")!.query);
    expect(q.get("unwatched")).toBe("true");
    expect(q.get("channelId")).toBe("41");
    expect(q.get("sort")).toBe("length");
    expect([...r.querySelectorAll(".vt a")].map((a) => a.getAttribute("href"))).toEqual([
      "#/youtube?unwatched=1&channel=41&sort=length",
      "#/youtube?view=channels&unwatched=1&channel=41&sort=length",
    ]);
    expect(r.querySelector(".vt [aria-current=page]")!.textContent).toBe("Videos");
    const chips = [...r.querySelectorAll(".chip")];
    expect(chips.map((c) => c.getAttribute("aria-pressed"))).toEqual(["true"]);
    chips[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(location.hash).toBe("#/youtube?channel=41&sort=length");
  });

  it("picks a channel by id, so channels of one title, or titled Any, are all reachable", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/channels": page([
          channel(40, "Any"),
          channel(41, "Workbench"),
          channel(42, "Workbench"),
        ]),
      }),
    });
    await until(() => r.querySelector(".grid.vids a"));
    const pick = [...r.querySelectorAll("label.sel")]
      .find((l) => l.textContent!.startsWith("Channel"))!
      .querySelector("select")!;
    const choose = (v: string) => {
      pick.value = v;
      pick.dispatchEvent(new Event("change"));
    };
    choose("42");
    expect(location.hash).toBe("#/youtube?channel=42");
    choose("40");
    expect(location.hash).toBe("#/youtube?channel=40");
    choose("");
    expect(location.hash).toBe("#/youtube");
  });

  it("reads more videos when the page says there are", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos": (_: unknown, u: URL) =>
          u.searchParams.get("offset") === "0"
            ? { items: [video(60, "First")], nextOffset: 60 }
            : page([video(61, "Second")]),
      }),
    });
    await until(() => r.querySelector(".grid.vids a"));
    const more = r.querySelector<HTMLButtonElement>("button.more")!;
    expect(more.hidden).toBe(false);
    more.click();
    await until(() => r.querySelectorAll(".grid.vids a").length === 2);
    expect(more.hidden).toBe(true);
  });

  it("asks for a page once when Show more is clicked twice", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos": (_: unknown, u: URL) =>
          u.searchParams.get("offset") === "0"
            ? { items: [video(60, "First")], nextOffset: 60 }
            : page([video(61, "Second")]),
      }),
    });
    await until(() => r.querySelector(".grid.vids a"));
    const more = r.querySelector<HTMLButtonElement>("button.more")!;
    more.click();
    more.click();
    await until(() => r.querySelectorAll(".grid.vids a").length === 2);
    await new Promise((x) => setTimeout(x, 20));
    expect(r.querySelectorAll(".grid.vids a")).toHaveLength(2);
    expect(calls.filter((c) => c.query.includes("offset=60"))).toHaveLength(1);
  });

  it("shows channels with their unwatched counts, and only those with some under Unwatched", async () => {
    await at("#/youtube?view=channels&unwatched=1");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/channels": page([
          channel(40, "Workbench Weekly"),
          channel(41, "Clockwork Corner", { unwatchedCount: 0 }),
        ]),
      }),
    });
    await until(() => r.querySelector(".grid.sq a"));
    expect(r.querySelectorAll(".grid.sq a").length).toBe(1);
    expect(r.querySelector(".grid.sq a")!.textContent).toBe("Workbench Weekly214 videos · 12 new");
  });

  it("has no Playlists view", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), "GET /youtube/channels": page([]) }) });
    await until(() => r.querySelector(".vt"));
    expect([...r.querySelectorAll(".vt a")].map((c) => c.textContent)).toEqual([
      "Videos",
      "Channels",
    ]);
  });
});

describe("Channel", () => {
  const routes = () => ({
    ...base(),
    "GET /youtube/channels/40": channel(40, "Workbench Weekly", {
      overview: "Deep dives on everyday technology.",
    }),
  });

  it("shows counts and videos, and plays the newest", async () => {
    await at("#/channel/40");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => r.querySelector(".grid.vids a"));
    expect(r.querySelector("h1")!.textContent).toBe("Workbench Weekly");
    expect(r.querySelector(".meta")!.textContent).toBe("214 videos12 unwatchedLast video today");
    expect(r.querySelector("[aria-label=Playlists]")).toBeNull();
    expect(r.querySelector(".grid.vids a")!.textContent).toBe("The dishwasher video42m · today");
    [...r.querySelectorAll("button")].find((b) => b.textContent === "Play newest")!.click();
    await until(() => location.hash === "#/play/60");
    const newest = calls.filter((c) => c.key === "GET /youtube/videos").pop()!.query;
    expect(newest).toContain("limit=1");
  });

  it("marks every video watched, or unwatched once none are left", async () => {
    await at("#/channel/40");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...routes(),
        "PUT /profiles/1/youtube/channels/40/watch-state": none,
      }),
    });
    await until(() => r.querySelector(".grid.vids a"));
    [...r.querySelectorAll("button")].find((b) => b.textContent === "Mark all watched")!.click();
    await until(() =>
      calls.some((c) => c.key === "PUT /profiles/1/youtube/channels/40/watch-state"),
    );
    expect(calls.find((c) => c.key.startsWith("PUT /profiles"))!.body).toEqual({ watched: true });
  });

  it("lets an adult mark it for kids and allow it for kids on a list; a kid gets neither", async () => {
    await at("#/channel/40");
    const r = root();
    const withIvy = {
      ...me(1),
      profiles: [...me(1).profiles, { ...acct(5, "Ivy", "kid"), filterMode: "allow_list" }],
    };
    await boot(r, {
      fetch: mockFetch({
        ...routes(),
        "GET /me": withIvy,
        "GET /items/40/allowed-for": { profileIds: [] },
        "PUT /youtube/channels/40/for-kids": (b: unknown) =>
          channel(40, "Workbench Weekly", b as object),
        "PUT /profiles/5/allowed-items/40": none,
      }),
    });
    await until(() => r.querySelector("[role=switch]"));
    const sw = r.querySelector<HTMLInputElement>("[role=switch][aria-label='For kids']")!;
    expect(sw.checked).toBe(false);
    sw.click();
    await until(() => calls.some((c) => c.key === "PUT /youtube/channels/40/for-kids"));
    expect(calls.find((c) => c.key === "PUT /youtube/channels/40/for-kids")!.body).toEqual({
      forKids: true,
    });
    const ivy = [...r.querySelectorAll<HTMLElement>("[role=menuitemcheckbox]")].find(
      (e) => e.textContent === "Ivy",
    )!;
    expect(ivy.getAttribute("aria-checked")).toBe("false");
    ivy.click();
    await until(() => calls.some((c) => c.key === "PUT /profiles/5/allowed-items/40"));

    document.body.replaceChildren();
    const kid = root();
    await boot(kid, { fetch: mockFetch({ ...routes(), "GET /me": me(2) }) });
    await until(() => kid.querySelector(".grid.vids a"));
    expect(kid.querySelector("[role=switch]")).toBeNull();
    expect(kid.querySelector("main [role=menu]")).toBeNull();
  });

  it("puts For kids back and says why when the server refuses it", async () => {
    await at("#/channel/40");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...routes(),
        "GET /items/40/allowed-for": { profileIds: [] },
        "PUT /youtube/channels/40/for-kids": () =>
          json({ code: "internal", message: "database is locked" }, 500),
      }),
    });
    await until(() => r.querySelector("[role=switch]"));
    const sw = r.querySelector<HTMLInputElement>("[role=switch][aria-label='For kids']")!;
    sw.click();
    await until(() => r.querySelector("[role=alert]:not([hidden])"));
    expect(sw.checked).toBe(false);
    expect(r.querySelector("[role=alert]")!.textContent).toBe("database is locked");
  });

  it("offers Mark all unwatched when nothing is unwatched", async () => {
    await at("#/channel/40");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...routes(),
        "GET /youtube/channels/40": channel(40, "Workbench Weekly", { unwatchedCount: 0 }),
      }),
    });
    await until(() => r.querySelector(".grid.vids a"));
    expect(
      [...r.querySelectorAll("button")].some((b) => b.textContent === "Mark all unwatched"),
    ).toBe(true);
  });
});

describe("Video details", () => {
  beforeEach(() => {
    // jsdom has no media capability APIs; the snapshot only needs answers.
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener() {},
      removeEventListener() {},
    }));
    Object.defineProperty(navigator, "mediaCapabilities", {
      configurable: true,
      value: { decodingInfo: async () => ({ supported: false }) },
    });
  });

  it("shows the video, its file facts and more from the channel", async () => {
    await at("#/video/60");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos/60": video(60, "The dishwasher video", {
          overview: "Your dishwasher is better than you think.",
          height: 1080,
          sourceUrl: "https://www.youtube.com/watch?v=abc",
        }),
        "GET /youtube/videos": page([
          video(60, "The dishwasher video"),
          video(61, "How CRTs actually work"),
        ]),
        "GET /media-files": page([
          {
            id: 1,
            itemId: 60,
            path: "/y/a.mkv",
            sizeBytes: 1e9,
            container: "matroska",
            bitrate: 4e6,
            tracks: [
              { id: 1, kind: "video", index: 0, codec: "av1", width: 1920, height: 1080 },
              { id: 2, kind: "subtitle", index: 1, codec: "srt", language: "eng" },
            ],
            chapters: [{ id: 1, position: 0, startMs: 0, endMs: 1, title: "a" }],
          },
        ]),
        "POST /items/60/playability": { verdict: "direct_play", negotiated: {} },
      }),
    });
    await until(() => r.querySelector("[aria-label^='More from'] a"));
    expect(r.querySelector(".back")!.getAttribute("href")).toBe("#/channel/40");
    expect(r.querySelector("h1")!.textContent).toBe("The dishwasher video");
    expect(r.querySelector(".meta .tag")!.textContent).toBe("1080p");
    expect(r.querySelector(".synopsis")!.textContent).toContain("better than you think");
    expect(r.querySelector("[aria-label^='More from'] a")!.textContent).toContain(
      "How CRTs actually work",
    );
    expect(r.querySelectorAll("[aria-label^='More from'] a").length).toBe(1);
    await until(() => r.querySelector(".kv .ok"));
    const kv = r.querySelector(".kv")!.textContent!;
    expect(kv).toContain("Chapters1");
    expect(kv).toContain("SubtitlesEnglish");
    expect(kv).toContain("Open on YouTube");
    const labels = [...r.querySelectorAll(".actions .btn")].map((b) => b.textContent);
    expect(labels).toEqual(["Play", "Mark watched", "Watch together"]);
  });

  it("links the source only when its address is http or https", async () => {
    await at("#/video/60");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos/60": video(60, "The dishwasher video", {
          sourceUrl: "javascript:alert(1)",
        }),
        "GET /media-files": page([]),
      }),
    });
    await until(() => r.querySelector(".kv"));
    expect(r.querySelector(".kv")!.textContent).not.toContain("Open on YouTube");
    expect(r.querySelector("a[href^='javascript']")).toBeNull();
  });

  it("resumes a started video and marks it watched through the item's watch state", async () => {
    await at("#/video/60");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos/60": video(60, "The dishwasher video", { positionMs: 600000 }),
        "GET /media-files": page([]),
        "PUT /profiles/1/watch-state/60": { itemId: 60, positionMs: 0, watched: true },
      }),
    });
    await until(() => r.querySelector(".actions"));
    expect(r.querySelector(".actions .btn.p")!.textContent).toBe("Resume");
    [...r.querySelectorAll("button")].find((b) => b.textContent === "Mark watched")!.click();
    await until(() => calls.some((c) => c.key === "PUT /profiles/1/watch-state/60"));
  });
});

describe("Home and kids", () => {
  it("shows New from your channels, unwatched and newest first", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("kind") === "movie"
              ? [
                  {
                    id: 1,
                    libraryId: 1,
                    kind: "movie",
                    parsedTitle: "A",
                    addedAt: ts,
                    updatedAt: ts,
                  },
                ]
              : [],
          ),
      }),
    });
    await until(() => r.querySelector("[aria-label='New from your channels'] a.vid"));
    const q = new URLSearchParams(calls.find((c) => c.key === "GET /youtube/videos")!.query);
    expect([q.get("sort"), q.get("unwatched")]).toEqual(["newest", "true"]);
    expect(
      r.querySelector("[aria-label='New from your channels'] .rowhead a")!.getAttribute("href"),
    ).toBe("#/youtube");
  });

  it("counts a watched item played partway as in progress", () => {
    expect(
      inProgress({ itemId: 1, watched: true, positionMs: 60000, playCount: 1, updatedAt: ts }),
    ).toBe(true);
    expect(
      inProgress({ itemId: 1, watched: true, positionMs: 0, playCount: 1, updatedAt: ts }),
    ).toBe(false);
  });

  const a = { id: 1, libraryId: 1, kind: "movie", parsedTitle: "A", addedAt: ts, updatedAt: ts };
  const oneMovie = (_: unknown, u: URL) => page(u.searchParams.get("kind") === "movie" ? [a] : []);

  it("continues videos and movies together, latest played first", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/watch-state": page([
          { itemId: 1, watched: true, positionMs: 600000, lastPlayedAt: "2026-02-01T00:00:00Z" },
        ]),
        "GET /youtube/videos": (_: unknown, u: URL) =>
          u.searchParams.get("inProgress") === "true"
            ? page([
                video(61, "The clock video", {
                  positionMs: 60000,
                  lastPlayedAt: "2026-02-02T00:00:00Z",
                }),
              ])
            : page([]),
        "GET /items": oneMovie,
        "GET /items/1": a,
      }),
    });
    const hero = "[aria-label='Pick up where you left off']";
    await until(() => r.querySelector(`${hero} .also`));
    const q = new URLSearchParams(
      calls.find((c) => c.key === "GET /youtube/videos" && c.query.includes("inProgress"))!.query,
    );
    expect(q.get("sort")).toBe("played");
    expect(r.querySelector(`${hero} h1`)!.textContent).toBe("The clock video");
    expect(r.querySelector(`${hero} .btn.p`)!.getAttribute("href")).toBe("#/play/61");
    expect(r.querySelector(`${hero} .also b`)!.textContent).toBe("A");
  });

  it("has no hero with nothing in progress: Home starts with its next row", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": oneMovie,
        "GET /youtube/videos": (_: unknown, u: URL) =>
          page(u.searchParams.has("inProgress") ? [] : [video(60, "The dishwasher video")]),
      }),
    });
    await until(() => r.querySelector("[aria-label='Recently added']"));
    expect(r.querySelector(".hero")).toBeNull();
    expect(r.querySelector("main > section")!.getAttribute("aria-label")).toBe("Recently added");
    expect(r.querySelector("[aria-label='New from your channels']")).not.toBeNull();
  });

  it("fills the hero with the one thing in progress, with no side list", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /items": oneMovie,
        "GET /items/1": a,
        "GET /youtube/videos": page([]),
        "GET /profiles/1/watch-state": page([
          { itemId: 1, watched: false, positionMs: 600000, lastPlayedAt: ts },
        ]),
      }),
    });
    await until(() => r.querySelector(".hero"));
    expect(r.querySelector(".hero h1")!.textContent).toBe("A");
    expect(r.querySelector(".hero .kicker")!.textContent).toBe("Continue watching");
    expect(r.querySelector(".hero .also")).toBeNull();
  });

  it("leaves the row out when the server has no YouTube", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /youtube/videos": () => json({ code: "not_implemented", message: "no" }, 501),
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("kind") === "movie"
              ? [
                  {
                    id: 1,
                    libraryId: 1,
                    kind: "movie",
                    parsedTitle: "A",
                    addedAt: ts,
                    updatedAt: ts,
                  },
                ]
              : [],
          ),
      }),
    });
    await until(() => r.querySelector("[aria-label='Recently added']"));
    expect(r.querySelector("[aria-label='New from your channels']")).toBeNull();
  });

  it("gives a kid YouTube, as the server filters it", async () => {
    await at("#/youtube");
    const r = root();
    await boot(r, { fetch: mockFetch(base(2)) });
    await until(() => r.querySelector(".poster.vid"));
    expect([...r.querySelectorAll("header nav a")].map((a) => a.textContent)).toEqual([
      "Home",
      "TV",
      "Movies",
      "Music",
      "YouTube",
      "Stations",
    ]);
  });
});

describe("Movies views and filters", () => {
  it("lists the views as tabs under the title, then the filters as chips", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".chip"));
    expect(r.querySelector(".titlerow + nav.vt + .filters")).not.toBeNull();
    expect([...r.querySelectorAll(".vt a")].map((c) => c.textContent)).toEqual([
      "Movies",
      "Collections",
      "Playlists",
      "Stations",
    ]);
    expect(r.querySelector(".vt [aria-current=page]")!.textContent).toBe("Movies");
    expect([...r.querySelectorAll(".chip")].map((c) => c.textContent)).toEqual([
      "All",
      "Unwatched",
      "In progress",
      "Kids",
    ]);
  });
});
