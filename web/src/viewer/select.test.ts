// Component tests for selection mode in the library grids: the real pages and generated
// client against a mocked API.
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
const me = (active: number) => ({
  device: { id: uid(1) },
  account: acct(1, "Heidi"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Heidi", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Heidi"), acct(2, "Jax", "kid")],
});
const page = (items: unknown[]) => ({ items });
const movie = (id: number) => ({
  id: uid(id),
  libraryId: uid(1),
  kind: "movie",
  parsedTitle: `M${id}`,
  addedAt: `2026-01-0${id}T00:00:00Z`,
  createdAt: ts,
  modifiedAt: ts,
});
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  [`GET /profiles/${uid(1)}/watch-state`]: page([]),
  [`GET /profiles/${uid(2)}/watch-state`]: page([]),
  [`GET /profiles/${uid(1)}/show-progress`]: page([]),
  [`GET /profiles/${uid(2)}/show-progress`]: page([]),
  "GET /genres": page([]),
  "GET /items": page([movie(3), movie(2), movie(1)]),
  [`PUT /profiles/${uid(1)}/watch-state/batch`]: none,
  "PUT /items/kids-content/batch": none,
});

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  localStorage.clear();
  history.replaceState(null, "", "/");
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
  setToken("tok");
});

const button = (r: HTMLElement, label: string) =>
  [...r.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);
const tile = (r: HTMLElement, id: number) =>
  r.querySelector<HTMLElement>(`[data-id="${uid(id)}"]`)!;
const click = (el: Element, init: MouseEventInit = {}) =>
  el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
const picked = (r: HTMLElement) =>
  [...r.querySelectorAll<HTMLElement>("[aria-selected=true]")].map((t) => t.dataset.id);

async function movies(active = 1) {
  await at("#/movies");
  const r = root();
  await boot(r, { fetch: mockFetch(base(active)) });
  await until(() => tile(r, 1));
  return r;
}

describe("selection mode", () => {
  it("toggles by click, takes a range by Shift-click, and marks the selection in one request", async () => {
    const r = await movies();
    expect(r.querySelector<HTMLElement>(".selbar")!.hidden).toBe(true);
    button(r, "Select")!.click();
    expect(r.querySelector<HTMLElement>(".selbar")!.hidden).toBe(false);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    tile(r, 3).dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    click(tile(r, 1), { shiftKey: true });
    expect(picked(r)).toEqual([uid(3), uid(2), uid(1)]);
    click(tile(r, 2));
    expect(r.querySelector(".selbar [role=status]")!.textContent).toBe("2 selected");
    button(r, "Mark watched")!.click();
    await until(() => calls.find((c) => c.key === `PUT /profiles/${uid(1)}/watch-state/batch`));
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(1)}/watch-state/batch`)!.body).toEqual({
      itemIds: [uid(3), uid(1)],
      watched: true,
    });
  });

  it("selects every loaded item with Cmd-A and leaves with Escape", async () => {
    const r = await movies();
    button(r, "Select")!.click();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a", metaKey: true }));
    expect(picked(r)).toEqual([uid(3), uid(2), uid(1)]);
    button(r, "Not kids' content")!.click();
    await until(() => calls.find((c) => c.key === "PUT /items/kids-content/batch"));
    expect(calls.find((c) => c.key === "PUT /items/kids-content/batch")!.body).toEqual({
      itemIds: [uid(3), uid(2), uid(1)],
      kidsContent: false,
    });
    await until(() => button(document.body, "Select"));
    const s = document.body;
    button(s, "Select")!.click();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(s.querySelector<HTMLElement>(".selbar")!.hidden).toBe(true);
    expect(picked(s)).toEqual([]);
  });

  it("offers a kid only marking watched", async () => {
    const r = await movies(2);
    button(r, "Select")!.click();
    const bar = r.querySelector(".selbar")!;
    expect([...bar.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Mark watched",
      "Mark unwatched",
      "Done",
    ]);
  });

  it("adds a selection of albums to a playlist in one request", async () => {
    await at("#/music");
    const r = root();
    const album = (id: number) => ({
      id: uid(id),
      libraryId: uid(3),
      artistId: uid(50),
      artistName: "Radiohead",
      title: `A${id}`,
      songCount: 1,
      durationMs: 1,
      lossless: true,
      genres: [],
      addedAt: ts,
    });
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /music/albums": page([album(100), album(101)]),
        [`GET /profiles/${uid(1)}/playlists`]: page([
          { id: uid(9), name: "Mix", kind: "music", itemCount: 0, durationMs: 0, createdAt: ts },
        ]),
        [`POST /profiles/${uid(1)}/playlists/${uid(9)}/entries/batch`]: none,
      }),
    });
    await until(() => tile(r, 101));
    button(r, "Select")!.click();
    expect(button(r, "Mark watched")).toBeUndefined();
    click(tile(r, 101));
    click(tile(r, 100));
    button(r, "Add to playlist")!.click();
    await until(() => button(document.body, "Mix"));
    button(document.body, "Mix")!.click();
    await until(() =>
      calls.find((c) => c.key === `POST /profiles/${uid(1)}/playlists/${uid(9)}/entries/batch`),
    );
    expect(
      calls.find((c) => c.key === `POST /profiles/${uid(1)}/playlists/${uid(9)}/entries/batch`)!
        .body,
    ).toEqual({ entries: [{ itemId: uid(100) }, { itemId: uid(101) }] });
  });

  it("puts nothing from YouTube in a playlist", async () => {
    await at("#/youtube?view=channels");
    const r = root();
    const channel = (id: number) => ({
      id: uid(id),
      libraryId: uid(5),
      title: `C${id}`,
      videoCount: 1,
      unwatchedCount: 0,
      addedAt: ts,
    });
    await boot(r, {
      fetch: mockFetch({ ...base(), "GET /youtube/channels": page([channel(40)]) }),
    });
    await until(() => tile(r, 40));
    button(r, "Select")!.click();
    expect(button(r, "Add to playlist")).toBeUndefined();
    expect(button(r, "Add to station")).toBeUndefined();
    expect(button(r, "Kids' content")).toBeDefined();
  });
});
