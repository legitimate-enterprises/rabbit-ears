// Paints artwork into tiles marked data-art="<itemId>/<kind>",
// "collection/<collectionId>/<kind>", "person/<personId>", "profile/<profileId>" or
// "image:<imageUrl>" as they come near the viewport. Images need the device token, so they arrive
// as blobs rather than through <img src>; a tile without artwork keeps its color. A tile marked
// data-ph="<kind>" shows that kind's placeholder until its image has loaded, and for good when it
// has none.
import type { Middleware } from "openapi-fetch";
import type { Api } from "../api/client";
import { fetchImage } from "./coverImage";

type Kind = "poster" | "backdrop";
const HELD_LIMIT = 200;

export type PlaceholderKind =
  | "movie"
  | "show"
  | "album"
  | "artist"
  | "song"
  | "channel"
  | "video"
  | "playlist"
  | "station"
  | "collection"
  | "person";

const OF_ITEM: Record<string, PlaceholderKind> = {
  movie: "movie",
  show: "show",
  season: "show",
  episode: "show",
  album: "album",
  artist: "artist",
  song: "song",
  channel: "channel",
  video: "video",
};

/** The placeholder an item of this kind shows: a season and an episode show their show's. */
export const placeholderKind = (itemKind: string): PlaceholderKind => OF_ITEM[itemKind] ?? "movie";

const FADE_MS = 200;
const reducedMotion = () =>
  typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

/** Which of the held keys, oldest use first, to release: the oldest unused ones beyond the limit. */
export const releasable = (held: string[], limit: number, inUse: Set<string>): string[] =>
  held.filter((k) => !inUse.has(k)).slice(0, Math.max(0, held.length - limit));

let live:
  | {
      observer: MutationObserver;
      near?: IntersectionObserver;
      urls: Map<string, Promise<string | undefined>>;
      api: Api;
      generations: Middleware;
    }
  | undefined;

