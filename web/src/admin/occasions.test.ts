// Component tests: the Occasions pages against a mocked API, on a fixed local day.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { boot } from "./app";
import { setToken } from "./api";
import { uid } from "../api/uid";

type Handler = (body: unknown, url: URL) => unknown;
const calls: { key: string; body: unknown }[] = [];

function mockFetch(routes: Record<string, Handler | unknown>) {
  return async (req: Request) => {
    const url = new URL(req.url, "http://x");
    const key = `${req.method} ${url.pathname.replace("/api/v2", "")}`;
    const body =
      req.method === "GET" || req.method === "DELETE"
        ? undefined
        : await req.json().catch(() => undefined);
    calls.push({ key: `${key}${url.search}`, body });
    const r = routes[key];
    if (r === undefined)
      return new Response(JSON.stringify({ code: "not_found", message: `no mock ${key}` }), {
        status: 404,
        headers: { "content-type": "application/json" },
      });
    const out = typeof r === "function" ? (r as Handler)(body, url) : r;
    if (out instanceof Response) return out;
    if (out === null) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(out), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
}

const adult = {
  id: uid(1),
  kind: "adult",
  name: "Ada",
  email: "ada@example.com",
  isAdmin: true,
  householdId: uid(1),
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: "2026-01-01T00:00:00Z",
  modifiedAt: "2026-01-01T00:00:00Z",
};
const device = {
  id: uid(9),
  name: "Admin web",
  platform: "web",
  accountId: uid(1),
  householdId: uid(1),
  createdAt: "2026-01-01T00:00:00Z",
};
const page = (items: unknown[]) => ({ items });
const err = (status: number, code: string, message: string) =>
  new Response(JSON.stringify({ code, message }), {
    status,
    headers: { "content-type": "application/json" },
  });
const settle = async () => {
  for (let i = 0; i < 16; i++) await new Promise((r) => setTimeout(r, 0));
};
const click = (el: Element | null | undefined) => (el as HTMLElement).click();
const byText = (sel: string, text: string) =>
  [...document.querySelectorAll(sel)].find((e) => e.textContent?.trim() === text) as
    HTMLElement | undefined;
const control = (label: string) =>
  [...document.querySelectorAll("label")].find((l) => l.textContent === label)
    ?.control as HTMLInputElement;
const aria = <T extends HTMLElement>(label: string) =>
  document.querySelector<T>(`[aria-label="${label}"]`)!;
const set = (el: HTMLInputElement | HTMLSelectElement, value: string) => {
  el.value = value;
  el.dispatchEvent(
    new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }),
  );
};
const submit = () =>
  document.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
const alertText = () => document.querySelector("[role=alert]:not([hidden])")?.textContent;
const body = (key: string) => calls.find((c) => c.key === key)!.body;

const holidays = [
  {
    key: "christmas",
    name: "Christmas",
    colors: ["#1F6B45", "#B3263E", "#E0B252"],
    dates: ["2026-12-25", "2027-12-25"],
  },
  { key: "easter", name: "Easter", dates: ["2027-03-28"] },
  { key: "halloween", name: "Halloween", dates: ["2026-10-31", "2027-10-31"] },
  { key: "nye", name: "New Year's Eve", dates: ["2026-12-31", "2027-12-31"] },
  { key: "thanksgiving", name: "Thanksgiving (US)", dates: ["2026-11-26", "2027-11-25"] },
  { key: "valentine", name: "Valentine's Day", dates: ["2026-02-14", "2027-02-14"] },
];
const occ = (id: number, name: string, extra: object) => ({
  id: uid(id),
  name,
  daysBefore: 0,
  daysAfter: 0,
  keywordIds: [],
  phrases: [],
  ...extra,
});
const christmas = occ(1, "Christmas", {
  holiday: "christmas",
  daysBefore: 40,
  daysAfter: 5,
  keywordIds: [207317, 65],
  phrases: ["Ho, Ho, Ho!", "Merry Movies"],
  colors: ["#1f6b45", "#b3263e", "#e0b252"],
});
const phrases = () =>
  [...document.querySelectorAll(".phrase")].map((k) => k.firstChild?.textContent);
const swatches = () =>
  [1, 2, 3].map(
    (n) => document.querySelector<HTMLInputElement>(`[aria-label="Color ${n}"]`)?.value,
  );
