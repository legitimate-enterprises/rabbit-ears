import { afterEach, expect, it, vi } from "vitest";
import { createApi } from "../admin/api";
import { releasable, watchArt } from "./artwork";
import { uid } from "../api/uid";

afterEach(() => vi.unstubAllGlobals());

it("paints artwork into tiles as they appear, with the device token", async () => {
  // jsdom has no object URLs.
  URL.createObjectURL = () => "blob:poster";
  const seen: string[] = [];
  const fetch = async (req: Request) => {
    seen.push(`${new URL(req.url, "http://x").pathname} ${req.headers.get("authorization")}`);
    return req.url.includes(`/items/${uid(1)}/`)
      ? new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } })
      : new Response(JSON.stringify({ code: "not_found", message: "x" }), { status: 404 });
  };
  const root = document.body.appendChild(document.createElement("div"));
  watchArt(root, createApi({ token: "t", fetch }));
  const tile = (key: string) => {
    const el = document.createElement("div");
    el.dataset.art = key;
    return el;
  };
  const [hit, miss] = [tile(`${uid(1)}/poster`), tile(`${uid(2)}/backdrop`)];
  root.append(hit, miss);
  await vi.waitFor(() => expect(hit.style.backgroundImage).toBe(`url("blob:poster")`));
  expect(miss.style.backgroundImage).toBe("");
  expect(seen).toEqual([
    `/api/v2/items/${uid(1)}/artwork/poster Bearer t`,
    `/api/v2/items/${uid(2)}/artwork/backdrop Bearer t`,
  ]);
});

it("fetches a collection's artwork from the collection endpoint", async () => {
  URL.createObjectURL = () => "blob:collection";
  const seen: string[] = [];
  const fetch = async (req: Request) => {
    seen.push(new URL(req.url, "http://x").pathname);
    return new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } });
  };
  const root = document.body.appendChild(document.createElement("div"));
  watchArt(root, createApi({ token: "t", fetch }));
  const el = document.createElement("div");
  el.dataset.art = `collection/${uid(7)}/poster`;
  root.append(el);
  await vi.waitFor(() => expect(el.style.backgroundImage).toBe(`url("blob:collection")`));
  expect(seen).toEqual([`/api/v2/collections/${uid(7)}/artwork/poster`]);
});

it("fetches a tile's artwork only once it comes near the viewport", async () => {
  URL.createObjectURL = () => "blob:near";
  const seen: string[] = [];
  const fetch = async (req: Request) => {
    seen.push(new URL(req.url, "http://x").pathname);
    return new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } });
  };
  let near!: (es: Partial<IntersectionObserverEntry>[]) => void;
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(cb: (es: unknown, obs: unknown) => void) {
        near = (es) => cb(es, this);
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  const root = document.body.appendChild(document.createElement("div"));
  watchArt(root, createApi({ token: "t", fetch }));
  const [a, b] = [document.createElement("div"), document.createElement("div")];
  a.dataset.art = `${uid(1)}/poster`;
  b.dataset.art = `${uid(2)}/poster`;
  root.append(a, b);
  await new Promise((r) => setTimeout(r, 0));
  expect(seen).toEqual([]);
  near([{ target: a, isIntersecting: true }]);
  await vi.waitFor(() => expect(a.style.backgroundImage).toBe(`url("blob:near")`));
  expect(seen).toEqual([`/api/v2/items/${uid(1)}/artwork/poster`]);
});

it("asks again for artwork whose fetch failed, not for artwork that is not there", async () => {
  URL.createObjectURL = () => "blob:again";
  const seen: string[] = [];
  let down = true;
  const fetch = async (req: Request) => {
    const p = new URL(req.url, "http://x").pathname;
    seen.push(p);
    if (p.includes(`/items/${uid(2)}/`))
      return new Response(JSON.stringify({ code: "not_found", message: "x" }), { status: 404 });
    if (down) throw new TypeError("Failed to fetch");
    return new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } });
  };
  const root = document.body.appendChild(document.createElement("div"));
  watchArt(root, createApi({ token: "t", fetch }));
  const tile = (key: string) => {
    const el = document.createElement("div");
    el.dataset.art = key;
    return root.appendChild(el);
  };
  tile(`${uid(1)}/poster`);
  tile(`${uid(2)}/poster`);
  await vi.waitFor(() => expect(seen).toHaveLength(2));
  await new Promise((r) => setTimeout(r, 0));
  down = false;
  const later = tile(`${uid(1)}/poster`);
  tile(`${uid(2)}/poster`);
  await vi.waitFor(() => expect(later.style.backgroundImage).toBe(`url("blob:again")`));
  expect(seen).toEqual([
    `/api/v2/items/${uid(1)}/artwork/poster`,
    `/api/v2/items/${uid(2)}/artwork/poster`,
    `/api/v2/items/${uid(1)}/artwork/poster`,
  ]);
});

it("releases the oldest unused artwork once more than the limit is held", () => {
  const held = ["a", "b", "c", "d"];
  expect(releasable(held, 2, new Set())).toEqual(["a", "b"]);
  expect(releasable(held, 2, new Set(["a"]))).toEqual(["b", "c"]);
  expect(releasable(held, 2, new Set(held))).toEqual([]);
  expect(releasable(held, 4, new Set())).toEqual([]);
  expect(releasable(held, 9, new Set())).toEqual([]);
});

it("keeps the image of a tile still in the document when artwork is released", async () => {
  let n = 0;
  const revoked: string[] = [];
  URL.createObjectURL = () => `blob:${++n}`;
  URL.revokeObjectURL = (u) => void revoked.push(u);
  const fetch = async () =>
    new Response(new Blob(["jpg"]), { headers: { "content-type": "image/jpeg" } });
  const root = document.body.appendChild(document.createElement("div"));
  watchArt(root, createApi({ token: "t", fetch }));
  const tile = (key: string) => {
    const el = document.createElement("div");
    el.dataset.art = key;
    return root.appendChild(el);
  };
  const kept = tile(`${uid(0)}/poster`);
  await vi.waitFor(() => expect(kept.style.backgroundImage).toBe(`url("blob:1")`));
  for (let i = 1; i <= 300; i++) tile(`${i}/poster`).remove();
  const last = tile(`${uid(301)}/poster`);
  await vi.waitFor(() => expect(last.style.backgroundImage).not.toBe(""));
  expect(revoked.length).toBeGreaterThan(0);
  expect(revoked).not.toContain("blob:1");
  expect(kept.style.backgroundImage).toBe(`url("blob:1")`);
});
