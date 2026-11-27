// Watch together: the sync rule against a fake player, the details controls
// and the player chip, against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createApi, setToken } from "../admin/api";
import { boot } from "./app";
import { findTogether, otherName, Sync, type Port, type Together } from "./together";
import { type Handler, at, calls, json, mockFetch, none, root } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
    static Events = { ERROR: "e", MANIFEST_PARSED: "m", LEVEL_SWITCHED: "l" };
  },
}));

const until = (f: () => unknown, timeout = 4000) =>
  vi.waitFor(() => expect(f()).toBeTruthy(), { timeout });
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
// The active adult is Bea (1); Helga (5) is the other adult and Jason (2) a kid.
const me = (active: number) => ({
  device: { id: uid(1) },
  account: acct(1, "Bea"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Bea", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Bea"), acct(5, "Helga"), acct(2, "Jason", "kid")],
});
const page = (items: unknown[]) => ({ items });
const member = (accountId: number, name: string, role: "host" | "guest") => ({
  accountId: uid(accountId),
  name,
  role,
  deviceName: "Apple TV",
  platform: "tvos" as const,
  present: true,
  joinedAt: ts,
});
const HELGA = member(5, "Helga", "host");
const BEA = member(1, "Bea", "guest");
const wt = (extra: Partial<Together> = {}): Together => ({
  id: uid(3),
  itemId: uid(10),
  status: "joined",
  members: [HELGA, BEA],
  joinBy: ts,
  joinMsLeft: 252_000,
  playing: false,
  positionMs: 0,
  version: 1,
  serverTime: ts,
  openedAt: ts,
  // On this clock, in effect now at positionMs unless the test says otherwise.
  serverTimeMs: Date.now(),
  effectiveAtMs: Date.now(),
  effectivePositionMs: extra.positionMs ?? 0,
  ...extra,
});
const waiting = (extra: Partial<Together> = {}) =>
  wt({ status: "waiting", members: [HELGA], ...extra });

class FakePort implements Port {
  isPaused = true;
  pos = 0;
  seeks: number[] = [];
  paused = () => this.isPaused;
  ms = () => this.pos;
  play = () => (this.isPaused = false);
  pause = () => (this.isPaused = true);
  seek = (ms: number) => (this.pos = ms) && this.seeks.push(ms);
}
const sync = (
  state: Together,
  routes: Record<string, Handler | unknown>,
  port = new FakePort(),
) => {
  const seen: Together[] = [];
  const api = createApi({ token: "t", fetch: mockFetch(routes) });
  return { s: new Sync(api, state, port, (w) => seen.push(w)), port, seen };
};

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

