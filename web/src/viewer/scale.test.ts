// Page cost on a large library: the real pages against a fake API that pages
// listings as the server does, counting the requests and items each first
// visit asks for.
import { beforeEach, describe, expect, it } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { nextEpisode, type Item, type WatchState } from "./data";

const ts = "2026-01-01T00:00:00Z";
const day = (n: number) => new Date(Date.UTC(2026, 0, 1) + n * 3_600_000).toISOString();

/** 300 shows of 5 seasons and 20,000 episodes, 3,000 movies, and a profile's 1,225 watch states. */
function library() {
  const all: Item[] = [];
  const add = (i: Partial<Item> & Pick<Item, "id" | "kind">) =>
    all.push({
      libraryId: 1,
      parsedTitle: `T${i.id}`,
      addedAt: day(i.id % 5000),
      updatedAt: ts,
      ...i,
    });
  let id = 1;
  const shows: number[] = [];
  const episodesOf = new Map<number, Item[]>();
  for (let s = 0; s < 300; s++) {
    const show = id++;
    shows.push(show);
    add({
      id: show,
      kind: "show",
      libraryId: 2,
      parsedTitle: `Show ${String(s).padStart(3, "0")}`,
    });
    episodesOf.set(show, []);
    for (let n = 1; n <= 5; n++) {
      const season = id++;
      add({ id: season, kind: "season", libraryId: 2, parentId: show, seasonNumber: n });
      const count = s * 5 + n <= 500 ? 14 : 13;
      for (let e = 1; e <= count; e++) {
        add({
          id: id++,
          kind: "episode",
          libraryId: 2,
          parentId: season,
          seasonNumber: n,
          episodeNumber: e,
        });
        episodesOf.get(show)!.push(all[all.length - 1]);
      }
    }
  }
  for (let m = 0; m < 3000; m++) add({ id: id++, kind: "movie", parsedTitle: `Movie ${m}` });
  const byId = new Map(all.map((i) => [i.id, i]));
  // The profile has watched 20 episodes each of 50 shows, round the shows, and is partway
  // into the 21st of every other one; between rounds it watched 200 movies and is partway into 3.
  const states: WatchState[] = [];
  let t = 0;
  const state = (itemId: number, going: boolean) =>
    states.push({
      itemId,
      positionMs: going ? 60_000 : 0,
      watched: !going,
      playCount: 1,
      lastPlayedAt: day(t++),
      updatedAt: ts,
    });
  const films = all.filter((i) => i.kind === "movie").slice(0, 200);
  for (let round = 0; round < 21; round++) {
    shows.slice(0, 50).forEach((s, k) => {
      if (round < 20 || k % 2 === 0) state(episodesOf.get(s)![round].id, round === 20);
    });
    films.slice(round * 10, round * 10 + 10).forEach((m, k) => state(m.id, round === 19 && k < 3));
  }
  states.sort((a, b) => b.lastPlayedAt!.localeCompare(a.lastPlayedAt!));
  const ws = new Map(states.map((w) => [w.itemId, w]));
  const progress = shows.map((show) => {
    const eps = episodesOf.get(show)!;
    const played = eps
      .map((e) => ws.get(e.id)?.lastPlayedAt ?? "")
      .sort()
      .pop();
    const touched = eps.some((e) => ws.has(e.id));
    return {
      showId: show,
      seasons: 5,
      episodes: eps.length,
      unwatched: eps.filter((e) => !ws.get(e.id)?.watched).length,
      inProgress: eps.some((e) => (ws.get(e.id)?.positionMs ?? 0) > 0),
      lastPlayedAt: played || undefined,
      nextEpisodeId: touched ? nextEpisode(eps, ws)?.id : eps[0].id,
    };
  });
  return { all, byId, states, progress };
}

const lib = library();
const asked: { path: string; items: number }[] = [];
const json = (b: unknown, status = 200) =>
  new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });
