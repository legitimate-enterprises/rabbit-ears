import { h } from "../../admin/dom";
import { ok } from "../../admin/api";
import { bytes } from "../../admin/format";
import type { Schemas } from "../../api/client";
import { tint } from "../../api/uid";
import {
  epCode,
  inProgress,
  episodesOf,
  itemOf,
  nextEpisode,
  playFraction,
  resumeFraction,
  seasonsOf,
  title,
  watchState,
  watchStates,
  type Ctx,
  type Item,
  type WatchState,
} from "../data";
import { deviceSnapshot } from "../device";
import { defaultEdition, editionLabel, preselect, resumeHref, resumes } from "../editions";
import { left, plural, runtime, subtitles } from "../format";
import { resolutionLabel, mbps } from "../playback";
import {
  allowedRows,
  art,
  back,
  btn,
  icon,
  menu,
  meta,
  opt,
  person,
  refresh,
  rule,
  tile,
  withWatched,
} from "../ui";
import { artKey, artOf } from "../artwork";
import { watchTogether } from "../together";
import { markWatched } from "../select";
import { shuffleButton } from "../shuffle";
import { addToCollection } from "./collections";
import { occasionRows } from "./occasion";
import { addToPlaylist, addToStation } from "./playlists";
import { videoTile } from "./youtube";

type File = Schemas["MediaFile"];
const kv = (rows: [string, string | Node | undefined][]) =>
  h(
    "dl.kv",
    ...rows.filter(([, v]) => v).flatMap(([k, v]) => [h("dt", k), h("dd", v as string | Node)]),
  );
const seasonName = (se: Item) => (se.seasonNumber === 0 ? "Specials" : `Season ${se.seasonNumber}`);
const synopsis = (i: Item) => (i.metadata?.overview ? h("p.synopsis", i.metadata.overview) : null);

async function put(ctx: Ctx, id: string, watched: boolean) {
  await ok(
    ctx.api.PUT("/profiles/{profileId}/watch-state/{itemId}", {
      params: { path: { profileId: ctx.profile.id, itemId: id } },
      body: { watched, positionMs: 0 },
    }),
  );
}
/** Takes a title out of progress, keeping whether it was watched. */
async function dismiss(ctx: Ctx, id: string) {
  await ok(
    ctx.api.DELETE("/profiles/{profileId}/watch-state/{itemId}/position", {
      params: { path: { profileId: ctx.profile.id, itemId: id } },
    }),
  );
}
async function files(ctx: Ctx, id: string): Promise<File[]> {
  return (await ok(ctx.api.GET("/media-files", { params: { query: { itemId: id } } })))
    .items as File[];
}
const lang = (t: Schemas["Track"]) => t.title || t.language || "Unknown";
const credits = (ctx: Ctx, itemId: string) =>
  ok(ctx.api.GET("/items/{itemId}/credits", { params: { path: { itemId } } }));
const genres = (i: Item) => i.genres?.map((g) => g.name).join(", ");
// What relates is extra to the page: a read that fails leaves it out.
type Related = Omit<Schemas["ItemRelated"], "similar">;
const related = (ctx: Ctx, itemId: string): Promise<Related> =>
  ok(ctx.api.GET("/items/{itemId}/related", { params: { path: { itemId } } })).catch(() => ({
    collections: [],
  }));

/** A neighbor's details, taking the current page's place in history so Back skips it. */
function neighbor(dir: "Previous" | "Next", noun: "film" | "episode", i?: Item) {
  if (!i) return null;
  const to = `#/${i.kind}/${i.id}`;
  const name = noun === "film" ? title(i) : epCode(i);
  return h(
    "a.btn.s",
    {
      href: to,
      "aria-label": `${dir} ${noun}, ${name}`,
      onclick: (e: Event) => (e.preventDefault(), location.replace(to)),
    },
    `${dir}: ${name}`,
  );
}

function crewLine(crew: Schemas["Credit"][]) {
  const names = (jobs: string[]) =>
    [...new Set(crew.filter((c) => jobs.includes(c.job ?? "")).map((c) => c.name))].join(", ");
  const line = (
    [
      ["Directed by", names(["Director"])],
      ["Written by", names(["Writer", "Screenplay"])],
      ["Created by", names(["Creator"])],
    ] as const
  )
    .filter(([, n]) => n)
    .map(([label, n]) => `${label} ${n}`)
    .join(" · ");
  return line ? h("p.sub", line) : null;
}

