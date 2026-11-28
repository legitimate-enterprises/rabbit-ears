// A profile's playlists and those shared with it. Each holds one library kind: movies, tv or
// music; the server expands containers into the play order. Kids own none and only read.
import { dialog, errorLine, field, h, input, showError } from "../../admin/dom";
import { ApiError, ok, paged } from "../../admin/api";
import type { Schemas } from "../../api/client";
import { albumArt, music as engine } from "../audio";
import { imageButtons, imageKey } from "../coverImage";
import { color, epCode, title, type Ctx } from "../data";
import { askEdition, named, needsEdition } from "../editions";
import { minutes, plural } from "../format";
import { artOf, placeholderKind } from "../artwork";
import { avatar, back, formEnd, guard, icon, refresh, smartBadge } from "../ui";
import { createSmart, rulesDialog } from "../rules";
import { reorder, row } from "./music";

type Kind = Schemas["PlaylistKind"];
type Playlist = Schemas["Playlist"];
type Entry = Schemas["PlaylistEntry"];
type Row = Schemas["PlaylistPlayOrderItem"];

const NOUN: Record<Kind, string> = { movies: "movie", tv: "episode", music: "song" };
const listHref = (k: Kind) => `#/${k}?view=playlists`;
export const count = (p: Playlist) => plural(p.itemCount, NOUN[p.kind]);
/**
 * Whether the profile gets a Playlists view of kind: an adult always, a kid once a playlist of
 * kind is shared with them. A failed check shows none.
 */
export const hasPlaylists = (ctx: Ctx, kind: Kind) =>
  ctx.profile.kind === "adult"
    ? Promise.resolve(true)
    : ok(
        ctx.api.GET("/profiles/{profileId}/playlists", {
          params: { path: path(ctx), query: { kind, limit: 1 } },
        }),
      ).then(
        (p) => p.items.length > 0,
        () => false,
      );
const sharedBy = (p: Playlist) => (p.ownerName ? `Shared by ${p.ownerName}` : "");
/** Whether the profile adds, removes and reorders the playlist's entries; a smart one's follow its rules. */
export const editable = (ctx: Ctx, p: Playlist) =>
  ctx.profile.kind === "adult" && p.access !== "read" && !p.smart;
// A shared playlist's artwork carries its Shared badge, so only then is it exposed.
const cover = (p: Playlist, cls: string) =>
  h(
    `div.art.${cls}`,
    {
      "aria-hidden": p.shared ? undefined : "true",
      style: `background:${color(p.id)}`,
      "data-art": p.imageUrl ? imageKey(p.imageUrl) : artOf(p.posterUrl),
      "data-ph": "playlist",
    },
    p.shared &&
      h("div.marks", h("span", { role: "img", "aria-label": "Shared" }, icon("users", 14))),
  );

const path = (ctx: Ctx) => ({ profileId: ctx.profile.id });
const list = (ctx: Ctx, kind: Kind) =>
  paged((offset) =>
    ok(
      ctx.api.GET("/profiles/{profileId}/playlists", {
        params: { path: path(ctx), query: { kind, limit: 200, offset } },
      }),
    ),
  );
const create = (ctx: Ctx, name: string, kind: Kind) =>
  ok(
    ctx.api.POST("/profiles/{profileId}/playlists", {
      params: { path: path(ctx) },
      body: { name, kind },
    }),
  );

/** A dialog around one name field; submit runs act, errors stay in the dialog. */
export function nameDialog(
  heading: string,
  go: string,
  value: string,
  act: (name: string) => Promise<unknown>,
) {
  const e = errorLine();
  const name = input("text", { required: true, maxLength: 100, value });
  const d = dialog(
    heading,
    h(
      "form",
      {
        onsubmit: (ev: Event) => {
          ev.preventDefault();
          act(name.value.trim()).then(d.close, (x: Error) => showError(e, x.message));
        },
      },
      field("Name", name),
      e,
      formEnd(() => d.close(), go),
    ),
  );
  name.focus();
}

