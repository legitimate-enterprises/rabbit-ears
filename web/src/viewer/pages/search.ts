import { h } from "../../admin/dom";
import { ok } from "../../admin/api";
import { epCode, items, type Ctx, type Item } from "../data";
import { plural, runtime } from "../format";
import { person, section, tile } from "../ui";
import { albumTile, artistTile, play, row } from "./music";
import { channelTile, videoTile } from "./youtube";

const caption = (i: Item) =>
  i.kind === "episode" ? epCode(i) : i.kind === "show" ? "TV" : runtime(i.metadata?.runtimeMinutes);

/** Everything matching ?q across the libraries, as the active profile sees it. */
export async function search(ctx: Ctx, _: string[], query: URLSearchParams) {
  const q = query.get("q")?.trim();
  if (!q) return [h("h1", "Search"), h("p.sub", "Type a title or a name.")];
  const r = await ok(ctx.api.GET("/search", { params: { query: { q } } }));
  const group = (label: string, cls: string, tiles: HTMLElement[]) =>
    tiles.length ? section(label, h("h2.sec", label), h(cls, ...tiles)) : null;
  const href = `#/search?q=${encodeURIComponent(q)}`;
  const songs = h(
    "ol.tracks",
    ...r.songs.map((s, i) =>
      row({
        id: s.id,
        title: s.title,
        sub: `${s.artistName} · ${s.albumTitle}`,
        ms: s.durationMs,
        on: () => play(r.songs, i, "Search", href),
      }),
    ),
  );
  // Sections run movies, TV, music, YouTube, then people; within one, the most specific kind first.
  const out = [
    group(
      "Movies",
      "div.grid",
      r.movies.map((i) => tile(i, caption(i))),
    ),
    group(
      "TV",
      "div.grid",
      [...r.episodes, ...r.shows].map((i) => tile(i, caption(i))),
    ),
    r.songs.length || r.albums.length || r.artists.length
      ? section(
          "Music",
          h("h2.sec", "Music"),
          r.songs.length ? songs : "",
          h(
            "div.grid.sq",
            ...r.albums.map((a) => albumTile(a, a.artistName)),
            ...r.artists.map(artistTile),
          ),
        )
      : null,
    r.videos.length || r.channels.length
      ? section(
          "YouTube",
          h("h2.sec", "YouTube"),
          h("div.grid.vids", ...r.videos.map((v) => videoTile(v, true))),
          h("div.grid.sq", ...r.channels.map(channelTile)),
        )
      : null,
    group(
      "People",
      "div.strip",
      r.people.map((p) => person(p.id, p.name, !!p.profilePath)),
    ),
  ].filter((e): e is HTMLElement => !!e);
  return [
    h("div.titlerow", h("h1", "Search"), h("span.sub", `"${q}"`)),
    ...(out.length ? out : [h("p.sub", `Nothing matches "${q}".`)]),
  ];
}

/** A person's movies and shows. */
export async function personPage(ctx: Ctx, [id]: string[]) {
  const personId = Number(id);
  const [p, list] = await Promise.all([
    ok(ctx.api.GET("/people/{personId}", { params: { path: { personId } } })),
    items(ctx.api, { personId }),
  ]);
  return [
    h(
      "div.titlerow.personhead",
      person(p.id, p.name, !!p.profilePath).firstElementChild!,
      h("h1", p.name),
      h("span.sub", plural(list.length, "title")),
    ),
    section("Titles", h("div.grid", ...list.map((i) => tile(i, caption(i))))),
  ] as HTMLElement[];
}
