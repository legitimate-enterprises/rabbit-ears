// Component tests for shared playlists: the real pages and generated client
// against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { at, calls, json, mockFetch, none, root, until } from "./testSupport";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label);
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
  account: acct(1, "Helga"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Helga", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")],
});
const page = (items: unknown[]) => ({ items });
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  [`GET /profiles/${uid(1)}/watch-state`]: page([]),
  [`GET /profiles/${uid(2)}/watch-state`]: page([]),
  [`GET /profiles/${uid(1)}/show-progress`]: page([]),
  [`GET /profiles/${uid(2)}/show-progress`]: page([]),
  "GET /genres": page([]),
  "GET /items": page([]),
});
const pl = (id: number, name: string, extra = {}) => ({
  id: uid(id),
  profileId: uid(1),
  access: "owner",
  name,
  kind: "movies",
  itemCount: 1,
  durationMs: 0,
  createdAt: ts,
  modifiedAt: ts,
  ...extra,
});
const entry = {
  id: uid(1),
  playlistId: uid(5),
  position: 0,
  itemCount: 1,
  durationMs: 0,
  addedAt: ts,
  kind: "movie",
  itemId: uid(30),
  item: {
    id: uid(30),
    libraryId: uid(1),
    kind: "movie",
    parsedTitle: "Dune",
    addedAt: ts,
    createdAt: ts,
    modifiedAt: ts,
  },
};
const share = (profileId: number, name: string, access: string, kind = "adult", extra = {}) => ({
  playlistId: uid(5),
  profileId: uid(profileId),
  name,
  kind,
  access,
  createdAt: ts,
  ...extra,
});

beforeEach(() => {
  calls.length = 0;
  setToken("tok");
  document.body.replaceChildren();
});

describe("a playlist shared with the profile", () => {
  it("says who shared it and offers no edits on read access", async () => {
    await at(`#/playlist/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}`]: pl(5, "Weekend", {
          profileId: uid(7),
          access: "read",
          ownerName: "Ann",
        }),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/entries`]: page([
          entry,
          { ...entry, id: uid(2), position: 1 },
        ]),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/play-order`]: page([]),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector(".meta")!.textContent).toContain("Shared by Ann");
    for (const label of ["Rename", "Delete", "Share"]) expect(button(r, label)).toBeUndefined();
    expect(r.querySelector("[aria-label='Remove Dune']")).toBeNull();
    expect(r.textContent).not.toContain("Drag a row to reorder.");
    expect(button(r, "Play")).toBeDefined();
  });

  it("edits entries on write access but leaves the playlist itself to its owner", async () => {
    await at(`#/playlist/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}`]: pl(5, "Weekend", {
          profileId: uid(7),
          access: "write",
          ownerName: "Ann",
        }),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/entries`]: page([entry]),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/play-order`]: page([]),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector("[aria-label='Remove Dune']")).not.toBeNull();
    for (const label of ["Rename", "Delete", "Share"]) expect(button(r, label)).toBeUndefined();
  });

  it("lists shared playlists for a kid, marked, with no New playlist", async () => {
    await at("#/movies?view=playlists");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(2),
        [`GET /profiles/${uid(2)}/playlists`]: page([
          pl(5, "Cartoons", { profileId: uid(1), access: "read", ownerName: "Helga" }),
        ]),
      }),
    });
    await until(() => r.querySelector(".poster"));
    expect(r.querySelector(".poster")!.textContent).toContain("Shared by Helga");
    expect(button(r, "New playlist")).toBeUndefined();
    expect(button(r, "Playlists")).toBeDefined();
  });
});