/** The profile's playlists of one kind as tiles, and for an adult a New playlist button that opens the new one. */
export async function playlistTiles(ctx: Ctx, kind: Kind) {
  const tiles = (await list(ctx, kind)).map((p) =>
    h(
      "a.poster",
      { href: `#/playlist/${p.id}` },
      cover(p, "sq"),
      h("b", p.name),
      h("span.sub", count(p)),
      p.ownerName ? h("span.sub", sharedBy(p)) : null,
    ),
  );
  if (ctx.profile.kind !== "adult") return { tiles, create: null };
  const make = h(
    "button.btn.s",
    {
      type: "button",
      onclick: () =>
        nameDialog("New playlist", "Create", "", async (name) =>
          ctx.go(`#/playlist/${(await create(ctx, name, kind)).id}`),
        ),
    },
    icon("plus", 16),
    "New playlist",
  );
  const smart = h(
    "button.btn.s",
    {
      type: "button",
      onclick: () =>
        rulesDialog({
          heading: "New smart playlist",
          kind,
          name: "",
          kindChoice: "choose",
          save: async (d) => {
            const k = d.kind as Kind;
            const made = await createSmart(
              () => create(ctx, d.name, k),
              (playlistId) =>
                ok(
                  ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}/rules", {
                    params: { path: { ...path(ctx), playlistId } },
                    body: d.rules,
                  }),
                ),
              (playlistId) =>
                ok(
                  ctx.api.DELETE("/profiles/{profileId}/playlists/{playlistId}", {
                    params: { path: { ...path(ctx), playlistId } },
                  }),
                ),
            );
            ctx.go(`#/playlist/${made.id}`);
          },
        }),
    },
    icon("sparkle", 16),
    "New smart playlist",
  );
  const both = document.createDocumentFragment();
  both.append(make, smart);
  return { tiles, create: both };
}

/** Every row of the playlist's play order. */
const playOrder = (ctx: Ctx, playlistId: string) =>
  paged((offset) =>
    ok(
      ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/play-order", {
        params: { path: { ...path(ctx), playlistId }, query: { limit: 200, offset } },
      }),
    ),
  );

/** Row n of the play order, the row playing. */
export const playRows = (ctx: Ctx, playlistId: string, n: number) =>
  ok(
    ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/play-order", {
      params: { path: { ...path(ctx), playlistId }, query: { limit: 1, offset: n } },
    }),
  ).then((p) => p.items);

function describe(e: Entry): { title: string; sub: string; art: HTMLElement; href: string } {
  if (e.collection)
    return {
      title: e.collection.name,
      sub: plural(e.itemCount, "movie"),
      art: h("div.art.sq36", {
        "aria-hidden": "true",
        style: `background:${color(e.collection.id)}`,
        "data-art": artOf(e.collection.posterUrl),
        "data-ph": "collection",
      }),
      href: `#/collection/${e.collection.id}`,
    };
  if (e.song)
    return {
      title: e.song.title,
      sub: `${e.song.artistName} · ${e.song.albumTitle}`,
      art: albumArt(e.song.albumId, "sq36"),
      href: "",
    };
  const art = h("div.art.sq36", {
    "aria-hidden": "true",
    style: `background:${color(e.itemId!)}`,
    "data-art": `${e.itemId}/poster`,
    "data-ph": placeholderKind(e.kind),
  });
  const contains = (noun: string) => plural(e.itemCount, noun);
  if (e.artist)
    return { title: e.artist.name, sub: contains("song"), art, href: `#/artist/${e.itemId}` };
  if (e.album)
    return {
      title: e.album.title,
      sub: `${e.album.artistName} · ${contains("song")}`,
      art,
      href: `#/album/${e.itemId}`,
    };
  const i = e.item;
  const show = e.show ? title(e.show) : "";
  if (e.kind === "show")
    return { title: i ? title(i) : "", sub: contains("episode"), art, href: `#/show/${e.itemId}` };
  if (e.kind === "season")
    return {
      title: `${show} · ${i?.seasonNumber ? `Season ${i.seasonNumber}` : "Specials"}`,
      sub: contains("episode"),
      art,
      href: `#/season/${e.itemId}`,
    };
  return {
    title: named(i ? title(i) : "", e.editionName),
    sub: i?.kind === "episode" ? `${show} · ${epCode(i)}` : String(i?.parsedYear ?? ""),
    art,
    href: `#/${e.kind}/${e.itemId}`,
  };
}

