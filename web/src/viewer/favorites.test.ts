// Favorites: the star on detail pages, the grid chip and the Favorites page,
// against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { at, calls, mockFetch, none, root, until } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id: uid(id),
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  modifiedAt: ts,
});
const me = (active = 1) => ({
  device: { id: uid(1) },
  account: acct(1, "Helga"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Helga", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")],
});
const page = (items: unknown[]) => ({ items });
const item = (id: number, kind: string, extra = {}) => ({
  id: uid(id),
  libraryId: uid(1),
  kind,
  parsedTitle: `T${id}`,
  addedAt: `2026-01-0${id % 9}T00:00:00Z`,
  modifiedAt: ts,
  ...extra,
});
const fav = (id: number, kind: string, createdAt = ts) => ({ itemId: uid(id), kind, createdAt });
const FAVS = `/profiles/${uid(1)}/favorites`;
const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label);

const dune = item(10, "movie", { metadata: { status: "matched", title: "Dune" } });
const heat = item(11, "movie", { metadata: { status: "matched", title: "Heat" } });
const routes = (favs: unknown[], extra = {}) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(),
  [`GET /profiles/${uid(1)}/watch-state`]: page([]),
  [`GET /items/${dune.id}`]: dune,
  [`GET /items/${heat.id}`]: heat,
  [`GET /items/${dune.id}/credits`]: { cast: [], crew: [] },
  "GET /media-files": page([]),
  "GET /genres": page([]),
  "GET /items": (_: unknown, u: URL) =>
    page(u.searchParams.get("kind") === "movie" ? [dune, heat] : []),
  [`GET ${FAVS}`]: (_: unknown, u: URL) => {
    const k = u.searchParams.get("kind");
    return page((favs as { kind: string }[]).filter((f) => !k || f.kind === k));
  },
  [`PUT ${FAVS}/${dune.id}`]: none,
  [`DELETE ${FAVS}/${dune.id}`]: none,
  ...extra,
});

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  localStorage.clear();
  setToken("tok");
  history.replaceState(null, "", "/");
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
});

describe("the star on a detail page", () => {
  const star = (r: HTMLElement) =>
    r.querySelector<HTMLButtonElement>(".actions button[aria-label=Favorite]")!;

  it("toggles a movie on with a PUT and off with a DELETE, pressed as it goes", async () => {
    await at(`#/movie/${dune.id}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes([])) });
    await until(() => star(r) && !star(r).disabled);
    expect(star(r).getAttribute("aria-pressed")).toBe("false");
    star(r).click();
    await until(() => star(r).getAttribute("aria-pressed") === "true");
    expect(calls.some((c) => c.key === `PUT ${FAVS}/${dune.id}`)).toBe(true);
    star(r).click();
    await until(() => star(r).getAttribute("aria-pressed") === "false");
    expect(calls.some((c) => c.key === `DELETE ${FAVS}/${dune.id}`)).toBe(true);
  });

  it("starts pressed for a favorite, and the More menu has no favorites row", async () => {
    await at(`#/movie/${dune.id}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes([fav(10, "movie")])) });
    await until(() => star(r)?.getAttribute("aria-pressed") === "true");
    const more = r.querySelector<HTMLElement>("[role=menu][aria-label=More]")!;
    expect(text(more.querySelectorAll("[role^=menuitem]")).join()).not.toMatch(/Favorite/);
    expect(calls.find((c) => c.key === `GET ${FAVS}`)!.query).toContain("kind=movie");
  });

  it("stays unpressed for a favorite of another item", async () => {
    await at(`#/movie/${dune.id}`);
    const r = root();
    await boot(r, { fetch: mockFetch(routes([fav(11, "movie")])) });
    await until(() => star(r) && !star(r).disabled);
    expect(star(r).getAttribute("aria-pressed")).toBe("false");
  });
});

describe("the Favorites chip", () => {
  it("sits between In progress and Kids on Movies and keeps only favorites", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: mockFetch(routes([fav(11, "movie")])) });
    await until(() => r.querySelector(".grid a"));
    expect(text(r.querySelectorAll(".chip"))).toEqual([
      "All",
      "Unwatched",
      "In progress",
      "Favorites",
      "Kids",
    ]);
    expect(calls.some((c) => c.key === `GET ${FAVS}`)).toBe(false);
    button(r.querySelector(".chips")!, "Favorites")!.click();
    await until(() => text(r.querySelectorAll(".grid b")).join() === "Heat");
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("1 movie");
  });
});

describe("the Favorites page", () => {
  const artist = {
    id: uid(50),
    libraryId: uid(3),
    name: "Radiohead",
    sortName: "Radiohead",
    albumCount: 9,
    songCount: 112,
    genres: [],
    addedAt: ts,
  };
  const song = {
    id: uid(70),
    libraryId: uid(3),
    albumId: uid(100),
    albumTitle: "In Rainbows",
    albumArtistId: uid(50),
    artistId: uid(50),
    artistName: "Radiohead",
    title: "Nude",
    durationMs: 240_000,
    codec: "flac",
    lossless: true,
  };

  it("is in the main navigation and lists a section per kind, newest star first", async () => {
    await at("#/favorites");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes(
          [
            fav(70, "song"),
            fav(10, "movie"),
            fav(50, "artist"),
            fav(11, "movie"),
            fav(99, "movie"),
          ],
          {
            [`GET /music/artists/${uid(50)}`]: artist,
            [`GET /music/songs/${uid(70)}`]: song,
          },
        ),
      ),
    });
    await until(() => r.querySelector("section[aria-label=Movies] a"));
    expect(r.querySelector("nav [aria-current=page]")!.textContent).toBe("Favorites");
    expect(text(r.querySelectorAll("main h2"))).toEqual(["Movies", "Artists", "Songs"]);
    // The favorite of a title no longer seen (99) is left out.
    expect(text(r.querySelectorAll("section[aria-label=Movies] b"))).toEqual(["Dune", "Heat"]);
    expect(r.querySelector("section[aria-label=Artists] a")!.getAttribute("href")).toBe(
      `#/artist/${uid(50)}`,
    );
    expect(r.querySelector("section[aria-label=Songs] .tracks b")!.firstChild!.textContent).toBe(
      "Nude",
    );
  });

  it("says how to star something when there is nothing", async () => {
    await at("#/favorites");
    const r = root();
    await boot(r, { fetch: mockFetch(routes([])) });
    await until(() => r.querySelector("main p.sub")?.textContent?.startsWith("Nothing"));
    expect(r.querySelector("main p.sub")!.textContent).toBe(
      "Nothing starred yet. Favorite is beside a title's Play or in its menu.",
    );
  });
});
