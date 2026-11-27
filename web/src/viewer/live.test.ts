// Live updates and handoff: the follower against a mocked event stream, and
// the pages and the player that act on what it hears.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApi, setToken } from "../admin/api";
import { boot } from "./app";
import { follow, onLive, type LiveEvent } from "./live";
import { type Handler, calls, json, mockFetch, none } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
    static Events = { ERROR: "e", MANIFEST_PARSED: "m", LEVEL_SWITCHED: "l" };
  },
}));

const until = (f: () => unknown, timeout = 4000) =>
  vi.waitFor(() => expect(f()).toBeTruthy(), { timeout });
const count = (key: string) => calls.filter((c) => c.key === key).length;

/** An event stream the test feeds: each poll with a cursor waits for the next batch. */
function stream() {
  let n = 0;
  let waiting: ((r: unknown) => void) | undefined;
  const asked: string[] = [];
  return {
    asked,
    route: (_b: unknown, url: URL) => {
      asked.push(url.searchParams.get("after") ?? "");
      if (!url.searchParams.has("after")) return { cursor: `c${n}`, resync: false, events: [] };
      return new Promise((done) => (waiting = done));
    },
    /** Answers the held poll. */
    say: async (events: Partial<LiveEvent>[], resync = false) => {
      await until(() => waiting);
      const done = waiting!;
      waiting = undefined;
      done({ cursor: `c${++n}`, resync, events });
    },
    held: () => !!waiting,
  };
}

let stop: (() => void) | undefined;
let off: (() => void) | undefined;
const hide = (state: "hidden" | "visible") => {
  Object.defineProperty(document, "visibilityState", { configurable: true, value: state });
  document.dispatchEvent(new Event("visibilitychange"));
};
const scrolled = vi.fn();
beforeEach(() => {
  calls.length = 0;
  document.body.replaceChildren();
  setToken("t");
  // jsdom has no scrollTo.
  window.scrollTo = scrolled as never;
  scrolled.mockClear();
});
afterEach(() => {
  stop?.();
  off?.();
  stop = off = undefined;
  hide("visible");
  // The app this test booted stops routing, and its player lets go.
  window.onhashchange = null;
  window.dispatchEvent(new HashChangeEvent("hashchange"));
});