/**
 * A playlist's entries as rows that open, reorder and go; on opens one. Every write renumbers
 * the playlist on the server, so the rows and the play order are read back after it.
 */
export async function entryRows(
  ctx: Ctx,
  playlistId: string,
  on: (e: Entry, href: string, order: Row[]) => void,
  canEdit = true,
  changed?: () => void,
) {
  const p = { params: { path: { ...path(ctx), playlistId } } };
  const entries = () =>
    paged((offset) =>
      ok(
        ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/entries", {
          params: { ...p.params, query: { limit: 200, offset } },
        }),
      ),
    );
  let [es, order] = await Promise.all([entries(), playOrder(ctx, playlistId)]);
  const entryPath = (e: Entry) => ({ params: { path: { ...p.params.path, entryId: e.id } } });
  const ol = h("ol.tracks");
  const body = h("section", { "aria-label": "Entries" });
  const err = errorLine();
  const draw = (focusAt = -1) => {
    const rows = es.map((e) => {
      const d = describe(e);
      return row({
        art: d.art,
        title: d.title,
        sub: d.sub,
        ms: e.durationMs,
        id: e.itemId,
        on: () => on(e, d.href, order),
        remove: canEdit
          ? () =>
              void change(() =>
                ok(
                  ctx.api.DELETE(
                    "/profiles/{profileId}/playlists/{playlistId}/entries/{entryId}",
                    entryPath(e),
                  ),
                ),
              )
          : undefined,
      });
    });
    if (canEdit)
      reorder(rows, (from, to) => {
        const e = es[from];
        void change(
          () =>
            ok(
              ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}/entries/{entryId}", {
                ...entryPath(e),
                // Exactly one of the two is set; JSON leaves the other out.
                body: { itemId: e.itemId, collectionId: e.collectionId, position: to },
              }),
            ),
          to,
        );
      });
    ol.replaceChildren(...rows);
    body.replaceChildren(
      es.length ? ol : h("p.sub", "Nothing here yet."),
      canEdit && es.length > 1 ? h("p.sub", "Drag a row to reorder.") : "",
    );
    if (focusAt >= 0) rows[focusAt]?.querySelector<HTMLElement>(".tr")?.focus();
  };
  const change = async (write: () => Promise<unknown>, focusAt = -1) => {
    try {
      await write();
      [es, order] = await Promise.all([entries(), playOrder(ctx, playlistId)]);
      showError(err);
      draw(focusAt);
      changed?.();
    } catch (x) {
      showError(err, (x as Error).message);
    }
  };
  draw();
  return { body, err, order: () => order };
}