const cast = (c: Schemas["ItemCredits"]) =>
  c.cast.length
    ? h(
        "section",
        { "aria-label": "Cast" },
        h("h2.sec", "Cast"),
        h(
          "div.strip",
          ...c.cast.map((p) => person(p.personId, p.name, !!p.profilePath, p.character)),
        ),
      )
    : null;

const collectionRows = (r: Related) =>
  r.collections.map(({ collection: { name }, items }) =>
    h(
      "section",
      { "aria-label": name },
      h("h2.sec", name),
      h("div.strip", ...items.map((i) => tile(i, runtime(i.metadata?.runtimeMinutes)))),
    ),
  );

// The same snapshot play starts with, so the line names the verdict play would get.
async function videoLine(ctx: Ctx, itemId: string, f?: File) {
  const v = f?.tracks.find((t) => t.kind === "video");
  if (!f || !v) return undefined;
  const verdict = await ok(
    ctx.api.POST("/items/{itemId}/playability", {
      params: { path: { itemId } },
      body: await deviceSnapshot(),
    }),
  ).then(
    (p) => p.verdict,
    () => undefined,
  );
  const direct = verdict === "direct_play";
  return h(
    "span",
    [
      v.width && v.height ? resolutionLabel(v.width, v.height) : "",
      v.hdrFormat?.toUpperCase().replace("_", " "),
      v.codec.toUpperCase(),
      f.bitrate ? mbps(f.bitrate) : "",
    ]
      .filter(Boolean)
      .join(" - ") + " - ",
    h(
      `span.${direct ? "ok" : "warn"}`,
      direct
        ? "Direct play on this device"
        : verdict
          ? "Converted for this device"
          : "No way to play this on this device",
    ),
  );
}

/**
 * A movie's or show's More: watched and, for a title in progress, Dismiss, then for an adult its kids' content and the kids' allow
 * lists, then what adds it elsewhere.
 */
async function more(
  ctx: Ctx,
  i: Item,
  watched: boolean,
  mark: () => Promise<unknown>,
  add: (HTMLElement | null)[],
  dismissIt?: () => Promise<unknown>,
) {
  const adult = ctx.profile.kind === "adult";
  const kidsPath = { params: { path: { itemId: i.id } } };
  return menu(h("button.btn.s", { type: "button" }, "More"), "More", [
    opt(watched ? "Mark as unwatched" : "Mark as watched", async () => (await mark(), refresh())),
    !!dismissIt && opt("Dismiss", async () => (await dismissIt(), refresh())),
    adult &&
      opt(
        "Kids' content",
        async () => (
          await ok(
            ctx.api.PUT("/items/{itemId}/kids-content", {
              ...kidsPath,
              body: { kidsContent: !i.kidsContent },
            }),
          ),
          refresh()
        ),
        !!i.kidsContent,
      ),
    adult &&
      i.kidsContentOverride !== undefined &&
      opt(
        "Use rating",
        async () => (await ok(ctx.api.DELETE("/items/{itemId}/kids-content", kidsPath)), refresh()),
      ),
    ...(await allowedRows(ctx, i.id)),
    ...(add.some(Boolean) ? [rule(), ...add] : []),
  ]);
}

