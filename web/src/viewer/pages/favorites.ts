import { h } from "../../admin/dom";
import { ApiError, ok } from "../../admin/api";
import { epCode, type Ctx, type Item } from "../data";
import { favorites, type FavoriteKind } from "../favorites";
import { runtime } from "../format";
import { section, tile } from "../ui";
import { albumTile, artistTile, play, row } from "./music";
import { videoTile } from "./youtube";

/** undefined for a favorite gone or hidden since it was listed. */
const unlessGone = <T>(p: Promise<T>) =>
  p.catch((e) => {
    if (e instanceof ApiError && e.status === 404) return undefined;
    throw e;
  });

const caption = (i: Item) =>
  i.kind === "episode"
    ? epCode(i)
    : i.kind === "season"
      ? i.seasonNumber
        ? `Season ${i.seasonNumber}`
        : "Specials"
      : i.kind === "show"
        ? "TV"
        : runtime(i.metadata?.runtimeMinutes);

/** The profile's favorites, a section per kind, each most recently starred first. */
export async function favoritesPage(ctx: Ctx): Promise<HTMLElement[]> {
  const all = await favorites(ctx);
  // shortcut: one read per favorite, as the listing carries ids only; batch them when the API can.
  const each = async <T>(kind: FavoriteKind, get: (id: string) => Promise<T>) =>
    (
      await Promise.all(all.filter((f) => f.kind === kind).map((f) => unlessGone(get(f.itemId))))
    ).filter((x): x is Awaited<T> => x !== undefined);
  const item = (itemId: string) =>
    ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId } } }));
  const [movies, shows, seasons, episodes, artists, albums, songs, videos] = await Promise.all([
    each("movie", item),
    each("show", item),
    each("season", item),
    each("episode", item),
    each("artist", (artistId) =>
      ok(ctx.api.GET("/music/artists/{artistId}", { params: { path: { artistId } } })),
    ),
    each("album", (albumId) =>
      ok(ctx.api.GET("/music/albums/{albumId}", { params: { path: { albumId } } })),
    ),
    each("song", (songId) =>
      ok(ctx.api.GET("/music/songs/{songId}", { params: { path: { songId } } })),
    ),
    each("video", (videoId) =>
      ok(ctx.api.GET("/youtube/videos/{videoId}", { params: { path: { videoId } } })),
    ),
  ]);
  const group = (label: string, cls: string, tiles: HTMLElement[]) =>
    tiles.length ? section(label, h("h2.sec", label), h(cls, ...tiles)) : null;
  const titles = (list: Item[]) => list.map((i) => tile(i, caption(i)));
  const out = [
    group("Movies", "div.grid", titles(movies)),
    group("Shows", "div.grid", titles(shows)),
    group("Seasons", "div.grid", titles(seasons)),
    group("Episodes", "div.grid", titles(episodes)),
    group("Artists", "div.grid.sq", artists.map(artistTile)),
    group(
      "Albums",
      "div.grid.sq",
      albums.map((a) => albumTile(a, a.artistName)),
    ),
    group(
      "Songs",
      "ol.tracks",
      songs.map((s, i) =>
        row({
          id: s.id,
          title: s.title,
          sub: `${s.artistName} · ${s.albumTitle}`,
          ms: s.durationMs,
          on: () => play(songs, i, "Favorites", "#/favorites"),
        }),
      ),
    ),
    group(
      "Videos",
      "div.grid.vids",
      videos.map((v) => videoTile(v, true)),
    ),
  ].filter((e): e is HTMLElement => !!e);
  return [
    h("div.titlerow", h("h1", "Favorites")),
    ...(out.length
      ? out
      : [h("p.sub", "Nothing starred yet. Favorite is beside a title's Play or in its menu.")]),
  ];
}