export async function playlist(ctx: Ctx, [id]: string[]) {
  const playlistId = id;
  const p = { params: { path: { ...path(ctx), playlistId } } };
  const pl = await ok(ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}", p));
  const href = `#/playlist/${pl.id}`;
  const songs = () => list.order().flatMap((r) => (r.song ? [r.song] : []));
  const start = (i: number, shuffle = false) =>
    void engine()?.play(songs(), i, pl.name, href, { shuffle, playlistId: pl.id });
  const owner = pl.access === "owner";
  const list = await entryRows(
    ctx,
    playlistId,
    (e, to, order) =>
      pl.kind === "music"
        ? start(
            Math.max(
              0,
              order.findIndex((r) => r.entryId === e.id),
            ),
          )
        : ctx.go(to),
    editable(ctx, pl),
  );
  const order = list.order;
  const playFirst = () =>
    pl.kind === "music"
      ? order().length && start(0)
      : order()[0] && ctx.go(`#/play/${order()[0].itemId}?playlist=${pl.id}&n=0`);
  const playBtn = h(
    "button.btn.p",
    { type: "button", onclick: playFirst },
    icon("play", 16),
    "Play",
  );
  return [
    back("Playlists", listHref(pl.kind)),
    h(
      "div.detail",
      cover(pl, "sq240"),
      h(
        "div.info",
        h("h1.big", pl.name, pl.smart ? smartBadge() : null),
        h(
          "div.meta",
          h("span", count(pl)),
          pl.durationMs ? h("span", minutes(pl.durationMs)) : null,
          pl.ownerName ? h("span", sharedBy(pl)) : null,
        ),
        h(
          "div.actions",
          playBtn,
          pl.kind === "music"
            ? h(
                "button.btn.s",
                {
                  type: "button",
                  onclick: () =>
                    order().length && start(Math.floor(Math.random() * order().length), true),
                },
                icon("shuffle", 16),
                "Shuffle",
              )
            : null,
          owner &&
            h(
              "button.btn.s",
              { type: "button", onclick: () => void shareDialog(ctx, pl) },
              "Share",
            ),
          owner &&
            pl.smart &&
            h(
              "button.btn.s",
              {
                type: "button",
                onclick: guard(async () => {
                  const rules = await ok(
                    ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/rules", p),
                  );
                  rulesDialog({
                    heading: "Edit rules",
                    kind: pl.kind,
                    name: pl.name,
                    kindChoice: "locked",
                    rules,
                    save: async (d) => {
                      await ok(
                        ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}/rules", {
                          ...p,
                          body: d.rules,
                        }),
                      );
                      if (d.name !== pl.name)
                        await ok(
                          ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}", {
                            ...p,
                            body: { name: d.name, kind: pl.kind },
                          }),
                        );
                      refresh();
                    },
                  });
                }),
              },
              "Edit rules…",
            ),
          owner &&
            h(
              "button.btn.s",
              {
                type: "button",
                onclick: () =>
                  nameDialog("Rename playlist", "Save", pl.name, async (name) => {
                    await ok(
                      ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}", {
                        ...p,
                        body: { name, kind: pl.kind },
                      }),
                    );
                    refresh();
                  }),
              },
              "Rename",
            ),
          ...(owner
            ? imageButtons(
                ctx.api,
                { playlist: p.params.path },
                p.params.path,
                pl.imageUrl,
                refresh,
              )
            : []),
          owner &&
            h(
              "button.btn.s",
              {
                type: "button",
                onclick: () =>
                  confirmDelete(
                    `Delete ${pl.name}?`,
                    "The playlist goes; what it lists stays in your library.",
                    () =>
                      ok(ctx.api.DELETE("/profiles/{profileId}/playlists/{playlistId}", p)).then(
                        () => ctx.go(listHref(pl.kind)),
                        (x) => {
                          throw x instanceof ApiError && x.status === 409
                            ? new Error(
                                "A station plays this playlist. Delete the station or give it another playlist first.",
                              )
                            : x;
                        },
                      ),
                  ),
              },
              "Delete",
            ),
        ),
        list.err,
      ),
    ),
    list.body,
  ];
}

/** A confirmation naming what goes; act errors stay in the dialog. */
export function confirmDelete(heading: string, note: string, act: () => Promise<unknown>) {
  const e = errorLine();
  const d = dialog(
    heading,
    h("p.sub", note),
    e,
    h(
      "div.end",
      h("button.btn.s", { type: "button", onclick: () => d.close() }, "Cancel"),
      h(
        "button.btn.p",
        {
          type: "button",
          onclick: () => act().then(d.close, (x: Error) => showError(e, x.message)),
        },
        "Delete",
      ),
    ),
  );
}