export async function movie(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const [m, fs, w, cr, rel, seasonal] = await Promise.all([
    ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId: id } } })),
    files(ctx, id),
    watchState(ctx, id),
    credits(ctx, id),
    related(ctx, id),
    occasionRows(ctx, id),
  ]);
  const eds = m.editions ?? [];
  let editionId = eds.length > 1 ? preselect(eds, w)?.id : undefined;
  let audioId: string | undefined;
  // Track ids, lines and file come from the copy the selected edition plays; the server
  // ignores an audio id that is not in the file it picks.
  const fileOf = () => {
    const e = eds.find((x) => x.id === editionId) ?? defaultEdition(eds);
    return e ? fs.find((x) => x.id === e.copies.find((c) => !c.unavailable)?.mediaFileId) : fs[0];
  };
  const play = h("a.btn.p", icon("play", 16), "Play");
  const label = play.lastChild as Text;
  const poster = withWatched(
    art(m, "poster220"),
    resumeFraction(w, (m.metadata?.runtimeMinutes ?? 0) * 60000),
  );
  const update = () => {
    const q = new URLSearchParams();
    if (editionId) q.set("edition", editionId);
    if (audioId) q.set("audio", audioId);
    play.setAttribute("href", `#/play/${m.id}${q.size ? `?${q}` : ""}`);
    label.data = resumes(eds, w, editionId) ? "Resume" : "Play";
    poster.querySelector(".bar")?.toggleAttribute("hidden", !resumes(eds, w, editionId));
  };
  update();
  const collections = m.collections?.length
    ? h(
        "span",
        ...m.collections.flatMap((c, n) => [
          n ? ", " : "",
          h("a", { href: `#/collection/${c.id}` }, c.name),
        ]),
      )
    : undefined;
  const rows = async (): Promise<HTMLElement> => {
    const f = fileOf();
    const audio = f?.tracks.filter((t) => t.kind === "audio") ?? [];
    const pick = h(
      "select.in",
      {
        "aria-label": "Audio",
        onchange: () => ((audioId = pick.value), update()),
      },
      ...audio.map((t) =>
        h(
          "option",
          { value: t.id, selected: t.isDefault },
          `${lang(t)} - ${t.codec.toUpperCase()}${t.channelLayout ? " " + t.channelLayout : ""}`,
        ),
      ),
    ) as HTMLSelectElement;
    return kv([
      ["Audio", audio.length ? pick : undefined],
      ["Subtitles", subtitles(f?.tracks ?? [])],
      ["Collections", collections],
      ["Video", await videoLine(ctx, m.id, f)],
      ["File", f ? `${f.path.split("/").pop()} - ${bytes(f.sizeBytes)}` : undefined],
    ]);
  };
  let list = await rows();
  const uhd = h("span.tag", "4K");
  const hdr = h("span.tag", "HDR");
  const tags = () => {
    const v = fileOf()?.tracks.find((t) => t.kind === "video");
    uhd.hidden = !(v?.height && v.height >= 2000);
    hdr.hidden = !v?.hdrFormat;
  };
  tags();
  const choose = async (id: string) => {
    editionId = id;
    audioId = undefined;
    update();
    const next = await rows();
    // A later pick has already moved on.
    if (editionId !== id) return;
    list.replaceWith(next);
    list = next;
    tags();
  };
  const edPick = editionId
    ? (h(
        "select.in",
        { onchange: () => void choose(edPick!.value) },
        ...eds.map((e) =>
          h("option", { value: e.id, selected: e.id === editionId }, editionLabel(e)),
        ),
      ) as HTMLSelectElement)
    : undefined;
  const edition = edPick && h("label.sel", "Edition", edPick);
  // A Watch together plays the default edition, so the choice goes while opening or inside one.
  const tg = await watchTogether(ctx, { id: m.id, title: title(m) }, play, (yes) => {
    if (!edition || edition.hidden === yes) return;
    edition.hidden = yes;
    edPick!.value = String(defaultEdition(eds).id);
    void choose(defaultEdition(eds).id);
  });
  return [
    back("Movies", "#/movies"),
    h(
      "div.detail",
      poster,
      h(
        "div.info",
        h("h1.big", title(m)),
        meta(
          runtime(m.metadata?.runtimeMinutes),
          m.parsedYear ? String(m.parsedYear) : undefined,
          m.metadata?.contentRating,
          genres(m),
          uhd,
          hdr,
          inProgress(w) && h("span.amber", left(m.metadata?.runtimeMinutes, w!.positionMs)),
        ),
        h(
          "div.actions",
          play,
          edition,
          await more(
            ctx,
            m,
            !!w?.watched,
            () => put(ctx, m.id, !w?.watched),
            [
              addToPlaylist(ctx, m.id, "movies", () => editionId),
              addToStation(ctx, m.id, "movies", () => editionId),
              addToCollection(ctx, m.id),
            ],
            inProgress(w) ? () => dismiss(ctx, m.id) : undefined,
          ),
          neighbor("Previous", "film", rel.previous),
          neighbor("Next", "film", rel.next),
          tg.actions,
        ),
        tg.note,
        synopsis(m),
        crewLine(cr.crew),
      ),
    ),
    ...collectionRows(rel),
    ...seasonal,
    cast(cr),
    h("section", { "aria-label": "Details" }, h("h2.sec", "Details"), list),
  ].filter((e): e is HTMLElement => !!e);
}

