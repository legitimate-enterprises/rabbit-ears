// Shuffle of a show, season or collection: the order it draws, and the player
// following it against a mocked API.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { shuffled } from "./shuffle";
import type { Item } from "./data";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";
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
const item = (id: number, kind: string, extra = {}): Item =>
  ({
    id: uid(id),
    libraryId: uid(1),
    kind,
    parsedTitle: `T${id}`,
    addedAt: ts,
    modifiedAt: ts,
    ...extra,
  }) as Item;
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label);
const ids = (xs: Item[]) => xs.map((x) => x.id);

// Season 1 of two episodes, Specials of one, and an episode whose file is gone.
const show = item(10, "show", { metadata: { status: "matched", title: "Severance" } });
const s1 = item(11, "season", { parentId: uid(10), seasonNumber: 1 });
const s0 = item(12, "season", { parentId: uid(10), seasonNumber: 0 });
const ep = (id: number, season: Item, n: number, extra = {}) =>
  item(id, "episode", {
    parentId: season.id,
    seasonNumber: season.seasonNumber,
    episodeNumber: n,
    ...extra,
  });
const e1 = ep(21, s1, 1);
const e2 = ep(22, s1, 2);
const gone = ep(23, s1, 3, { unavailable: true });
const special = ep(31, s0, 1);
const children: Record<string, Item[]> = {
  [show.id]: [s1, s0],
  [s1.id]: [e1, e2, gone],
  [s0.id]: [special],
};
const session = () =>
  json(
    {
      sessionId: uid(7),
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
      negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
      rungs: [],
      sidecars: [],
    },
    201,
  );
const routes = (extra = {}) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me,
  // One watched and one half-watched: both are in the shuffle and start from the beginning.
  [`GET /profiles/${uid(1)}/watch-state`]: page([
    { itemId: e1.id, positionMs: 0, watched: true, playCount: 1, changedAt: ts },
    { itemId: e2.id, positionMs: 600000, watched: false, playCount: 0, changedAt: ts },
  ]),
  [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] },
  ...Object.fromEntries(
    [show, s1, s0, e1, e2, gone, special].map((i) => [`GET /items/${i.id}`, i]),
  ),
  "GET /items": (_: unknown, u: URL) => page(children[u.searchParams.get("parentId")!] ?? []),
  "GET /media-files": page([]),
  [`GET /profiles/${uid(1)}/up-next/${e1.id}`]: { itemId: e2.id, kind: "episode", title: "T22" },
  [`GET /profiles/${uid(1)}/up-next/${e2.id}`]: { itemId: gone.id, kind: "episode", title: "T23" },
  [`GET /profiles/${uid(1)}/up-next/${special.id}`]: none,
  "POST /playback/sessions": session,
  [`PUT /playback/sessions/${uid(7)}/position`]: none,
  [`DELETE /playback/sessions/${uid(7)}`]: none,
  ...extra,
});
const upNextCalls = () => calls.filter((c) => c.key.includes("/up-next/"));
const starts = () =>
  calls
    .filter((c) => c.key === "POST /playback/sessions")
    .map((c) => c.body as { itemId: string; positionMs?: number });