/** A button that opens a dialog of choices from list, or a new one made by make, and adds to the pick. Adults only. */
export function addButton<T extends { id: string; name: string }>(
  ctx: Ctx,
  label: string,
  noun: string,
  list: () => Promise<T[]>,
  make: (name: string) => Promise<T>,
  put: (target: T) => Promise<unknown>,
) {
  if (ctx.profile.kind !== "adult") return null;
  const b = h("button.btn.s", { type: "button" }, label);
  const add = async (target: T) => {
    await put(target);
    b.textContent = `Added to ${target.name}`;
  };
  b.addEventListener("click", async () => {
    const e = errorLine();
    const name = input("text", { required: true, maxLength: 100, placeholder: noun });
    const choices = h("div.choices", h("p.sub", "Loading"));
    const d = dialog(
      label,
      choices,
      h(
        "form",
        {
          onsubmit: (ev: Event) => {
            ev.preventDefault();
            make(name.value.trim())
              .then(add)
              .then(d.close, (x: Error) => showError(e, x.message));
          },
        },
        field(noun, name),
        e,
        formEnd(() => d.close(), "Create and add"),
      ),
    );
    try {
      const all = await list();
      choices.replaceChildren(
        ...all.map((c) =>
          h(
            "button.btn.s.wide",
            {
              type: "button",
              onclick: () => add(c).then(d.close, (x: Error) => showError(e, x.message)),
            },
            c.name,
          ),
        ),
      );
      (choices.querySelector("button") as HTMLElement | null)?.focus();
    } catch (x) {
      choices.replaceChildren();
      showError(e, (x as Error).message);
    }
  });
  return b;
}

type What = string | { collectionId: string };

/**
 * Adds a collection as one entry, or a title with the edition edition() names; without one, a
 * title of several editions asks which.
 */
async function addEntry(
  ctx: Ctx,
  playlistId: string,
  what: What,
  edition?: () => string | undefined,
) {
  const add = (editionId?: string) =>
    ok(
      ctx.api.POST("/profiles/{profileId}/playlists/{playlistId}/entries", {
        params: { path: { ...path(ctx), playlistId } },
        body: typeof what === "string" ? { itemId: what, editionId } : what,
      }),
    );
  try {
    return await add(edition?.());
  } catch (x) {
    if (typeof what !== "string" || !needsEdition(x)) throw x;
    const it = await ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId: what } } }));
    const id = await askEdition(it.editions ?? []);
    // An empty message keeps the dialog open without an error line.
    if (id === undefined) throw new Error("", { cause: x });
    return add(id);
  }
}

/** Add to playlist: pick one of the playlists of kind the profile edits, or name a new one; put adds to it. */
const playlistButton = (ctx: Ctx, kind: Kind, put: (playlistId: string) => Promise<unknown>) =>
  addButton(
    ctx,
    "Add to playlist",
    "New playlist",
    async () => (await list(ctx, kind)).filter((p) => p.access !== "read" && !p.smart),
    (name) => create(ctx, name, kind),
    (pl) => put(pl.id),
  );

/** Add to station: pick one of the adult's own stations of kind, or name a new one; put adds to the station's playlist. */
function stationButton(ctx: Ctx, kind: Kind, put: (playlistId: string) => Promise<unknown>) {
  const made = new Set<string>();
  return addButton(
    ctx,
    "Add to station",
    "New station",
    () => ownStations(ctx, kind),
    (name) => createStation(ctx, name, kind).then((st) => (made.add(st.id), st)),
    // A station just made has nothing scheduled to keep.
    (st) => put(st.playlistId).then(() => made.has(st.id) || askReschedule(ctx, st.id)),
  );
}

export const addToPlaylist = (
  ctx: Ctx,
  what: What,
  kind: Kind,
  edition?: () => string | undefined,
) => playlistButton(ctx, kind, (id) => addEntry(ctx, id, what, edition));

export const addToStation = (
  ctx: Ctx,
  what: What,
  kind: Kind,
  edition?: () => string | undefined,
) => stationButton(ctx, kind, (id) => addEntry(ctx, id, what, edition));

const ownStations = async (ctx: Ctx, kind: Kind) =>
  (
    await paged((offset) =>
      ok(ctx.api.GET("/stations", { params: { query: { kind, limit: 200, offset } } })),
    )
  ).filter((s) => s.ownerId === ctx.profile.id && !s.smart);
