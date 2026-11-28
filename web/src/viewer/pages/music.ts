import { h } from "../../admin/dom";
import { ok, paged } from "../../admin/api";
import type { Schemas } from "../../api/client";
import { albumArt, music as engine, type Music } from "../audio";
import { color, type Ctx } from "../data";
import { audioDetails, clock, minutes, plural } from "../format";
import type { Song } from "../queue";
import { caption, programTitle } from "../stations";
import { back, btn, empty, icon, logo, meta, sel, viewTabs } from "../ui";
import { addToPlaylist, addToStation, hasPlaylists, playlistTiles } from "./playlists";
import { stationTiles } from "./stations";
import { selection } from "../select";
import { artOf } from "../artwork";

type Album = Schemas["MusicAlbum"];
type Artist = Schemas["MusicArtist"];

const time = (ms?: number) => (ms ? clock(ms / 1000) : "");

export const albumTile = (a: Album, caption: string) =>
  h(
    "a.poster",
    { href: `#/album/${a.id}`, "data-id": a.id },
    albumArt(a.id, "sq"),
    h("b", a.title),
    h("span.sub", caption),
  );
export const artistTile = (a: Artist) =>
  h(
    "a.poster",
    { href: `#/artist/${a.id}`, "data-id": a.id },
    h("div.art.sq.round", {
      "aria-hidden": "true",
      style: `background:${color(a.id)}`,
      "data-art": artOf(a.posterUrl),
      "data-ph": "artist",
    }),
    h("b", a.name),
    h("span.sub", plural(a.albumCount, "album")),
  );

interface Row {
  n?: string;
  art?: HTMLElement;
  title: string;
  sub?: string;
  ms?: number;
  id?: string;
  on: () => void;
  remove?: () => void;
}
/** One song-like row: a button that plays or opens it, and an optional remove. */
export const row = (r: Row) =>
  h(
    "li.qrow",
    { "data-id": r.id },
    h(
      "button.tr",
      { type: "button", onclick: r.on },
      r.n !== undefined ? h("span.n", r.n) : null,
      r.art,
      h("b", r.title, r.sub ? h("span.sub", r.sub) : null),
      h("span.t", time(r.ms)),
    ),
    r.remove
      ? h(
          "button.pb",
          { type: "button", "aria-label": `Remove ${r.title}`, title: "Remove", onclick: r.remove },
          icon("x", 16),
        )
      : null,
  );

/** Rows that move by drag, or by Alt+Up and Alt+Down; move gets indexes into rows. */
export function reorder(rows: HTMLElement[], move: (from: number, to: number) => void) {
  let from = -1;
  rows.forEach((r, i) => {
    r.draggable = true;
    r.querySelector(".tr")?.setAttribute("aria-keyshortcuts", "Alt+ArrowUp Alt+ArrowDown");
    r.addEventListener("dragstart", (e) => {
      from = i;
      // Firefox starts a drag only with data set.
      (e as DragEvent).dataTransfer?.setData("text/plain", "");
    });
    r.addEventListener("dragover", (e) => (e.preventDefault(), r.classList.add("over")));
    r.addEventListener("dragleave", () => r.classList.remove("over"));
    r.addEventListener("drop", (e) => {
      e.preventDefault();
      r.classList.remove("over");
      if (from >= 0 && from !== i) move(from, i);
      from = -1;
    });
    r.addEventListener("keydown", (e) => {
      const to = !e.altKey ? -1 : e.key === "ArrowUp" ? i - 1 : e.key === "ArrowDown" ? i + 1 : -1;
      if (to < 0 || to >= rows.length) return;
      e.preventDefault();
      move(i, to);
    });
  });
}

/** Re-runs paint on every engine change while this route is shown. */
function live(paint: (m: Music) => void) {
  const m = engine();
  if (!m) return;
  const off = m.on(() => paint(m));
  window.addEventListener("hashchange", off, { once: true });
  paint(m);
}
const markPlaying = (list: HTMLElement) =>
  live((m) =>
    list.querySelectorAll<HTMLElement>("li[data-id]").forEach((li) => {
      li.setAttribute("aria-current", String(li.dataset.id === m.q.current?.id));
    }),
  );

export const play = (songs: Song[], i: number, from: string, href: string, shuffle = false) =>
  void engine()?.play(songs, i, from, href, { shuffle });