beforeEach(async () => {
  calls.length = 0;
  setToken("tok");
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
  document.body.replaceChildren();
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("shuffled", () => {
  it("draws every playable item once, unavailable ones left out", () => {
    const all = [e1, e2, gone, special];
    expect(ids(shuffled(all, () => 0))).toEqual([e2.id, special.id, e1.id]);
    expect(ids(shuffled(all, () => 0.99)).sort()).toEqual(ids([e1, e2, special]).sort());
    expect(shuffled([gone])).toEqual([]);
    expect(shuffled([])).toEqual([]);
    expect(ids(shuffled([e1]))).toEqual([e1.id]);
  });

  it("reaches every order of three exactly once over every draw", () => {
    const seen = new Set<string>();
    for (const a of [0, 1, 2])
      for (const b of [0, 1]) {
        const draws = [a / 3, b / 2];
        seen.add(ids(shuffled([e1, e2, special], () => draws.shift()!)).join());
      }
    expect(seen.size).toBe(6);
  });
});

describe("Shuffle", () => {
  it("is beside Play on a show, season and collection, and absent with nothing to play", async () => {
    const col = { id: uid(7), source: "custom", name: "Rainy", movieCount: 1, customOrder: false };
    const extra = {
      [`GET /collections/${uid(7)}`]: col,
      [`GET /collections/${uid(7)}/movies`]: page([item(40, "movie")]),
      [`GET /collections/${uid(8)}`]: { ...col, id: uid(8) },
      [`GET /collections/${uid(8)}/movies`]: page([item(41, "movie", { unavailable: true })]),
      "GET /collections": page([]),
    };
    const r = root();
    for (const [hash, shown] of [
      [`#/show/${show.id}`, true],
      [`#/season/${s1.id}`, true],
      [`#/season/${s0.id}`, true],
      [`#/collection/${uid(7)}`, true],
      [`#/collection/${uid(8)}`, false],
    ] as const) {
      await at(hash);
      await boot(r, { fetch: mockFetch(routes(extra)) });
      await until(() => r.querySelector("h1.big"));
      const b = button(r, "Shuffle");
      expect(!!b, hash).toBe(shown);
      if (b) {
        expect(b.tagName).toBe("BUTTON");
        expect(b.className).toBe("btn s");
        expect(b.querySelector("svg")).toBeTruthy();
        expect(b.parentElement!.className).toBe("actions");
      }
    }
    // A collection offers no Play.
    expect(button(r, "Play")).toBeUndefined();
  });

  it("plays a show's run to its end from the beginning of each, never asking the server", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    vi.spyOn(Math, "random").mockReturnValue(0);
    await at(`#/show/${show.id}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => button(r, "Shuffle"));
    button(r, "Shuffle")!.click();
    // Across seasons and specials, gone left out: e1 e2 special by () => 0 is e2 special e1.
    await until(() => location.hash === `#/play/${e2.id}?pos=0&shuffle=0`);
    const order = [e2, special, e1];
    for (const [n, x] of order.entries()) {
      await until(() => starts().length === n + 1 && r.querySelector(".player video"));
      expect(starts()[n]).toMatchObject({ itemId: x.id, positionMs: 0 });
      r.querySelector("video")!.dispatchEvent(new Event("ended"));
      if (n === order.length - 1) break;
      const t = order[n + 1];
      expect(r.querySelector(".upnext")!.textContent).toContain(
        `Up next: Severance · S${t.seasonNumber} E${t.episodeNumber} · ${t.parsedTitle} in 10s`,
      );
      vi.advanceTimersByTime(11_000);
      expect(location.hash).toBe(`#/play/${t.id}?pos=0&shuffle=${n + 1}`);
    }
    // The run is over: no card, and nothing asked of the server.
    expect(r.querySelector<HTMLElement>(".upnext")!.hidden).toBe(true);
    expect(upNextCalls()).toEqual([]);
    expect(starts()).toHaveLength(3);
  });

  it("follows a season's run with Next, and a play by hand ends the run", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    await at(`#/season/${s1.id}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => button(r, "Shuffle"));
    button(r, "Shuffle")!.click();
    // e1 e2 by () => 0 is e2 e1.
    await until(() => location.hash === `#/play/${e2.id}?pos=0&shuffle=0`);
    await until(() => r.querySelector<HTMLElement>(".player [aria-label=Next]"));
    r.querySelector<HTMLElement>(".player [aria-label=Next]")!.click();
    await until(() => location.hash === `#/play/${e1.id}?pos=0&shuffle=1`);
    await until(() => starts().length === 2 && r.querySelector(".player video"));
    expect(starts()[1]).toMatchObject({ itemId: e1.id, positionMs: 0 });
    expect(r.querySelector(".player [aria-label=Next]")).toBeNull();
    expect(upNextCalls()).toEqual([]);

    // Played by hand, an item of the run asks the server again, and the run is gone after.
    await at(`#/play/${e2.id}`);
    await until(() => upNextCalls().length === 1);
    await at(`#/play/${e1.id}?pos=0&shuffle=1`);
    await until(() => starts().length === 4 && r.querySelector(".player video"));
    expect(r.querySelector(".player [aria-label=Next]")).toBeNull();
    expect(upNextCalls()).toHaveLength(1);
  });
});
