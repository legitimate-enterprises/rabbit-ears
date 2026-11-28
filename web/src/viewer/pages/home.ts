import { h } from "../../admin/dom";
import { ok } from "../../admin/api";
import type { Schemas } from "../../api/client";
import { tint } from "../../api/uid";
import {
  color,
  epCode,
  findItem,
  inProgress,
  newest,
  playFraction,
  resumeFraction,
  showProgress,
  title,
  today,
  watchState,
  type Ctx,
  type Item,
  type WatchState,
} from "../data";
import { left, plural, runtime } from "../format";
import { art, empty, playmark, section, watched } from "../ui";
import { resumeHref } from "../editions";
import { artKey, artOf, placeholderKind, type PlaceholderKind } from "../artwork";
import { videoTile } from "./youtube";
import { onNowTiles } from "./stations";

const href = (i: Item) => `#/${i.kind}/${i.id}`;
/** With nothing in progress, whether the hero features the newest movie instead of leaving Home without one. */
const FEATURE_NEWEST = false;

/** A movie, episode or video as the hero and Also in progress draw it. */
type Card = {
  at?: string;
  head: string;
  line: string;
  play: string;
  detail: string;
  hero?: string;
  id: string;
  thumb?: string;
  ph: PlaceholderKind;
  done?: number;
};

/** How many titles in progress Home shows: the hero and three more. */
const RESUMING = 4;
/** How many shows Next up shows. */
const NEXT_UP = 12;
/** How many titles Recently added shows. */
const RECENT = 14;

/**
 * The profile's latest movies and episodes in progress, most recently played
 * first, read from only as many pages of its watch state as hold them, and
 * every state those pages hold. Only an episode is read on its own.
 */
async function resumable(ctx: Ctx) {
  const going: [Item, WatchState][] = [];
  const seen = new Map<string, WatchState>();
  for (let offset: number | undefined = 0; offset !== undefined && going.length < RESUMING;) {
    const p: Schemas["WatchStatePage"] = await ok(
      ctx.api.GET("/profiles/{profileId}/watch-state", {
        params: { path: { profileId: ctx.profile.id }, query: { limit: 200, offset } },
      }),
    );
    for (const w of p.items) seen.set(w.itemId, w);
    const todo = p.items.filter(inProgress);
    while (todo.length && going.length < RESUMING) {
      const ws = todo.splice(0, RESUMING - going.length);
      const found = await Promise.all(ws.map((w) => findItem(ctx, w.itemId)));
      // A video or song in progress is not an item here; YouTube's own listing brings videos.
      found.forEach((i, k) => i && /^(movie|episode)$/.test(i.kind) && going.push([i, ws[k]]));
    }
    offset = p.nextOffset;
  }
  return { going, seen };
}

/** Each occasion in season, nearest first, with its titles; none when the server errs, so Home never breaks over it. */
async function occasions(ctx: Ctx): Promise<{ id: string; name: string; items: Item[] }[]> {
  try {
    const { items } = await ok(
      ctx.api.GET("/profiles/{profileId}/occasions", {
        params: { path: { profileId: ctx.profile.id }, query: { date: today() } },
      }),
    );
    return await Promise.all(
      items.map(async (o) => ({
        id: o.id,
        name: o.headline ?? o.name,
        items: (
          await ok(
            ctx.api.GET("/items", {
              params: { query: { occasionId: o.id, sort: "daily", limit: RECENT } },
            }),
          )
        ).items,
      })),
    );
  } catch {
    return [];
  }
}

