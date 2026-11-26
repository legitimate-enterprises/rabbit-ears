// Component tests for editions: the real pages and generated client against a
// mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label)!;
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
const page = (items: unknown[]) => ({ items });
const base = (watch: unknown[] = []) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": {
    device: { id: 1 },
    account: acct(1, "Heidi"),
    activeProfileId: 1,
    household: { id: 1, name: "Heidi", adultId: 1, createdAt: ts },
    profiles: [acct(1, "Heidi"), acct(5, "Bea")],
  },
  "GET /profiles/1/watch-state": page(watch),
  "GET /profiles/1/show-progress": page([]),
  ...Object.fromEntries(
    watch.map((w) => [`GET /profiles/1/watch-state/${(w as { itemId: number }).itemId}`, w]),
  ),
  "GET /watch-together": page([]),
  "GET /media-files": page([]),
});
const copy = (mediaFileId: number, quality: string) => ({ mediaFileId, quality });
const STANDARD = {
  id: 401,
  name: "Standard",
  isDefault: true,
  runtimeMs: 7_800_000,
  copies: [copy(1, "4K"), copy(2, "1080p")],
};
const EXTENDED = {
  id: 402,
  name: "Extended",
  isDefault: false,
  runtimeMs: 13_680_000,
  copies: [copy(3, "1080p")],
};
const movie = (editions: unknown[]) => ({
  id: 30,
  libraryId: 1,
  kind: "movie",
  parsedTitle: "Lantern Coast",
  parsedYear: 2004,
  addedAt: ts,
  updatedAt: ts,
  editions,
});
const position = (editionId?: number) => ({
  itemId: 30,
  positionMs: 600_000,
  editionId,
  watched: false,
  playCount: 0,
  updatedAt: ts,
});
const details = (editions: unknown[], watch: unknown[] = [], extra = {}) => ({
  ...base(watch),
  "GET /items/30": movie(editions),
  "GET /items/30/credits": { cast: [], crew: [] },
  "GET /profiles/1/playlists": page([
    {
      id: 5,
      profileId: 1,
      access: "owner",
      name: "Weekend",
      kind: "movies",
      itemCount: 0,
      durationMs: 0,
      createdAt: ts,
      updatedAt: ts,
    },
  ]),
  "POST /profiles/1/playlists/5/entries": {},
  ...extra,
});
const playLink = (r: HTMLElement) => r.querySelector<HTMLAnchorElement>("a.btn.p")!;
const edition = (r: HTMLElement) =>
  [...r.querySelectorAll("label")].find((l) => l.textContent!.startsWith("Edition"));
const choose = (s: HTMLSelectElement, value: number) => {
  s.value = String(value);
  s.dispatchEvent(new Event("change"));
};

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
  setToken("tok");
});

