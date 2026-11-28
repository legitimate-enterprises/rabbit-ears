// The YouTube section: Videos and Channels behind view tabs, and a
// Channel page. A kid sees the channels the server lets them.
import "../youtube.css";
import { artOf } from "../artwork";
import { errorLine, h } from "../../admin/dom";
import { saveSwitch } from "../../admin/common";
import { ok, paged } from "../../admin/api";
import type { Schemas } from "../../api/client";
import { color, resumeFraction, type Ctx } from "../data";
import { minutes, plural, since } from "../format";
import {
  allowedRows,
  back,
  chip,
  empty,
  icon,
  menu,
  refresh,
  sel,
  viewTabs,
  withWatched,
} from "../ui";
import { selection } from "../select";

type Video = Schemas["YouTubeVideo"];
type Channel = Schemas["YouTubeChannel"];
type Sort = Schemas["YouTubeVideoSort"];
type VideoQuery = {
  channelId?: string;
  unwatched?: boolean;
  sort?: Sort;
};

const PAGE = 60;
const SORTS: Record<string, Sort> = {
  Newest: "newest",
  Oldest: "oldest",
  Title: "title",
  Channel: "channel",
  Length: "length",
};
const sortIn = (q: URLSearchParams) =>
  (Object.values(SORTS).includes(q.get("sort") as Sort) ? q.get("sort") : "newest") as Sort;
const sortName = (s: Sort) => Object.keys(SORTS).find((k) => SORTS[k] === s) ?? "Newest";

const art = (id: string, cls: string) =>
  h(`div.art.${cls}`, {
    "aria-hidden": "true",
    style: `background:${color(id)}`,
    "data-art": `${id}/poster`,
    "data-ph": "channel",
  });

/** A video's thumbnail with the unwatched dot and the progress of one in progress. */
export const videoTile = (v: Video, withChannel: boolean) =>
  h(
    "a.poster.vid",
    { href: `#/video/${v.id}`, "data-id": v.id },
    withWatched(
      h(
        "div.art.v",
        {
          "aria-hidden": "true",
          style: `background:${color(v.id)}`,
          "data-art": artOf(v.posterUrl),
          "data-ph": "video",
        },
        v.watched ? null : h("span.dot", { role: "img", "aria-label": "Unwatched" }),
      ),
      resumeFraction(v, v.durationMs),
    ),
    h("b", v.title),
    h(
      "span.sub",
      [
        withChannel ? v.channelTitle : "",
        minutes(v.durationMs),
        v.publishedOn ? since(v.publishedOn) : "",
      ]
        .filter(Boolean)
        .join(" · "),
    ),
  );

/** A grid of videos that reads a page at a time; Show more reads the next. A redraw reads on until the `shown` videos it was showing are back. */
async function videoGrid(ctx: Ctx, query: VideoQuery, withChannel: boolean, shown = 0) {
  const grid = h("div.grid.vids");
  const more = h(
    "button.btn.s.more",
    { type: "button", hidden: true },
    "Show more",
  ) as HTMLButtonElement;
  let offset: number | undefined = 0;
  // Disabled while a page is read, so a second click cannot ask for the same offset.
  const load = async () => {
    more.disabled = true;
    try {
      const p = await ok(
        ctx.api.GET("/youtube/videos", { params: { query: { ...query, limit: PAGE, offset } } }),
      );
      grid.append(...p.items.map((v) => videoTile(v, withChannel)));
      offset = p.nextOffset;
      more.hidden = offset === undefined;
      if (!grid.children.length) grid.replaceChildren(h("p.sub", "No videos match."));
    } finally {
      more.disabled = false;
    }
  };
  // The count rides in the hash without a history entry, so Back leaves the page as it would have.
  const keep = () => {
    if (!grid.isConnected) return;
    const [path, q] = location.hash.split("?");
    const p = new URLSearchParams(q);
    p.set("shown", String(grid.children.length));
    history.replaceState(null, "", `${path}?${p}`);
  };
  more.addEventListener(
    "click",
    () =>
      void load()
        .then(keep)
        .catch(() => {}),
  );
  await load();
  while (offset !== undefined && grid.children.length < shown) await load();
  return h("div", grid, more);
}

const allChannels = (ctx: Ctx) =>
  paged((offset) =>
    ok(
      ctx.api.GET("/youtube/channels", {
        params: { query: { sort: "title", limit: 200, offset } },
      }),
    ),
  );

export const channelTile = (c: Channel) =>
  h(
    "a.poster",
    { href: `#/channel/${c.id}`, "data-id": c.id },
    art(c.id, "sq.round"),
    h("b", c.title),
    h(
      "span.sub",
      `${plural(c.videoCount, "video")}${c.unwatchedCount ? ` · ${c.unwatchedCount} new` : ""}`,
    ),
  );

const VIEWS = { videos: "Videos", channels: "Channels" };
type View = keyof typeof VIEWS;