export async function home(ctx: Ctx): Promise<HTMLElement[]> {
  const [movies, shows, byShow, { going, seen: ws }, fresh, onNow, started, seasonal] =
    await Promise.all([
      newest(ctx, "movie", RECENT),
      newest(ctx, "show", RECENT),
      showProgress(ctx),
      resumable(ctx),
      // A server without YouTube, or one that errs, only leaves the row out.
      ok(
        ctx.api.GET("/youtube/videos", {
          params: { query: { sort: "newest", unwatched: true, limit: 12 } },
        }),
      ).then(
        (p) => p.items,
        () => [],
      ),
      onNowTiles(ctx),
      ok(
        ctx.api.GET("/youtube/videos", {
          params: { query: { inProgress: true, sort: "played", limit: 4 } },
        }),
      ).then(
        (p) => p.items,
        () => [],
      ),
      occasions(ctx),
    ]);
  // An episode's show, by its season.
  const bySeason = new Map<string, Item>();
  const seasons = new Set(going.filter(([i]) => i.kind === "episode").map(([i]) => i.parentId!));
  await Promise.all(
    [...seasons].map(async (id) => {
      const showId = (await findItem(ctx, id))?.parentId;
      const s = showId && (await findItem(ctx, showId));
      if (s) bySeason.set(id, s);
    }),
  );
  const showOf = (e: Item) => (e.parentId === undefined ? undefined : bySeason.get(e.parentId));

  // Next up: shows the profile has started, by title, at their next episode unless that one is in progress.
  const begun = [...byShow.values()].flatMap((p) =>
    p.nextEpisodeId && (p.lastPlayedAt || p.inProgress || p.unwatched < p.episodes)
      ? [{ show: p.showId, id: p.nextEpisodeId, going: p.inProgress }]
      : [],
  );
  const next: Item[] = [];
  for (let k = 0; k < begun.length && next.length < NEXT_UP;) {
    const some = begun.slice(k, (k += NEXT_UP - next.length));
    const eps = await Promise.all(
      some.map(async (b) => {
        // Only a show with an episode in progress can be at one.
        const w = b.going ? (ws.get(b.id) ?? (await watchState(ctx, b.id))) : undefined;
        if (inProgress(w)) return undefined;
        const [e, s] = await Promise.all([findItem(ctx, b.id), findItem(ctx, b.show)]);
        return e && s && { e, s };
      }),
    );
    eps.forEach((x) => x && (next.push(x.e), bySeason.set(x.e.parentId!, x.s)));
  }

  const label = (i: Item) => {
    const w = ws.get(i.id);
    const l = left(i.metadata?.runtimeMinutes, w?.positionMs ?? 0);
    return i.kind === "episode" ? `${epCode(i)} - ${title(i)}${l ? " - " + l : ""}` : l;
  };
  const head = (i: Item) => (i.kind === "episode" ? title(showOf(i) ?? i) : title(i));
  const itemCard = (i: Item): Card => {
    const w = ws.get(i.id);
    return {
      at: w?.lastPlayedAt,
      head: head(i),
      line: label(i),
      play: resumeHref(i.id, w),
      detail: i.kind === "episode" ? `#/show/${showOf(i)?.id}` : href(i),
      hero: artKey(showOf(i) ?? i, true),
      id: i.id,
      thumb: artKey(i, true),
      ph: placeholderKind(i.kind),
      done: playFraction(i, ws),
    };
  };
  const videoCard = (v: Schemas["YouTubeVideo"]): Card => ({
    at: v.lastPlayedAt,
    head: v.title,
    line: [v.channelTitle, left(v.durationMs && v.durationMs / 60000, v.positionMs)]
      .filter(Boolean)
      .join(" - "),
    play: `#/play/${v.id}`,
    detail: `#/video/${v.id}`,
    hero: artOf(v.posterUrl),
    id: v.id,
    thumb: artOf(v.posterUrl),
    ph: "video",
    done: resumeFraction(v, v.durationMs),
  });

  const progress = [...going.map(([i]) => itemCard(i)), ...started.map(videoCard)].sort((a, b) =>
    (b.at ?? "").localeCompare(a.at ?? ""),
  );

  const sections: HTMLElement[] = [];
  const latest = movies[0];
  const resuming = progress.length > 0;
  const featured =
    progress[0] ??
    (FEATURE_NEWEST &&
      latest && { ...itemCard(latest), line: runtime(latest.metadata?.runtimeMinutes) });
  if (featured) {
    sections.push(
      section(
        "Pick up where you left off",
        h(
          "div.hero",
          h(
            "div.tile.hero-tile",
            {
              style: "background:linear-gradient(160deg,#2E6B8A,#17304A 60%,#0E1C2C)",
              "data-art": featured.hero,
            },
            h(
              "div.copy",
              h("span.kicker", resuming ? "Continue watching" : "Featured"),
              h("h1.big", featured.head),
              h("p.line", featured.line),
              h(
                "div.actions",
                h("a.btn.p", { href: featured.play }, resuming ? "Resume" : "Play"),
                h("a.btn.g", { href: featured.detail }, "Show details"),
              ),
            ),
            watched(featured.done),
          ),
          progress.length > 1
            ? h(
                "div.also",
                h("span.label", "Also in progress"),
                ...progress.slice(1, 4).map((c) =>
                  h(
                    "a.row-link",
                    { href: c.play },
                    h("div.art.thumb", {
                      "aria-hidden": "true",
                      style: `background:${color(c.id)}`,
                      "data-art": c.thumb,
                      "data-ph": c.ph,
                    }),
                    h("span", h("b", c.head), h("span.sub", c.line), watched(c.done)),
                  ),
                ),
              )
            : null,
        ),
      ),
    );
  }

  const poster = (i: Item) =>
    h(
      "a.poster",
      { href: href(i) },
      h(
        "div.art.p",
        {
          style: `background:var(--c${tint(i.id, 10)})`,
          "data-art": artKey(i),
          "data-ph": placeholderKind(i.kind),
        },
        h("span.tag", i.kind === "movie" ? "Movie" : "TV"),
      ),
      h("b", title(i)),
      h(
        "span.sub",
        i.kind === "movie"
          ? [runtime(i.metadata?.runtimeMinutes), i.parsedYear].filter(Boolean).join(" - ")
          : plural(byShow.get(i.id)?.seasons ?? 0, "season"),
      ),
    );
  const links = [
    ["TV", "#/tv"],
    ["Movies", "#/movies"],
  ];
  if (next.length)
    sections.push(
      row(
        "Next up",
        [["All shows", "#/tv"]],
        next.map((e) =>
          h(
            "a.wide",
            {
              href: resumeHref(e.id, ws.get(e.id)),
              "aria-label": `Play ${title(showOf(e) ?? e)}, ${epCode(e)} ${title(e)}`,
            },
            withPlaymark(art(e, "w")),
            h("b", title(showOf(e) ?? e)),
            h("span.sub", `${epCode(e)} - ${title(e)} - ${runtime(e.metadata?.runtimeMinutes)}`),
          ),
        ),
      ),
    );

  if (onNow.length)
    sections.push(
      row(
        "On now",
        [
          ["Movies", "#/movies?view=stations"],
          ["TV", "#/tv?view=stations"],
          ["Music", "#/music?view=stations"],
        ],
        onNow,
      ),
    );

  for (const o of seasonal)
    if (o.items.length)
      sections.push(row(o.name, [["See all", `#/occasion/${o.id}`]], o.items.map(poster)));

  const recent = [...movies, ...shows]
    .sort((a, b) => b.addedAt.localeCompare(a.addedAt))
    .slice(0, RECENT);
  if (recent.length) sections.push(row("Recently added", links, recent.map(poster)));

  if (fresh.length)
    sections.push(
      row(
        "New from your channels",
        [["YouTube", "#/youtube"]],
        fresh.map((v) => videoTile(v, true)),
      ),
    );
  return sections.length ? sections : [empty(ctx)];
}

const withPlaymark = (a: HTMLElement) => (a.append(playmark()), a);

function row(name: string, links: string[][], tiles: HTMLElement[]) {
  return section(
    name,
    h("div.rowhead", h("h2", name), ...links.map(([l, to]) => h("a.sub", { href: to }, l))),
    h("div.strip", ...tiles),
  );
}