const createStation = (ctx: Ctx, name: string, kind: Kind) =>
  ok(ctx.api.POST("/stations", { body: { name, kind, order: "playlist" } }));

/** Appends every item in ids in one request; a title of several editions takes its default. */
const addAll = (ctx: Ctx, playlistId: string, ids: string[]) =>
  ok(
    ctx.api.POST("/profiles/{profileId}/playlists/{playlistId}/entries/batch", {
      params: { path: { ...path(ctx), playlistId } },
      body: { entries: ids.map((itemId) => ({ itemId })) },
    }),
  );

export const addAllToPlaylist = (ctx: Ctx, ids: () => string[], kind: Kind) =>
  playlistButton(ctx, kind, (id) => addAll(ctx, id, ids()));

export const addAllToStation = (ctx: Ctx, ids: () => string[], kind: Kind) =>
  stationButton(ctx, kind, (id) => addAll(ctx, id, ids()));

/**
 * Reschedule now? once the owner has saved a station or changed what it plays: Now ends the
 * schedule after what is on, Later keeps it. Either answer runs then.
 */
export function askReschedule(ctx: Ctx, stationId: string, then = () => {}) {
  const e = errorLine();
  const later = () => (d.close(), then());
  const d = dialog(
    "Reschedule now?",
    h(
      "p.sub",
      "Now: what is on finishes, then the new schedule starts. Later: what is scheduled plays out, and the change follows as the schedule is extended.",
    ),
    e,
    h(
      "div.end",
      h("button.btn.s", { type: "button", onclick: later }, "Later"),
      h(
        "button.btn.p",
        {
          type: "button",
          onclick: () =>
            ok(
              ctx.api.POST("/stations/{stationId}/reschedule", { params: { path: { stationId } } }),
            ).then(later, (x: Error) => showError(e, x.message)),
        },
        "Now",
      ),
    ),
  );
}

type Access = Schemas["PlaylistShareAccess"];
const ACCESS: [Access | "", string][] = [
  ["", "Not shared"],
  ["read", "Can view"],
  ["write", "Can edit"],
];

function pick(options: [string, string][], value: string, label: string, on?: (v: string) => void) {
  const s = h(
    "select.in",
    { "aria-label": label, onchange: () => on?.(s.value) },
    ...options.map(([v, l]) => h("option", { value: v, selected: v === value }, l)),
  ) as HTMLSelectElement;
  return s;
}

/** The writes behind one access control: share, change the access, stop sharing. */
type Writes = {
  create: (a: Access) => Promise<unknown>;
  change: (a: Access) => Promise<unknown>;
  remove: () => Promise<unknown>;
};

/**
 * The owner's Share sheet: each household they are in and each profile they may share with, or
 * already have, with an access control that writes at once. A kid only views.
 */