describe("follow", () => {
  const heard: LiveEvent[][] = [];
  const start = (
    routes: Record<string, Handler | unknown>,
    sleep?: (ms: number) => Promise<void>,
    signedOut?: () => void,
  ) => {
    heard.length = 0;
    off = onLive((e) => heard.push(e));
    stop = follow(createApi({ fetch: mockFetch(routes) }), sleep, signedOut);
  };

  it("asks for a cursor, then holds a poll from each answer's cursor and tells what it hears", async () => {
    const s = stream();
    start({ "GET /events": s.route });
    await s.say([{ kind: "watch_state", itemId: uid(10) }]);
    await until(() => heard.length === 1);
    expect(heard[0]).toEqual([{ kind: "watch_state", itemId: uid(10) }]);
    await s.say([]);
    await s.say([{ kind: "station", stationId: uid(4) }, { kind: "playlist" }]);
    await until(() => heard.length === 2);
    expect(heard[1].map((e) => e.kind)).toEqual(["station", "playlist"]);
    await until(() => s.asked.length > 3);
    expect(s.asked.slice(0, 4)).toEqual(["", "c0", "c1", "c2"]);
  });

  it("follows library and household events, which the server sends only to a client that asks", async () => {
    const kinds: string[][] = [];
    const s = stream();
    start({
      "GET /events": (b: unknown, url: URL) => (
        kinds.push(url.searchParams.getAll("kinds")),
        s.route(b, url)
      ),
    });
    await s.say([{ kind: "library", libraryId: uid(1) }]);
    await until(() => heard.length === 1);
    expect(kinds.slice(0, 2)).toEqual([
      ["library", "household"],
      ["library", "household"],
    ]);
  });

  it("tells a resync as an empty batch: reload everything shown", async () => {
    const s = stream();
    start({ "GET /events": s.route });
    await s.say([], true);
    await until(() => heard.length === 1);
    expect(heard[0]).toEqual([]);
  });

  it("asks a failed poll again from the same cursor after a growing wait", async () => {
    const waits: number[] = [];
    let fail = 3;
    const s = stream();
    start(
      {
        "GET /events": (b: unknown, url: URL) =>
          url.searchParams.has("after") && fail-- > 0
            ? json({ code: "internal", message: "x" }, 500)
            : s.route(b, url),
      },
      async (ms) => void waits.push(ms),
    );
    await s.say([{ kind: "playlist" }]);
    await until(() => heard.length === 1);
    expect(waits).toEqual([1000, 2000, 4000]);
    expect(calls.filter((c) => new URLSearchParams(c.query).get("after") === "c0").length).toBe(4);
  });

  it("asks again after a proxy's 408 or 429, which are not a refusal", async () => {
    const waits: number[] = [];
    const answers = [429, 408];
    const s = stream();
    start(
      {
        "GET /events": (b: unknown, url: URL) =>
          url.searchParams.has("after") && answers.length
            ? json({ code: "x", message: "x" }, answers.shift())
            : s.route(b, url),
      },
      async (ms) => void waits.push(ms),
    );
    await s.say([{ kind: "playlist" }]);
    await until(() => heard.length === 1);
    expect(waits).toEqual([1000, 2000]);
  });

  it("gives up on a server without the stream, and on a device signed out or locked", async () => {
    for (const status of [404, 401, 403]) {
      calls.length = 0;
      const signedOut = vi.fn();
      start(
        { "GET /events": () => json({ code: "x", message: "x" }, status) },
        async () => {},
        signedOut,
      );
      await new Promise((r) => setTimeout(r, 20));
      expect(count("GET /events")).toBe(1);
      // Only a device the server no longer knows, as one of an account deleted elsewhere, signs out.
      expect(signedOut).toHaveBeenCalledTimes(status === 401 ? 1 : 0);
      stop!();
      off!();
    }
  });

  it("stops asking once stopped", async () => {
    const s = stream();
    start({ "GET /events": s.route });
    await until(() => s.held());
    stop!();
    await s.say([{ kind: "playlist" }]);
    await new Promise((r) => setTimeout(r, 20));
    expect(heard).toEqual([]);
    expect(s.asked).toEqual(["", "c0"]);
  });

  it("stops in the background, and on return reloads once and follows from a new cursor", async () => {
    const s = stream();
    start({ "GET /events": s.route });
    await until(() => s.held());
    hide("hidden");
    await s.say([{ kind: "playlist" }]);
    await new Promise((r) => setTimeout(r, 20));
    expect(heard).toEqual([]);
    expect(s.asked).toEqual(["", "c0"]);
    hide("visible");
    await until(() => s.asked.length === 4);
    expect(heard).toEqual([[]]);
    expect(s.asked.slice(2)).toEqual(["", "c1"]);
  });
});

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
  account: acct(1, "Bea"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Bea", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Bea"), acct(2, "Jax", "kid")],
});
const page = (items: unknown[]) => ({ items });
const at = async (hash: string) => {
  location.hash = hash;
  await new Promise((r) => setTimeout(r, 0));
};
const root = () => document.body.appendChild(document.createElement("div"));
const movie = {
  id: uid(10),
  libraryId: uid(1),
  kind: "movie",
  parsedTitle: "Dune",
  addedAt: ts,
  modifiedAt: ts,
};
const shell = {
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(1),
};

