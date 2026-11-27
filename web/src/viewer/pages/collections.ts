// Movie collections: TMDB franchises, read-only, and custom ones kept by a household.
// Everyone sees them; only an adult edits, and only a custom collection.
import { errorLine, h, showError } from "../../admin/dom";
import { ok, paged } from "../../admin/api";
import type { Schemas } from "../../api/client";
import { color, title, type Ctx } from "../data";
import { plural } from "../format";
import { back, icon, refresh, section } from "../ui";
import { addButton, addToPlaylist, addToStation, confirmDelete, nameDialog } from "./playlists";
import { reorder, row } from "./music";

type Collection = Schemas["Collection"];

const listHref = "#/movies?view=collections";
const orderLabel = "In your order";
// A tile of a hand-ordered collection carries its mark, so only then is the artwork exposed.
const cover = (c: Collection, cls: string, marked = false) =>
  h(
    `div.art.${cls}`,
    {
      "aria-hidden": marked && c.customOrder ? undefined : "true",
      style: `background:${color(c.id)}`,
      "data-art": `collection/${c.id}/poster`,
      "data-ph": "collection",
    },
    marked &&
      c.customOrder &&
      h("div.marks", h("span", { role: "img", "aria-label": orderLabel }, icon("order", 14))),
  );

const list = (ctx: Ctx) =>
  paged((offset) => ok(ctx.api.GET("/collections", { params: { query: { limit: 200, offset } } })));
const create = (ctx: Ctx, name: string) => ok(ctx.api.POST("/collections", { body: { name } }));
const member = (collectionId: string, itemId: string) => ({
  params: { path: { collectionId, itemId } },
});

export async function collectionTiles(ctx: Ctx) {
  const tiles = (await list(ctx)).map((c) =>
    h(
      "a.poster",
      { href: `#/collection/${c.id}` },
      cover(c, "p", true),
      h("b", c.name),
      h("span.sub", plural(c.movieCount, "movie")),
    ),
  );
  const make =
    ctx.profile.kind === "adult"
      ? h(
          "button.btn.s",
          {
            type: "button",
            onclick: () =>
              nameDialog("New collection", "Create", "", async (name) =>
                ctx.go(`#/collection/${(await create(ctx, name)).id}`),
              ),
          },
          icon("plus", 16),
          "New collection",
        )
      : null;
  return { tiles, create: make };
}

export const addToCollection = (ctx: Ctx, itemId: string) =>
  addButton(
    ctx,
    "Add to collection",
    "New collection",
    async () => (await list(ctx)).filter((c) => c.source === "custom"),
    (name) => create(ctx, name),
    (c) => ok(ctx.api.PUT("/collections/{collectionId}/movies/{itemId}", member(c.id, itemId))),
  );

export async function collection(ctx: Ctx, [id]: string[]) {
  const collectionId = id;
  const p = { params: { path: { collectionId } } };
  const listMovies = () =>
    paged((offset) =>
      ok(
        ctx.api.GET("/collections/{collectionId}/movies", {
          params: { ...p.params, query: { limit: 200, offset } },
        }),
      ),
    );
  let [c, movies] = await Promise.all([
    ok(ctx.api.GET("/collections/{collectionId}", p)),
    listMovies(),
  ]);
  const edit = ctx.profile.kind === "adult" && c.source === "custom";
  let editing = false;
  const err = errorLine();
  const meta = h("div.meta");
  const dyn = h("span.dyn");
  const body = section("Movies");
  const btn = (label: string, onclick: () => void) =>
    h("button.btn.s", { type: "button", onclick }, label);
  // A write returns the collection. A position indexes every movie, ones hidden from this
  // profile included, so the list is read back rather than patched; a refused write changes nothing.
  const change = async (write: () => Promise<Collection>, focusAt = -1) => {
    try {
      c = await write();
      movies = await listMovies();
      showError(err);
      paint(focusAt);
    } catch (x) {
      showError(err, (x as Error).message);
    }
  };
  const paint = (focusAt = -1) => {
    meta.replaceChildren(
      h("span", plural(c.movieCount, "movie")),
      ...(c.customOrder ? [h("span.mark", icon("order", 14), orderLabel)] : []),
    );
    dyn.replaceChildren(
      ...(edit && movies.length > 1
        ? [
            editing
              ? btn("Done", () => ((editing = false), paint()))
              : btn("Edit order", () => ((editing = true), paint())),
          ]
        : []),
      ...(edit && c.customOrder
        ? [
            btn(
              "Reset to release order",
              () => void change(() => ok(ctx.api.DELETE("/collections/{collectionId}/order", p))),
            ),
          ]
        : []),
    );
    const rows = movies.map((m, i) => {
      const r = row({
        n: editing ? String(i + 1) : undefined,
        art: h("div.art.sq36", {
          "aria-hidden": "true",
          style: `background:${color(m.id)}`,
          "data-art": `${m.id}/poster`,
          "data-ph": "movie",
        }),
        title: title(m),
        sub: m.parsedYear ? String(m.parsedYear) : undefined,
        ms: (m.metadata?.runtimeMinutes ?? 0) * 60000,
        on: () => ctx.go(`#/movie/${m.id}`),
        remove: edit
          ? () =>
              void ok(
                ctx.api.DELETE("/collections/{collectionId}/movies/{itemId}", member(c.id, m.id)),
              ).then(refresh)
          : undefined,
      });
      if (editing) r.append(h("span.grip", { "aria-hidden": "true" }, icon("grip", 22)));
      return r;
    });
    if (editing)
      reorder(
        rows,
        (from, to) =>
          void change(
            () =>
              ok(
                ctx.api.PUT("/collections/{collectionId}/movies/{itemId}/position", {
                  params: { path: { collectionId, itemId: movies[from].id } },
                  body: { position: to },
                }),
              ),
            to,
          ),
      );
    body.replaceChildren(
      rows.length ? h("ol.tracks", ...rows) : h("p.sub", "No movies here yet."),
      editing
        ? h(
            "p.sub",
            "Drag a movie to reorder. Everyone who can see this collection sees your order.",
          )
        : "",
      err,
    );
    if (focusAt >= 0) rows[focusAt]?.querySelector<HTMLElement>(".tr")?.focus();
  };
  paint();
  return [
    back("Collections", listHref),
    h(
      "div.detail",
      cover(c, "poster220"),
      h(
        "div.info",
        h("h1.big", c.name),
        meta,
        h(
          "div.actions",
          addToPlaylist(ctx, { collectionId: c.id }, "movies"),
          addToStation(ctx, { collectionId: c.id }, "movies"),
          dyn,
          edit
            ? h(
                "button.btn.s",
                {
                  type: "button",
                  onclick: () =>
                    nameDialog("Rename collection", "Save", c.name, async (name) => {
                      await ok(
                        ctx.api.PUT("/collections/{collectionId}", { ...p, body: { name } }),
                      );
                      refresh();
                    }),
                },
                "Rename",
              )
            : null,
          edit
            ? h(
                "button.btn.s",
                {
                  type: "button",
                  onclick: () =>
                    confirmDelete(
                      `Delete ${c.name}?`,
                      "The collection goes; its movies stay in your library.",
                      () =>
                        ok(ctx.api.DELETE("/collections/{collectionId}", p)).then(() =>
                          ctx.go(listHref),
                        ),
                    ),
                },
                "Delete",
              )
            : null,
        ),
        c.overview ? h("p.synopsis", c.overview) : null,
      ),
    ),
    body,
  ];
}