const playAll = (
  songs: () => Promise<Song[]>,
  from: string,
  href: string,
  ...extra: (HTMLElement | null)[]
) =>
  h(
    "div.actions",
    btn("Play", "play", "p", async () => play(await songs(), 0, from, href)),
    btn("Shuffle", "shuffle", "s", async () => {
      const all = await songs();
      play(all, Math.floor(Math.random() * all.length), from, href, true);
    }),
    ...extra,
  );

const albumsOf = (ctx: Ctx, query: { artistId?: string; sort?: Schemas["MusicAlbumSort"] }) =>
  paged((offset) =>
    ok(ctx.api.GET("/music/albums", { params: { query: { ...query, limit: 200, offset } } })),
  );
const songsOf = (
  ctx: Ctx,
  query: { albumId?: string; artistId?: string; sort?: Schemas["MusicSongSort"] },
) =>
  paged((offset) =>
    ok(ctx.api.GET("/music/songs", { params: { query: { ...query, limit: 200, offset } } })),
  );
const theless = (s: string) => s.replace(/^the\s+/i, "");

const VIEWS = {
  albums: "Albums",
  artists: "Artists",
  songs: "Songs",
  playlists: "Playlists",
  stations: "Stations",
};
type View = keyof typeof VIEWS;
const SORTS: Record<string, (a: Album, b: Album) => number> = {
  "Recently added": (a, b) => b.addedAt.localeCompare(a.addedAt),
  Title: (a, b) => theless(a.title).localeCompare(theless(b.title)),
  Artist: (a, b) =>
    theless(a.artistName).localeCompare(theless(b.artistName)) || (a.year ?? 0) - (b.year ?? 0),
  Year: (a, b) => (b.year ?? 0) - (a.year ?? 0),
};

export async function music(ctx: Ctx, _a: string[], query: URLSearchParams) {
  const shown = await hasPlaylists(ctx, "music");
  const asked = (query.get("view") ?? "albums") as View;
  const view = !shown && asked === "playlists" ? "albums" : asked;
  const views = viewTabs(
    Object.entries(VIEWS).map(([v, label]) =>
      !shown && v === "playlists"
        ? false
        : [label, `#/music${v === "albums" ? "" : `?view=${v}`}`, v === view],
    ),
  );
  const count = h("span.sub");
  const head = h("div.titlerow", h("h1", "Music"), count);
  const filters = h("div.filters", h("span.grow"));
  const body = h("div");

  if (view === "playlists") {
    const { tiles, create } = await playlistTiles(ctx, "music");
    if (create) filters.append(create);
    count.textContent = plural(tiles.length, "playlist");
    body.replaceChildren(
      tiles.length ? h("div.grid.sq", ...tiles) : h("p.sub", "No playlists yet."),
    );
  } else if (view === "stations") {
    const { count: n, grid, create } = await stationTiles(ctx, "music");
    if (create) filters.append(create);
    count.textContent = n;
    body.replaceChildren(grid);
  } else if (view === "artists") {
    const list = (
      await paged((offset) =>
        ok(ctx.api.GET("/music/artists", { params: { query: { limit: 200, offset } } })),
      )
    ).sort((a, b) => a.sortName.localeCompare(b.sortName));
    count.textContent = plural(list.length, "artist");
    body.replaceChildren(list.length ? h("div.grid.sq", ...list.map(artistTile)) : empty(ctx));
  } else if (view === "songs") {
    // shortcut: every song loads at once, as albums do; page it when libraries outgrow that.
    const list = await songsOf(ctx, { sort: "title" });
    count.textContent = plural(list.length, "song");
    const ol = h(
      "ol.tracks",
      ...list.map((s, i) =>
        row({
          id: s.id,
          title: s.title,
          sub: `${s.artistName} · ${s.albumTitle}`,
          ms: s.durationMs,
          on: () => play(list, i, "Songs", "#/music?view=songs"),
        }),
      ),
    );
    markPlaying(ol);
    body.replaceChildren(list.length ? ol : empty(ctx));
  } else {
    const list = await albumsOf(ctx, { sort: "added" });
    const ANY = "Any";
    const genres = [...new Set(list.flatMap((a) => a.genres.map((g) => g.name)))].sort();
    let sort = Object.keys(SORTS).includes(query.get("sort")!)
      ? query.get("sort")!
      : "Recently added";
    let genre = genres.includes(query.get("genre")!) ? query.get("genre")! : ANY;
    // The picks ride in the hash without a history entry, so a redraw or a return to the page keeps them.
    const remember = () => {
      if (!body.isConnected) return;
      const [path, q] = location.hash.split("?");
      const p = new URLSearchParams(q);
      if (sort === "Recently added") p.delete("sort");
      else p.set("sort", sort);
      if (genre === ANY) p.delete("genre");
      else p.set("genre", genre);
      history.replaceState(null, "", p.size ? `${path}?${p}` : path);
    };
    const draw = () => {
      const shown = list
        .filter((a) => genre === ANY || a.genres.some((g) => g.name === genre))
        .sort(SORTS[sort]);
      count.textContent = plural(shown.length, "album");
      body.replaceChildren(
        list.length
          ? h("div.grid.sq", ...shown.map((a) => albumTile(a, a.artistName)))
          : empty(ctx),
      );
    };
    filters.append(
      sel("Sort", Object.keys(SORTS), sort, (v) => ((sort = v), remember(), draw())),
      genres.length
        ? sel("Genre", [ANY, ...genres], genre, (v) => ((genre = v), remember(), draw()))
        : "",
    );
    draw();
  }
  const kind = ({ artists: "artist", songs: "song", albums: "album" } as const)[
    view as "artists" | "songs" | "albums"
  ];
  const pick = kind && ctx.profile.kind !== "kid" ? selection(ctx, kind, body) : undefined;
  if (pick) filters.append(pick.toggle);
  return [
    head,
    ...views,
    filters,
    h("section", { "aria-label": VIEWS[view] ?? "Albums" }, body),
    ...(pick ? [pick.bar] : []),
  ];
}