describe("the Shared badge", () => {
  it("marks shared playlists' tiles and none other", async () => {
    await at("#/movies?view=playlists");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /profiles/${uid(1)}/playlists`]: page([
          pl(5, "Weekend", { shared: true }),
          pl(6, "Solo", { shared: false }),
          pl(7, "Stored"),
        ]),
      }),
    });
    await until(() => r.querySelectorAll(".poster").length === 3);
    const [weekend, solo, stored] = r.querySelectorAll(".poster");
    expect(weekend.querySelector(".art [role=img][aria-label=Shared]")).not.toBeNull();
    expect(weekend.querySelector(".art[aria-hidden]")).toBeNull();
    expect(solo.querySelector("[aria-label=Shared]")).toBeNull();
    expect(stored.querySelector("[aria-label=Shared]")).toBeNull();
  });

  it("marks a shared playlist's artwork on its page", async () => {
    await at(`#/playlist/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}`]: pl(5, "Weekend", { shared: true }),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/entries`]: page([]),
        [`GET /profiles/${uid(1)}/playlists/${uid(5)}/play-order`]: page([]),
      }),
    });
    await until(() => r.querySelector(".detail"));
    expect(r.querySelector(".detail .art [aria-label=Shared]")).not.toBeNull();
  });
});

const hshare = (householdId: number, name: string, access: string) => ({
  playlistId: uid(5),
  householdId: uid(householdId),
  name,
  access,
  createdAt: ts,
});
const households = [
  { id: uid(1), name: "Helga's household", profiles: 3 },
  { id: uid(4), name: "Ann's household", profiles: 1 },
];
const owner = (routes: Record<string, unknown>) => ({
  ...base(),
  [`GET /profiles/${uid(1)}/playlists/${uid(5)}`]: pl(5, "Weekend"),
  [`GET /profiles/${uid(1)}/playlists/${uid(5)}/entries`]: page([]),
  [`GET /profiles/${uid(1)}/playlists/${uid(5)}/play-order`]: page([]),
  [`GET /profiles/${uid(1)}/share-households`]: { items: households },
  [`GET /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`]: page([]),
  [`GET /profiles/${uid(1)}/playlists/${uid(5)}/shares`]: page([]),
  [`GET /profiles/${uid(1)}/share-candidates`]: page([]),
  ...routes,
});
const openShare = async (routes: Record<string, unknown>) => {
  await at(`#/playlist/${uid(5)}`);
  const r = root();
  await boot(r, { fetch: mockFetch(owner(routes)) });
  await until(() => button(r, "Share"));
  button(r, "Share")!.click();
  const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
  await until(() => dlg.querySelector("[aria-label=Households] li"));
  return dlg;
};
const rows = (dlg: Element, section: string) =>
  [...dlg.querySelectorAll(`[aria-label=${section}] li`)].map((li) => [
    li.querySelector("b")!.firstChild!.textContent,
    li.querySelector("b .sub")?.textContent ?? "",
    li.querySelector("select")!.value,
  ]);
const choose = (dlg: Element, name: string, value: string) => {
  const s = dlg.querySelector<HTMLSelectElement>(`[aria-label="Access for ${name}"]`)!;
  s.value = value;
  s.dispatchEvent(new Event("change"));
  return s;
};
const sent = (key: string) => calls.find((c) => c.key === key);