describe("Sync", () => {
  const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

  it("plays and pauses to match the shared state and seeks only when more than 0.5 s off", async () => {
    let shared = wt({ playing: true, positionMs: 1000, version: 2 });
    const { s, port } = sync(wt(), { [`GET /watch-together/${uid(3)}`]: () => shared });
    port.pos = 1200;
    await s.poll();
    expect(port.isPaused).toBe(false);
    expect(port.seeks).toEqual([]);

    port.pos = 1800;
    await s.poll();
    expect(port.seeks.length).toBe(1);
    expect(Math.abs(port.seeks[0] - 1000)).toBeLessThan(200);

    port.pos = 61_500;
    shared = wt({ playing: false, positionMs: 61_500, version: 3 });
    await s.poll();
    expect(port.isPaused).toBe(true);
    expect(port.seeks.length).toBe(1);
  });

  it("holds each poll with the version it has", async () => {
    const { s } = sync(wt({ version: 4 }), {
      [`GET /watch-together/${uid(3)}`]: () => wt({ version: 4 }),
    });
    await s.poll();
    expect(calls[0].query).toBe("?after=4");
  });

  it("reports its latest quick round trip on held polls and actions, and nothing before it has one", async () => {
    const { s, port } = sync(wt({ playing: true, positionMs: 5000 }), {
      [`GET /watch-together/${uid(3)}`]: () => wt({ playing: true, positionMs: 5000 }),
      [`PUT /watch-together/${uid(3)}/state`]: () =>
        wt({ playing: false, positionMs: 7000, version: 2 }),
    });
    await s.poll();
    expect(calls[0].query).toBe("?after=1");
    await s.sample();
    expect(calls[1].query).toMatch(/^\?$|^$/);
    await s.poll();
    expect(calls[2].query).toMatch(/^\?after=1&rttMs=\d+$/);
    await wait(30); // a change still ahead by a few ms is met, not sent over
    port.isPaused = true;
    port.pos = 7000;
    s.local();
    await until(() => s.wt.version === 2);
    expect(calls[3].query).toMatch(/^\?rttMs=\d+$/);
  });

  it("asks again at once when a hold ran out", async () => {
    let held = 0;
    const { s } = sync(wt(), {
      [`GET /watch-together/${uid(3)}`]: async (_b: unknown, url: URL) => {
        if (!url.search.includes("after")) return wt();
        held++;
        if (held === 1) await wait(1100);
        return wt();
      },
    });
    const run = s.start();
    // Three samples, a hold of 1.1 s, then the next hold with no gap.
    await until(() => calls.filter((c) => c.query.includes("after")).length >= 2, 1800);
    s.stop();
    await run;
    expect(held).toBeGreaterThanOrEqual(2);
  });

  it("applies a change at its instant on the server's clock", async () => {
    const { s, port } = sync(wt(), {
      [`GET /watch-together/${uid(3)}`]: () =>
        wt({
          playing: true,
          positionMs: 30_000,
          effectiveAtMs: Date.now() + 10_000 + 150,
          version: 2,
        }),
    });
    s.offset = 10_000;
    await s.poll();
    expect(port.isPaused).toBe(true);
    await until(() => !port.isPaused);
    expect(port.seeks.length).toBe(1);
    expect(Math.abs(port.seeks[0] - 30_000)).toBeLessThan(100);
  });

  it("estimates the server's clock from the quickest round trip", async () => {
    const { s } = sync(wt(), {
      [`GET /watch-together/${uid(3)}`]: () => wt({ serverTimeMs: Date.now() + 60_000 }),
    });
    await s.sample();
    expect(Math.abs(s.offset - 60_000)).toBeLessThan(100);
  });

  it("sends the viewer's pause with its position, and nothing for a change that follows the shared state", async () => {
    const { s, port } = sync(wt({ playing: true, positionMs: 5000 }), {
      [`PUT /watch-together/${uid(3)}/state`]: () =>
        wt({ playing: false, positionMs: 7000, version: 2 }),
    });
    port.isPaused = false;
    port.pos = 5000;
    s.local();
    expect(calls.length).toBe(0);

    port.isPaused = true;
    port.pos = 7000;
    s.local();
    await until(() => s.wt.version === 2);
    expect(calls.map((c) => c.key)).toEqual([`PUT /watch-together/${uid(3)}/state`]);
    expect(calls[0].body).toEqual({ playing: false, positionMs: 7000 });
  });

  it("holds the viewer's own play for the instant both start at", async () => {
    const { s, port } = sync(wt({ positionMs: 5000 }), {
      [`PUT /watch-together/${uid(3)}/state`]: () =>
        wt({ playing: true, positionMs: 5000, effectiveAtMs: Date.now() + 200, version: 2 }),
    });
    port.isPaused = false;
    port.pos = 5000;
    s.local();
    expect(port.isPaused).toBe(true);
    await until(() => s.wt.version === 2);
    expect(calls[0].body).toEqual({ playing: true, positionMs: 5000 });
    expect(port.isPaused).toBe(true);
    await until(() => !port.isPaused);
  });

  it("sends a seek of more than 2 s while playing", async () => {
    const { s, port } = sync(wt({ playing: true, positionMs: 5000 }), {
      [`PUT /watch-together/${uid(3)}/state`]: () =>
        wt({ playing: true, positionMs: 90_000, version: 2 }),
    });
    port.isPaused = false;
    port.pos = 90_000;
    s.local();
    await until(() => s.wt.version === 2);
    expect(calls[0].body).toEqual({ playing: true, positionMs: 90_000 });
  });

  it("leaves a failed send to the next poll, which sees that it ended", async () => {
    const { s, port, seen } = sync(wt({ playing: true }), {});
    port.isPaused = true;
    s.local();
    await wait(50);
    expect(calls.map((c) => c.key)).toEqual([`PUT /watch-together/${uid(3)}/state`]);
    await s.poll();
    expect(seen.at(-1)!.status).toBe("ended");
  });

  it("ignores an answer older than one it has", async () => {
    const { s, port } = sync(wt({ version: 5, playing: false }), {
      [`GET /watch-together/${uid(3)}`]: () => wt({ version: 4, playing: true }),
    });
    await s.poll();
    expect(port.isPaused).toBe(true);
    expect(s.wt.version).toBe(5);
  });

  it("follows the viewer when it already plays as the second adult joins", async () => {
    const { s, port } = sync(waiting(), {
      [`GET /watch-together/${uid(3)}`]: () => wt({ version: 2 }),
      [`PUT /watch-together/${uid(3)}/state`]: () =>
        wt({ version: 3, playing: true, positionMs: 4000 }),
    });
    port.isPaused = false;
    port.pos = 4000;
    await s.poll();
    await until(() => s.wt.version === 3);
    expect(calls.find((c) => c.key.startsWith("PUT"))!.body).toEqual({
      playing: true,
      positionMs: 4000,
    });
  });

  it("does nothing to the player while waiting", async () => {
    const { s, port } = sync(waiting(), {
      [`GET /watch-together/${uid(3)}`]: () => waiting({ version: 2 }),
    });
    port.isPaused = false;
    await s.poll();
    expect(port.isPaused).toBe(false);
  });

  it("ends and stops polling when the server no longer knows it", async () => {
    const { s, seen } = sync(wt(), {});
    await s.poll();
    expect(seen.at(-1)!.status).toBe("ended");
    await s.poll();
    expect(calls.length).toBe(1);
  });

  it("extrapolates the position while playing", () => {
    const { s } = sync(wt({ playing: true, positionMs: 1000 }), {});
    expect(s.expected(Date.now() + 3000)).toBeGreaterThanOrEqual(3990);
  });
});