export async function show(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const [s, t, ws, cr, seasonal] = await Promise.all([
    itemOf(ctx, id, "show"),
    seasonsOf(ctx, id),
    watchStates(ctx, id),
    credits(ctx, id),
    occasionRows(ctx, id),
  ]);
  const eps = [...t.episodes.values()].flat();
  const next = nextEpisode(eps, ws);
  const started = eps.some((e) => ws.has(e.id));
  const allWatched = eps.length > 0 && eps.every((e) => ws.get(e.id)?.watched);
  const w = next && ws.get(next.id);
  return [
    back("TV", "#/tv"),
    h(
      "div.detail",
      withWatched(art(s, "poster220"), playFraction(s, ws, eps)),
      h(
        "div.info",
        h("h1.big", title(s)),
        meta(
          runtime(s.metadata?.runtimeMinutes),
          plural(t.seasons.length, "season"),
          s.parsedYear ? `${s.parsedYear}-` : undefined,
          s.metadata?.contentRating,
          genres(s),
          next &&
            inProgress(w) &&
            h(
              "span.amber",
              `${left(next.metadata?.runtimeMinutes, w!.positionMs)} in ${epCode(next)}`,
            ),
        ),
        h(
          "div.actions",
          next
            ? h(
                "a.btn.p",
                { href: resumeHref(next.id, w) },
                icon("play", 16),
                `${inProgress(w) ? "Resume" : started ? "Play next" : "Play"} ${epCode(next)}`,
              )
            : null,
          shuffleButton(ctx, eps, s),
          await more(ctx, s, allWatched, () => markWatched(ctx, [s.id], !allWatched), [
            addToPlaylist(ctx, s.id, "tv"),
            addToStation(ctx, s.id, "tv"),
          ]),
        ),
        synopsis(s),
        crewLine(cr.crew),
      ),
    ),
    ...seasonal,
    cast(cr),
    h(
      "section",
      { "aria-label": "Seasons" },
      h("h2.sec", "Seasons"),
      h(
        "div.strip",
        ...t.seasons.map((se) => {
          const es = t.episodes.get(se.id) ?? [];
          const unseen = es.filter((e) => !ws.get(e.id)?.watched).length;
          return h(
            "a.poster.season",
            { href: `#/season/${se.id}` },
            withWatched(
              h(
                "div.art.p",
                {
                  style: `background:var(--c${tint(se.id, 10)})`,
                  "data-art": artKey(se),
                  "data-ph": "show",
                },
                unseen ? h("span.badge", `${unseen} new`) : null,
              ),
              playFraction(se, ws, es),
            ),
            h("b", seasonName(se)),
            h("span.sub", plural(es.length, "episode")),
          );
        }),
      ),
    ),
  ].filter((e): e is HTMLElement => !!e);
}

function episodeRow(e: Item, ws: Map<string, WatchState>, current?: boolean) {
  const w = ws.get(e.id);
  const rt = e.metadata?.runtimeMinutes;
  return h(
    "a.ep",
    { href: `#/episode/${e.id}`, "aria-current": current ? "true" : undefined },
    withWatched(
      h(
        "div.art.thumb200",
        {
          style: `background:var(--c${tint(e.id, 10)})`,
          "data-art": artKey(e),
          "data-ph": "show",
        },
        w?.watched
          ? h("span.seen", { role: "img", "aria-label": "Watched" }, icon("check", 12))
          : null,
      ),
      playFraction(e, ws),
    ),
    h(
      "span",
      h("b", `${e.episodeNumber}. ${title(e)}`),
      h(
        "span.sub",
        [runtime(rt), e.metadata?.releaseDate].filter(Boolean).join(" - "),
        inProgress(w) ? " - " : "",
        inProgress(w) ? h("span.amber", left(rt, w!.positionMs)) : "",
      ),
      e.metadata?.overview ? h("span.desc", e.metadata.overview) : null,
    ),
  );
}