function pageOf<T>(rows: T[], u: URL) {
  const limit = Number(u.searchParams.get("limit") ?? 50);
  const offset = Number(u.searchParams.get("offset") ?? 0);
  return {
    items: rows.slice(offset, offset + limit),
    nextOffset: offset + limit < rows.length ? offset + limit : undefined,
  };
}
const acct = {
  id: 1,
  kind: "adult",
  name: "Bea",
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  updatedAt: ts,
};
function answer(path: string, u: URL): unknown {
  const q = u.searchParams;
  if (path === "/health") return { status: "ok", version: "1.0.0", setupRequired: false };
  if (path === "/me")
    return {
      device: { id: 1 },
      account: acct,
      activeProfileId: 1,
      household: { id: 1, name: "Bea", adultId: 1, createdAt: ts },
      profiles: [acct],
    };
  if (path === "/items") {
    const rows = lib.all.filter(
      (i) =>
        (!q.get("kind") || i.kind === q.get("kind")) &&
        (!q.get("parentId") || i.parentId === Number(q.get("parentId"))),
    );
    if (q.get("sort") === "added")
      rows.sort((a, b) => b.addedAt.localeCompare(a.addedAt) || b.id - a.id);
    return pageOf(rows, u);
  }
  let m = /^\/items\/(\d+)$/.exec(path);
  if (m) return lib.byId.get(Number(m[1])) ?? json({ code: "not_found", message: "x" }, 404);
  if (/^\/items\/\d+\/credits$/.test(path)) return { cast: [], crew: [] };
  if (path === "/profiles/1/watch-state") {
    const under = Number(q.get("parentId"));
    const parent = (id?: number) => lib.byId.get(id ?? 0)?.parentId;
    return pageOf(
      under
        ? lib.states.filter((w) => parent(w.itemId) === under || parent(parent(w.itemId)) === under)
        : lib.states,
      u,
    );
  }
  m = /^\/profiles\/1\/watch-state\/(\d+)$/.exec(path);
  if (m)
    return (
      lib.states.find((w) => w.itemId === Number(m![1])) ??
      json({ code: "not_found", message: "x" }, 404)
    );
  if (path === "/profiles/1/show-progress") return pageOf(lib.progress, u);
  if (
    ["/genres", "/youtube/videos", "/stations", "/profiles/1/playlists", "/collections"].includes(
      path,
    )
  )
    return { items: [] };
  return json({ code: "not_found", message: `no fake ${path}` }, 404);
}
const fetch = async (req: Request) => {
  const u = new URL(req.url, "http://x");
  const path = u.pathname.replace("/api/v1", "");
  const out = answer(path, u);
  const r = out instanceof Response ? out : json(out);
  // Artwork is per tile drawn, not what the page reads to draw.
  if (!["/health", "/me", "/events"].includes(path) && !path.includes("/artwork/")) {
    const b = out instanceof Response ? undefined : (out as { items?: unknown[] });
    asked.push({ path: path + u.search, items: b?.items ? b.items.length : 1 });
  }
  return r;
};

/** What the first visit to hash asks for once its page has drawn. */
async function cost(hash: string, drawn: string) {
  window.onhashchange = null;
  document.body.replaceChildren();
  location.hash = hash;
  await new Promise((r) => setTimeout(r, 20));
  asked.length = 0;
  const r = document.body.appendChild(document.createElement("div"));
  await boot(r, { fetch }, false);
  await expect.poll(() => r.querySelector(drawn), { timeout: 15_000 }).toBeTruthy();
  await new Promise((r) => setTimeout(r, 20));
  window.onhashchange = null;
  return {
    requests: asked.length,
    items: asked.reduce((n, a) => n + a.items, 0),
    episodes: asked.filter((a) => /kind=episode/.test(a.path) && !/parentId/.test(a.path)).length,
  };
}

const show = lib.all.find((i) => i.kind === "show")!;
const season = lib.all.find((i) => i.kind === "season")!;

describe("a large library", () => {
  beforeEach(() => setToken("t"));

  it("leads Home with the latest episode in progress and puts started shows at their next one in Next up", async () => {
    await cost("#/", "[aria-label='Next up'] a");
    const shown = (sel: string) => [...document.querySelectorAll(sel)].map((e) => e.textContent);
    expect(shown(".hero h1")).toEqual(["Show 048"]);
    expect(shown(".also b")).toEqual(["Show 046", "Show 044", "Show 042"]);
    // Every other show is partway into its next episode, which Continue watching holds instead.
    const next = Array.from({ length: 12 }, (_, k) => `Show ${String(2 * k + 1).padStart(3, "0")}`);
    expect(shown("[aria-label='Next up'] a b")).toEqual(next);
    expect(shown("[aria-label='Next up'] a .sub")[0]).toMatch(/^S2 E7 - /);
    // A tile plays its episode; its arrow shows over the art.
    const first = document.querySelector("[aria-label='Next up'] .strip a")!;
    expect(first.getAttribute("href")).toMatch(/^#\/play\/\d+/);
    expect(first.getAttribute("aria-label")).toMatch(/^Play Show 001, S2 E7 /);
    expect(first.querySelector(".art .playmark")).not.toBeNull();
  });

  it("draws Home and the TV grid without reading every episode", { timeout: 60_000 }, async () => {
    const out: Record<string, unknown> = {};
    out.home = await cost("#/", ".strip");
    out.tv = await cost("#/tv", ".grid a");
    out.show = await cost(`#/show/${show.id}`, ".poster.season");
    out.season = await cost(`#/season/${season.id}`, ".eps a");
    out.movies = await cost("#/movies", ".grid a");
    console.log(JSON.stringify(out));
    for (const page of [out.home, out.tv] as { episodes: number; items: number }[]) {
      expect(page.episodes).toBe(0);
      expect(page.items).toBeLessThan(6_000);
    }
    // Home reads the newest titles, never a whole movie or show list.
    expect((out.home as { items: number }).items).toBeLessThan(1_000);
    // A show or season reads its own episodes' states, not the profile's every one.
    for (const page of [out.show, out.season] as { items: number }[])
      expect(page.items).toBeLessThan(200);
  });
});