describe("movie details with editions", () => {
  it("lists each edition with its runtime and qualities, the default first and chosen", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(details([STANDARD, EXTENDED])) });
    await until(() => edition(r));
    const s = edition(r)!.querySelector("select")!;
    expect(text(s.options)).toEqual(["Standard · 2h 10m · 4K, 1080p", "Extended · 3h 48m · 1080p"]);
    expect(s.value).toBe("401");
    expect(playLink(r).textContent).toBe("Play");
    expect(playLink(r).getAttribute("href")).toBe("#/play/30?edition=401");
    choose(s, 402);
    expect(playLink(r).getAttribute("href")).toBe("#/play/30?edition=402");
  });

  it("lists the audio and subtitles of the selected edition's file, and sends only its track ids", async () => {
    const file = (id: number, size: number, audio: string, subtitle: string) => ({
      id,
      itemId: 30,
      path: `/movies/Lantern Coast/${audio}.mkv`,
      sizeBytes: size,
      modifiedAt: ts,
      tracks: [
        { id: `${id}-v`, index: 0, kind: "video", codec: "h264", width: 1920, height: 1080 },
        { id: `${id}-a`, index: 1, kind: "audio", codec: "aac", language: audio, isDefault: true },
        { id: `${id}-s`, index: 2, kind: "subtitle", codec: "subrip", language: subtitle },
      ],
      chapters: [],
    });
    vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {} }));
    Object.defineProperty(navigator, "mediaCapabilities", {
      configurable: true,
      value: { decodingInfo: async () => ({ supported: false }) },
    });
    await at("#/movie/30");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        details([STANDARD, EXTENDED], [], {
          // The largest file, listed first, is the Standard's; the Extended plays file 3.
          "GET /media-files": page([
            file(1, 9e9, "English", "fre"),
            file(2, 5e9, "English", "fre"),
            file(3, 5e8, "German", "dut"),
          ]),
          "POST /items/30/playability": { verdict: "direct_play" },
        }),
      ),
    });
    await until(() => edition(r));
    const audio = () => r.querySelector<HTMLSelectElement>("select[aria-label=Audio]")!;
    const subs = () => r.querySelector("dl.kv")!.textContent;
    expect(text(audio().options)).toEqual(["English - AAC"]);
    choose(edition(r)!.querySelector("select")!, 402);
    await until(() => text(audio().options)[0] === "German - AAC");
    expect(subs()).toContain("Dutch");
    expect(subs()).not.toContain("French");
    expect(subs()).toContain("German.mkv - 500 MB");
    audio().value = "3-a";
    audio().dispatchEvent(new Event("change"));
    expect(playLink(r).getAttribute("href")).toBe("#/play/30?edition=402&audio=3-a");
    choose(edition(r)!.querySelector("select")!, 401);
    await until(() => text(audio().options)[0] === "English - AAC");
    expect(playLink(r).getAttribute("href")).toBe("#/play/30?edition=401");
  });

  it("shows the 4K and HDR tags of the selected edition's file", async () => {
    const file = (id: number, height: number, hdrFormat?: string) => ({
      id,
      itemId: 30,
      path: `/movies/Lantern Coast/${id}.mkv`,
      sizeBytes: 1e9,
      modifiedAt: ts,
      tracks: [{ id: `${id}-v`, index: 0, kind: "video", codec: "hevc", height, hdrFormat }],
      chapters: [],
    });
    vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {} }));
    Object.defineProperty(navigator, "mediaCapabilities", {
      configurable: true,
      value: { decodingInfo: async () => ({ supported: false }) },
    });
    await at("#/movie/30");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        details([STANDARD, EXTENDED], [], {
          "GET /media-files": page([file(1, 2160, "hdr10"), file(3, 1080)]),
          "POST /items/30/playability": { verdict: "direct_play" },
        }),
      ),
    });
    await until(() => edition(r));
    const tags = () =>
      text([...r.querySelectorAll(".meta .tag")].filter((t) => !(t as HTMLElement).hidden));
    expect(tags()).toEqual(["4K", "HDR"]);
    choose(edition(r)!.querySelector("select")!, 402);
    await until(() => tags().length === 0);
    choose(edition(r)!.querySelector("select")!, 401);
    await until(() => tags().length === 2);
  });

  it("chooses the edition the saved position belongs to and offers Resume only for it", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(details([STANDARD, EXTENDED], [position(402)])) });
    await until(() => edition(r));
    const s = edition(r)!.querySelector("select")!;
    expect(s.value).toBe("402");
    expect(calls.map((c) => c.key)).not.toContain("GET /profiles/1/watch-state");
    expect(playLink(r).textContent).toBe("Resume");
    choose(s, 401);
    expect(playLink(r).textContent).toBe("Play");
    choose(s, 402);
    expect(playLink(r).textContent).toBe("Resume");
  });

  it("shows no choice and sends no edition for a title with one edition", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(details([STANDARD], [position(401)])) });
    await until(() => playLink(r));
    expect(edition(r)).toBeUndefined();
    expect(playLink(r).textContent).toBe("Resume");
    expect(playLink(r).getAttribute("href")).toBe("#/play/30");

    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    button(document.querySelector("[role=dialog]")!, "Weekend").click();
    await until(() => button(r, "Added to Weekend"));
    expect(calls.find((c) => c.key === "POST /profiles/1/playlists/5/entries")!.body).toEqual({
      itemId: 30,
    });
  });

  it("adds the chosen edition to a playlist without asking", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(details([STANDARD, EXTENDED])) });
    await until(() => edition(r));
    choose(edition(r)!.querySelector("select")!, 402);
    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    button(document.querySelector("[role=dialog]")!, "Weekend").click();
    await until(() => button(r, "Added to Weekend"));
    const posts = calls.filter((c) => c.key === "POST /profiles/1/playlists/5/entries");
    expect(posts.map((c) => c.body)).toEqual([{ itemId: 30, editionId: 402 }]);
  });

  it("hides the choice while opening or inside a Watch together", async () => {
    const host = {
      accountId: 1,
      name: "Heidi",
      role: "host",
      deviceName: "Web",
      platform: "web",
      present: true,
      joinedAt: ts,
    };
    const waiting = {
      id: 3,
      itemId: 30,
      status: "waiting",
      members: [host],
      joinBy: ts,
      joinMsLeft: 252_000,
      playing: false,
      positionMs: 0,
      version: 1,
      serverTime: ts,
      openedAt: ts,
      serverTimeMs: Date.now(),
      effectiveAtMs: Date.now(),
      effectivePositionMs: 0,
    };
    await at("#/movie/30");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        details([STANDARD, EXTENDED], [position(402)], {
          "GET /watch-together": page([waiting]),
          "GET /watch-together/3": waiting,
        }),
      ),
    });
    await until(() => edition(r));
    expect(edition(r)!.hidden).toBe(true);
    // The default edition plays in a Watch together, from the start when the position is another's.
    expect(playLink(r).textContent).toBe("Play");
  });
});

