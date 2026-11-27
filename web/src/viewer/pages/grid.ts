import { h } from "../../admin/dom";
import { ok, paged } from "../../admin/api";
import { tint } from "../../api/uid";
import {
  inProgress,
  items,
  playFraction,
  showProgress,
  title,
  titles,
  watchStates,
  type Ctx,
  type Item,
} from "../data";
import { plural, runtime } from "../format";
import { chip, section, sel, viewTabs, withWatched } from "../ui";
import { artKey, placeholderKind } from "../artwork";
import { collectionTiles } from "./collections";
import { hasPlaylists, playlistTiles } from "./playlists";
import { stationTiles } from "./stations";
import { selection } from "../select";

const SORTS = {
  tv: ["Recently added", "Title", "Last watched", "Released"],
  movies: ["Recently added", "Title", "Last watched", "Released"],
};

/** TV and Movies share one grid: view tabs, filter chips, sort, and unwatched markers drawn from the profile's watch state, a Playlists view, and Movies a Collections view for everyone. */
export async function grid(
  ctx: Ctx,
  kind: "tv" | "movies",
  query = new URLSearchParams(),
): Promise<HTMLElement[]> {
  const view = query.get("view");
  const shown = await hasPlaylists(ctx, kind);
  const playlists = shown && view === "playlists";
  const collections = kind === "movies" && view === "collections";
  const stations = view === "stations";
  const views = viewTabs([
    [kind === "tv" ? "Shows" : "Movies", `#/${kind}`, !playlists && !collections && !stations],
    kind === "movies" && ["Collections", `#/${kind}?view=collections`, collections],
    shown && ["Playlists", `#/${kind}?view=playlists`, playlists],
    ["Stations", `#/${kind}?view=stations`, stations],
  ]);
  if (stations) {
    const { count, grid, create } = await stationTiles(ctx, kind);
    return [
      h("div.titlerow", h("h1", kind === "tv" ? "TV" : "Movies"), h("span.sub", count)),
      ...views,
      h("div.filters", h("span.grow"), create),
      section("Stations", grid),
    ];
  }
  if (collections) {
    const { tiles, create } = await collectionTiles(ctx);
    return [
      h("div.titlerow", h("h1", "Movies"), h("span.sub", plural(tiles.length, "collection"))),
      ...views,
      h("div.filters", h("span.grow"), create),
      section(
        "Collections",
        tiles.length ? h("div.grid", ...tiles) : h("p.sub", "No collections yet."),
      ),
    ];
  }
  if (playlists) {
    const { tiles, create } = await playlistTiles(ctx, kind);
    return [
      h(
        "div.titlerow",
        h("h1", kind === "tv" ? "TV" : "Movies"),
        h("span.sub", plural(tiles.length, "playlist")),
      ),
      ...views,
      h("div.filters", h("span.grow"), create),
      section(
        "Playlists",
        tiles.length ? h("div.grid.sq", ...tiles) : h("p.sub", "No playlists yet."),
      ),
    ];
  }
  const itemKind = kind === "tv" ? "show" : "movie";
  // A show's counts come from its progress, a movie's from its own watch state.
  const [all, progress, ws] = await Promise.all([
    titles(ctx, itemKind),
    kind === "tv" ? showProgress(ctx) : undefined,
    kind === "tv" ? undefined : watchStates(ctx),
  ]);
  // Genres of the libraries these titles come from; names are unique ignoring case.
  const genres = (
    await Promise.all(
      [...new Set(all.map((i) => i.libraryId))].map((libraryId) =>
        paged((offset) =>
          ok(ctx.api.GET("/genres", { params: { query: { libraryId, limit: 200, offset } } })),
        ),
      ),
    )
  )
    .flat()
    .sort((a, b) => a.name.localeCompare(b.name));
  // Per title once, so filtering and sorting never rescan a show's episodes.
  const statEntries = (list: Item[]) =>
    list.map((i) => {
      const p = progress?.get(i.id);
      const w = ws?.get(i.id);
      const stat =
        kind === "tv"
          ? {
              unwatched: p?.unwatched ?? 0,
              started: !!p?.inProgress,
              at: p?.lastPlayedAt,
              seasons: p?.seasons ?? 0,
            }
          : {
              unwatched: w?.watched ? 0 : 1,
              started: inProgress(w),
              at: w?.lastPlayedAt,
              seasons: 0,
            };
      return [i.id, { ...stat, played: Date.parse(stat.at ?? "") || 0 }] as const;
    });
  const stats = new Map(statEntries(all));
  const isUnwatched = (i: Item) => stats.get(i.id)!.unwatched > 0;
  const isStarted = (i: Item) => stats.get(i.id)!.started;
  const played = (i: Item) => stats.get(i.id)!.played;

  const filters = [
    "All",
    "Unwatched",
    "In progress",
    ...(ctx.profile.kind === "adult" ? ["Kids"] : []),
  ];
  let filter = filters.includes(query.get("filter")!) ? query.get("filter")! : "All";
  let sort = SORTS[kind].includes(query.get("sort")!) ? query.get("sort")! : "Recently added";
  const asked = query.get("genre") ?? undefined;
  let genre = genres.some((g) => g.id === asked) ? asked : undefined;
  // With Hide on, the unfiltered lists leave kids' titles out; the Kids pill asks for them by kidsContent.
  const hiding = !!ctx.profile.hideKidsContent;
  const kidsOnly = () => hiding && filter === "Kids";
  let kids: Item[] | undefined;
  const inGenre = new Map<string, Set<string>>();
  const genreKey = (g: string) => `${g}${kidsOnly() ? "k" : ""}`;
  // Loads what the current picks need; the kids list stays out of the shared cache of unfiltered titles.
  const ready = async () => {
    if (kidsOnly() && !kids) {
      kids = await items(ctx.api, { kind: itemKind, kidsContent: true });
      statEntries(kids).forEach(([id, s]) => stats.set(id, s));
    }
    if (genre && !inGenre.has(genreKey(genre))) {
      const ids = await items(ctx.api, {
        kind: itemKind,
        genreId: genre,
        kidsContent: kidsOnly() || undefined,
      });
      inGenre.set(genreKey(genre), new Set(ids.map((i) => i.id)));
    }
  };
  await ready();
  // Draws at once when everything is loaded, so a pick that needs no request stays synchronous.
  const loaded = () => !(kidsOnly() && !kids) && !(genre && !inGenre.has(genreKey(genre)));
  const redraw = () => (loaded() ? draw() : void ready().then(draw));
  // The picks ride in the hash without a history entry, so a redraw or a return to the page keeps them.
  const remember = () => {
    if (!out.isConnected) return;
    const [path, q] = location.hash.split("?");
    const p = new URLSearchParams(q);
    if (filter === "All") p.delete("filter");
    else p.set("filter", filter);
    if (sort === "Recently added") p.delete("sort");
    else p.set("sort", sort);
    if (genre === undefined) p.delete("genre");
    else p.set("genre", String(genre));
    history.replaceState(null, "", p.size ? `${path}?${p}` : path);
  };
  const out = h("div.grid");
  const chips = h("div.chips", { role: "group", "aria-label": "Filter" });
  const count = h("span.sub");

  const draw = () => {
    const keep: Record<string, (i: Item) => boolean> = {
      All: () => true,
      Unwatched: isUnwatched,
      "In progress": isStarted,
      Kids: (i) => !!i.kidsContent,
    };
    const list = (kidsOnly() ? kids! : all)
      .filter(
        (i) =>
          keep[filter](i) && (genre === undefined || !!inGenre.get(genreKey(genre))?.has(i.id)),
      )
      .sort((a, b) => {
        if (sort === "Title") return title(a).localeCompare(title(b));
        if (sort === "Released") return (b.parsedYear ?? 0) - (a.parsedYear ?? 0);
        if (sort === "Last watched") return played(b) - played(a);
        return b.addedAt.localeCompare(a.addedAt);
      });
    count.textContent = plural(list.length, kind === "tv" ? "show" : "movie");
    chips.replaceChildren(
      ...filters.map((f) =>
        chip(f, f === filter, () => {
          filter = f;
          remember();
          redraw();
        }),
      ),
    );
    out.replaceChildren(
      ...(list.length
        ? list.map((i) => {
            const n = kind === "tv" ? stats.get(i.id)!.unwatched : 0;
            const seasons = stats.get(i.id)!.seasons;
            return h(
              "a.poster",
              { href: `#/${i.kind}/${i.id}`, "data-id": i.id },
              withWatched(
                h(
                  "div.art.p",
                  {
                    style: `background:var(--c${tint(i.id, 10)})`,
                    "data-art": artKey(i),
                    "data-ph": placeholderKind(i.kind),
                  },
                  n ? h("span.badge", `${n} new`) : null,
                  kind === "movies" && isUnwatched(i)
                    ? h("span.dot", { role: "img", "aria-label": "Unwatched" })
                    : null,
                ),
                ws && playFraction(i, ws),
              ),
              h("b", title(i)),
              h(
                "span.sub",
                // Show progress leaves out the kids' shows an adult hides, so the Kids pill has no season count for them.
                kind === "tv" && seasons
                  ? `${runtime(i.metadata?.runtimeMinutes)} - ${plural(seasons, "season")}`.replace(
                      /^ - /,
                      "",
                    )
                  : runtime(i.metadata?.runtimeMinutes),
              ),
            );
          })
        : [h("p.sub", kind === "tv" ? "No shows match." : "No movies match.")]),
    );
  };
  draw();
  const pick = selection(ctx, itemKind, out);
  return [
    h("div.titlerow", h("h1", kind === "tv" ? "TV" : "Movies"), count),
    ...views,
    h(
      "div.filters",
      chips,
      h("span.grow"),
      genres.length
        ? h(
            "label.sel",
            "Genre",
            h(
              "select.in",
              {
                onchange: (e: Event) => {
                  genre = (e.target as HTMLSelectElement).value || undefined;
                  remember();
                  redraw();
                },
              },
              h("option", { value: "" }, "All genres"),
              ...genres.map((g) => h("option", { value: g.id, selected: g.id === genre }, g.name)),
            ),
          )
        : null,
      sel("Sort", SORTS[kind], sort, (v) => ((sort = v), remember(), draw())),
      pick.toggle,
    ),
    section(kind === "tv" ? "Shows" : "Movies", out),
    pick.bar,
  ];
}