describe("pages", () => {
  const home = (s: ReturnType<typeof stream>, state: () => unknown[]) => ({
    ...shell,
    "GET /events": s.route,
    "GET /items": (_b: unknown, url: URL) =>
      page(url.searchParams.get("kind") === "movie" ? [movie] : []),
    [`GET /items/${uid(10)}`]: movie,
    [`GET /profiles/${uid(1)}/watch-state`]: () => page(state()),
    [`GET /profiles/${uid(1)}/show-progress`]: page([]),
    [`GET /profiles/${uid(2)}/show-progress`]: page([]),
    "GET /youtube/videos": page([]),
    "GET /stations": page([]),
  });
  const state = (positionMs: number) => ({
    accountId: uid(1),
    itemId: uid(10),
    positionMs,
    watched: false,
    playCount: 0,
    lastPlayedAt: ts,
    changedAt: ts,
  });

  it("Home takes a title started on another device into Continue watching, with no Loading and no scroll jump", async () => {
    const s = stream();
    let states: unknown[] = [];
    await at("#/");
    const r = root();
    await boot(r, { fetch: mockFetch(home(s, () => states)) });
    await until(() => r.querySelector("main")!.textContent!.includes("Recently added"));
    expect(r.textContent).not.toContain("Continue watching");
    const strip = r.querySelector<HTMLElement>(".strip")!;
    strip.scrollLeft = 120;
    const seen: string[] = [];
    const watch = new MutationObserver(() => seen.push(r.querySelector("main")!.textContent ?? ""));
    watch.observe(r, { childList: true, subtree: true });

    states = [state(60_000)];
    await s.say([{ kind: "watch_state", itemId: uid(10) }]);
    await until(() => r.textContent!.includes("Continue watching"));
    watch.disconnect();
    expect(seen.some((t) => t === "Loading")).toBe(false);
    expect(r.querySelector<HTMLElement>(".strip")!.scrollLeft).toBe(120);
    expect(scrolled).toHaveBeenCalledWith(0, 0);
    expect(count("GET /me")).toBe(1);
  });

  it("reads the library again when a scan changes it or everything shown reloads", async () => {
    const s = stream();
    let added: unknown[] = [];
    await at("#/movies");
    const r = root();
    const routes = home(s, () => []);
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        "GET /genres": page([]),
        [`GET /profiles/${uid(1)}/playlists`]: page([]),
        "GET /items": (b: unknown, url: URL) =>
          url.searchParams.get("kind") === "movie"
            ? page([movie, ...added])
            : routes["GET /items"](b, url),
      }),
    });
    await until(() => r.querySelector(".grid")?.textContent!.includes("Dune"));
    added = [{ ...movie, id: uid(11), parsedTitle: "Arrival", addedAt: "2026-02-01T00:00:00Z" }];
    await s.say([{ kind: "watch_state", itemId: uid(10) }]);
    await until(() => count(`GET /profiles/${uid(1)}/watch-state`) === 2);
    expect(r.textContent).not.toContain("Arrival");
    await s.say([{ kind: "library", libraryId: uid(1) }]);
    await until(() => r.textContent!.includes("Arrival"));
    // A resync may have missed a scan's event.
    added.push({ ...movie, id: uid(12), parsedTitle: "Sicario", addedAt: "2026-02-02T00:00:00Z" });
    await s.say([], true);
    await until(() => r.textContent!.includes("Sicario"));
  });

  it("redraws Manage household when its profiles or offers change", async () => {
    const s = stream();
    let incoming: unknown[] = [];
    await at("#/household");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...shell,
        "GET /events": s.route,
        "GET /profiles": page(me(1).profiles),
        "GET /household/offers": () => ({ incoming, outgoing: [] }),
      }),
    });
    await until(() => r.querySelector(".members li"));
    incoming = [
      {
        id: uid(3),
        profileId: uid(7),
        profileName: "Mia",
        profileKind: "kid",
        householdId: uid(5),
        householdName: "Mark",
        fromProfileId: uid(8),
        fromName: "Mark",
        toProfileId: uid(1),
        toName: "Bea",
        createdAt: ts,
      },
    ];
    await s.say([{ kind: "household" }]);
    await until(() => r.textContent!.includes("Mark offers to share Mia with your household"));
  });

  describe("a profile's Hide kids' content changed on another device", () => {
    const run = async (hide: boolean) => {
      const s = stream();
      let flag = false;
      await at("#/");
      const r = root();
      const routes = home(s, () => []);
      await boot(r, {
        fetch: mockFetch({
          ...routes,
          "GET /me": () => ({
            ...me(1),
            profiles: [{ ...acct(1, "Bea"), hideKidsContent: flag }, acct(2, "Jax", "kid")],
          }),
        }),
      });
      await until(() => r.querySelector("main")!.textContent!.includes("Recently added"));
      const lists = count("GET /items");
      flag = hide;
      await s.say([{ kind: "household" }]);
      await until(() => count("GET /me") >= 2);
      return { r, lists };
    };

    it("boots again with fresh lists when the active profile's flag differs", async () => {
      const { r, lists } = await run(true);
      await until(() => count("GET /health") === 2);
      await until(() => r.querySelector("main")!.textContent!.includes("Recently added"));
      expect(count("GET /items")).toBeGreaterThan(lists);
    });

    it("stays put when the flag is the same", async () => {
      await run(false);
      await new Promise((r) => setTimeout(r, 50));
      expect(count("GET /health")).toBe(1);
    });
  });

  it("follows once after two boots overlap, and shows the later boot's profile", async () => {
    const s = stream();
    await at("#/");
    const r = root();
    const first = boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        "GET /me": () => new Promise((done) => setTimeout(() => done(me(1)), 50)),
      }),
    });
    const second = boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        "GET /me": me(2),
        [`GET /profiles/${uid(2)}/watch-state`]: page([]),
      }),
    });
    await Promise.all([first, second]);
    await new Promise((r) => setTimeout(r, 50));
    await until(() => r.querySelector("main")!.textContent!.includes("Recently added"));
    expect(s.asked.filter((a) => a === "")).toHaveLength(1);
    expect(r.querySelector(".profile")!.textContent).toContain("Jax");
  });

  it("leaves a page alone for an event it shows nothing of, and under an open dialog until it closes", async () => {
    const s = stream();
    await at("#/");
    const r = root();
    await boot(r, { fetch: mockFetch(home(s, () => [])) });
    await until(() => r.querySelector("main")!.textContent!.includes("Recently added"));
    const before = count(`GET /profiles/${uid(1)}/watch-state`);
    await s.say([{ kind: "playlist", playlistId: uid(3) }]);
    await s.say([]);
    expect(count(`GET /profiles/${uid(1)}/watch-state`)).toBe(before);

    const scrim = document.body.appendChild(document.createElement("div"));
    scrim.className = "scrim";
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await s.say([{ kind: "watch_state", itemId: uid(10) }]);
      await vi.advanceTimersByTimeAsync(1000);
      expect(count(`GET /profiles/${uid(1)}/watch-state`)).toBe(before);
      scrim.remove();
      await vi.advanceTimersByTimeAsync(2500);
    } finally {
      vi.useRealTimers();
    }
    await until(() => count(`GET /profiles/${uid(1)}/watch-state`) === before + 1);
  });

  it("keeps a menu opened while a redraw was still reading the page", async () => {
    const s = stream();
    let hold = Promise.resolve();
    await at(`#/movie/${uid(10)}`);
    const r = root();
    const routes = home(s, () => []);
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        [`GET /items/${uid(10)}`]: async () => (await hold, movie),
        [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] },
        "GET /media-files": page([]),
      }),
    });
    const more = () => [...r.querySelectorAll("button")].find((b) => b.textContent === "More");
    await until(more);
    let release!: () => void;
    hold = new Promise((done) => (release = done));
    const asked = count(`GET /items/${uid(10)}`);
    await s.say([{ kind: "watch_state", itemId: uid(10) }]);
    await until(() => count(`GET /items/${uid(10)}`) > asked);
    more()!.click();
    const open = r.querySelector(".menu.drop:not([hidden])")!;
    release();
    await new Promise((done) => setTimeout(done, 100));
    expect(open.isConnected).toBe(true);
    // The redraw it gave way to follows once the menu closes.
    more()!.click();
    await until(() => count(`GET /items/${uid(10)}`) > asked + 1);
  });

  it("paints artwork in a dialog, which sits outside the app's root", async () => {
    URL.createObjectURL = () => "blob:pick";
    const s = stream();
    await at("#/movies");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        "GET /genres": page([]),
        [`GET /profiles/${uid(1)}/playlists`]: page([]),
        "GET /items": page([]),
        [`GET /items/${uid(10)}/artwork/poster`]: () =>
          new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } }),
      }),
    });
    const scrim = document.body.appendChild(document.createElement("div"));
    scrim.className = "scrim";
    const pick = scrim.appendChild(document.createElement("div"));
    pick.dataset.art = `${uid(10)}/poster`;
    await until(() => pick.style.backgroundImage === `url("blob:pick")`);
  });

  it("keeps the Movies filter and sort across a redraw", async () => {
    const s = stream();
    const movies = [
      { ...movie, addedAt: "2026-01-01T00:00:00Z" },
      { ...movie, id: uid(11), parsedTitle: "Arrival", addedAt: "2026-02-01T00:00:00Z" },
      { ...movie, id: uid(12), parsedTitle: "Sicario", addedAt: "2026-03-01T00:00:00Z" },
    ];
    const seen = (itemId: number) => ({ ...state(0), itemId: uid(itemId), watched: true });
    let watched = [seen(10)];
    await at("#/movies");
    const r = root();
    const routes = home(s, () => watched);
    await boot(r, {
      fetch: mockFetch({
        ...routes,
        "GET /genres": page([]),
        [`GET /profiles/${uid(1)}/playlists`]: page([]),
        "GET /items": (_: unknown, url: URL) =>
          page(url.searchParams.get("kind") === "movie" ? movies : []),
      }),
    });
    const titles = () => [...r.querySelectorAll(".grid b")].map((b) => b.textContent);
    await until(() => titles().length === 3);
    const entries = history.length;
    const asked = count("GET /items");
    [...r.querySelectorAll<HTMLElement>(".chip")]
      .find((c) => c.textContent === "Unwatched")!
      .click();
    expect(titles()).toEqual(["Sicario", "Arrival"]);
    const sort = [...r.querySelectorAll<HTMLSelectElement>("select")].find((e) =>
      e.parentElement!.textContent!.startsWith("Sort"),
    )!;
    sort.value = "Title";
    sort.dispatchEvent(new Event("change"));
    expect(titles()).toEqual(["Arrival", "Sicario"]);
    // A pick redraws from memory and leaves no history entry.
    expect(history.length).toBe(entries);
    expect(count("GET /items")).toBe(asked);

    watched = [seen(10), seen(12)];
    await s.say([{ kind: "watch_state", itemId: uid(12) }]);
    await until(() => titles().join() === "Arrival");
    expect(r.querySelector(".chip[aria-pressed=true]")!.textContent).toBe("Unwatched");
    expect(
      [...r.querySelectorAll<HTMLSelectElement>("select")].some((e) => e.value === "Title"),
    ).toBe(true);
    expect(location.hash).toBe("#/movies?filter=Unwatched&sort=Title");
  });

  it("keeps the Music albums genre and sort across a redraw", async () => {
    const s = stream();
    const album = (id: number, title: string, genre: string, addedAt: string) => ({
      id: uid(id),
      libraryId: uid(3),
      artistId: uid(50),
      artistName: "Radiohead",
      title,
      year: 2007,
      songCount: 4,
      durationMs: 1_000_000,
      lossless: true,
      genres: [{ id: uid(genre.length), name: genre }],
      addedAt,
    });
    const albums = [
      album(100, "In Rainbows", "Rock", "2026-01-01T00:00:00Z"),
      album(101, "Blue Train", "Jazz", "2026-02-01T00:00:00Z"),
      album(102, "Kid A", "Rock", "2026-03-01T00:00:00Z"),
    ];
    await at("#/music");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        [`GET /profiles/${uid(1)}/playlists`]: page([]),
        "GET /music/albums": () => page(albums),
      }),
    });
    const titles = () => [...r.querySelectorAll(".grid b")].map((b) => b.textContent);
    await until(() => titles().length === 3);
    const pick = (label: string, value: string) => {
      const e = [...r.querySelectorAll<HTMLSelectElement>("select")].find((x) =>
        x.parentElement!.textContent!.startsWith(label),
      )!;
      e.value = value;
      e.dispatchEvent(new Event("change"));
    };
    const entries = history.length;
    const asked = count("GET /music/albums");
    pick("Genre", "Rock");
    expect(titles()).toEqual(["Kid A", "In Rainbows"]);
    pick("Sort", "Title");
    expect(titles()).toEqual(["In Rainbows", "Kid A"]);
    // A pick redraws from memory and leaves no history entry.
    expect(history.length).toBe(entries);
    expect(count("GET /music/albums")).toBe(asked);

    albums.push(album(103, "Amnesiac", "Rock", "2026-04-01T00:00:00Z"));
    await s.say([{ kind: "playlist" }]);
    await until(() => titles().join() === "Amnesiac,In Rainbows,Kid A");
    expect(location.hash).toBe("#/music?genre=Rock&sort=Title");
  });

  it("keeps the YouTube videos shown by Show more across a redraw", async () => {
    const s = stream();
    let first = "First";
    const video = (id: number, title: string) => ({
      id: uid(id),
      libraryId: uid(5),
      channelId: uid(40),
      channelTitle: "Workbench Weekly",
      title,
      durationMs: 60_000,
      watched: false,
      positionMs: 0,
      addedAt: ts,
    });
    await at("#/youtube");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        "GET /youtube/channels": page([
          {
            id: uid(40),
            libraryId: uid(5),
            title: "Workbench Weekly",
            videoCount: 2,
            unwatchedCount: 2,
            addedAt: ts,
          },
        ]),
        "GET /youtube/videos": (_: unknown, url: URL) =>
          url.searchParams.get("offset") === "0"
            ? { items: [video(60, first)], nextOffset: 1 }
            : page([video(61, "Second")]),
      }),
    });
    const titles = () => [...r.querySelectorAll(".grid.vids b")].map((b) => b.textContent);
    await until(() => titles().length === 1);
    r.querySelector<HTMLButtonElement>("button.more")!.click();
    await until(() => titles().length === 2);

    first = "First, again";
    await s.say([{ kind: "watch_state", itemId: uid(60) }]);
    await until(() => titles()[0] === "First, again");
    expect(titles()).toEqual(["First, again", "Second"]);
  });

  it("keeps a channel page's Sort across a redraw", async () => {
    const s = stream();
    const video = (id: number, title: string) => ({
      id: uid(id),
      libraryId: uid(5),
      channelId: uid(40),
      channelTitle: "Workbench Weekly",
      title,
      durationMs: 60_000,
      watched: false,
      positionMs: 0,
      addedAt: ts,
    });
    await at(`#/channel/${uid(40)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...home(s, () => []),
        [`GET /youtube/channels/${uid(40)}`]: {
          id: uid(40),
          libraryId: uid(5),
          title: "Workbench Weekly",
          videoCount: 1,
          unwatchedCount: 1,
          addedAt: ts,
        },
        [`GET /youtube/channels/${uid(40)}/allowed-profiles`]: page([]),
        "GET /youtube/videos": (_: unknown, url: URL) =>
          page([video(60, `by ${url.searchParams.get("sort")}`)]),
      }),
    });
    const titles = () => [...r.querySelectorAll(".grid.vids b")].map((b) => b.textContent);
    await until(() => titles().length === 1);
    const sort = r.querySelector<HTMLSelectElement>("select")!;
    const entries = history.length;
    sort.value = "Oldest";
    sort.dispatchEvent(new Event("change"));
    await until(() => titles()[0] === "by oldest");
    expect(history.length).toBe(entries);

    await s.say([{ kind: "watch_state", itemId: uid(60) }]);
    await until(() => count(`GET /youtube/channels/${uid(40)}`) === 2 && titles().length === 1);
    expect(titles()).toEqual(["by oldest"]);
    expect(r.querySelector<HTMLSelectElement>("select")!.value).toBe("Oldest");
    expect(location.hash).toBe(`#/channel/${uid(40)}?sort=oldest`);
  });
});