describe("resuming from the watch state", () => {
  const ep = (id: number, n: number) => ({
    id,
    libraryId: 2,
    kind: "episode",
    parentId: 11,
    seasonNumber: 1,
    episodeNumber: n,
    parsedTitle: `Episode ${n}`,
    addedAt: ts,
    updatedAt: ts,
  });
  const show = { ...ep(10, 0), kind: "show", parentId: undefined, parsedTitle: "Harbor Lights" };
  const season = { ...ep(11, 0), kind: "season", parentId: 10 };
  const library = {
    "GET /items": (_: unknown, u: URL) => {
      const k = u.searchParams.get("kind");
      return page(
        k === "movie"
          ? [movie([]), { ...movie([]), id: 31, parsedTitle: "Tidewater" }]
          : k === "show"
            ? [show]
            : k === "season"
              ? [season]
              : k === "episode"
                ? [ep(21, 1), ep(22, 2)]
                : [],
      );
    },
    "GET /items/10": show,
    "GET /items/11": season,
    "GET /items/21": ep(21, 1),
    "GET /items/30": movie([]),
    "GET /items/31": { ...movie([]), id: 31, parsedTitle: "Tidewater" },
  };
  const at2 = (itemId: number, editionId: number | undefined, lastPlayedAt: string) => ({
    ...position(editionId),
    itemId,
    lastPlayedAt,
  });
  const watch = [
    at2(30, 402, "2026-01-03T00:00:00Z"),
    at2(31, undefined, "2026-01-02T00:00:00Z"),
    at2(21, 501, "2026-01-01T00:00:00Z"),
  ];

  it("sends the edition the position belongs to from Home and the show and season pages", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(watch),
        ...library,
        "GET /youtube/videos": page([]),
        "GET /items/10/credits": { cast: [], crew: [] },
      }),
    });
    await until(() => r.querySelector(".hero"));
    expect(r.querySelector(".hero-tile")!.getAttribute("data-art")).toBe("30/backdrop");
    expect(button(r, "Resume").getAttribute("href")).toBe("#/play/30?edition=402");
    const also = [...r.querySelectorAll(".also a")].map((a) => a.getAttribute("href"));
    expect(also).toEqual(["#/play/31", "#/play/21?edition=501"]);

    for (const [hash, label] of [
      ["#/show/10", "Resume S1 E1"],
      ["#/season/11", "Resume E1"],
      ["#/episode/21", "Resume"],
    ]) {
      await at(hash);
      await until(() => button(r, label));
      expect(button(r, label).getAttribute("href")).toBe("#/play/21?edition=501");
    }
  });

  it("shows an episode's show backdrop in the Home hero", async () => {
    await at("#/");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base([at2(21, 501, ts)]),
        ...library,
        "GET /youtube/videos": page([]),
      }),
    });
    await until(() => r.querySelector(".hero"));
    expect(r.querySelector(".hero-tile")!.getAttribute("data-art")).toBe("10/backdrop");
  });
});

