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
