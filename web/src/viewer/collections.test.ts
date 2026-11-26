// Component tests for movie collections: the real pages and generated client
// against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label)!;
const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id,
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  updatedAt: ts,
});
const me = (active: number) => ({
  device: { id: 1 },
  account: acct(1, "Heidi"),
  activeProfileId: active,
  household: { id: 1, name: "Heidi", adultId: 1, createdAt: ts },
  profiles: [acct(1, "Heidi"), acct(2, "Jax", "kid")],
});
const page = (items: unknown[]) => ({ items });
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  "GET /profiles/1/watch-state": page([]),
  "GET /profiles/2/watch-state": page([]),
  "GET /profiles/1/show-progress": page([]),
  "GET /profiles/2/show-progress": page([]),
  "GET /genres": page([]),
  "GET /items": page([]),
});
const col = (id: number, name: string, source = "custom", movieCount = 2, customOrder = false) => ({
  id,
  source,
  name,
  movieCount,
  customOrder,
  ...(source === "tmdb" ? { tmdbId: 10, overview: "A franchise." } : { householdId: 1 }),
  createdAt: ts,
  updatedAt: ts,
});
const movie = (id: number, name: string, year: number, extra = {}) => ({
  id,
  libraryId: 1,
  kind: "movie",
  parsedTitle: name,
  parsedYear: year,
  addedAt: ts,
  updatedAt: ts,
  ...extra,
});
const MOVIES = [movie(30, "Dune", 1984), movie(31, "Arrival", 2016)];
const pl = (id: number, name: string) => ({
  id,
  profileId: 1,
  access: "owner",
  name,
  kind: "movies",
  itemCount: 2,
  durationMs: 0,
  createdAt: ts,
  updatedAt: ts,
});

beforeEach(() => {
  calls.length = 0;
  setToken("tok");
  document.body.replaceChildren();
});

describe("Collections view", () => {
  const routes = {
    "GET /collections": page([col(7, "Dune saga", "tmdb"), col(8, "Date night", "custom", 1)]),
    "POST /collections": col(9, "Rainy days"),
    "GET /collections/9": col(9, "Rainy days", "custom", 0),
    "GET /collections/9/movies": page([]),
  };

  it("lists collections as tiles for an adult, who can make one", async () => {
    await at("#/movies?view=collections");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(), ...routes }) });
    await until(() => r.querySelector(".grid .poster"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual([
      "Movies",
      "Collections",
      "Playlists",
      "Stations",
    ]);
    expect(r.querySelector(".vt [aria-current=page]")!.getAttribute("href")).toBe(
      "#/movies?view=collections",
    );
    expect(r.querySelector(".chip")).toBeNull();
    expect(text(r.querySelectorAll(".grid .poster b"))).toEqual(["Dune saga", "Date night"]);
    expect(text(r.querySelectorAll(".grid .poster .sub"))).toEqual(["2 movies", "1 movie"]);
    expect(r.querySelector<HTMLAnchorElement>(".grid .poster")!.getAttribute("href")).toBe(
      "#/collection/7",
    );
    expect(r.querySelector(".grid .art")!.getAttribute("data-art")).toBe("collection/7/poster");

    button(r, "New collection").click();
    const dlg = document.querySelector("[role=dialog]")!;
    dlg.querySelector("input")!.value = "Rainy days";
    dlg.querySelector("form")!.requestSubmit();
    await until(() => r.querySelector("h1")?.textContent === "Rainy days");
    expect(calls.find((c) => c.key === "POST /collections")!.body).toEqual({ name: "Rainy days" });
  });

  it("lists them for a kid, with no way to make one and no Playlists", async () => {
    await at("#/movies?view=collections");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(2), ...routes }) });
    await until(() => r.querySelector(".grid .poster"));
    expect(text(r.querySelectorAll(".vt a"))).toEqual(["Movies", "Collections", "Stations"]);
    expect(button(r, "New collection")).toBeUndefined();
  });

  it("gives a kid's TV page no Collections or Playlists", async () => {
    await at("#/tv");
    const r = root();
    await boot(r, { fetch: mockFetch(base(2)) });
    await until(() => r.querySelector(".chips"));
    expect(text(r.querySelectorAll("nav[aria-label=View] a"))).toEqual(["Shows", "Stations"]);
  });
});