describe("add to playlist where editions are not known", () => {
  it("asks which edition after a 400 on editionId and adds again with it", async () => {
    const ep = {
      id: 21,
      libraryId: 2,
      kind: "episode",
      parentId: 11,
      seasonNumber: 1,
      episodeNumber: 1,
      parsedTitle: "Pilot",
      addedAt: ts,
      updatedAt: ts,
    };
    await at("#/episode/21");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...details([]),
        "GET /items": (_: unknown, u: URL) => {
          const k = u.searchParams.get("kind");
          return page(
            k === "show"
              ? [{ ...ep, id: 10, kind: "show", parsedTitle: "Harbor Lights" }]
              : k === "season"
                ? [{ ...ep, id: 11, kind: "season", parentId: 10 }]
                : k === "episode"
                  ? [ep]
                  : [],
          );
        },
        "GET /items/21": { ...ep, editions: [STANDARD, EXTENDED] },
        "GET /items/11": { ...ep, id: 11, kind: "season", parentId: 10 },
        "GET /items/10": { ...ep, id: 10, kind: "show", parsedTitle: "Harbor Lights" },
        "GET /profiles/1/playlists": page([
          {
            id: 6,
            profileId: 1,
            access: "owner",
            name: "Binge",
            kind: "tv",
            itemCount: 0,
            durationMs: 0,
            createdAt: ts,
            updatedAt: ts,
          },
        ]),
        "POST /profiles/1/playlists/6/entries": (b: unknown) =>
          (b as { editionId?: number }).editionId
            ? {}
            : json(
                {
                  code: "bad_request",
                  message: "invalid",
                  fields: [{ field: "editionId", message: "choose an edition" }],
                },
                400,
              ),
      }),
    });
    await until(() => button(r, "Add to playlist"));
    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    button(document.querySelector("[role=dialog]")!, "Binge").click();
    await until(() => button(document.body, "Extended · 3h 48m · 1080p"));
    button(document.body, "Extended · 3h 48m · 1080p").click();
    await until(() => button(r, "Added to Binge"));
    const posts = calls.filter((c) => c.key === "POST /profiles/1/playlists/6/entries");
    expect(posts.map((c) => c.body)).toEqual([{ itemId: 21 }, { itemId: 21, editionId: 402 }]);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });
});

describe("playlist rows", () => {
  it("names the edition after the title only when the entry carries one", async () => {
    const entry = (id: number, editionName?: string) => ({
      id,
      playlistId: 5,
      position: id - 1,
      kind: "movie",
      itemId: 30,
      editionId: 402,
      editionName,
      itemCount: 1,
      durationMs: 0,
      addedAt: ts,
      item: movie([]),
    });
    await at("#/playlist/5");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/playlists/5": {
          id: 5,
          profileId: 1,
          access: "owner",
          name: "Weekend",
          kind: "movies",
          itemCount: 2,
          durationMs: 0,
          createdAt: ts,
          updatedAt: ts,
        },
        "GET /profiles/1/playlists/5/entries": page([entry(1, "Extended"), entry(2)]),
        "GET /profiles/1/playlists/5/play-order": page([]),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    const titles = text(r.querySelectorAll(".tracks .qrow b")).map((t) => t!.split("2004")[0]);
    expect(titles).toEqual(["Lantern Coast · Extended", "Lantern Coast"]);
  });
});