const six = [
  occ(6, "Halloween", { month: 10, day: 31, daysBefore: 30, daysAfter: 1 }),
  occ(5, "Valentine's Day", { holiday: "valentine", daysBefore: 10 }),
  occ(4, "Snow days", { month: 1, day: 15, daysBefore: 14, daysAfter: 30 }),
  occ(3, "Thanksgiving", { holiday: "thanksgiving", daysBefore: 7, daysAfter: 1 }),
  occ(2, "New Year's Eve", { holiday: "nye", daysBefore: 40, daysAfter: 1 }),
  christmas,
];
const title = (id: number, name: string, kind = "movie", year = 2020) => ({
  id: uid(id),
  posterUrl: `/api/v2/artwork/w500/${uid(id)}.jpg`,
  libraryId: uid(1),
  kind,
  parsedTitle: name.toLowerCase(),
  metadata: { provider: "tmdb", status: "matched", title: name, releaseDate: `${year}-01-01` },
  addedAt: "",
  modifiedAt: "",
});
const member = (id: number, name: string, via = "keyword", kind = "movie", year = 2020) => ({
  item: title(id, name, kind, year),
  via,
});

let occasions: unknown[];
let members: Record<number, unknown[]>;
let extra: Record<string, Handler | unknown>;
const routes = () => {
  setToken("t");
  return mockFetch({
    "GET /health": { status: "ok", version: "1.2.3", setupRequired: false, serverName: "Den" },
    "GET /me": {
      device,
      account: adult,
      activeProfileId: uid(1),
      household: { id: uid(1), name: "Ada", adultId: uid(1), createdAt: "" },
      profiles: [adult],
    },
    "GET /holidays": page(holidays),
    "GET /occasions": () => page(occasions),
    ...Object.fromEntries(
      [1, 2, 3, 4, 5, 6, 7].flatMap((n) => [
        [
          `GET /occasions/${uid(n)}`,
          () => (occasions as { id: string }[]).find((o) => o.id === uid(n)),
        ],
        [`GET /occasions/${uid(n)}/members`, () => page(members[n] ?? [])],
      ]),
    ),
    ...extra,
  });
};

let root: HTMLElement;
beforeEach(() => {
  // Only the clock: the tests wait on real timers.
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2026, 10, 22, 9, 30));
  calls.length = 0;
  document.body.replaceChildren((root = document.createElement("div")));
  localStorage.clear();
  location.hash = "";
  occasions = six;
  members = { 1: [], 2: [], 3: [], 4: [], 5: [], 6: [] };
  extra = {};
});
afterEach(() => vi.useRealTimers());

const open = async (hash: string) => {
  location.hash = hash;
  await boot(root, { fetch: routes() });
  await settle();
};

