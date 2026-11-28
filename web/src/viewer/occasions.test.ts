// Home's occasion shelves against a mocked server.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { uid } from "../api/uid";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
    static Events = { ERROR: "e", MANIFEST_PARSED: "m", LEVEL_SWITCHED: "l" };
  },
}));

const ts = "2026-01-01T00:00:00Z";
const json = (b: unknown, status = 200) =>
  new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });
const page = (items: unknown[]) => ({ items });
const acct = {
  id: uid(1),
  kind: "adult",
  name: "Bea",
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  modifiedAt: ts,
};
const title = (id: number, name: string) => ({
  id: uid(id),
  libraryId: uid(1),
  kind: "movie",
  parsedTitle: name,
  addedAt: ts,
  createdAt: ts,
  modifiedAt: ts,
});
const asked: string[] = [];
const server =
  (occasions: () => Response | unknown, extra: Record<string, (u: URL) => unknown> = {}) =>
  async (req: Request) => {
    const url = new URL(req.url, "http://x");
    const key = `${req.method} ${url.pathname.replace("/api/v2", "")}`;
    asked.push(key + url.search);
    const routes: Record<string, () => Response | unknown> = {
      "GET /health": () => ({ status: "ok", version: "1.0.0", setupRequired: false }),
      "GET /me": () => ({
        device: { id: uid(1) },
        account: acct,
        activeProfileId: uid(1),
        household: { id: uid(1), name: "Bea", adultId: uid(1), createdAt: ts },
        profiles: [acct],
      }),
      "GET /items": () => {
        const o = url.searchParams.get("occasionId");
        if (o) return page(o === "7" ? [title(70, "Elf")] : [title(80, "Hocus Pocus")]);
        return page(url.searchParams.get("kind") === "movie" ? [title(10, "Dune")] : []);
      },
      [`GET /profiles/${uid(1)}/occasions`]: occasions,
      [`GET /profiles/${uid(1)}/watch-state`]: () => page([]),
      [`GET /profiles/${uid(1)}/show-progress`]: () => page([]),
      "GET /youtube/videos": () => page([]),
      "GET /stations": () => page([]),
      ...Object.fromEntries(Object.entries(extra).map(([k, f]) => [k, () => f(url)])),
    };
    const r = routes[key];
    if (!r) return json({ code: "not_found", message: key }, 404);
    const out = r();
    return out instanceof Response ? out : json(out);
  };
const active = (id: number, name: string) => ({
  id: uid(id),
  name,
  anchorDate: "2026-12-25",
  firstDate: "2026-11-25",
  lastDate: "2026-12-31",
});
const home = async (occasions: () => Response | unknown) => {
  location.hash = "#/";
  await new Promise((r) => setTimeout(r, 0));
  const r = document.body.appendChild(document.createElement("div"));
  await boot(r, { fetch: server(occasions) });
  await vi.waitFor(() => expect(r.textContent).toContain("Recently added"));
  return [...r.querySelectorAll("h2")].map((h) => h.textContent);
};

beforeEach(() => {
  asked.length = 0;
  document.body.replaceChildren();
  setToken("t");
});
afterEach(() => vi.useRealTimers());

describe("Home occasions", () => {
  it("shelves each occasion in the server's order before Recently added", async () => {
    const heads = await home(() => page([active(8, "Halloween"), active(7, "Christmas")]));
    expect(heads).toEqual(["Halloween", "Christmas", "Recently added"]);
    expect(asked).toContain(`GET /items?occasionId=${uid(8)}&sort=daily&limit=14`);
  });

  it("sends the device's calendar date", async () => {
    vi.useFakeTimers({ toFake: ["Date"], now: new Date(2026, 11, 5, 23, 59) });
    await home(() => page([]));
    expect(asked).toContain(`GET /profiles/${uid(1)}/occasions?date=2026-12-05`);
  });

  it("adds nothing when none is in season", async () => {
    expect(await home(() => page([]))).toEqual(["Recently added"]);
  });

  it("keeps Home intact when the request fails", async () => {
    expect(await home(() => json({ code: "internal", message: "x" }, 500))).toEqual([
      "Recently added",
    ]);
  });

  it("titles a row with its headline, else its name, and links it to the occasion's page", async () => {
    const heads = await home(() =>
      page([{ ...active(8, "Halloween"), headline: "Boo!" }, active(7, "Christmas")]),
    );
    expect(heads).toEqual(["Boo!", "Christmas", "Recently added"]);
    const links = [...document.querySelectorAll(".rowhead")]
      .slice(0, 2)
      .map((r) => [...r.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("href")]));
    expect(links).toEqual([
      [["See all", `#/occasion/${uid(8)}`]],
      [["See all", `#/occasion/${uid(7)}`]],
    ]);
  });
});

describe("occasion page", () => {
  const occasion = (colors?: string[]) => ({
    id: uid(7),
    name: "Christmas",
    holiday: "christmas",
    daysBefore: 30,
    daysAfter: 5,
    keywordIds: [],
    phrases: ["Ho, Ho, Ho!"],
    ...(colors ? { colors } : {}),
  });
  const open = async (o: ReturnType<typeof occasion>) => {
    location.hash = `#/occasion/${uid(7)}`;
    await new Promise((r) => setTimeout(r, 0));
    const r = document.body.appendChild(document.createElement("div"));
    await boot(r, {
      fetch: server(() => page([]), {
        [`GET /occasions/${uid(7)}`]: () => o,
        "GET /items": (u) =>
          u.searchParams.get("offset") === "200"
            ? page([title(71, "Krampus")])
            : { items: [title(70, "Elf")], nextOffset: 200 },
      }),
    });
    await vi.waitFor(() => expect(r.querySelector("h1")).toBeTruthy());
    return r;
  };

  it("is titled with the name and lists every member, page after page", async () => {
    const r = await open(occasion());
    expect(r.querySelector("h1")!.textContent).toBe("Christmas");
    expect([...r.querySelectorAll(".grid a.poster")].map((a) => a.getAttribute("href"))).toEqual([
      `#/movie/${uid(70)}`,
      `#/movie/${uid(71)}`,
    ]);
    expect(asked).toContain(`GET /items?occasionId=${uid(7)}&limit=200&offset=0`);
  });

  it("washes the page in the occasion's three colors", async () => {
    const r = await open(occasion(["#1F6B45", "#B3263E", "#E0B252"]));
    const wash = r.querySelector<HTMLElement>(".wash")!;
    expect(wash.getAttribute("aria-hidden")).toBe("true");
    expect(["--w1", "--w2", "--w3"].map((v) => wash.style.getPropertyValue(v))).toEqual([
      "#1F6B45",
      "#B3263E",
      "#E0B252",
    ]);
  });

  it("keeps the plain background without colors", async () => {
    const r = await open(occasion());
    expect(r.querySelector(".wash")).toBeNull();
  });
});