export async function artist(ctx: Ctx, [id]: string[]) {
  const artistId = id;
  const [a, popular, albums] = await Promise.all([
    ok(ctx.api.GET("/music/artists/{artistId}", { params: { path: { artistId } } })),
    ok(
      ctx.api.GET("/music/songs", {
        params: { query: { artistId, sort: "popular", limit: 5 } },
      }),
    ).then((p) => p.items),
    albumsOf(ctx, { artistId, sort: "year" }),
  ]);
  const href = `#/artist/${a.id}`;
  const top = h(
    "ol.tracks",
    ...popular.map((s, i) =>
      row({
        id: s.id,
        n: String(i + 1),
        title: s.title,
        ms: s.durationMs,
        on: () => play(popular, i, a.name, href),
      }),
    ),
  );
  markPlaying(top);
  return [
    back("Artists", "#/music?view=artists"),
    h(
      "div.detail",
      h("div.art.face200", {
        "aria-hidden": "true",
        style: `background:${color(a.id)}`,
        "data-art": artOf(a.posterUrl),
        "data-ph": "artist",
      }),
      h(
        "div.info",
        h("h1.big", a.name),
        meta(
          plural(a.albumCount, "album"),
          plural(a.songCount, "song"),
          a.genres.map((g) => g.name).join(", "),
        ),
        playAll(
          () => songsOf(ctx, { artistId, sort: "album" }),
          a.name,
          href,
          addToPlaylist(ctx, a.id, "music"),
          addToStation(ctx, a.id, "music"),
        ),
        a.overview ? h("p.synopsis", a.overview) : null,
      ),
    ),
    popular.length ? h("section", { "aria-label": "Popular" }, h("h2.sec", "Popular"), top) : null,
    albums.length
      ? h(
          "section",
          { "aria-label": "Albums" },
          h("h2.sec", "Albums"),
          h("div.strip.sq", ...albums.map((al) => albumTile(al, al.year ? String(al.year) : ""))),
        )
      : null,
  ].filter((e): e is HTMLElement => !!e);
}

