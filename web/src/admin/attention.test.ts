// Component tests: the Needs attention page and its nav item against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
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
const settle = async () => {
  for (let i = 0; i < 16; i++) await new Promise((r) => setTimeout(r, 0));
};
const at = "2026-11-16T08:00:00Z";

const conflicting = {
  id: uid(1),
  libraryId: uid(2),
  itemId: uid(7),
  itemKind: "show",
  title: "The Office",
  reason: "conflicting_identity",
  raisedAt: at,
  evidence: [
    {
      role: "stated",
      provider: "tvdb",
      externalId: "78107",
      path: "/tv/The Office (2001) {tvdb-78107}",
    },
    { role: "current", provider: "tmdb", externalId: "2316", title: "The Office", year: 2005 },
    {
      role: "candidate",
      provider: "tmdb",
      externalId: "2996",
      title: "The Office",
      year: 2001,
      country: "GB",
    },
  ],
};
const tied = {
  id: uid(2),
  libraryId: uid(1),
  itemId: uid(8),
  itemKind: "movie",
  title: "Crash",
  reason: "tied_candidates",
  raisedAt: at,
  evidence: [
    {
      role: "candidate",
      provider: "tmdb",
      externalId: "20",
      title: "Crash",
      year: 1996,
      country: "CA",
    },
    {
      role: "candidate",
      provider: "tmdb",
      externalId: "21",
      title: "Crash",
      year: 2004,
      country: "US",
    },
  ],
};
const moved = {
  id: uid(3),
  libraryId: uid(1),
  itemId: uid(9),
  itemKind: "movie",
  title: "Heat",
  reason: "ambiguous_move",
  raisedAt: at,
  evidence: [
    { role: "stated", path: "/movies/Heat.mkv" },
    { role: "candidate", path: "/movies/Heat (1995).mkv" },
    { role: "candidate", path: "/movies/Heat (1995) alt.mkv" },
  ],
};
const file = (id: number, over: Record<string, unknown>) => ({
  id: uid(id),
  itemId: uid(8),
  libraryId: uid(1),
  sizeBytes: 1,
  fileModifiedAt: at,
  tracks: [],
  chapters: [],
  ...over,
});
const noDuration = file(21, {
  path: "/movies/Election Night.mkv",
  title: "Election Night",
  probedAt: at,
});
const unreadable = file(22, {
  path: "/tv/Old Show/S01E01.avi",
  libraryId: uid(2),
  title: "Pilot",
  probeFailedAt: at,
  probeError:
    "ffprobe /tv/Old Show/S01E01.avi: exit status 1: [avi @ 0x7795018000] header parsing failed\n/tv/Old Show/S01E01.avi: Invalid data found when processing input\n",
});
const missing = file(23, { path: "/movies/Gone.mkv", title: "Gone", missingAt: at });
const run = (status: string, error?: string) => ({
  id: uid(1),
  taskId: uid(1),
  status,
  progress: 0,
  startedAt: at,
  error,
});
const scanMovies = {
  id: uid(2),
  name: "Scan Movies",
  kind: `scan:${uid(1)}`,
  enabled: true,
  createdAt: at,
  lastRun: run("succeeded"),
};
const refresh = {
  id: uid(5),
  name: "Refresh metadata",
  kind: "metadata",
  enabled: true,
  createdAt: at,
  lastRun: run("failed", "TMDB rate limit"),
};
const conversion = {
  id: uid(4),
  mediaFileId: uid(11),
  status: "failed",
  verdict: "video_transcode",
  container: "mp4",
  height: 1080,
  bitrate: 0,
  sizeBytes: 0,
  estimated: true,
  progress: 0,
  downloads: 1,
  error: "ffmpeg exited with status 1",
  createdAt: at,
  finishedAt: at,
};

let reviews: unknown[];
let problems: Record<string, unknown[]>;
let tasks: unknown[];
let conversions: unknown[];
let extra: Record<string, Handler | unknown>;
const counts = () => ({
  identity: reviews.length,
  unstreamable: problems.no_duration.length + problems.unreadable.length,
  missing: problems.missing.length,
  failed:
    tasks.filter((t) => (t as typeof refresh).lastRun?.status === "failed").length +
    conversions.length,
});
const routes = () => {
  setToken("t");
  return mockFetch({
    "GET /health": { status: "ok", version: "1.2.3", setupRequired: false, serverName: "Den" },
    "GET /me": {
      device,
      account: adult,
      activeProfileId: uid(1),
      household: { id: uid(1), name: "Ada", adultId: uid(1), createdAt: at },
      profiles: [adult],
    },
    "GET /attention": () => counts(),
    "GET /identity-reviews": () => page(reviews),
    "GET /media-files": (_: unknown, url: URL) => page(problems[url.searchParams.get("problem")!]),
    "GET /tasks": () => page(tasks),
    "GET /conversions": () => page(conversions),
    "GET /libraries": page([
      { id: uid(1), name: "Movies", kind: "movies" },
      { id: uid(2), name: "TV", kind: "tv" },
    ]),
    [`GET /media-files/${uid(11)}`]: file(11, { path: "/movies/Dune.mkv" }),
    [`GET /items/${uid(8)}`]: {
      id: uid(8),
      kind: "movie",
      parsedTitle: "dune",
      metadata: { title: "Dune: Part Two" },
    },
    // The dashboard, where a test that boots without a hash lands.
    "GET /stats": {
      cpuPercent: 1,
      memoryUsedBytes: 1,
      memoryTotalBytes: 2,
      storageUsedBytes: 1,
      storageTotalBytes: 2,
      activeTranscodes: 0,
      maxTranscodes: 3,
    },
    "GET /sessions": page([]),
    "GET /accounts": page([adult]),
    "GET /devices": page([device]),
    ...extra,
  });
};