async function shareDialog(ctx: Ctx, pl: Playlist) {
  const base = { params: { path: { ...path(ctx), playlistId: pl.id } } };
  const e = errorLine();
  const houses = h("ul.shares");
  const people = h("ul.shares");
  const d = dialog(
    "Share",
    h("p.sub", pl.name),
    h(
      "section",
      { "aria-label": "Households" },
      h("h2", "Households"),
      houses,
      h("p.sub", "Everyone in the household can see it."),
    ),
    h("section", { "aria-label": "Profiles" }, h("h2", "Profiles"), people),
    e,
    h("div.end", h("button.btn.s", { type: "button", onclick: () => done() }, "Done")),
  );
  // Closing either way redraws the page beneath, so its Shared badge follows the changes.
  const done = () => (d.close(), refresh());
  d.box.addEventListener("keydown", (ev) => ev.key === "Escape" && refresh());
  // A control holds still while its write is out, so writes never overlap; a failed one shows why
  // and puts the control back on the access the server still holds.
  const control = (name: string, value: Access | undefined, kid: boolean, w: Writes) => {
    let now: Access | "" = value ?? "";
    const s = pick(kid ? ACCESS.slice(0, 2) : ACCESS, now, `Access for ${name}`, (v) => {
      showError(e);
      const to = v as Access | "";
      const focused = document.activeElement === s;
      s.disabled = true;
      (!now ? w.create(to as Access) : !to ? w.remove() : w.change(to))
        .then(
          () => (now = to),
          (x: Error) => ((s.value = now), showError(e, x.message)),
        )
        .finally(() => {
          s.disabled = false;
          if (focused) s.focus();
        });
    });
    return s;
  };
  const shareRow = (lead: Node, name: string, sub: string, select: HTMLSelectElement) =>
    h("li.share", lead, h("b", name, sub ? h("span.sub", sub) : null), select);
  try {
    const [hs, hshares, shares, candidates] = await Promise.all([
      ok(ctx.api.GET("/profiles/{profileId}/share-households", { params: { path: path(ctx) } })),
      paged((offset) =>
        ok(
          ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/household-shares", {
            params: { ...base.params, query: { limit: 200, offset } },
          }),
        ),
      ),
      paged((offset) =>
        ok(
          ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/shares", {
            params: { ...base.params, query: { limit: 200, offset } },
          }),
        ),
      ),
      paged((offset) =>
        ok(
          ctx.api.GET("/profiles/{profileId}/share-candidates", {
            params: { path: path(ctx), query: { limit: 200, offset } },
          }),
        ),
      ),
    ]);
    houses.replaceChildren(
      ...hs.items.map((hh) => {
        const one = { params: { path: { ...base.params.path, householdId: hh.id } } };
        return shareRow(
          h("span.avatar.house", icon("house", 18)),
          hh.name,
          plural(hh.profiles, "profile"),
          control(hh.name, hshares.find((s) => s.householdId === hh.id)?.access, false, {
            create: (access) =>
              ok(
                ctx.api.POST("/profiles/{profileId}/playlists/{playlistId}/household-shares", {
                  ...base,
                  body: { householdId: hh.id, access },
                }),
              ),
            change: (access) =>
              ok(
                ctx.api.PUT(
                  "/profiles/{profileId}/playlists/{playlistId}/household-shares/{householdId}",
                  { ...one, body: { access } },
                ),
              ),
            remove: () =>
              ok(
                ctx.api.DELETE(
                  "/profiles/{profileId}/playlists/{playlistId}/household-shares/{householdId}",
                  one,
                ),
              ),
          }),
        );
      }),
    );
    // A household is named only where it tells the owner's several households apart.
    const named = new Map(hs.items.length > 1 ? hs.items.map((hh) => [hh.id, hh.name]) : []);
    const ids = new Set(candidates.map((c) => c.profileId));
    const who = [...candidates, ...shares.filter((s) => !ids.has(s.profileId))];
    people.replaceChildren(
      ...who.map((c) => {
        const one = { params: { path: { ...base.params.path, shareProfileId: c.profileId } } };
        return shareRow(
          avatar({ id: c.profileId, name: c.name, color: c.color }, 36),
          c.name,
          [named.get(c.householdId!), c.kind === "kid" && "Kid"].filter(Boolean).join(" · "),
          control(
            c.name,
            shares.find((s) => s.profileId === c.profileId)?.access,
            c.kind === "kid",
            {
              create: (access) =>
                ok(
                  ctx.api.POST("/profiles/{profileId}/playlists/{playlistId}/shares", {
                    ...base,
                    body: { profileId: c.profileId, access },
                  }),
                ),
              change: (access) =>
                ok(
                  ctx.api.PUT(
                    "/profiles/{profileId}/playlists/{playlistId}/shares/{shareProfileId}",
                    { ...one, body: { access } },
                  ),
                ),
              remove: () =>
                ok(
                  ctx.api.DELETE(
                    "/profiles/{profileId}/playlists/{playlistId}/shares/{shareProfileId}",
                    one,
                  ),
                ),
            },
          ),
        );
      }),
    );
    if (!who.length) people.replaceWith(h("p.sub", "No one else to share with yet."));
  } catch (x) {
    showError(e, (x as Error).message);
  }
}