describe("collection page", () => {
  const routes = (c: ReturnType<typeof col>) => {
    let movies = MOVIES;
    return {
      ...base(),
      [`GET /collections/${c.id}`]: c,
      [`GET /collections/${c.id}/movies`]: () => page(movies),
      [`DELETE /collections/${c.id}/movies/30`]: () => (
        (movies = movies.filter((m) => m.id !== 30)),
        none()
      ),
      [`PUT /collections/${c.id}`]: { ...c, name: "Sandworms" },
      [`DELETE /collections/${c.id}`]: none,
      "GET /profiles/1/playlists": page([pl(5, "Weekend")]),
      "POST /profiles/1/playlists/5/entries": {},
    };
  };

  it("lets an adult rename, remove a movie, add to a playlist and delete a custom one", async () => {
    await at("#/collection/8");
    const r = root();
    await boot(r, { fetch: mockFetch(routes(col(8, "Date night"))) });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector("h1")!.textContent).toBe("Date night");
    expect(text(r.querySelectorAll(".tracks .tr b")).map((t) => t!.slice(0, 4))).toEqual([
      "Dune",
      "Arri",
    ]);
    r.querySelector<HTMLElement>(".tracks .tr")!.click();
    expect(location.hash).toBe("#/movie/30");
    await at("#/collection/8");

    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    button(document.querySelector("[role=dialog]")!, "Weekend").click();
    await until(() => button(r, "Added to Weekend"));
    expect(calls.find((c) => c.key === "POST /profiles/1/playlists/5/entries")!.body).toEqual({
      collectionId: 8,
    });

    r.querySelector<HTMLElement>("[aria-label='Remove Dune']")!.click();
    await until(() => r.querySelectorAll(".tracks .qrow").length === 1);

    button(r, "Rename").click();
    const dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.querySelector("input")!.value).toBe("Date night");
    dlg.querySelector("input")!.value = "Sandworms";
    dlg.querySelector("form")!.requestSubmit();
    await until(() => calls.some((c) => c.key === "PUT /collections/8"));
    expect(calls.find((c) => c.key === "PUT /collections/8")!.body).toEqual({ name: "Sandworms" });

    await until(() => !document.querySelector("[role=dialog]"));
    button(r, "Delete").click();
    button(document.querySelector("[role=dialog]")!, "Delete").click();
    await until(() => location.hash === "#/movies?view=collections");
    expect(calls.some((c) => c.key === "DELETE /collections/8")).toBe(true);
  });

  it("shows a TMDB collection with its overview and no editing, but Add to playlist", async () => {
    await at("#/collection/7");
    const r = root();
    await boot(r, { fetch: mockFetch(routes(col(7, "Dune saga", "tmdb"))) });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector(".synopsis")!.textContent).toBe("A franchise.");
    expect(button(r, "Add to playlist")).toBeDefined();
    for (const label of ["Rename", "Delete"]) expect(button(r, label)).toBeUndefined();
    expect(r.querySelector("[aria-label^=Remove]")).toBeNull();
  });

  it("gives a kid the movies and nothing to change", async () => {
    await at("#/collection/8");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...routes(col(8, "Date night")), ...base(2) }) });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelectorAll(".tracks .qrow")).toHaveLength(2);
    for (const label of ["Rename", "Delete", "Add to playlist"])
      expect(button(r, label)).toBeUndefined();
    expect(r.querySelector("[aria-label^=Remove]")).toBeNull();
  });
});