/** Starts painting root; a later call (a new sign in or profile) replaces it. */
export function watchArt(
  root: HTMLElement,
  api: Api,
  placeholder?: (kind: PlaceholderKind) => Element,
) {
  if (live) {
    live.observer.disconnect();
    live.near?.disconnect();
    live.api.eject(live.generations);
    for (const u of live.urls.values()) void u.then((v) => v && URL.revokeObjectURL(v));
  }
  const urls = new Map<string, Promise<string | undefined>>();
  // Keys whose image is in memory, so a tile asking again is painted at once, without a fade.
  const ready = new Set<string>();
  // The server's artwork generation, from the latest response; images are kept under it.
  let generation: number | undefined;
  // Object URLs still alive, least recently painted first.
  const held = new Map<string, string>();
  const evict = () => {
    const inUse = new Set(
      Array.from(root.querySelectorAll<HTMLElement>("[data-art]"), (e) => e.dataset.art!),
    );
    for (const k of releasable([...held.keys()], HELD_LIMIT, inUse)) {
      URL.revokeObjectURL(held.get(k)!);
      held.delete(k);
      urls.delete(k);
      ready.delete(k);
    }
  };
  // A new generation means some image changed: what is held is asked for again. Tiles in
  // the page keep the image they show, so their blobs are left to the page.
  const generations: Middleware = {
    onResponse({ request, response }) {
      // An image the browser cache answers carries the generation it was stored under.
      if (/\/(artwork|avatar|image)\b/.test(new URL(request.url, location.href).pathname)) return;
      const g = Number(response.headers.get("Artwork-Generation"));
      if (!(g > 0) || g === generation) return;
      const first = generation === undefined;
      generation = g;
      if (first) return;
      const inUse = new Set(
        Array.from(root.querySelectorAll<HTMLElement>("[data-art]"), (e) => e.dataset.art!),
      );
      for (const [k, u] of held) if (!inUse.has(k)) URL.revokeObjectURL(u);
      held.clear();
      urls.clear();
      ready.clear();
    },
  };
  api.use(generations);
  const load = (key: string) => {
    if (!urls.has(key)) {
      const [itemId, kind, last] = key.split("/");
      const query = generation === undefined ? undefined : { v: generation };
      const get = key.startsWith("image:")
        ? fetchImage(api, key.slice("image:".length))
        : itemId === "profile"
          ? api.GET("/profiles/{profileId}/avatar", {
              params: { path: { profileId: Number(kind) } },
              parseAs: "blob",
            })
          : itemId === "person"
            ? api.GET("/people/{personId}/artwork", {
                params: { path: { personId: Number(kind) }, query },
                parseAs: "blob",
              })
            : itemId === "collection"
              ? api.GET("/collections/{collectionId}/artwork/{kind}", {
                  params: { path: { collectionId: Number(kind), kind: last as Kind }, query },
                  parseAs: "blob",
                })
              : api.GET("/items/{itemId}/artwork/{kind}", {
                  params: { path: { itemId: Number(itemId), kind: kind as Kind }, query },
                  parseAs: "blob",
                });
      urls.set(
        key,
        get.then(
          (r) => {
            if (r.data) {
              const u = URL.createObjectURL(r.data);
              held.set(key, u);
              ready.add(key);
              return u;
            }
            // Only a 404, artwork that is not there, stays known; anything else is asked again.
            if (r.response.status !== 404) urls.delete(key);
          },
          () => void urls.delete(key),
        ),
      );
    }
    return urls.get(key)!;
  };
  // The placeholder gives way to the image underneath it: at once when the image was already
  // in memory, else by fading out.
  const reveal = (el: HTMLElement, instant: boolean) => {
    const ph = el.querySelector(":scope > .artph");
    if (!ph) return;
    if (instant || reducedMotion()) return ph.remove();
    ph.classList.add("out");
    ph.addEventListener("transitionend", () => ph.remove(), { once: true });
    setTimeout(() => ph.remove(), FADE_MS * 2);
  };
  const paint = (el: HTMLElement) => {
    const instant = ready.has(el.dataset.art!);
    void load(el.dataset.art!).then((u) => {
      if (!u) return;
      Object.assign(el.style, {
        backgroundImage: `url("${u}")`,
        backgroundSize: "cover",
        backgroundPosition: "center",
      });
      reveal(el, instant);
      const key = el.dataset.art!;
      if (held.get(key) === u) {
        held.delete(key);
        held.set(key, u);
      }
      evict();
    });
  };
  // Where IntersectionObserver is missing (jsdom), a tile paints as it appears.
  const near =
    typeof IntersectionObserver === "function"
      ? new IntersectionObserver(
          (es, obs) =>
            es.forEach((e) => {
              if (!e.isIntersecting) return;
              obs.unobserve(e.target);
              paint(e.target as HTMLElement);
            }),
          { rootMargin: "300px" },
        )
      : undefined;
  // A tile that already shows its image, or whose image is in memory, needs no placeholder.
  const holdPlace = (el: HTMLElement) => {
    const kind = el.dataset.ph as PlaceholderKind | undefined;
    if (!kind || !placeholder || el.style.backgroundImage || el.querySelector(":scope > .artph"))
      return;
    if (el.dataset.art && ready.has(el.dataset.art)) return;
    const ph = placeholder(kind);
    ph.classList.add("artph");
    el.prepend(ph);
  };
  const wait = (el: HTMLElement) => {
    holdPlace(el);
    if (!el.dataset.art) return;
    if (near) near.observe(el);
    else paint(el);
  };
  const scan = (n: Node) => {
    if (!(n instanceof HTMLElement)) return;
    if (n.dataset.art || n.dataset.ph) wait(n);
    n.querySelectorAll<HTMLElement>("[data-art], [data-ph]").forEach(wait);
  };
  const forget = (n: Node) => {
    if (!(n instanceof HTMLElement)) return;
    near?.unobserve(n);
    n.querySelectorAll<HTMLElement>("[data-art]").forEach((e) => near?.unobserve(e));
  };
  const observer = new MutationObserver((ms) =>
    ms.forEach((m) => {
      m.removedNodes.forEach(forget);
      m.addedNodes.forEach(scan);
    }),
  );
  observer.observe(root, { childList: true, subtree: true });
  live = { observer, near, urls, api, generations };
  scan(root);
}

/** The data-art key for item i: wide tiles show a movie's or show's backdrop, an episode's still. */
export const artKey = (i: { id: number; kind: string }, wide = false): string =>
  `${i.id}/${wide && i.kind !== "episode" ? "backdrop" : "poster"}`;