describe("occasions list", () => {
  it("sits after Stations in the nav", async () => {
    await open("#/occasions");
    const nav = [...root.querySelectorAll("nav a")].map((a) => a.textContent);
    expect(nav.indexOf("Occasions")).toBe(nav.indexOf("Stations") + 1);
    expect(document.title).toBe("Occasions - Rabbit Ears Admin");
  });

  it("lists active occasions first, then by next start, each with its window and count", async () => {
    members[1] = Array.from({ length: 3 }, (_, i) => member(10 + i, `Film ${i}`));
    members[6] = [member(20, "Carved")];
    await open("#/occasions");
    expect(root.textContent).toContain("6 occasions · 3 active today, Sun, Nov 22");
    const rows = [...root.querySelectorAll("tbody tr")].map((tr) =>
      [...tr.querySelectorAll("td")].map((td) => td.textContent),
    );
    expect(rows.map((r) => r[0])).toEqual([
      "Christmas",
      "Thanksgiving",
      "New Year's Eve",
      "Snow days",
      "Valentine's Day",
      "Halloween",
    ]);
    expect(rows[0]).toEqual([
      "Christmas",
      "ChristmasHoliday · Fri, Dec 25",
      "40 days before, 5 afterNov 15 – Dec 30",
      "3",
      "Active",
      "Edit",
    ]);
    expect(rows[3].slice(1)).toEqual([
      "January 15Fixed date",
      "14 days before, 30 afterJan 1 – Feb 14",
      "0",
      "Starts Jan 1",
      "Edit",
    ]);
    expect(rows[4][4]).toBe("Starts Feb 4");
    expect(rows[5][4]).toBe("Starts Oct 1");
    expect(rows[5][3]).toBe("1");
    const edit = root.querySelector<HTMLAnchorElement>("a[aria-label='Edit Christmas']")!;
    expect(edit.getAttribute("href")).toBe(`#/occasions/${uid(1)}`);
    expect(byText("a", "Add occasion")!.getAttribute("href")).toBe("#/occasions/new");
  });

  it("counts every page of members", async () => {
    extra[`GET /occasions/${uid(1)}/members`] = (_: unknown, url: URL) =>
      url.searchParams.get("offset") === "0"
        ? { items: [member(1, "A"), member(2, "B")], nextOffset: 2 }
        : { items: [member(3, "C")] };
    await open("#/occasions");
    expect(root.querySelector("tbody tr")!.querySelectorAll("td")[3].textContent).toBe("3");
  });

  it("says when a holiday has no date this year, and still names its next start", async () => {
    occasions = [occ(7, "Spring", { holiday: "easter", daysBefore: 7 })];
    await open("#/occasions");
    const cells = [...root.querySelectorAll("tbody td")].map((td) => td.textContent);
    expect(cells[1]).toBe("EasterNo date for 2026");
    expect(cells[4]).toBe("Starts Mar 21");
  });

  it("says so when a holiday has no date to come", async () => {
    occasions = [occ(7, "Lost", { holiday: "gone" })];
    await open("#/occasions");
    const cells = [...root.querySelectorAll("tbody td")].map((td) => td.textContent);
    expect(cells[1]).toBe("goneNo date for 2026");
    expect(cells[4]).toBe("No date for 2026");
  });

  it("shows an empty state", async () => {
    occasions = [];
    await open("#/occasions");
    expect(root.textContent).toContain("No occasions yet.");
    expect(root.querySelector("table")).toBeNull();
  });
});