export async function album(ctx: Ctx, [id]: string[]) {
  const albumId = id;
  const al = await ok(ctx.api.GET("/music/albums/{albumId}", { params: { path: { albumId } } }));
  const [songs, more] = await Promise.all([
    songsOf(ctx, { albumId }),
    albumsOf(ctx, { artistId: al.artistId, sort: "year" }),
  ]);
  const href = `#/album/${al.id}`;
  const list = h(
    "ol.tracks",
    ...songs.map((s, i) =>
      row({
        id: s.id,
        n: String(s.trackNumber ?? i + 1),
        title: s.title,
        // A guest performer is named; the album artist is already in the header.
        sub: s.artistId !== al.artistId ? s.artistName : undefined,
        ms: s.durationMs,
        on: () => play(songs, i, al.title, href),
      }),
    ),
  );
  markPlaying(list);
  const others = more.filter((m) => m.id !== al.id);
  return [
    back(al.artistName, `#/artist/${al.artistId}`),
    h(
      "div.detail",
      albumArt(al.id, "sq240"),
      h(
        "div.info",
        h("h1.big", al.title),
        meta(
          h("a.amber", { href: `#/artist/${al.artistId}` }, h("b", al.artistName)),
          al.year ? String(al.year) : undefined,
          plural(al.songCount, "song"),
          minutes(al.durationMs),
          al.lossless && h("span.tag", "Lossless"),
        ),
        playAll(
          async () => songs,
          al.title,
          href,
          addToPlaylist(ctx, al.id, "music"),
          addToStation(ctx, al.id, "music"),
        ),
        al.overview ? h("p.synopsis", al.overview) : null,
      ),
    ),
    h("section", { "aria-label": "Songs" }, list),
    others.length
      ? h(
          "section",
          { "aria-label": `More by ${al.artistName}` },
          h("h2.sec", `More by ${al.artistName}`),
          h("div.strip.sq", ...others.map((o) => albumTile(o, o.year ? String(o.year) : ""))),
        )
      : null,
  ].filter((e): e is HTMLElement => !!e);
}

/** Title in a two-line slot, artist and album one line each, so nothing below moves between songs. */
const songText = (s: Pick<Song, "title" | "artistId" | "artistName" | "albumId" | "albumTitle">) =>
  h(
    "div.np-text",
    h("h1", s.title),
    h("a.amber", { href: `#/artist/${s.artistId}` }, s.artistName),
    h("a.amber", { href: `#/album/${s.albumId}` }, s.albumTitle),
  );

const detailsPanel = (a: Schemas["AudioDetails"]) =>
  h(
    "section.np-details",
    { "aria-label": "Audio details" },
    ...audioDetails(a).map((g) =>
      h(
        "div",
        h("h2", g.title),
        h(
          "dl",
          ...g.rows.map((r) =>
            h(
              "div.dr",
              h("dt", r.label),
              h(
                "dd",
                r.lossless && h("span.tag", "Lossless"),
                r.tone ? h(`b.${r.tone}`, r.value) : r.value,
              ),
            ),
          ),
        ),
      ),
    ),
  );

/** The art, or the file details in its place; a click anywhere on it, or its info button, flips it. */
function artSquare(
  albumId: string,
  audio: Schemas["AudioDetails"] | undefined,
  flip: { open: boolean },
) {
  const art = albumArt(albumId, "sq360");
  if (!audio) return h("div.np-art", art);
  const panel = detailsPanel(audio);
  const info = h(
    "button.np-info",
    { type: "button", "aria-label": "File details" },
    icon("info", 16),
  );
  const box = h("div.np-art.flip", { onclick: () => ((flip.open = !flip.open), paint()) }, info);
  let face: HTMLElement | undefined;
  const paint = () => {
    face?.remove();
    face = flip.open ? panel : art;
    box.prepend(face);
    info.setAttribute("aria-pressed", String(flip.open));
  };
  paint();
  return box;
}