describe("the owner's Share sheet", () => {
  it("titles the sheet Share over the playlist's name and closes on Done", async () => {
    const dlg = await openShare({});
    expect(dlg.querySelector("h1")!.textContent).toBe("Share");
    expect(dlg.querySelector("h1 + .sub")!.textContent).toBe("Weekend");
    const redrawn = vi.fn();
    window.addEventListener("hashchange", redrawn);
    button(dlg, "Done")!.click();
    window.removeEventListener("hashchange", redrawn);
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(redrawn).toHaveBeenCalledOnce();
  });

  it("closes on Escape and redraws the page beneath, as Done does", async () => {
    const dlg = await openShare({});
    const redrawn = vi.fn();
    window.addEventListener("hashchange", redrawn);
    dlg.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    window.removeEventListener("hashchange", redrawn);
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(redrawn).toHaveBeenCalledOnce();
  });

  it("lists every household with its profiles and access, under a caption", async () => {
    const dlg = await openShare({
      [`GET /profiles/${uid(1)}/share-households`]: {
        items: [...households, { id: uid(6), name: "Big house", profiles: 2 }],
      },
      [`GET /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`]: page([
        hshare(1, "Helga's household", "read"),
        hshare(6, "Big house", "write"),
      ]),
    });
    expect(rows(dlg, "Households")).toEqual([
      ["Helga's household", "3 profiles", "read"],
      ["Ann's household", "1 profile", ""],
      ["Big house", "2 profiles", "write"],
    ]);
    const opts = dlg.querySelector<HTMLSelectElement>(
      '[aria-label="Access for Ann\'s household"]',
    )!;
    expect([...opts.options].map((o) => o.textContent)).toEqual([
      "Not shared",
      "Can view",
      "Can edit",
    ]);
    expect(dlg.querySelector("[aria-label=Households]")!.textContent).toContain(
      "Everyone in the household can see it.",
    );
  });

  it("lists candidates, then shares beyond them, a kid to view only", async () => {
    const dlg = await openShare({
      [`GET /profiles/${uid(1)}/share-candidates`]: page([
        {
          profileId: uid(2),
          name: "Jason",
          kind: "kid",
          householdName: "Helga's household",
          householdId: uid(1),
        },
        // A household of the same name that is not one of the owner's is not named.
        {
          profileId: uid(9),
          name: "Ola",
          kind: "adult",
          householdName: "Helga's household",
          householdId: uid(99),
        },
        {
          profileId: uid(7),
          name: "Ann",
          kind: "adult",
          householdName: "Ann's household",
          householdId: uid(4),
          color: "#6b3a2e",
        },
      ]),
      [`GET /profiles/${uid(1)}/playlists/${uid(5)}/shares`]: page([
        share(7, "Ann", "write", "adult", { householdId: uid(4), color: "#6b3a2e" }),
        share(3, "Sam", "read", "adult", { householdId: uid(1) }),
        share(2, "Jason", "read", "kid", { householdId: uid(1) }),
      ]),
    });
    await until(() => dlg.querySelector("[aria-label=Profiles] li"));
    expect(rows(dlg, "Profiles")).toEqual([
      ["Jason", "Helga's household · Kid", "read"],
      ["Ola", "", ""],
      ["Ann", "Ann's household", "write"],
      ["Sam", "Helga's household", "read"],
    ]);
    const jason = dlg.querySelector<HTMLSelectElement>("[aria-label='Access for Jason']")!;
    expect([...jason.options].map((o) => o.textContent)).toEqual(["Not shared", "Can view"]);
    const avatars = [...dlg.querySelectorAll<HTMLElement>("[aria-label=Profiles] li .avatar")];
    expect(avatars.map((a) => a.textContent)).toEqual(["J", "O", "A", "S"]);
    expect(avatars[2].style.background).toBe("rgb(107, 58, 46)");
    expect(avatars[0].style.background).toBe("var(--card)");
  });

  it("names no household when the owner is in only one", async () => {
    const dlg = await openShare({
      [`GET /profiles/${uid(1)}/share-households`]: { items: [households[0]] },
      [`GET /profiles/${uid(1)}/share-candidates`]: page([
        {
          profileId: uid(2),
          name: "Jason",
          kind: "kid",
          householdName: "Helga's household",
          householdId: uid(1),
        },
        {
          profileId: uid(3),
          name: "Sam",
          kind: "adult",
          householdName: "Helga's household",
          householdId: uid(1),
        },
      ]),
    });
    await until(() => dlg.querySelector("[aria-label=Profiles] li"));
    expect(rows(dlg, "Profiles").map((r) => r[1])).toEqual(["Kid", ""]);
  });

  it("says so when there is no one else to share with", async () => {
    const dlg = await openShare({});
    await until(() => dlg.querySelector("[aria-label=Profiles] .sub"));
    expect(dlg.querySelector("[aria-label=Profiles]")!.textContent).toContain(
      "No one else to share with yet.",
    );
  });

  it("shares, changes and stops sharing with a household at once", async () => {
    const dlg = await openShare({
      [`POST /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`]: hshare(
        4,
        "Ann's household",
        "read",
      ),
      [`PUT /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(4)}`]: hshare(
        4,
        "Ann's household",
        "write",
      ),
      [`DELETE /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(4)}`]: none,
    });
    choose(dlg, "Ann's household", "read");
    await until(() => sent(`POST /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`));
    expect(sent(`POST /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`)!.body).toEqual({
      householdId: uid(4),
      access: "read",
    });
    choose(dlg, "Ann's household", "write");
    await until(() =>
      sent(`PUT /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(4)}`),
    );
    expect(
      sent(`PUT /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(4)}`)!.body,
    ).toEqual({
      access: "write",
    });
    choose(dlg, "Ann's household", "");
    await until(() =>
      sent(`DELETE /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(4)}`),
    );
  });

  it("shares, changes and stops sharing with a profile at once", async () => {
    const dlg = await openShare({
      [`GET /profiles/${uid(1)}/share-candidates`]: page([
        { profileId: uid(9), name: "Ola", kind: "adult", householdName: "Next door" },
      ]),
      [`POST /profiles/${uid(1)}/playlists/${uid(5)}/shares`]: share(9, "Ola", "write"),
      [`PUT /profiles/${uid(1)}/playlists/${uid(5)}/shares/${uid(9)}`]: share(9, "Ola", "read"),
      [`DELETE /profiles/${uid(1)}/playlists/${uid(5)}/shares/${uid(9)}`]: none,
    });
    await until(() => dlg.querySelector("[aria-label='Access for Ola']"));
    choose(dlg, "Ola", "write");
    await until(() => sent(`POST /profiles/${uid(1)}/playlists/${uid(5)}/shares`));
    expect(sent(`POST /profiles/${uid(1)}/playlists/${uid(5)}/shares`)!.body).toEqual({
      profileId: uid(9),
      access: "write",
    });
    choose(dlg, "Ola", "read");
    await until(() => sent(`PUT /profiles/${uid(1)}/playlists/${uid(5)}/shares/${uid(9)}`));
    expect(sent(`PUT /profiles/${uid(1)}/playlists/${uid(5)}/shares/${uid(9)}`)!.body).toEqual({
      access: "read",
    });
    choose(dlg, "Ola", "");
    await until(() => sent(`DELETE /profiles/${uid(1)}/playlists/${uid(5)}/shares/${uid(9)}`));
  });

  it("holds a row's control while its change is saved", async () => {
    let answer: (r: Response) => void = () => {};
    const dlg = await openShare({
      [`POST /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`]: () =>
        new Promise<Response>((r) => (answer = r)),
    });
    const s = choose(dlg, "Ann's household", "read");
    await until(() => sent(`POST /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`));
    expect(s.disabled).toBe(true);
    expect(
      dlg.querySelector<HTMLSelectElement>('[aria-label="Access for Helga\'s household"]')!
        .disabled,
    ).toBe(false);
    answer(json(hshare(4, "Ann's household", "read"), 201));
    await until(() => !s.disabled);
    expect(s.value).toBe("read");
  });

  it("shows a failed change and puts the control back", async () => {
    const dlg = await openShare({
      [`GET /profiles/${uid(1)}/playlists/${uid(5)}/household-shares`]: page([
        hshare(1, "Helga's household", "read"),
      ]),
      [`PUT /profiles/${uid(1)}/playlists/${uid(5)}/household-shares/${uid(1)}`]: () =>
        json({ code: "conflict", message: "Not now." }, 409),
    });
    const s = choose(dlg, "Helga's household", "write");
    await until(() => dlg.querySelector<HTMLElement>(".err:not([hidden])"));
    expect(dlg.querySelector(".err")!.textContent).toBe("Not now.");
    expect(s.value).toBe("read");
  });
});