describe("player", () => {
  const stream = () =>
    json(
      {
        sessionId: 7,
        entry: {
          position: 0,
          verdict: "direct_play",
          videoAction: "copy",
          audioAction: "copy",
          subtitleAction: "none",
          bad: false,
        },
        streamUrl: "/api/v1/streams/k/0/file",
        streamKind: "file",
        positionMs: 0,
        negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
        rungs: [],
        sidecars: [],
      },
      201,
    );
  const routes = (extra = {}) => ({
    ...base(),
    "GET /items/30": movie([STANDARD, EXTENDED]),
    "GET /items/31": { ...movie([]), id: 31, parsedTitle: "Tidewater" },
    "POST /playback/sessions": stream,
    "PUT /playback/sessions/7/position": none,
    "DELETE /playback/sessions/7": none,
    ...extra,
  });
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
  const started = () => calls.find((c) => c.key === "POST /playback/sessions")!.body;

  it("starts the edition chosen on details", async () => {
    await at("#/play/30?edition=402");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => r.querySelector(".player video"));
    expect(started()).toMatchObject({ itemId: 30, editionId: 402 });
  });

  it("starts a play-order row with its edition and names it, and the next row's", async () => {
    await at("#/play/30?playlist=9&n=0");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /profiles/1/playlists/9/play-order": page([
            {
              entryId: 1,
              itemId: 30,
              kind: "movie",
              editionId: 402,
              editionName: "Extended",
              item: movie([]),
            },
            {
              entryId: 2,
              itemId: 31,
              kind: "movie",
              editionId: 403,
              editionName: "Director's Cut",
              item: { ...movie([]), id: 31, parsedTitle: "Tidewater" },
            },
          ]),
          "GET /profiles/1/up-next/30": {
            itemId: 31,
            kind: "movie",
            title: "Tidewater · Director's Cut",
            editionId: 403,
            index: 1,
          },
        }),
      ),
    });
    await until(() => r.querySelector(".player video"));
    expect(started()).toMatchObject({ itemId: 30, editionId: 402, playlistId: 9 });
    expect(r.querySelector(".ph b.t")!.textContent).toBe("Lantern Coast · Extended");
    expect(calls.find((c) => c.key === "GET /profiles/1/up-next/30")!.query).toBe(
      "?playlistId=9&index=0",
    );
    r.querySelector("video")!.dispatchEvent(new Event("ended"));
    expect(r.querySelector(".upnext")!.textContent).toContain(
      "Up next: Tidewater · Director's Cut in 10s",
    );
  });

  it("sends no edition in a Watch together", async () => {
    await at("#/play/30?edition=402");
    const r = root();
    const joined = {
      id: 3,
      itemId: 30,
      status: "joined",
      members: [
        { accountId: 5, name: "Bea", role: "host", present: true, joinedAt: ts },
        { accountId: 1, name: "Heidi", role: "guest", present: true, joinedAt: ts },
      ],
      joinBy: ts,
      joinMsLeft: 0,
      playing: false,
      positionMs: 0,
      version: 1,
      serverTime: ts,
      openedAt: ts,
      serverTimeMs: Date.now(),
      effectiveAtMs: Date.now(),
      effectivePositionMs: 0,
    };
    await boot(r, {
      fetch: mockFetch(
        routes({ "GET /watch-together": page([joined]), "GET /watch-together/3": joined }),
      ),
    });
    await until(() => r.querySelector(".player video"));
    expect(started()).not.toHaveProperty("editionId");
  });

  it("says so when the edition has nothing to play", async () => {
    await at("#/play/30?edition=402");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "POST /playback/sessions": () =>
            json({ code: "conflict", message: "nothing playable" }, 409),
        }),
      ),
    });
    await until(() => r.querySelector(".player .fail"));
    expect(r.querySelector(".fail .err")!.textContent).toBe("No way to play this on this device.");
  });
});
