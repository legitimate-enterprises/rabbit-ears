// Paints artwork into tiles marked data-art="art:<posterUrl or another artwork URL>",
// "<itemId>/<kind>", "collection/<collectionId>/<kind>", "person/<personId>",
// "profile/<profileId>" or "image:<imageUrl>" as they come near the viewport. Images need the device token, so they arrive
// as blobs rather than through <img src>; a tile without artwork keeps its color. A tile marked
// data-ph="<kind>" shows that kind's placeholder until its image has loaded, and for good when it
// has none.
import { API_BASE, type Api, type Schemas } from "../api/client";
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
    for (const u of live.urls.values()) void u.then((v) => v && URL.revokeObjectURL(v));
  }
  const urls = new Map<string, Promise<string | undefined>>();
  // Keys whose image is in memory, so a tile asking again is painted at once, without a fade.
  const ready = new Set<string>();
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
  const load = (key: string) => {
    if (!urls.has(key)) {
      const [itemId, kind, last] = key.split("/");
      const address = ADDRESS.exec(key);
      const get = address
        ? api.GET("/artwork/{store}/{name}", {
            params: { path: { store: address[1] as Schemas["ArtworkStore"], name: address[2] } },
            parseAs: "blob",
          })
        : key.startsWith("image:")
          ? fetchImage(api, key.slice("image:".length))
          : itemId === "profile"
            ? api.GET("/profiles/{profileId}/avatar", {
                params: { path: { profileId: kind } },
                parseAs: "blob",
              })
            : itemId === "person"
              ? api.GET("/people/{personId}/artwork", {
                  params: { path: { personId: kind } },
                  parseAs: "blob",
                })
              : itemId === "collection"
                ? api.GET("/collections/{collectionId}/artwork/{kind}", {
                    params: { path: { collectionId: kind, kind: last as Kind } },
                    parseAs: "blob",
                  })
                : api.GET("/items/{itemId}/artwork/{kind}", {
                    params: { path: { itemId, kind: kind as Kind } },
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
  live = { observer, near, urls };
  scan(root);
}

const ADDRESS = new RegExp(`^art:${API_BASE}/artwork/([^/]+)/([^/]+)$`);

/** The data-art key of an artwork URL the data carries; none for no image. */
export const artOf = (url: string | undefined) => (url ? `art:${url}` : undefined);

/** The data-art key for item i: wide tiles show a movie's or show's backdrop, an episode's still. */
export const artKey = (
  i: { kind: string; posterUrl?: string; backdropUrl?: string },
  wide = false,
): string | undefined => artOf(wide && i.kind !== "episode" ? i.backdropUrl : i.posterUrl);