describe("new occasion", () => {
  beforeEach(() => {
    extra["POST /occasions"] = (b: object) => {
      const made = occ(7, "x", b);
      occasions = [...occasions, made];
      return made;
    };
  });

  it("saves a holiday anchor and moves to the occasion's page", async () => {
    await open("#/occasions/new");
    expect(root.querySelector("h1")!.textContent).toBe("New occasion");
    expect(document.querySelector("section[aria-label=Titles]")).toBeNull();
    expect(control("Holiday").checked).toBe(true);
    expect(aria<HTMLSelectElement>("Month").disabled).toBe(true);
    set(control("Name"), "  Yule ");
    set(aria<HTMLSelectElement>("Choose a holiday"), "christmas");
    set(control("Days before"), "40");
    set(control("Days after"), "5");
    submit();
    await settle();
    expect(body("POST /occasions")).toEqual({
      name: "Yule",
      holiday: "christmas",
      daysBefore: 40,
      daysAfter: 5,
      keywordIds: [],
    });
    expect(location.hash).toBe(`#/occasions/${uid(7)}`);
  });

  it("saves a fixed month and day, with keyword ids", async () => {
    await open("#/occasions/new");
    click(control("Fixed date"));
    expect(aria<HTMLSelectElement>("Choose a holiday").disabled).toBe(true);
    expect(aria<HTMLSelectElement>("Month").disabled).toBe(false);
    set(control("Name"), "Spooky");
    set(aria<HTMLSelectElement>("Month"), "10");
    set(aria<HTMLInputElement>("Day"), "31");
    set(aria<HTMLInputElement>("Keyword id to add"), "3335");
    click(byText("button", "Add"));
    submit();
    await settle();
    expect(body("POST /occasions")).toEqual({
      name: "Spooky",
      month: 10,
      day: 31,
      daysBefore: 0,
      daysAfter: 0,
      keywordIds: [3335],
    });
  });

  it("shows the window under the days, and the holiday's date beside the choice", async () => {
    await open("#/occasions/new");
    set(aria<HTMLSelectElement>("Choose a holiday"), "christmas");
    set(control("Days before"), "40");
    set(control("Days after"), "5");
    expect(root.textContent).toContain("This year: Fri, Dec 25, 2026");
    expect(root.textContent).toContain("Shows from Sun, Nov 15 through Wed, Dec 30.");
    click(control("Fixed date"));
    set(aria<HTMLSelectElement>("Month"), "1");
    set(aria<HTMLInputElement>("Day"), "5");
    expect(root.textContent).toContain("Shows from Thu, Nov 26 through Sun, Jan 10.");
    set(aria<HTMLSelectElement>("Month"), "2");
    set(aria<HTMLInputElement>("Day"), "29");
    expect(root.textContent).toContain("Shows from Thu, Jan 20, 2028 through Sun, Mar 5, 2028.");
  });

  it("names a holiday year without a date", async () => {
    await open("#/occasions/new");
    set(aria<HTMLSelectElement>("Choose a holiday"), "easter");
    expect(root.textContent).toContain("This year: No date for 2026");
  });

  it.each([
    ["an empty name", () => set(control("Name"), "  "), "Name must be 1 to 100 characters."],
    [
      "a name over 100 characters",
      () => set(control("Name"), "x".repeat(101)),
      "Name must be 1 to 100 characters.",
    ],
    [
      "a window of 91 days before",
      () => set(control("Days before"), "91"),
      "Days before must be a whole number from 0 to 90.",
    ],
    [
      "a fractional window after",
      () => set(control("Days after"), "1.5"),
      "Days after must be a whole number from 0 to 90.",
    ],
    [
      "a blank window after",
      () => set(control("Days after"), ""),
      "Days after must be a whole number from 0 to 90.",
    ],
    [
      "a day the month lacks",
      () => {
        click(control("Fixed date"));
        set(aria<HTMLSelectElement>("Month"), "4");
        set(aria<HTMLInputElement>("Day"), "31");
      },
      "Day must be a day of that month.",
    ],
  ])("refuses %s without asking the server", async (_, change, message) => {
    await open("#/occasions/new");
    set(control("Name"), "Valid");
    change();
    submit();
    await settle();
    expect(alertText()).toBe(message);
    expect(calls.some((c) => c.key === "POST /occasions")).toBe(false);
  });

  it("takes a keyword id once, as a whole number above zero", async () => {
    await open("#/occasions/new");
    const add = (v: string) => {
      set(aria<HTMLInputElement>("Keyword id to add"), v);
      click(byText("button", "Add"));
    };
    add("65");
    add("65");
    expect(alertText()).toBe("Keyword 65 is already listed.");
    add("1.5");
    expect(alertText()).toBe("A keyword id is a whole number above 0.");
    add("0");
    expect(alertText()).toBe("A keyword id is a whole number above 0.");
    add("");
    expect(alertText()).toBe("A keyword id is a whole number above 0.");
    add("207317");
    expect(alertText()).toBeUndefined();
    expect([...document.querySelectorAll(".kw")].map((k) => k.firstChild?.textContent)).toEqual([
      "65",
      "207317",
    ]);
    click(aria("Remove keyword 65"));
    expect([...document.querySelectorAll(".kw")].map((k) => k.firstChild?.textContent)).toEqual([
      "207317",
    ]);
  });

  it("sends phrases and colors only once given, so a holiday's own fill the rest", async () => {
    await open("#/occasions/new");
    set(control("Name"), "Yule");
    set(aria<HTMLInputElement>("Phrase to add"), "  Yuletide  ");
    const e = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    aria<HTMLInputElement>("Phrase to add").dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    click(byText("button", "Add colors"));
    expect(swatches().every((c) => /^#[0-9a-f]{6}$/i.test(c!))).toBe(true);
    submit();
    await settle();
    expect(body("POST /occasions")).toMatchObject({ phrases: ["Yuletide"], colors: swatches() });
  });

  it("starts Add colors from the chosen holiday's own, else from neutral ones", async () => {
    await open("#/occasions/new");
    set(aria<HTMLSelectElement>("Choose a holiday"), "christmas");
    click(byText("button", "Add colors"));
    expect(swatches().map((c) => c?.toLowerCase())).toEqual(["#1f6b45", "#b3263e", "#e0b252"]);
    click(byText("button", "Clear colors"));
    click(control("Fixed date"));
    click(byText("button", "Add colors"));
    expect(swatches()).toEqual(["#2e6b8a", "#6b3a2e", "#5b6b3f"]);
  });

  it("takes a phrase once ignoring case, of 1 to 60 characters", async () => {
    await open("#/occasions/new");
    const add = (v: string) => {
      set(aria<HTMLInputElement>("Phrase to add"), v);
      click(byText("button", "Add phrase"));
    };
    add("Merry Movies");
    add("merry movies ");
    expect(alertText()).toBe("“merry movies” is already listed.");
    add("   ");
    expect(alertText()).toBe("A phrase is 1 to 60 characters.");
    add("x".repeat(61));
    expect(alertText()).toBe("A phrase is 1 to 60 characters.");
    add("x".repeat(60));
    expect(alertText()).toBeUndefined();
    expect(phrases()).toEqual(["Merry Movies", "x".repeat(60)]);
  });

  it("holds an occasion to 200 phrases", async () => {
    await open("#/occasions/new");
    for (let i = 1; i <= 201; i++) {
      set(aria<HTMLInputElement>("Phrase to add"), `P${i}`);
      click(byText("button", "Add phrase"));
    }
    expect(phrases()).toHaveLength(200);
    expect(alertText()).toBe("An occasion holds at most 200 phrases.");
  });

  it("holds an occasion to 100 keywords", async () => {
    await open("#/occasions/new");
    for (let i = 1; i <= 101; i++) {
      set(aria<HTMLInputElement>("Keyword id to add"), String(i));
      click(byText("button", "Add"));
    }
    expect(document.querySelectorAll(".kw")).toHaveLength(100);
    expect(alertText()).toBe("An occasion holds at most 100 keywords.");
  });

  it("adds a keyword on Enter without saving the form", async () => {
    await open("#/occasions/new");
    const input = aria<HTMLInputElement>("Keyword id to add");
    set(input, "65");
    const e = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    input.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(document.querySelectorAll(".kw")).toHaveLength(1);
  });

  it.each([
    [400, "bad_request", "daysBefore: must be at most 90"],
    [409, "conflict", "an occasion with that name already exists"],
  ])("shows the server's %s in the form", async (status, code, message) => {
    extra["POST /occasions"] = () => err(status, code, message);
    await open("#/occasions/new");
    set(control("Name"), "Christmas");
    submit();
    await settle();
    expect(alertText()).toBe(message);
    expect(location.hash).toBe("#/occasions/new");
  });
});

describe("edit occasion", () => {
  beforeEach(() => {
    members[1] = [
      member(11, "The Snow Globe Inn", "keyword", "movie", 2019),
      member(12, "Tinsel Street", "keyword", "show", 2023),
      member(13, "A Lantern for December", "added", "movie", 2022),
    ];
  });

  it("fills the form from the occasion and says it is active", async () => {
    await open(`#/occasions/${uid(1)}`);
    expect(root.querySelector("h1")!.textContent).toBe("Christmas");
    expect(root.textContent).toContain("Active today");
    expect(root.textContent).toContain("Shows Nov 15 – Dec 30");
    expect(control("Name").value).toBe("Christmas");
    expect(control("Holiday").checked).toBe(true);
    expect(aria<HTMLSelectElement>("Choose a holiday").value).toBe("christmas");
    expect(control("Days before").value).toBe("40");
    expect(control("Days after").value).toBe("5");
    expect([...document.querySelectorAll(".kw")].map((k) => k.firstChild?.textContent)).toEqual([
      "207317",
      "65",
    ]);
    expect(byText("a", "Occasions")!.getAttribute("href")).toBe("#/occasions");
    expect(byText("a", "Cancel")!.getAttribute("href")).toBe("#/occasions");
    expect(document.querySelector("nav a[aria-current=page]")!.textContent).toBe("Occasions");
  });

  it("fills a fixed anchor", async () => {
    await open(`#/occasions/${uid(4)}`);
    expect(control("Fixed date").checked).toBe(true);
    expect(aria<HTMLSelectElement>("Month").value).toBe("1");
    expect(aria<HTMLInputElement>("Day").value).toBe("15");
    expect(aria<HTMLSelectElement>("Choose a holiday").disabled).toBe(true);
    expect(root.textContent).not.toContain("Active today");
    expect(root.textContent).toContain("Shows Jan 1 – Feb 14");
  });

  it("replaces the occasion with the changed form, switching anchor kinds", async () => {
    extra[`PUT /occasions/${uid(1)}`] = (b: object) => occ(1, "x", b);
    await open(`#/occasions/${uid(1)}`);
    click(control("Fixed date"));
    set(aria<HTMLSelectElement>("Month"), "12");
    set(aria<HTMLInputElement>("Day"), "24");
    set(control("Days after"), "7");
    click(aria("Remove keyword 207317"));
    submit();
    await settle();
    expect(body(`PUT /occasions/${uid(1)}`)).toEqual({
      name: "Christmas",
      month: 12,
      day: 24,
      daysBefore: 40,
      daysAfter: 7,
      keywordIds: [65],
      phrases: ["Ho, Ho, Ho!", "Merry Movies"],
      colors: ["#1f6b45", "#b3263e", "#e0b252"],
    });
    expect(root.textContent).toContain("Saved");
  });

  it("fills the phrases and colors, and replaces them as edited", async () => {
    extra[`PUT /occasions/${uid(1)}`] = (b: object) => occ(1, "x", b);
    await open(`#/occasions/${uid(1)}`);
    expect(phrases()).toEqual(["Ho, Ho, Ho!", "Merry Movies"]);
    expect(swatches()).toEqual(["#1f6b45", "#b3263e", "#e0b252"]);
    click(aria("Remove phrase Ho, Ho, Ho!"));
    set(aria<HTMLInputElement>("Phrase to add"), "Sleigh Bells");
    click(byText("button", "Add phrase"));
    set(aria<HTMLInputElement>("Color 2"), "#000000");
    submit();
    await settle();
    expect(body(`PUT /occasions/${uid(1)}`)).toMatchObject({
      phrases: ["Merry Movies", "Sleigh Bells"],
      colors: ["#1f6b45", "#000000", "#e0b252"],
    });
    click(byText("button", "Clear colors"));
    expect(swatches()).toEqual([undefined, undefined, undefined]);
    calls.length = 0;
    submit();
    await settle();
    expect(body(`PUT /occasions/${uid(1)}`)).toMatchObject({
      phrases: ["Merry Movies", "Sleigh Bells"],
      colors: [],
    });
  });

  it("sends no colors and no phrases for an occasion with none", async () => {
    extra[`PUT /occasions/${uid(4)}`] = (b: object) => occ(4, "x", b);
    await open(`#/occasions/${uid(4)}`);
    expect(phrases()).toEqual([]);
    expect(swatches()).toEqual([undefined, undefined, undefined]);
    submit();
    await settle();
    expect(body(`PUT /occasions/${uid(4)}`)).toMatchObject({ phrases: [], colors: [] });
  });

  it("shows a duplicate name from the server", async () => {
    extra[`PUT /occasions/${uid(1)}`] = () => err(409, "conflict", "name taken");
    await open(`#/occasions/${uid(1)}`);
    submit();
    await settle();
    expect(alertText()).toBe("name taken");
  });

  it("lists the members by source, with a count summary", async () => {
    await open(`#/occasions/${uid(1)}`);
    const sec = document.querySelector("section[aria-label=Titles]")!;
    expect(sec.querySelector("h2")!.textContent).toBe("Titles32 by keyword · 1 added");
    const rows = [...sec.querySelectorAll("tbody tr")].map((tr) =>
      [...tr.querySelectorAll("td")].map((td) => td.textContent),
    );
    expect(rows).toEqual([
      ["The Snow Globe Inn", "Movie", "2019", "Keyword", "×"],
      ["Tinsel Street", "Show", "2023", "Keyword", "×"],
      ["A Lantern for December", "Movie", "2022", "Added", "×"],
    ]);
    expect(sec.querySelector("tbody tr i")!.getAttribute("data-art")).toBe(
      `art:/api/v2/artwork/w500/${uid(11)}.jpg`,
    );
    expect(byText("button", "Show all 3")).toBeUndefined();
  });

  it("shows the first ten members until told to show all", async () => {
    members[1] = Array.from({ length: 12 }, (_, i) => member(30 + i, `Film ${i}`));
    await open(`#/occasions/${uid(1)}`);
    expect(document.querySelectorAll("section[aria-label=Titles] tbody tr")).toHaveLength(10);
    click(byText("button", "Show all 12"));
    expect(document.querySelectorAll("section[aria-label=Titles] tbody tr")).toHaveLength(12);
    expect(byText("button", "Show all 12")).toBeUndefined();
  });

  it("removes a member and reads the list and counts again", async () => {
    extra[`DELETE /occasions/${uid(1)}/members/${uid(13)}`] = () => {
      members[1] = members[1].slice(0, 2);
      return null;
    };
    await open(`#/occasions/${uid(1)}`);
    click(aria("Remove A Lantern for December"));
    await settle();
    expect(calls.some((c) => c.key === `DELETE /occasions/${uid(1)}/members/${uid(13)}`)).toBe(
      true,
    );
    const sec = document.querySelector("section[aria-label=Titles]")!;
    expect(sec.querySelector("h2")!.textContent).toBe("Titles22 by keyword · 0 added");
    expect(sec.querySelectorAll("tbody tr")).toHaveLength(2);
  });

  it("shows a refused removal", async () => {
    extra[`DELETE /occasions/${uid(1)}/members/${uid(13)}`] = () =>
      err(404, "not_found", "no such title");
    await open(`#/occasions/${uid(1)}`);
    click(aria("Remove A Lantern for December"));
    await settle();
    expect(alertText()).toBe("no such title");
  });

  it("searches movies and shows, adds the picked title, and refreshes", async () => {
    extra["GET /admin/search"] = () => ({
      movies: [title(11, "The Snow Globe Inn", "movie", 2019), title(40, "Frosty Nights")],
      shows: [title(41, "Tinsel Town", "show", 2021)],
      episodes: [title(99, "An episode", "episode")],
      people: [],
      artists: [],
      albums: [],
      songs: [],
      channels: [],
      videos: [],
    });
    extra[`PUT /occasions/${uid(1)}/members/${uid(40)}`] = () => {
      members[1] = [...members[1], member(40, "Frosty Nights", "added")];
      return null;
    };
    await open(`#/occasions/${uid(1)}`);
    const q = aria<HTMLInputElement>("Add a movie or show");
    set(q, "fro");
    await new Promise((r) => setTimeout(r, 300));
    expect(calls.find((c) => c.key.startsWith("GET /admin/search"))!.key).toContain("q=fro");
    const found = [...document.querySelectorAll(".found li")].map((li) => li.textContent);
    expect(found).toEqual([
      "The Snow Globe Inn2019 · MovieIn this occasion",
      "Frosty Nights2020 · MovieAdd",
      "Tinsel Town2021 · ShowAdd",
    ]);
    expect(document.querySelector("[aria-label='Add An episode']")).toBeNull();
    click(aria("Add Frosty Nights"));
    await settle();
    expect(calls.some((c) => c.key === `PUT /occasions/${uid(1)}/members/${uid(40)}`)).toBe(true);
    const sec = document.querySelector("section[aria-label=Titles]")!;
    expect(sec.querySelector("h2")!.textContent).toBe("Titles42 by keyword · 2 added");
    expect(sec.querySelectorAll("tbody tr")).toHaveLength(4);
    expect(q.value).toBe("fro");
    expect([...document.querySelectorAll(".found li")].map((li) => li.textContent)).toEqual([
      "The Snow Globe Inn2019 · MovieIn this occasion",
      "Frosty Nights2020 · MovieIn this occasion",
      "Tinsel Town2021 · ShowAdd",
    ]);
  });

  it("says when a search finds nothing", async () => {
    extra["GET /admin/search"] = () => ({
      movies: [],
      shows: [],
      episodes: [],
      people: [],
      artists: [],
      albums: [],
      songs: [],
      channels: [],
      videos: [],
    });
    await open(`#/occasions/${uid(1)}`);
    set(aria<HTMLInputElement>("Add a movie or show"), "zzz");
    await new Promise((r) => setTimeout(r, 300));
    expect(document.querySelector(".found")!.textContent).toBe("No movie or show matches.");
  });

  it("deletes after asking in the page, then returns to the list", async () => {
    extra[`DELETE /occasions/${uid(1)}`] = () => null;
    await open(`#/occasions/${uid(1)}`);
    click(byText("button", "Delete occasion"));
    expect(root.textContent).toContain("Delete Christmas?");
    expect(calls.some((c) => c.key === `DELETE /occasions/${uid(1)}`)).toBe(false);
    click(
      [...document.querySelectorAll("button")]
        .filter((b) => b.textContent === "Delete occasion")
        .at(-1),
    );
    await settle();
    expect(calls.some((c) => c.key === `DELETE /occasions/${uid(1)}`)).toBe(true);
    expect(location.hash).toBe("#/occasions");
  });

  it("offers no delete or titles for a new occasion, and fails on one that is missing", async () => {
    await open("#/occasions/new");
    expect(byText("button", "Delete occasion")).toBeUndefined();
    await open(`#/occasions/${uid(99)}`);
    expect(root.querySelector("[role=alert]")).not.toBeNull();
  });
});