/** Now playing, opened from the mini player: the song and the Up next queue. */
export async function nowPlaying(ctx: Ctx) {
  const m = engine();
  if (!m?.q.current && !m?.station)
    return [h("p.sub", "Nothing is playing. "), h("a", { href: "#/music" }, "Open Music")];
  const left = h("section", { "aria-label": "Now playing" });
  const right = h("section", { "aria-label": "Up next" });
  const backEl = h("span");
  // The song whose Add to playlist button is drawn, so a queue change keeps it.
  let shownId: string | undefined;
  let focusAt = -1;
  // Whether the art shows the file details; a visit to the page starts on the art.
  const flip = { open: false };
  live((m) => {
    if (m.station) {
      shownId = undefined;
      return station(m, backEl, left, right);
    }
    const s = m.q.current;
    if (!s) return ctx.go("#/music");
    backEl.replaceChildren(back(m.q.from, m.q.fromHref));
    if (s.id !== shownId || !m.pb) {
      shownId = m.pb ? s.id : undefined;
      left.replaceChildren(
        artSquare(s.albumId, m.pb?.stream.audio, flip),
        songText(s),
        h(
          "div.actions",
          toggle("Shuffle", "shuffle", m.q.shuffle, () => m.q.setShuffle(!m.q.shuffle)),
          toggle("Repeat", "repeat", m.q.repeat, () => (m.q.repeat = !m.q.repeat)),
          addToPlaylist(ctx, s.id, "music"),
          addToStation(ctx, s.id, "music"),
        ),
        m.error ? h("p.err", { role: "alert" }, m.error) : "",
      );
    } else
      left.querySelectorAll<HTMLElement>("[aria-pressed]").forEach((b) => {
        b.setAttribute(
          "aria-pressed",
          String(b.textContent === "Shuffle" ? m.q.shuffle : m.q.repeat),
        );
      });
    const up = m.q.upNext.map((song, k) => ({ song, i: m.q.index + 1 + k }));
    const rows = up.map(({ song, i }) =>
      row({
        art: albumArt(song.albumId, "sq36"),
        title: song.title,
        sub: song.artistName,
        ms: song.durationMs,
        on: () => void m.jump(i),
        remove: () => (m.q.remove(i), m.changed()),
      }),
    );
    reorder(rows, (from, to) => {
      m.q.move(up[from].i, up[to].i);
      focusAt = to;
      m.changed();
    });
    const last = up.at(-1)?.song;
    right.replaceChildren(
      h(
        "div.qhead",
        h("h2", "Up next"),
        h(
          "span.sub",
          [`Playing from ${m.q.from}`, plural(up.length, "song"), minutes(m.q.upNextMs())]
            .filter(Boolean)
            .join(" · "),
        ),
        up.length
          ? h("button.link", { type: "button", onclick: () => (m.q.clear(), m.changed()) }, "Clear")
          : null,
      ),
      up.length ? h("ol.tracks", ...rows) : h("p.sub", "Nothing up next."),
      last
        ? h(
            "p.sub",
            `Drag a song to reorder. The queue ${m.q.repeat ? "starts over" : "ends"} after ${last.title}.`,
          )
        : "",
    );
    if (focusAt >= 0) rows[focusAt]?.querySelector<HTMLElement>(".tr")?.focus();
    focusAt = -1;
  });
  return [backEl, h("div.np-page", left, right)];
}

/** Now playing on a music station: one Stop, and Up next read only, from what the server says follows. */
function station(m: Music, backEl: HTMLElement, left: HTMLElement, right: HTMLElement) {
  const { s: st, onAir } = m.station!;
  const s = onAir.now?.song;
  const songs = onAir.upNext.filter((p) => p.song);
  backEl.replaceChildren(back("Stations", "#/music?view=stations"));
  left.replaceChildren(
    s ? albumArt(s.albumId, "sq360") : h("div.art.sq360.ident", logo(64), h("b", st.name)),
    s ? songText(s) : h("div", h("h1", st.name), h("p.sub", caption({ kind: "music", onAir })[0])),
    m.error ? h("p.err", { role: "alert" }, m.error) : "",
    h(
      "div.actions",
      btn("Stop", "stop", "s", () => m.end()),
    ),
  );
  right.replaceChildren(
    h("div.qhead", h("h2", "Up next"), h("span.sub", `On ${st.name}`)),
    songs.length
      ? h(
          "ol.tracks",
          ...songs.map((p) =>
            h(
              "li.qrow",
              h(
                "div.tr",
                albumArt(p.song!.albumId, "sq36"),
                h("b", programTitle(p), h("span.sub", p.song!.artistName)),
                h("span.t", time(p.durationMs)),
              ),
            ),
          ),
        )
      : h("p.sub", "Nothing up next."),
    h("p.sub", "The station sets the order. Songs cannot be skipped, reordered or removed."),
  );
}

const toggle = (label: string, ic: string, on: boolean, flip: () => void) => {
  const b = btn(label, ic, "s", () => {
    flip();
    engine()?.changed();
  });
  b.setAttribute("aria-pressed", String(on));
  return b;
};