export async function season(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const [se, eps, ws] = await Promise.all([
    itemOf(ctx, id, "season"),
    episodesOf(ctx, id),
    watchStates(ctx, id),
  ]);
  const sh = await itemOf(ctx, se.parentId!, "show");
  const next = nextEpisode(eps, ws);
  const allWatched = eps.length > 0 && eps.every((e) => ws.get(e.id)?.watched);
  const name = seasonName(se);
  return [
    back(title(sh), `#/show/${sh.id}`),
    h(
      "div.detail",
      withWatched(art(se, "poster160"), playFraction(se, ws, eps)),
      h(
        "div.info",
        h("h1.big", `${title(sh)} `, h("span.dim", "·"), ` ${name}`),
        meta(
          plural(eps.length, "episode"),
          `${eps.filter((e) => !ws.get(e.id)?.watched).length} unwatched`,
        ),
        h(
          "div.actions",
          next
            ? h(
                "a.btn.p",
                { href: resumeHref(next.id, ws.get(next.id)) },
                icon("play", 16),
                `${inProgress(ws.get(next.id)) ? "Resume" : "Play"} E${next.episodeNumber}`,
              )
            : null,
          shuffleButton(ctx, eps, sh),
          btn(
            allWatched ? "Mark all unwatched" : "Mark all watched",
            "check",
            "s",
            async () => (await markWatched(ctx, [se.id], !allWatched), refresh()),
          ),
          addToPlaylist(ctx, se.id, "tv"),
          addToStation(ctx, se.id, "tv"),
        ),
        synopsis(se),
      ),
    ),
    h(
      "section",
      { "aria-label": "Episodes" },
      h("h2.sec", "Episodes"),
      h("div.eps", ...eps.map((e) => episodeRow(e, ws, e.id === next?.id))),
    ),
  ];
}

export async function episode(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const [e, fs, rel] = await Promise.all([
    itemOf(ctx, id, "episode"),
    files(ctx, id),
    related(ctx, id),
  ]);
  const se = await itemOf(ctx, e.parentId!, "season");
  const [sh, t] = await Promise.all([
    itemOf(ctx, se.parentId!, "show"),
    seasonsOf(ctx, se.parentId!),
  ]);
  const f = fs[0];
  const eps = [...t.episodes.values()].flat();
  const upNext = eps.slice(
    eps.findIndex((x) => x.id === e.id) + 1,
    eps.findIndex((x) => x.id === e.id) + 3,
  );
  const ws = new Map<string, WatchState>();
  await Promise.all(
    [e, ...upNext].map(async (x) => {
      const s = await watchState(ctx, x.id);
      if (s) ws.set(x.id, s);
    }),
  );
  const w = ws.get(e.id);
  const a = f?.tracks
    .filter((x) => x.kind === "audio")
    .map((x) => `${lang(x)} - ${x.channelLayout ?? x.codec}`)
    .join(", ");
  const subs = subtitles(f?.tracks ?? []);
  const rt = e.metadata?.runtimeMinutes;
  const play = h(
    "a.btn.p",
    { href: resumeHref(e.id, w) },
    icon("play", 16),
    inProgress(w) ? "Resume" : "Play",
  );
  const tg = await watchTogether(ctx, { id: e.id, title: title(e) }, play);
  return [
    back(seasonName(se), `#/season/${se.id}`),
    h(
      "div.detail",
      withWatched(
        h("div.art.still", {
          style: `background:var(--c${tint(e.id, 10)})`,
          "aria-hidden": "true",
          "data-art": artKey(e),
          "data-ph": "show",
        }),
        playFraction(e, ws),
      ),
      h(
        "div.info",
        h("a.crumb", { href: `#/season/${se.id}` }, `${title(sh)} · ${seasonName(se)}`),
        h("h1.big.ep", `${e.episodeNumber}. ${title(e)}`),
        meta(
          runtime(rt),
          e.metadata?.releaseDate,
          f?.tracks.some((x) => (x.height ?? 0) >= 2000) ? h("span.tag", "4K") : false,
          inProgress(w) && h("span.amber", left(rt, w!.positionMs)),
        ),
        h(
          "div.actions",
          play,
          h("a.btn.s", { href: `#/play/${e.id}?pos=0` }, "Start over"),
          btn(
            w?.watched ? "Mark unwatched" : "Mark watched",
            "check",
            "s",
            async () => (await put(ctx, e.id, !w?.watched), refresh()),
          ),
          inProgress(w) &&
            btn("Dismiss", "x", "s", async () => (await dismiss(ctx, e.id), refresh())),
          addToPlaylist(ctx, e.id, "tv"),
          addToStation(ctx, e.id, "tv"),
          neighbor("Previous", "episode", rel.previous),
          neighbor("Next", "episode", rel.next),
          tg.actions,
        ),
        tg.note,
        synopsis(e),
      ),
    ),
    h(
      "section",
      { "aria-label": "Details" },
      h("h2.sec", "Details"),
      kv([
        ["Audio", a],
        ["Subtitles", subs],
        ["Video", await videoLine(ctx, e.id, f)],
      ]),
    ),
    upNext.length
      ? h(
          "section",
          { "aria-label": "Up next" },
          h("h2.sec", "Up next"),
          h("div.eps", ...upNext.map((x) => episodeRow(x, ws))),
        )
      : null,
  ].filter((e): e is HTMLElement => !!e);
}