describe("hand order", () => {
  const BADGE = "[aria-label='In your order']";
  const trio = [movie(30, "Dune", 1984), movie(31, "Arrival", 2016), movie(32, "Sicario", 2015)];
  const order = (r: ParentNode) =>
    text(r.querySelectorAll(".tracks .tr b")).map((t) => t!.slice(0, 4));
  // The server's truth: the movies and the flag change when a move or reset lands.
  const server = (opts: { source?: string; customOrder?: boolean; fail?: boolean } = {}) => {
    let movies = trio;
    let customOrder = opts.customOrder ?? false;
    const c = () => col(8, "Date night", opts.source ?? "custom", 3, customOrder);
    return {
      ...base(),
      "GET /collections": () => page([c(), col(9, "Plain", "custom", 1)]),
      "GET /collections/8": () => c(),
      "GET /collections/8/movies": () => page(movies),
      "PUT /collections/8/movies/30/position": () => {
        if (opts.fail) return json({ code: "forbidden", message: "Not yours to order" }, 403);
        movies = [movies[1], movies[2], movies[0]];
        customOrder = true;
        return c();
      },
      "DELETE /collections/8/order": () => ((movies = trio), (customOrder = false), c()),
    };
  };
  const open = async (routes: Record<string, unknown>) => {
    await at("#/collection/8");
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => r.querySelector(".tracks"));
    return r;
  };
  const drag = (r: ParentNode, from: number, to: number) => {
    const rows = r.querySelectorAll(".tracks .qrow");
    rows[from].dispatchEvent(new Event("dragstart"));
    rows[to].dispatchEvent(new Event("drop", { cancelable: true }));
  };

  it("marks a hand-ordered collection's tile and page, and only that", async () => {
    await at("#/movies?view=collections");
    const r = root();
    await boot(r, { fetch: mockFetch(server({ customOrder: true })) });
    await until(() => r.querySelector(".grid .poster"));
    const tiles = [...r.querySelectorAll(".grid .poster")];
    expect(tiles.map((t) => t.querySelector(BADGE) !== null)).toEqual([true, false]);
    document.body.replaceChildren();

    const ordered = await open(server({ customOrder: true }));
    expect(ordered.querySelector(".meta")!.textContent).toContain("In your order");
    document.body.replaceChildren();
    const plain = await open(server());
    expect(plain.querySelector(".meta")!.textContent).not.toContain("In your order");
  });

  it("moves a movie to a position among all of them and reads the list back", async () => {
    const r = await open(server());
    expect(order(r)).toEqual(["Dune", "Arri", "Sica"]);
    expect(button(r, "Reset to release order")).toBeUndefined();
    expect(r.querySelector(".tracks .n")).toBeNull();

    button(r, "Edit order").click();
    expect(text(r.querySelectorAll(".tracks .n"))).toEqual(["1", "2", "3"]);
    expect(button(r, "Edit order")).toBeUndefined();
    drag(r, 0, 2);
    await until(() => order(r)[0] === "Arri");
    const put = calls.find((c) => c.key === "PUT /collections/8/movies/30/position")!;
    expect(put.body).toEqual({ position: 2 });
    expect(order(r)).toEqual(["Arri", "Sica", "Dune"]);
    expect(r.querySelector(".tracks .n")).not.toBeNull();
    await until(() => r.querySelector(".meta")!.textContent!.includes("In your order"));

    button(r, "Done").click();
    expect(r.querySelector(".tracks .n")).toBeNull();
    expect(button(r, "Edit order")).toBeDefined();
  });

  it("resets to release order and reloads", async () => {
    const r = await open(server({ customOrder: true }));
    button(r, "Reset to release order").click();
    await until(() => calls.some((c) => c.key === "DELETE /collections/8/order"));
    await until(() => button(r, "Reset to release order") === undefined);
    expect(r.querySelector(".meta")!.textContent).not.toContain("In your order");
  });

  it("restores the list and says why when a move is refused", async () => {
    const r = await open(server({ fail: true }));
    button(r, "Edit order").click();
    drag(r, 0, 2);
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe("Not yours to order");
    expect(order(r)).toEqual(["Dune", "Arri", "Sica"]);
  });

  it("offers nothing on a TMDB collection or to a kid", async () => {
    const tmdb = await open(server({ source: "tmdb", customOrder: true }));
    for (const label of ["Edit order", "Reset to release order"])
      expect(button(tmdb, label)).toBeUndefined();
    document.body.replaceChildren();
    const kid = await open({ ...server({ customOrder: true }), ...base(2) });
    for (const label of ["Edit order", "Reset to release order"])
      expect(button(kid, label)).toBeUndefined();
    expect(kid.querySelector(".meta")!.textContent).toContain("In your order");
  });
});

