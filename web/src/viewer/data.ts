// What the viewer knows about the library, read through the API as the active
// profile. The server filters by the profile's ceilings; nothing here does.
import type { Api, paths, Schemas } from "../api/client";
import { tint } from "../api/uid";
import { ApiError, ok, paged } from "../admin/api";

export type Item = Schemas["Item"];
export type Me = Schemas["Me"];
export type WatchState = Schemas["WatchState"];

export interface Ctx {
  api: Api;
  me: Me;
  health: Schemas["Health"];
  /** The profile this device currently acts as. */
  profile: Schemas["Account"];
  go(hash: string): void;
  signOut(): void;
  switchProfile(): void;
  /** Starts over from /me, so every page answers as the active profile. */
  reload(): void;
  cache: Map<string, unknown>;
}

/**
 * Every item of a listing. The listing carries no total, so after the first
 * page the rest are asked for several at a time, in order, until one is the
 * last; at most a few past the end come back empty.
 */
export async function items(
  api: Api,
  query: NonNullable<paths["/items"]["get"]["parameters"]["query"]>,
): Promise<Item[]> {
  const limit = 200;
  const at = (offset: number) =>
    ok(api.GET("/items", { params: { query: { ...query, limit, offset } } }));
  let page = await at(0);
  const out = [...page.items];
  while (page.nextOffset !== undefined) {
    const from = page.nextOffset;
    const wave = await Promise.all([0, 1, 2, 3].map((i) => at(from + i * limit)));
    for (page of wave) {
      out.push(...page.items);
      if (page.nextOffset === undefined) break;
    }
  }
  return out;
}

/** The profile's watch states, or only those under the show or season parentId names. */
export async function watchStates(ctx: Ctx, parentId?: string): Promise<Map<string, WatchState>> {
  const id = ctx.profile.id;
  const all = await paged((offset) =>
    ok(
      ctx.api.GET("/profiles/{profileId}/watch-state", {
        params: { path: { profileId: id }, query: { parentId, limit: 200, offset } },
      }),
    ),
  );
  return new Map(all.map((w) => [w.itemId, w]));
}

/** One item's state; undefined when the profile has none, which the API answers with a 404. */
export async function watchState(ctx: Ctx, itemId: string): Promise<WatchState | undefined> {
  try {
    return await ok(
      ctx.api.GET("/profiles/{profileId}/watch-state/{itemId}", {
        params: { path: { profileId: ctx.profile.id, itemId } },
      }),
    );
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return undefined;
    throw e;
  }
}

/**
 * Every movie or show, cached for the boot; a scan's library event, or an
 * empty live batch as on a return to the tab, clears the cache so titles a
 * scan added since appear.
 */
export async function titles(ctx: Ctx, kind: "movie" | "show"): Promise<Item[]> {
  const hit = ctx.cache.get(kind) as Item[] | undefined;
  if (hit) return hit;
  const all = await items(ctx.api, { kind });
  ctx.cache.set(kind, all);
  return all;
}

/** The n movies or shows added last, newest first. */
export const newest = async (ctx: Ctx, kind: "movie" | "show", n: number) =>
  (await ok(ctx.api.GET("/items", { params: { query: { kind, sort: "added", limit: n } } }))).items;

/** The profile's progress through each show it sees, by show. */
export async function showProgress(ctx: Ctx): Promise<Map<string, Schemas["ShowProgress"]>> {
  const all = await paged((offset) =>
    ok(
      ctx.api.GET("/profiles/{profileId}/show-progress", {
        params: { path: { profileId: ctx.profile.id }, query: { limit: 200, offset } },
      }),
    ),
  );
  return new Map(all.map((p) => [p.showId, p]));
}

/** The movie, show, season or episode id names; undefined for one the profile cannot see or of another kind. */
export const findItem = (ctx: Ctx, id: string): Promise<Item | undefined> =>
  getItem(ctx, id).catch((e) => {
    if (e instanceof ApiError && e.status === 404) return undefined;
    throw e;
  });