const day = (iso: string) =>
  new Date(iso).toLocaleDateString(undefined, { dateStyle: "medium", timeZone: "UTC" });

export async function video(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const videoId = id;
  const [v, fs] = await Promise.all([
    ok(ctx.api.GET("/youtube/videos/{videoId}", { params: { path: { videoId } } })),
    files(ctx, videoId).catch(() => [] as File[]),
  ]);
  const more = (
    await ok(
      ctx.api.GET("/youtube/videos", {
        params: { query: { channelId: v.channelId, sort: "newest", limit: 13 } },
      }),
    )
  ).items
    .filter((x) => x.id !== v.id)
    .slice(0, 12);
  const f = fs[0];
  const subs = subtitles(f?.tracks ?? []);
  const resumed = v.positionMs > 0;
  const rt = v.durationMs ? Math.round(v.durationMs / 60000) : undefined;
  const play = h(
    "a.btn.p",
    { href: `#/play/${v.id}` },
    icon("play", 16),
    resumed ? "Resume" : "Play",
  );
  const tg = await watchTogether(ctx, { id: v.id, title: v.title }, play);
  return [
    back(v.channelTitle, `#/channel/${v.channelId}`),
    h(
      "div.detail",
      withWatched(
        h("div.art.v480", {
          "aria-hidden": "true",
          style: `background:var(--c${tint(v.id, 10)})`,
          "data-art": artOf(v.posterUrl),
          "data-ph": "video",
        }),
        resumeFraction(v, v.durationMs),
      ),
      h(
        "div.info",
        h(
          "a.chcrumb",
          { href: `#/channel/${v.channelId}` },
          h("div.art", {
            "aria-hidden": "true",
            style: `background:var(--c${tint(v.channelId, 10)})`,
            "data-art": artOf(v.channelPosterUrl),
            "data-ph": "channel",
          }),
          v.channelTitle,
        ),
        h("h1.big", v.title),
        meta(
          runtime(rt),
          v.publishedOn ? day(v.publishedOn) : undefined,
          v.height ? h("span.tag", resolutionLabel((v.height * 16) / 9, v.height)) : false,
          !!rt && resumed && h("span.amber", left(rt, v.positionMs)),
        ),
        h(
          "div.actions",
          play,
          btn(
            v.watched ? "Mark unwatched" : "Mark watched",
            "check",
            "s",
            async () => (await put(ctx, v.id, !v.watched), refresh()),
          ),
          resumed && btn("Dismiss", "x", "s", async () => (await dismiss(ctx, v.id), refresh())),
          tg.actions,
        ),
        tg.note,
        v.overview ? h("p.synopsis", v.overview) : null,
      ),
    ),
    h(
      "section",
      { "aria-label": "Details" },
      h("h2.sec", "Details"),
      kv([
        ["Chapters", f?.chapters.length ? String(f.chapters.length) : undefined],
        ["Subtitles", subs],
        ["Video", await videoLine(ctx, v.id, f)],
        [
          "Added",
          new Date(v.addedAt).toLocaleString(undefined, {
            dateStyle: "medium",
            timeStyle: "short",
          }),
        ],
        [
          "Source",
          /^https?:\/\//i.test(v.sourceUrl ?? "")
            ? h("a", { href: v.sourceUrl, target: "_blank", rel: "noopener" }, "Open on YouTube")
            : undefined,
        ],
      ]),
    ),
    more.length
      ? h(
          "section",
          { "aria-label": `More from ${v.channelTitle}` },
          h("h2.sec", `More from ${v.channelTitle}`),
          h("div.strip", ...more.map((x) => videoTile(x, false))),
        )
      : null,
  ].filter((e): e is HTMLElement => !!e);
}