describe("movie details", () => {
  const routes = (collections: unknown[]) => ({
    ...base(),
    "GET /items/30": movie(30, "Dune", 1984, { collections }),
    "GET /media-files": page([]),
    "GET /items/30/credits": { cast: [], crew: [] },
    "GET /collections": page([
      col(7, "Dune saga", "tmdb"),
      col(8, "Date night"),
      col(10, "Rainy days"),
    ]),
    "POST /collections": col(11, "Epics"),
    "PUT /collections/8/movies/30": none,
    "PUT /collections/11/movies/30": none,
  });

  it("names its collections as links", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, {
      fetch: mockFetch(routes([col(7, "Dune saga", "tmdb"), col(8, "Date night")])),
    });
    await until(() => r.querySelector("dl.kv"));
    const links = [...r.querySelectorAll<HTMLAnchorElement>("dl.kv a")];
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Dune saga", "#/collection/7"],
      ["Date night", "#/collection/8"],
    ]);
  });

  it("adds to a custom collection or a new one; a kid has no button", async () => {
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(routes([])) });
    await until(() => button(r, "More"));
    button(r, "More").click();
    button(r, "Add to collection").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    const dlg = document.querySelector("[role=dialog]")!;
    expect(text(dlg.querySelectorAll(".choices button"))).toEqual(["Date night", "Rainy days"]);
    button(dlg, "Date night").click();
    await until(() => button(r, "Added to Date night"));
    expect(calls.some((c) => c.key === "PUT /collections/8/movies/30")).toBe(true);

    button(r, "Added to Date night").click();
    const again = document.querySelector("[role=dialog]")!;
    await until(() => again.querySelector(".choices button"));
    again.querySelector("input")!.value = "Epics";
    again.querySelector("form")!.requestSubmit();
    await until(() => calls.some((c) => c.key === "PUT /collections/11/movies/30"));
    expect(calls.find((c) => c.key === "POST /collections")!.body).toEqual({ name: "Epics" });

    document.body.replaceChildren();
    const kid = root();
    await boot(kid, { fetch: mockFetch({ ...routes([]), ...base(2) }) });
    await until(() => kid.querySelector("h1"));
    expect(button(kid, "Add to collection")).toBeUndefined();
  });
});

describe("playlist with a collection", () => {
  it("draws a collection entry as one row and sends its collectionId on reorder", async () => {
    const entry = (id: number, position: number, extra: object) => ({
      id,
      playlistId: 5,
      position,
      itemCount: 1,
      durationMs: 0,
      addedAt: ts,
      ...extra,
    });
    let entries = [
      entry(1, 0, {
        kind: "collection",
        collectionId: 8,
        itemCount: 2,
        collection: col(8, "Date night"),
      }),
      entry(2, 1, { kind: "movie", itemId: 30, item: MOVIES[0] }),
    ];
    await at("#/playlist/5");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/playlists/5": pl(5, "Weekend"),
        "GET /profiles/1/playlists/5/entries": () => page(entries),
        "GET /profiles/1/playlists/5/play-order": page([]),
        "PUT /profiles/1/playlists/5/entries/1": () => (
          (entries = [entries[1], entries[0]]),
          entries[1]
        ),
        "DELETE /profiles/1/playlists/5/entries/2": () => (
          (entries = entries.filter((e) => e.id !== 2)),
          none()
        ),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    const first = r.querySelector(".tracks .qrow")!;
    expect(first.querySelector("b")!.textContent).toBe("Date night2 movies");
    expect(first.querySelector(".art")!.getAttribute("data-art")).toBe("collection/8/poster");
    first
      .querySelector(".tr")!
      .dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, bubbles: true }),
      );
    await until(() => calls.some((c) => c.key === "PUT /profiles/1/playlists/5/entries/1"));
    expect(calls.find((c) => c.key === "PUT /profiles/1/playlists/5/entries/1")!.body).toEqual({
      collectionId: 8,
      position: 1,
    });
    await until(() => r.querySelector(".tracks .qrow b")!.textContent!.startsWith("Dune"));
    r.querySelector<HTMLElement>("[aria-label='Remove Dune']")!.click();
    await until(() => r.querySelectorAll(".tracks .qrow").length === 1);
    r.querySelector<HTMLElement>(".tracks .tr")!.click();
    expect(location.hash).toBe("#/collection/8");
  });
});