/** The item id names when it is of kind, else the error "<Kind> not found". */
export async function itemOf(ctx: Ctx, id: string, kind: Item["kind"]): Promise<Item> {
  const i = await findItem(ctx, id);
  if (i?.kind !== kind) throw new Error(`${kind[0].toUpperCase()}${kind.slice(1)} not found`);
  return i;
}

/** A season's episodes, by number. */
export const episodesOf = (ctx: Ctx, seasonId: string) =>
  items(ctx.api, { kind: "episode", parentId: seasonId });

/** A show's seasons, specials last, and each one's episodes. */
export async function seasonsOf(ctx: Ctx, showId: string) {
  const seasons = (await items(ctx.api, { kind: "season", parentId: showId })).sort(specialsLast);
  const eps = await Promise.all(seasons.map((s) => episodesOf(ctx, s.id)));
  return { seasons, episodes: new Map(seasons.map((s, i) => [s.id, eps[i]])) };
}

// Seasons by number, specials (season 0) last.
const specialsLast = (a: Item, b: Item) => (a.seasonNumber || 99) - (b.seasonNumber || 99);

const getItem = (ctx: Ctx, itemId: string) =>
  ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId } } }));

/** An episode's show, read through its season. */
export async function showOf(ctx: Ctx, ep: Item): Promise<Item> {
  return getItem(ctx, (await getItem(ctx, ep.parentId!)).parentId!);
}

/** What the server plays when itemId ends, from row index of the playlist if any; undefined for nothing. */
export async function upNext(ctx: Ctx, itemId: string, playlistId?: string, index?: number) {
  const r = await ctx.api.GET("/profiles/{profileId}/up-next/{itemId}", {
    params: {
      path: { profileId: ctx.profile.id, itemId },
      query: playlistId ? { playlistId, index } : {},
    },
  });
  return r.response.status === 204 ? undefined : ok(Promise.resolve(r));
}

/** Turns the active profile's autoplay on or off, which every device then follows. */
export async function setAutoplay(ctx: Ctx, autoplay: boolean) {
  ctx.profile.autoplay = (
    await ok(
      ctx.api.PUT("/profiles/{profileId}/preferences", {
        params: { path: { profileId: ctx.profile.id } },
        body: { autoplay },
      }),
    )
  ).autoplay;
}

/** The calendar date on this device, which is what decides what is in season. */
export const today = () => {
  const d = new Date();
  return [d.getFullYear(), d.getMonth() + 1, d.getDate()]
    .map((n) => String(n).padStart(2, "0"))
    .join("-");
};

export const title = (i: Item) => i.metadata?.title || i.parsedTitle;
export const epCode = (i: Item) => `S${i.seasonNumber ?? 0} E${i.episodeNumber ?? 0}`;
export const inProgress = (w?: WatchState) => !!w && w.positionMs > 0;

/** The episode to play next: the one in progress, else the first unwatched after the latest played. */
export function nextEpisode(eps: Item[], ws: Map<string, WatchState>): Item | undefined {
  const lastPlayed = eps
    .map((e, i) => ({ i, w: ws.get(e.id) }))
    .filter((x) => x.w && (x.w.watched || inProgress(x.w)))
    .sort((a, b) => (b.w!.lastPlayedAt ?? "").localeCompare(a.w!.lastPlayedAt ?? ""))[0];
  const last = lastPlayed ? lastPlayed.i : -1;
  if (lastPlayed && inProgress(lastPlayed.w)) return eps[last];
  return eps.slice(last + 1).find((e) => !ws.get(e.id)?.watched) ?? (last < 0 ? eps[0] : undefined);
}

/** The server's eight profile colors, by name; placeholders take them too. */
export const PALETTE: [hex: string, name: string][] = [
  ["#3C4170", "Indigo"],
  ["#2E6B5B", "Teal"],
  ["#6B3A2E", "Rust"],
  ["#5B4A8A", "Violet"],
  ["#8A5A2B", "Ochre"],
  ["#2E5B8A", "Blue"],
  ["#6B2E5A", "Plum"],
  ["#4A6B2E", "Olive"],
];
/** A placeholder's color; a profile's is its own Account.color. */
export const color = (id: string) => PALETTE[tint(id, PALETTE.length)][0];
