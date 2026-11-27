import { afterEach, expect, it, vi } from "vitest";
import { createApi } from "../admin/api";
import { placeholderKind, watchArt } from "./artwork";
import { uid } from "../api/uid";

afterEach(() => vi.unstubAllGlobals());

const jpg = () => new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } });
const missing = () =>
  new Response(JSON.stringify({ code: "not_found", message: "x" }), { status: 404 });
const place = (kind: string) => {
  const el = document.createElement("i");
  el.textContent = kind;
  return el;
};
const tile = (root: HTMLElement, key: string | undefined, ph: string) => {
  const el = document.createElement("div");
  if (key) el.dataset.art = key;
  el.dataset.ph = ph;
  return root.appendChild(el);
};
const mount = (fetch: (r: Request) => Promise<Response>) => {
  const root = document.body.appendChild(document.createElement("div"));
  const api = createApi({ token: "t", fetch });
  watchArt(root, api, place);
  return { root, api };
};

it("names a placeholder for every kind an item or a tile can be", () => {
  expect(
    ["movie", "show", "season", "episode", "album", "artist", "song", "channel", "video"].map(
      placeholderKind,
    ),
  ).toEqual(["movie", "show", "show", "show", "album", "artist", "song", "channel", "video"]);
  expect(placeholderKind("anything else")).toBe("movie");
});

it("shows a tile's placeholder at once and fades it out over the image that arrives", async () => {
  URL.createObjectURL = () => "blob:p";
  const { root } = mount(async () => jpg());
  const el = tile(root, `${uid(1)}/poster`, "movie");
  await new Promise((r) => setTimeout(r, 0));
  expect(el.style.backgroundImage).toBe(`url("blob:p")`);
  const ph = el.querySelector(".artph");
  expect(ph?.textContent).toBe("movie");
  expect(ph?.classList.contains("out")).toBe(true);
  await vi.waitFor(() => expect(el.querySelector(".artph")).toBeNull());
});

it("paints an image already in memory with no placeholder and no fade", async () => {
  URL.createObjectURL = () => "blob:m";
  const { root } = mount(async () => jpg());
  const first = tile(root, `${uid(1)}/poster`, "movie");
  await vi.waitFor(() => expect(first.style.backgroundImage).toBe(`url("blob:m")`));
  const again = tile(root, `${uid(1)}/poster`, "movie");
  expect(again.querySelector(".artph")).toBeNull();
  await vi.waitFor(() => expect(again.style.backgroundImage).toBe(`url("blob:m")`));
  expect(again.querySelector(".artph")).toBeNull();
});

it("keeps the placeholder of a title with no image and of a tile with no key at all", async () => {
  const { root } = mount(async () => missing());
  const none = tile(root, `${uid(2)}/poster`, "show");
  const bare = tile(root, undefined, "playlist");
  await new Promise((r) => setTimeout(r, 50));
  expect(none.querySelector(".artph")?.textContent).toBe("show");
  expect(bare.querySelector(".artph")?.textContent).toBe("playlist");
});

it("skips the fade when the viewer prefers reduced motion", async () => {
  URL.createObjectURL = () => "blob:r";
  vi.stubGlobal("matchMedia", () => ({ matches: true }));
  const { root } = mount(async () => jpg());
  const el = tile(root, `${uid(1)}/poster`, "movie");
  await vi.waitFor(() => expect(el.style.backgroundImage).toBe(`url("blob:r")`));
  expect(el.querySelector(".artph")).toBeNull();
});

it("asks for images under the generation the server last named, and again when it moves", async () => {
  let n = 0;
  URL.createObjectURL = () => `blob:g${++n}`;
  let generation = "3";
  const seen: string[] = [];
  const fetch = async (req: Request) => {
    const url = new URL(req.url, "http://x");
    seen.push(url.pathname + url.search);
    const res = url.pathname.includes("/artwork/") ? jpg() : new Response("{}");
    // Images carry no generation here, as when a cached one is answered.
    if (!url.pathname.includes("/artwork/")) res.headers.set("Artwork-Generation", generation);
    return res;
  };
  const { root, api } = mount(fetch);
  const early = tile(root, `${uid(1)}/poster`, "movie");
  await vi.waitFor(() => expect(early.style.backgroundImage).not.toBe(""));
  await api.GET("/health");
  const next = tile(root, `${uid(2)}/poster`, "movie");
  await vi.waitFor(() => expect(next.style.backgroundImage).not.toBe(""));
  generation = "4";
  await api.GET("/health");
  const after = tile(root, `${uid(1)}/poster`, "movie");
  await vi.waitFor(() => expect(after.style.backgroundImage).not.toBe(""));
  expect(seen.filter((s) => s.includes("/artwork/"))).toEqual([
    `/api/v2/items/${uid(1)}/artwork/poster`,
    `/api/v2/items/${uid(2)}/artwork/poster?v=3`,
    `/api/v2/items/${uid(1)}/artwork/poster?v=4`,
  ]);
});

it("reads no generation from a custom image, which the browser may answer from its cache", async () => {
  URL.createObjectURL = () => "blob:c";
  const seen: string[] = [];
  const fetch = async (req: Request) => {
    const url = new URL(req.url, "http://x");
    seen.push(url.pathname + url.search);
    const custom = url.pathname.endsWith("/image");
    const res = custom || url.pathname.includes("/artwork/") ? jpg() : new Response("{}");
    res.headers.set("Artwork-Generation", custom ? "2" : "3");
    return res;
  };
  const { root, api } = mount(fetch);
  await api.GET("/health");
  const custom = tile(root, `image:/api/v2/stations/${uid(5)}/image?v=9`, "station");
  await vi.waitFor(() => expect(custom.style.backgroundImage).not.toBe(""));
  const next = tile(root, `${uid(1)}/poster`, "movie");
  await vi.waitFor(() => expect(next.style.backgroundImage).not.toBe(""));
  expect(seen.filter((s) => /\/(artwork|image)\b/.test(s))).toEqual([
    `/api/v2/stations/${uid(5)}/image?v=9`,
    `/api/v2/items/${uid(1)}/artwork/poster?v=3`,
  ]);
});