describe("player", () => {
  beforeEach(() => {
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
  const playing = (id: number) => ({
    sessionId: uid(id),
    entry: {
      position: 0,
      verdict: "direct_play",
      videoAction: "copy",
      audioAction: "copy",
      subtitleAction: "none",
      bad: false,
    },
    streamUrl: "/api/v2/streams/k/0/file",
    streamKind: "file",
    positionMs: 0,
    negotiated: { verdict: "direct_play" },
    rungs: [],
    sidecars: [],
  });
  const there = {
    id: uid(7),
    itemId: uid(10),
    deviceId: uid(2),
    deviceName: "Living Room",
    platform: "roku",
    thisDevice: false,
    positionMs: 2_472_000,
    modifiedAt: ts,
  };
  const routes = (extra: Record<string, Handler | unknown> = {}) => ({
    ...shell,
    [`GET /items/${uid(10)}`]: movie,
    "GET /media-files": page([]),
    "GET /watch-together": page([]),
    "GET /playback/sessions": page([there]),
    "POST /playback/sessions": () => json(playing(8), 201),
    [`PUT /playback/sessions/${uid(8)}/position`]: none,
    [`DELETE /playback/sessions/${uid(8)}`]: none,
    ...extra,
  });
  const prompt = () => document.querySelector<HTMLElement>(".dialog");
  const press = (label: string) =>
    [...prompt()!.querySelectorAll("button")].find((b) => b.textContent === label)!.click();

  it("asks before playing a title that plays on another device, and Move here takes it over where it is", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    void boot(r, {
      fetch: mockFetch(
        routes({
          [`POST /playback/sessions/${uid(7)}/move`]: () => json({ ...there, endedAt: ts }, 200),
        }),
      ),
    });
    await until(prompt);
    expect(prompt()!.querySelector("h1")!.textContent).toBe("Playing on Living Room at 41:12");
    expect([...prompt()!.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Move here",
      "Watch together",
      "Cancel",
    ]);
    expect(document.activeElement!.textContent).toBe("Move here");
    expect(calls.find((c) => c.key === "GET /playback/sessions")!.query).toBe(`?itemId=${uid(10)}`);
    expect(count("POST /playback/sessions")).toBe(0);
    press("Move here");
    await until(() => count("POST /playback/sessions") === 1);
    expect(count(`POST /playback/sessions/${uid(7)}/move`)).toBe(1);
    expect(calls.find((c) => c.key === "POST /playback/sessions")!.body).toMatchObject({
      itemId: uid(10),
      positionMs: 2_472_000,
    });
    expect(prompt()).toBeNull();
  });

  it("Cancel plays nothing and leaves the player", async () => {
    await at(`#/movie/${uid(10)}`);
    await at(`#/play/${uid(10)}`);
    const r = root();
    void boot(r, { fetch: mockFetch(routes()) });
    await until(prompt);
    press("Cancel");
    await until(() => !location.hash.startsWith("#/play"));
    expect(count("POST /playback/sessions")).toBe(0);
    expect(count(`POST /playback/sessions/${uid(7)}/move`)).toBe(0);
  });

  it("Watch together opens one from the other device's session, then plays", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    void boot(r, { fetch: mockFetch(routes({ "POST /watch-together": () => json({}, 201) })) });
    await until(prompt);
    press("Watch together");
    await until(() => count("POST /playback/sessions") === 1);
    expect(calls.find((c) => c.key === "POST /watch-together")!.body).toEqual({
      itemId: uid(10),
      fromSessionId: uid(7),
    });
    expect(count(`POST /playback/sessions/${uid(7)}/move`)).toBe(0);
  });

  it("ends the session of a player that a new boot replaces without a navigation", async () => {
    await at(`#/play/${uid(10)}`);
    void boot(root(), { fetch: mockFetch(routes({ "GET /playback/sessions": page([]) })) });
    await until(() => count("POST /playback/sessions") === 1);
    // A sign-out or a profile pick boots again where the viewer stands.
    void boot(root(), { fetch: mockFetch(routes({ "GET /playback/sessions": page([]) })) });
    await until(() => count(`DELETE /playback/sessions/${uid(8)}`) === 1);
  });

  it("does not ask for this device's own session, and offers a kid Watch together too", async () => {
    await at(`#/play/${uid(10)}`);
    let r = root();
    void boot(r, {
      fetch: mockFetch(
        routes({ "GET /playback/sessions": page([{ ...there, thisDevice: true }]) }),
      ),
    });
    await until(() => count("POST /playback/sessions") === 1);
    expect(prompt()).toBeNull();

    document.body.replaceChildren();
    r = root();
    void boot(r, { fetch: mockFetch(routes({ "GET /me": me(2) })) });
    await until(prompt);
    expect([...prompt()!.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Move here",
      "Watch together",
      "Cancel",
    ]);
  });

  it("closes with Moved to the device that took its session over", async () => {
    const s = stream();
    await at(`#/movie/${uid(10)}`);
    await at(`#/play/${uid(10)}`);
    const r = root();
    void boot(r, {
      fetch: mockFetch(
        routes({
          "GET /events": s.route,
          "GET /playback/sessions": page([]),
          [`GET /playback/sessions/${uid(8)}`]: {
            ...there,
            id: uid(8),
            thisDevice: true,
            endedAt: ts,
            movedToDeviceName: "Kitchen iPad",
          },
        }),
      ),
    });
    await until(() => count("POST /playback/sessions") === 1);
    await s.say([{ kind: "playback", change: "moved", sessionId: uid(99), itemId: uid(10) }]);
    await s.say([{ kind: "playback", change: "moved", sessionId: uid(8), itemId: uid(10) }]);
    await until(() => document.querySelector(".toast"));
    expect(document.querySelector(".toast")!.textContent).toBe("Moved to Kitchen iPad");
    expect(document.querySelector(".toast")!.getAttribute("role")).toBe("status");
    await until(() => !location.hash.startsWith("#/play"));
    expect(count(`GET /playback/sessions/${uid(99)}`)).toBe(0);
  });

  it("joins the Watch together another device opened from its session, and plays for both", async () => {
    const s = stream();
    await at(`#/play/${uid(10)}`);
    const r = root();
    void boot(r, {
      fetch: mockFetch(
        routes({
          "GET /events": s.route,
          "GET /playback/sessions": page([]),
          [`POST /watch-together/${uid(3)}/join`]: () => json({}, 200),
          [`PUT /watch-together/${uid(3)}/state`]: () => json({}, 200),
        }),
      ),
    });
    await until(() => count("POST /playback/sessions") === 1);
    await s.say([
      { kind: "watch_together", change: "opened", watchTogetherId: uid(2), itemId: uid(10) },
    ]);
    await s.say([
      {
        kind: "watch_together",
        change: "opened",
        watchTogetherId: uid(3),
        itemId: uid(10),
        sessionId: uid(8),
      },
    ]);
    await until(() => count(`PUT /watch-together/${uid(3)}/state`) === 1);
    expect(count(`POST /watch-together/${uid(3)}/join`)).toBe(1);
    expect(count(`POST /watch-together/${uid(2)}/join`)).toBe(0);
    expect(calls.find((c) => c.key === `PUT /watch-together/${uid(3)}/state`)!.body).toMatchObject({
      playing: true,
    });
    // The player starts over, which finds the Watch together it is now in.
    await until(() => count("POST /playback/sessions") === 2);
  });
});