/** The YouTube section. Videos filter and sort on the server; Unwatched and Channel ride in the hash. */
export async function youtube(ctx: Ctx, _a: string[], query: URLSearchParams) {
  const view = (VIEWS[query.get("view") as View] ? query.get("view") : "videos") as View;
  const unwatched = query.get("unwatched") === "1";
  const channelId = query.get("channel") || undefined;
  const sort = sortIn(query);
  const href = (p: { view?: View; unwatched?: boolean; channel?: string; sort?: Sort }) => {
    const q = new URLSearchParams();
    const v = p.view ?? view;
    const u = p.unwatched ?? unwatched;
    const c = "channel" in p ? p.channel : channelId;
    const s = p.sort ?? sort;
    if (v !== "videos") q.set("view", v);
    if (u) q.set("unwatched", "1");
    if (c) q.set("channel", c);
    if (s !== "newest") q.set("sort", s);
    return `#/youtube${q.size ? `?${q}` : ""}`;
  };
  const to = (p: Parameters<typeof href>[0]) => ctx.go(href(p));

  const channels = await allChannels(ctx);
  const head = h(
    "div.titlerow",
    h("h1", "YouTube"),
    h("span.sub", plural(channels.length, "channel")),
  );
  const views = viewTabs(
    Object.entries(VIEWS).map(([v, label]) => [label, href({ view: v as View }), v === view]),
  );
  const filters = h(
    "div.filters",
    h(
      "div.chips",
      { role: "group", "aria-label": "Filter" },
      chip("Unwatched", unwatched, () => to({ unwatched: !unwatched })),
    ),
    h("span.grow"),
  );
  let body: HTMLElement;

  if (!channels.length) {
    body = empty(ctx);
  } else if (view === "channels") {
    const list = channels.filter((c) => !unwatched || c.unwatchedCount > 0);
    body = list.length
      ? h("div.grid.sq", ...list.map(channelTile))
      : h("p.sub", "No channels match.");
  } else {
    filters.append(
      sel("Sort", Object.keys(SORTS), sortName(sort), (v) => to({ sort: SORTS[v] })),
      h(
        "label.sel",
        "Channel",
        h(
          "select.in",
          {
            onchange: (e: Event) =>
              to({ channel: (e.target as HTMLSelectElement).value || undefined }),
          },
          h("option", { value: "" }, "Any"),
          ...channels.map((c) =>
            h("option", { value: c.id, selected: c.id === channelId }, c.title),
          ),
        ),
      ),
    );
    body = await videoGrid(
      ctx,
      { channelId, unwatched: unwatched || undefined, sort },
      true,
      Number(query.get("shown")),
    );
  }
  if (!channels.length) return [head, ...views, filters, body];
  const pick = selection(ctx, view === "channels" ? "channel" : "video", body);
  filters.append(pick.toggle);
  return [head, ...views, filters, body, pick.bar];
}

export async function channel(ctx: Ctx, [id]: string[], query: URLSearchParams) {
  const channelId = id;
  const c = await ok(
    ctx.api.GET("/youtube/channels/{channelId}", { params: { path: { channelId } } }),
  );
  const done = c.unwatchedCount === 0;
  const adult = ctx.profile.kind === "adult";
  const forKidsErr = errorLine();
  const forKids = saveSwitch("For kids", !!c.forKids, forKidsErr, (forKids) =>
    ok(
      ctx.api.PUT("/youtube/channels/{channelId}/for-kids", {
        params: { path: { channelId } },
        body: { forKids },
      }),
    ),
  );
  const allowed = await allowedRows(ctx, channelId);
  const holder = h("div");
  const show = async (sort: Sort, shown?: number) =>
    holder.replaceChildren(await videoGrid(ctx, { channelId, sort }, false, shown));
  const sort = sortIn(query);
  await show(sort, Number(query.get("shown")));
  return [
    back("YouTube", "#/youtube"),
    h(
      "div.detail",
      art(c.id, "avatar160"),
      h(
        "div.info",
        h("h1.big", c.title),
        h(
          "div.meta",
          h("span", plural(c.videoCount, "video")),
          h("span", `${c.unwatchedCount} unwatched`),
          c.lastVideoOn ? h("span", `Last video ${since(c.lastVideoOn)}`) : null,
        ),
        h(
          "div.actions",
          h(
            "button.btn.p",
            {
              type: "button",
              onclick: async () => {
                const p = await ok(
                  ctx.api.GET("/youtube/videos", {
                    params: { query: { channelId, sort: "newest", limit: 1 } },
                  }),
                );
                if (p.items[0]) ctx.go(`#/play/${p.items[0].id}`);
              },
            },
            icon("play", 16),
            "Play newest",
          ),
          h(
            "button.btn.s",
            {
              type: "button",
              onclick: async () => {
                await ok(
                  ctx.api.PUT("/profiles/{profileId}/youtube/channels/{channelId}/watch-state", {
                    params: { path: { profileId: ctx.profile.id, channelId } },
                    body: { watched: !done },
                  }),
                );
                refresh();
              },
            },
            icon("check", 16),
            done ? "Mark all unwatched" : "Mark all watched",
          ),
          adult ? h("label.switch", forKids, "For kids") : null,
          allowed.length
            ? menu(
                h("button.btn.s", { type: "button" }, "Allowed for kids"),
                "Allowed for kids",
                allowed,
              )
            : null,
        ),
        forKidsErr,
        c.overview ? h("p.synopsis", c.overview) : null,
      ),
    ),
    h(
      "section",
      { "aria-label": "Videos" },
      h(
        "div.rowhead",
        h("h2", "Videos"),
        h("span.grow"),
        sel("Sort", Object.keys(SORTS), sortName(sort), (v) => {
          // The pick rides in the hash without a history entry, so a redraw keeps it.
          const path = location.hash.split("?")[0];
          history.replaceState(null, "", SORTS[v] === "newest" ? path : `${path}?sort=${SORTS[v]}`);
          void show(SORTS[v]);
        }),
      ),
      holder,
    ),
  ];
}