describe("otherName", () => {
  it("names the other adult, or the profile's own other device when it is both members", () => {
    expect(otherName(wt(), uid(1))).toBe("Helga");
    const here = { ...member(1, "Bea", "host"), deviceName: "Laptop", thisDevice: true };
    const there = { ...member(1, "Bea", "guest"), deviceName: "Living Room", thisDevice: false };
    expect(otherName(wt({ members: [here, there] }), uid(1))).toBe("Living Room");
    expect(otherName(wt({ status: "waiting", members: [here] }), uid(1))).toBe("");
  });

  it("is not in one that only the profile's other device is in", async () => {
    const there = { ...member(1, "Bea", "host"), deviceName: "Living Room", thisDevice: false };
    const waits = wt({ status: "waiting", members: [there] });
    const api = createApi({ fetch: mockFetch({ "GET /watch-together": page([waits]) }) });
    expect(await findTogether(api, uid(10), uid(1))).toBeUndefined();
  });
});

describe("details", () => {
  const movie = {
    id: uid(10),
    libraryId: uid(1),
    kind: "movie",
    parsedTitle: "Dune",
    addedAt: ts,
    modifiedAt: ts,
  };
  const routes = (extra: Record<string, Handler | unknown> = {}) => ({
    "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
    "GET /me": me(1),
    [`GET /profiles/${uid(1)}/watch-state`]: page([]),
    [`GET /items/${uid(10)}`]: movie,
    "GET /media-files": page([]),
    [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] },
    ...extra,
  });
  const text = (r: HTMLElement) => r.querySelector(".actions")!.textContent;

  it("opens one, shows the countdown and cancels", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": page([]),
          "POST /watch-together": () =>
            waiting({ joinMsLeft: 300_000, members: [member(1, "Bea", "host")] }),
          [`GET /watch-together/${uid(3)}`]: () =>
            waiting({ joinMsLeft: 300_000, members: [member(1, "Bea", "host")] }),
          [`DELETE /watch-together/${uid(3)}`]: none,
        }),
      ),
    });
    await until(() => text(r)?.includes("Watch together"));
    [...r.querySelectorAll<HTMLElement>("button")]
      .find((b) => b.textContent === "Watch together")!
      .click();
    await until(() => r.querySelector(".pill.wait"));
    expect(r.querySelector(".pill.wait")!.textContent).toBe("Waiting for someone to join · 5:00");
    expect(calls.find((c) => c.key === "POST /watch-together")!.body).toEqual({ itemId: uid(10) });
    expect(r.querySelector(".tgnote")!.textContent).toContain(
      "Someone else who opens Dune and taps Watch together in the next 5 minutes",
    );
    r.querySelector<HTMLElement>(".pill.wait button")!.click();
    await until(() => calls.some((c) => c.key === `DELETE /watch-together/${uid(3)}`));
    await until(() => !r.querySelector(".pill.wait"));
    expect(text(r)).toContain("Watch together");
  });

  it("offers the second adult Join with the host's name and time left, then shows In sync with Leave", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": page([waiting({ joinMsLeft: 252_000 })]),
          [`POST /watch-together/${uid(3)}/join`]: () => wt({ version: 2 }),
          [`GET /watch-together/${uid(3)}`]: () => wt({ version: 2 }),
          [`DELETE /watch-together/${uid(3)}`]: none,
        }),
      ),
    });
    await until(() => r.querySelector(".btn.join"));
    expect(r.querySelector(".btn.join")!.textContent).toBe("Join Helga · 4:12");
    expect(r.querySelector(".tgnote")!.textContent).toBe(
      "Helga wants to watch this together. Join in the next 4:12 to play in sync.",
    );
    r.querySelector<HTMLElement>(".btn.join")!.click();
    await until(() => r.querySelector(".pill.sync"));
    expect(r.querySelector(".pill.sync")!.textContent).toBe("In sync with HelgaLeave");
    expect(r.querySelector(".btn.p")!.textContent).toBe("Play together");
    expect(r.querySelector(".tgnote")!.textContent).toBe(
      "Pause and play on either device apply to both. Helga is on Apple TV.",
    );
    r.querySelector<HTMLElement>(".pill .leave")!.click();
    await until(() => calls.some((c) => c.key === `DELETE /watch-together/${uid(3)}`));
    await until(() => !r.querySelector(".pill.sync"));
    expect(r.querySelector(".btn.p")!.textContent).toBe("Play");
  });

  it("opens the player when the other adult plays", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    let shared = wt();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": () => page([shared]),
          [`GET /watch-together/${uid(3)}`]: () => shared,
        }),
      ),
    });
    await until(() => r.querySelector(".pill.sync"));
    expect(location.hash).toBe(`#/movie/${uid(10)}`);
    shared = wt({ playing: true, positionMs: 5000, version: 2 });
    await until(() => location.hash === `#/play/${uid(10)}`);
  });

  it("Play together plays for both at the shared position and opens the player", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    let shared = wt({ positionMs: 42_000, version: 2 });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": () => page([shared]),
          [`GET /watch-together/${uid(3)}`]: () => shared,
          [`PUT /watch-together/${uid(3)}/state`]: () =>
            (shared = wt({ playing: true, positionMs: 42_000, version: 3 })),
        }),
      ),
    });
    await until(() => r.querySelector(".btn.p")?.textContent === "Play together");
    r.querySelector<HTMLElement>(".btn.p")!.click();
    await until(() => location.hash === `#/play/${uid(10)}`);
    expect(calls.find((c) => c.key === `PUT /watch-together/${uid(3)}/state`)!.body).toEqual({
      playing: true,
      positionMs: 42_000,
    });
  });

  it("does not open the player for a state that was already playing when the page opened", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    const shared = wt({ playing: true, version: 4 });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": page([shared]),
          [`GET /watch-together/${uid(3)}`]: shared,
        }),
      ),
    });
    await until(() => r.querySelector(".pill.sync"));
    await new Promise((res) => setTimeout(res, 1300));
    expect(location.hash).toBe(`#/movie/${uid(10)}`);
  });

  it("stops polling when the viewer leaves before the first answer", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    let release = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const mine = waiting({ members: [member(1, "Bea", "host")] });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": async () => (await gate, page([mine])),
          [`GET /watch-together/${uid(3)}`]: mine,
        }),
      ),
    });
    await until(() => calls.some((c) => c.key === "GET /watch-together"));
    await at("#/now-playing");
    release();
    await new Promise((res) => setTimeout(res, 1500));
    expect(calls.some((c) => c.key === `GET /watch-together/${uid(3)}`)).toBe(false);
  });

  it("counts the join window down every second", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch(routes({ "GET /watch-together": page([waiting({ joinMsLeft: 252_000 })]) })),
    });
    await until(() => r.querySelector(".btn.join"));
    await until(() => r.querySelector(".btn.join")!.textContent === "Join Helga · 4:10", 3500);
  });

  it("still shows the page when the server cannot do it", async () => {
    await at(`#/movie/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": () =>
            json({ code: "not_implemented", message: "not available in this server version" }, 501),
        }),
      ),
    });
    await until(() => text(r)?.includes("Watch together"));
    expect(r.querySelector("h1")!.textContent).toBe("Dune");
  });

  it("is on Episode details too, and offers a kid the same", async () => {
    const episode = {
      id: uid(22),
      libraryId: uid(1),
      kind: "episode",
      parentId: uid(11),
      seasonNumber: 1,
      episodeNumber: 2,
      parsedTitle: "Half Loop",
      addedAt: ts,
      modifiedAt: ts,
    };
    const show = { ...movie, id: uid(9), kind: "show" };
    const season = { ...movie, id: uid(11), kind: "season", parentId: uid(9), seasonNumber: 1 };
    const tv = (active: number, open: Together[] = []) =>
      routes({
        "GET /me": me(active),
        [`GET /profiles/${uid(2)}/watch-state`]: page([]),
        [`GET /items/${uid(9)}`]: show,
        [`GET /items/${uid(11)}`]: season,
        [`GET /items/${uid(22)}`]: episode,
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("kind") === "season"
              ? [season]
              : u.searchParams.get("kind") === "episode"
                ? [episode]
                : [],
          ),
        "GET /watch-together": page(open),
      });
    await at(`#/episode/${uid(22)}`);
    const adult = root();
    await boot(adult, { fetch: mockFetch(tv(1)) });
    await until(() => text(adult)?.includes("Watch together"));
    expect(text(adult)).toContain("Start over");

    document.body.replaceChildren();
    window.onhashchange = null;
    calls.length = 0;
    const kid = root();
    await boot(kid, { fetch: mockFetch(tv(2)) });
    await until(() => text(kid)?.includes("Watch together"));

    document.body.replaceChildren();
    window.onhashchange = null;
    const invited = root();
    await boot(invited, { fetch: mockFetch(tv(2, [waiting({ itemId: uid(22) })])) });
    await until(() => invited.querySelector(".btn.join"));
    expect(invited.querySelector(".btn.join")!.textContent).toBe("Join Helga · 4:12");
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
  const stream = {
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
    negotiated: { verdict: "direct_play" },
    rungs: [],
    sidecars: [],
  };
  const routes = (extra: Record<string, Handler | unknown> = {}) => ({
    "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
    "GET /me": me(1),
    [`GET /items/${uid(10)}`]: {
      id: uid(10),
      libraryId: uid(1),
      kind: "movie",
      parsedTitle: "Dune",
      addedAt: ts,
      modifiedAt: ts,
    },
    "GET /media-files": page([]),
    "POST /playback/sessions": () => json(stream, 201),
    [`PUT /playback/sessions/${uid(7)}/position`]: none,
    [`DELETE /playback/sessions/${uid(7)}`]: none,
    ...extra,
  });

  it("starts at the shared position, shows who it is in sync with and who paused, and leaves", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    const shared = wt({
      positionMs: 72_000,
      pausedById: uid(5),
      pausedByName: "Helga",
      version: 3,
    });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": page([shared]),
          [`GET /watch-together/${uid(3)}`]: shared,
          [`DELETE /watch-together/${uid(3)}`]: none,
        }),
      ),
    });
    await until(() => r.querySelector(".pill.sync:not([hidden])"));
    expect(calls.find((c) => c.key === "POST /playback/sessions")!.body).toMatchObject({
      positionMs: 72_000,
    });
    expect(r.querySelector(".pill.sync")!.textContent).toBe("In sync with HelgaLeave");
    expect(r.querySelector(".pnote")!.textContent).toBe("Helga paused");
    expect((r.querySelector(".pnote") as HTMLElement).hidden).toBe(false);
    r.querySelector<HTMLElement>(".pill .leave")!.click();
    await until(() => calls.some((c) => c.key === `DELETE /watch-together/${uid(3)}`));
    expect((r.querySelector(".pill.sync") as HTMLElement).hidden).toBe(true);
  });

  it("says nothing of its own pause, and tells when the other side leaves", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    let shared = wt({ pausedById: uid(1), pausedByName: "Bea", version: 3 });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /watch-together": () => page([shared]),
          [`GET /watch-together/${uid(3)}`]: () => shared,
        }),
      ),
    });
    await until(() => r.querySelector(".pill.sync:not([hidden])"));
    expect((r.querySelector(".pnote") as HTMLElement).hidden).toBe(true);
    shared = wt({ status: "ended", version: 4 });
    await until(() => r.querySelector(".pnote")?.textContent === "Helga left");
    expect((r.querySelector(".pill.sync") as HTMLElement).hidden).toBe(true);
  });

  it("follows the one a kid is in", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    const shared = wt({ members: [HELGA, member(2, "Jason", "guest")], positionMs: 72_000 });
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /me": me(2),
          "GET /watch-together": page([shared]),
          [`GET /watch-together/${uid(3)}`]: shared,
        }),
      ),
    });
    await until(() => r.querySelector(".pill.sync:not([hidden])"));
    expect(r.querySelector(".pill.sync")!.textContent).toBe("In sync with HelgaLeave");
  });

  it("plays alone when no Watch together is open", async () => {
    await at(`#/play/${uid(10)}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes({ "GET /watch-together": page([]) })) });
    await until(() => calls.some((c) => c.key === "POST /playback/sessions"));
    expect((r.querySelector(".pill.sync") as HTMLElement).hidden).toBe(true);
    expect(calls.find((c) => c.key === "POST /playback/sessions")!.body).not.toHaveProperty(
      "positionMs",
    );
  });

  it("names a YouTube video, which is not a catalog item", async () => {
    await at(`#/play/${uid(60)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          [`GET /items/${uid(10)}`]: undefined,
          [`GET /youtube/videos/${uid(60)}`]: {
            id: uid(60),
            libraryId: uid(5),
            channelId: uid(40),
            channelTitle: "Workbench Weekly",
            title: "The dishwasher video",
            watched: false,
            positionMs: 0,
            addedAt: ts,
          },
        }),
      ),
    });
    await until(() => r.querySelector(".ph .t"));
    expect(r.querySelector(".ph")!.textContent).toContain("The dishwasher video");
    expect(r.querySelector(".ph .sub")!.textContent).toBe("Workbench Weekly");
  });
});