let root: HTMLElement;
beforeEach(() => {
  calls.length = 0;
  document.body.replaceChildren((root = document.createElement("div")));
  localStorage.clear();
  location.hash = "";
  reviews = [conflicting, tied, moved];
  problems = { no_duration: [noDuration], unreadable: [unreadable], missing: [missing] };
  tasks = [scanMovies, refresh];
  conversions = [conversion];
  extra = {};
});

const navLinks = () => [...root.querySelectorAll("nav a")].map((a) => a.textContent);
const cardOf = (name: string) => root.querySelector<HTMLElement>(`section[aria-label='${name}']`);
const button = (scope: Element | null, text: string) =>
  [...(scope?.querySelectorAll("button") ?? [])].find((b) => b.textContent === text) as
    HTMLButtonElement | undefined;
const open = async () => {
  location.hash = "#/attention";
  await boot(root, { fetch: routes() });
  await settle();
};
const count = (key: string) => calls.filter((c) => c.key.startsWith(key)).length;

describe("needs attention nav", () => {
  it("is hidden while nothing needs attention, and its page says so", async () => {
    reviews = [];
    problems = { no_duration: [], unreadable: [], missing: [] };
    tasks = [scanMovies];
    conversions = [];
    await open();
    expect(navLinks()).not.toContain("Needs attention");
    expect(navLinks().some((t) => t?.startsWith("Needs attention"))).toBe(false);
    expect(root.querySelector("main")?.textContent).toContain("Nothing needs attention.");
    expect(root.querySelectorAll("main section.card")).toHaveLength(0);
  });

  it("shows second with the count, and follows it on the poll", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await boot(root, { fetch: routes() });
      await settle();
      expect(navLinks()[1]).toBe("Needs attention8");
      expect(root.querySelector("nav a:nth-child(2) .tag")?.textContent).toBe("8");
      reviews = [];
      problems = { no_duration: [], unreadable: [], missing: [] };
      tasks = [scanMovies];
      conversions = [];
      vi.advanceTimersByTime(5000);
      await settle();
      expect(navLinks().some((t) => t?.startsWith("Needs attention"))).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("needs attention page", () => {
  it("sums the groups under the title and shows each card with its rows", async () => {
    await open();
    expect(root.querySelector("main .head")?.textContent).toContain(
      "3 identity · 2 playback · 1 missing · 2 failed",
    );
    const idn = cardOf("Identity")!;
    expect(idn.querySelectorAll("tbody tr")).toHaveLength(3);
    expect([...idn.querySelectorAll("th")].map((th) => th.textContent)).toEqual([
      "Title",
      "Library",
      "Problem",
      "What was found",
      "",
    ]);
    const tags = [...idn.querySelectorAll(".tag")].map((t) => t.textContent);
    expect(tags).toEqual(["3", "Conflicting ID", "Tied match", "Ambiguous move"]);
    expect(idn.textContent).toContain("TVDB 78107");
    const play = cardOf("Cannot be streamed")!;
    expect([...play.querySelectorAll("td .tag")].map((t) => t.textContent)).toEqual([
      "No duration",
      "Unreadable",
    ]);
    expect(play.querySelector("h2 .tag")?.textContent).toBe("2");
    const why = [...play.querySelectorAll("td .sub")].at(-1)!;
    expect(why.textContent).toBe("Invalid data found when processing input");
    expect(why.getAttribute("title")).toContain("header parsing failed");
    expect(play.textContent).toContain("TV");
    expect(cardOf("Missing")!.textContent).toContain("/movies/Gone.mkv");
    const failed = cardOf("Failed")!;
    expect(failed.textContent).toContain("Refresh metadata");
    expect(failed.textContent).toContain("TMDB rate limit");
    expect(failed.textContent).toContain("Dune: Part Two · 1080p");
    expect(failed.textContent).toContain("ffmpeg exited with status 1");
    expect(button(failed, "Retry")).toBeUndefined();
  });

  it("hides each card without rows and leaves its group out of the sum", async () => {
    reviews = [];
    problems = { no_duration: [], unreadable: [], missing: [missing] };
    conversions = [];
    await open();
    expect(cardOf("Identity")).toBeNull();
    expect(cardOf("Cannot be streamed")).toBeNull();
    expect(cardOf("Missing")).not.toBeNull();
    expect(cardOf("Failed")).not.toBeNull();
    expect(root.querySelector("main .head")?.textContent).toContain("1 missing · 1 failed");
    expect(root.querySelector("main .head")?.textContent).not.toContain("identity");
  });
});

describe("needs attention actions", () => {
  const rematch = (body: unknown) => ({ id: uid(7), kind: "show", parsedTitle: "x", body });

  it("rematches to the folder's ID, then reloads the page and the nav count", async () => {
    extra = { [`PUT /items/${uid(7)}/metadata`]: rematch };
    await open();
    const before = [count("GET /attention"), count("GET /identity-reviews")];
    const row = cardOf("Identity")!.querySelector("tbody tr")!;
    expect([...row.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Use the folder's ID",
      "Keep match",
    ]);
    reviews = [tied, moved];
    button(row, "Use the folder's ID")!.click();
    await settle();
    expect(calls.find((c) => c.key === `PUT /items/${uid(7)}/metadata`)?.body).toEqual({
      provider: "tmdb",
      providerId: 2996,
      status: "matched",
      title: "The Office",
    });
    expect(count("GET /attention")).toBeGreaterThan(before[0]);
    expect(count("GET /identity-reviews")).toBeGreaterThan(before[1]);
    expect(cardOf("Identity")!.querySelectorAll("tbody tr")).toHaveLength(2);
    expect(navLinks()[1]).toBe("Needs attention7");
  });

  it("offers each tied candidate by title, year and country", async () => {
    extra = { [`PUT /items/${uid(8)}/metadata`]: rematch };
    await open();
    const row = cardOf("Identity")!.querySelectorAll("tbody tr")[1];
    expect([...row.querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Crash (1996, CA)",
      "Crash (2004, US)",
      "Dismiss",
    ]);
    button(row, "Crash (2004, US)")!.click();
    await settle();
    expect(calls.find((c) => c.key === `PUT /items/${uid(8)}/metadata`)?.body).toMatchObject({
      providerId: 21,
    });
  });

  it("dismisses a review, and offers only that for an ambiguous move", async () => {
    extra = { [`PUT /identity-reviews/${uid(3)}`]: (b: unknown) => ({ ...moved, body: b }) };
    await open();
    const row = cardOf("Identity")!.querySelectorAll("tbody tr")[2];
    expect([...row.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Dismiss"]);
    expect(row.textContent).toContain("/movies/Heat (1995) alt.mkv");
    button(row, "Dismiss")!.click();
    await settle();
    expect(calls.find((c) => c.key === `PUT /identity-reviews/${uid(3)}`)?.body).toEqual({
      dismissed: true,
    });
  });

  it("reads a file again", async () => {
    extra = { [`POST /media-files/${uid(22)}/probe`]: null };
    await open();
    const before = count("GET /attention");
    const row = cardOf("Cannot be streamed")!.querySelectorAll("tbody tr")[1];
    button(row, "Read again")!.click();
    await settle();
    expect(count(`POST /media-files/${uid(22)}/probe`)).toBe(1);
    expect(count("GET /attention")).toBeGreaterThan(before);
  });

  it("scans a missing file's library and removes its record after asking in the page", async () => {
    extra = {
      [`POST /tasks/${uid(2)}/runs`]: scanMovies,
      [`DELETE /media-files/${uid(23)}`]: null,
    };
    await open();
    const row = cardOf("Missing")!.querySelector("tbody tr")!;
    button(row, "Scan now")!.click();
    await settle();
    expect(count(`POST /tasks/${uid(2)}/runs`)).toBe(1);
    const missingRow = cardOf("Missing")!.querySelector("tbody tr")!;
    button(missingRow, "Remove")!.click();
    expect(count(`DELETE /media-files/${uid(23)}`)).toBe(0);
    problems.missing = [];
    button(missingRow, "Remove")!.click();
    await settle();
    expect(count(`DELETE /media-files/${uid(23)}`)).toBe(1);
    expect(cardOf("Missing")).toBeNull();
  });

  it("runs a failed task again and removes a failed conversion", async () => {
    extra = { [`POST /tasks/${uid(5)}/runs`]: refresh, [`DELETE /conversions/${uid(4)}`]: null };
    await open();
    const failed = cardOf("Failed")!;
    button(failed, "Run now")!.click();
    await settle();
    expect(count(`POST /tasks/${uid(5)}/runs`)).toBe(1);
    const row = [...cardOf("Failed")!.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("Dune"),
    )!;
    button(row, "Remove")!.click();
    conversions = [];
    button(row, "Remove")!.click();
    await settle();
    expect(count(`DELETE /conversions/${uid(4)}`)).toBe(1);
    expect(cardOf("Failed")!.textContent).not.toContain("Dune");
  });

  it("shows a failed action in the page", async () => {
    extra = {
      [`PUT /identity-reviews/${uid(1)}`]: new Response(
        JSON.stringify({ code: "internal", message: "boom" }),
        {
          status: 500,
          headers: { "content-type": "application/json" },
        },
      ),
    };
    await open();
    button(cardOf("Identity"), "Keep match")!.click();
    await settle();
    expect(root.querySelector("main [role=alert]:not([hidden])")?.textContent).toBe("boom");
  });
});
