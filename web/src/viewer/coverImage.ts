// The custom image the owner of a playlist or a station sets: a file from the device, one of
// its titles' artwork, or a link the server fetches once. The server checks and keeps it.
import { ok, paged } from "../admin/api";
import { dialog, errorLine, field, h, input, showError } from "../admin/dom";
import type { Api, Schemas } from "../api/client";
import { color, title } from "./data";

type Entry = Schemas["PlaylistEntry"];
type ArtKind = Schemas["ArtworkKind"];
type Input = Schemas["CustomImageInput"];

export type Target =
  { playlist: { profileId: number; playlistId: number } } | { station: { stationId: number } };

/** The data-art key of an imageUrl; the address changes with the image, so the key does. */
export const imageKey = (url: string) => `image:${url}`;

const ROUTE = /^\/api\/v1\/(?:profiles\/(\d+)\/playlists\/(\d+)|stations\/(\d+))\/image\?v=(\d+)$/;

export function imageRoute(url: string): (Target & { v: number }) | undefined {
  const m = ROUTE.exec(url);
  if (!m) return undefined;
  const v = Number(m[4]);
  return m[3]
    ? { station: { stationId: Number(m[3]) }, v }
    : { playlist: { profileId: Number(m[1]), playlistId: Number(m[2]) }, v };
}

/** Fetches the image an imageUrl names, as a blob; any other address is not found. */
export function fetchImage(api: Api, url: string) {
  const r = imageRoute(url);
  if (!r) return Promise.resolve({ data: undefined, response: { status: 404 } });
  return "station" in r
    ? api.GET("/stations/{stationId}/image", {
        params: { path: r.station, query: { v: r.v } },
        parseAs: "blob",
      })
    : api.GET("/profiles/{profileId}/playlists/{playlistId}/image", {
        params: { path: r.playlist, query: { v: r.v } },
        parseAs: "blob",
      });
}

/** One choice of "Pick a title's artwork". */
export type ArtChoice = { itemId: number; kind: ArtKind; label: string };

/**
 * The artwork the entries offer, each title once: a poster, and a backdrop for what has one
 * (movies and TV; music has only covers). Collections offer none.
 */
export function artChoices(entries: Entry[]): ArtChoice[] {
  const seen = new Set<number>();
  return entries.flatMap((e) => {
    if (e.itemId === undefined || seen.has(e.itemId)) return [];
    seen.add(e.itemId);
    const name = e.item
      ? title(e.item)
      : (e.song?.title ?? e.album?.title ?? e.artist?.name ?? "Untitled");
    const kinds: ArtKind[] = e.item ? ["poster", "backdrop"] : ["poster"];
    return kinds.map((kind) => ({
      itemId: e.itemId!,
      kind,
      label: `${name}, ${kind}`,
    }));
  });
}

function put(api: Api, t: Target, body: Input | Blob) {
  const file = body instanceof Blob;
  const opts = {
    body: body as Input,
    ...(file
      ? {
          bodySerializer: (b: unknown) => b as BodyInit,
          headers: { "Content-Type": "application/octet-stream" },
        }
      : {}),
  };
  return "station" in t
    ? ok(api.PUT("/stations/{stationId}/image", { params: { path: t.station }, ...opts }))
    : ok(
        api.PUT("/profiles/{profileId}/playlists/{playlistId}/image", {
          params: { path: t.playlist },
          ...opts,
        }),
      );
}

const removeImage = (api: Api, t: Target) =>
  "station" in t
    ? ok(api.DELETE("/stations/{stationId}/image", { params: { path: t.station } }))
    : ok(
        api.DELETE("/profiles/{profileId}/playlists/{playlistId}/image", {
          params: { path: t.playlist },
        }),
      );

/** done runs after the server keeps an image; the server's errors stay in the dialog. */
export function imageDialog(
  api: Api,
  t: Target,
  playlist: { profileId: number; playlistId: number },
  done: () => void,
) {
  const e = errorLine();
  const set = (body: Input | Blob) =>
    put(api, t, body).then(
      () => {
        d.close();
        done();
      },
      (x: Error) => showError(e, x.message),
    );
  const file = input("file", { accept: "image/jpeg,image/png,image/gif,image/webp" });
  file.addEventListener("change", () => file.files?.[0] && void set(file.files[0]));
  const link = input("url", { placeholder: "https://", maxLength: 2048 });
  const picks = h("div.picks", { role: "list", "aria-label": "Artwork" });
  const d = dialog(
    "Set image",
    field("Upload a file", file),
    h("h2.sec", "Pick a title's artwork"),
    picks,
    h(
      "form",
      {
        onsubmit: (ev: Event) => {
          ev.preventDefault();
          if (link.value.trim()) void set({ url: link.value.trim() });
        },
      },
      field("Paste an image link", link),
      h("div.end", h("button.btn.s", { type: "submit" }, "Use link")),
    ),
    e,
    h("div.end", h("button.btn.s", { type: "button", onclick: () => d.close() }, "Cancel")),
  );
  d.box.classList.add("imagepick");
  void paged((offset) =>
    ok(
      api.GET("/profiles/{profileId}/playlists/{playlistId}/entries", {
        params: { path: playlist, query: { limit: 200, offset } },
      }),
    ),
  ).then(
    (entries) => {
      const choices = artChoices(entries);
      if (!choices.length) picks.replaceWith(h("p.sub", "Nothing in it has artwork to pick."));
      for (const c of choices)
        picks.append(
          h(
            "button.pick",
            {
              type: "button",
              role: "listitem",
              "aria-label": c.label,
              title: c.label,
              onclick: () => void set({ itemId: c.itemId, kind: c.kind }),
            },
            h(`div.art.${c.kind === "backdrop" ? "st" : "p"}`, {
              style: `background:${color(c.itemId)}`,
              "data-art": `${c.itemId}/${c.kind}`,
            }),
          ),
        );
    },
    (x: Error) => showError(e, x.message),
  );
}

/** The owner's Set image and, with one set, Remove image buttons. */
export function imageButtons(
  api: Api,
  t: Target,
  playlist: { profileId: number; playlistId: number },
  imageUrl: string | undefined,
  done: () => void,
) {
  return [
    h(
      "button.btn.s",
      { type: "button", onclick: () => imageDialog(api, t, playlist, done) },
      "Set image",
    ),
    imageUrl
      ? h(
          "button.btn.s",
          { type: "button", onclick: () => void removeImage(api, t).then(done) },
          "Remove image",
        )
      : null,
  ];
}
