// Component tests: the dashboard's Running tasks card against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { boot } from "./app";
import { setToken } from "./api";
import { uid } from "../api/uid";

type Handler = (url: URL) => unknown;
const calls: string[] = [];

function mockFetch(routes: Record<string, Handler | unknown>) {
  return async (req: Request) => {
    const url = new URL(req.url, "http://x");
    const key = `${req.method} ${url.pathname.replace("/api/v2", "")}`;
    calls.push(`${key}${url.search}`);
    const r = routes[key];
    if (r === undefined)
      return new Response(JSON.stringify({ code: "not_found", message: `no mock ${key}` }), {
        status: 404,
        headers: { "content-type": "application/json" },
      });
    const out = typeof r === "function" ? (r as Handler)(url) : r;
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
  for (let i = 0; i < 12; i++) await new Promise((r) => setTimeout(r, 0));
};

const conversion = (id: number, mediaFileId: number, over: Record<string, unknown>) => ({
  id: uid(id),
  mediaFileId: uid(mediaFileId),
  status: "queued",
  verdict: "video_transcode",
  container: "mp4",
  height: 0,
  bitrate: 0,
  sizeBytes: 1,
  estimated: true,
  progress: 0,
  downloads: 1,
  createdAt: "2026-01-01T00:00:00Z",
  ...over,
});
const file = (id: number, itemId: number) => ({
  id: uid(id),
  itemId: uid(itemId),
  path: "f",
  sizeBytes: 1,
  fileModifiedAt: "",
  tracks: [],
  chapters: [],
});
const scan = {
  id: uid(2),
  name: "Scan Movies",
  kind: `scan:${uid(1)}`,
  enabled: true,
  createdAt: "",
  lastRun: {
    id: uid(3),
    taskId: uid(2),
    status: "running",
    progress: 0.5,
    startedAt: "2026-01-01T00:00:00Z",
  },
};

let root: HTMLElement;
let active: unknown[];
let tasks: unknown[];
const fetch = (extra: Record<string, Handler | unknown> = {}) => {
  setToken("t");
  return mockFetch({
    "GET /health": { status: "ok", version: "1.2.3", setupRequired: false, serverName: "Den" },
    "GET /me": {
      device,
      account: adult,
      activeProfileId: uid(1),
      household: { id: uid(1), name: "Ada", adultId: uid(1), createdAt: "2026-01-01T00:00:00Z" },
      profiles: [adult],
    },
    "GET /stats": {
      cpuPercent: 1,
      memoryUsedBytes: 1,
      memoryTotalBytes: 2,
      storageUsedBytes: 1,
      storageTotalBytes: 2,
      activeTranscodes: 0,
      maxTranscodes: 3,
    },
    "GET /libraries": page([]),
    "GET /tasks": () => page(tasks),
    "GET /sessions": page([]),
    "GET /accounts": page([adult]),
    "GET /devices": page([device]),
    "GET /conversions": (url: URL) =>
      url.searchParams.get("active") === "true" ? page(active) : page([]),
    [`GET /media-files/${uid(11)}`]: file(11, 8),
    [`GET /media-files/${uid(12)}`]: file(12, 20),
    [`GET /media-files/${uid(13)}`]: file(13, 30),
    [`GET /items/${uid(8)}`]: { id: uid(8), kind: "movie", parsedTitle: "Heat" },
    [`GET /items/${uid(20)}`]: {
      id: uid(20),
      kind: "episode",
      parsedTitle: "Show",
      metadata: { title: "Pilot" },
    },
    [`GET /music/songs/${uid(30)}`]: { id: uid(30), title: "Blue Monday" },
    ...extra,
  });
};

const rows = () =>
  [...root.querySelectorAll("[aria-label='Running tasks'] > div")].map((r) => [
    r.firstElementChild?.textContent,
    r.querySelector("[role=progressbar]")?.getAttribute("aria-valuenow"),
  ]);

beforeEach(() => {
  calls.length = 0;
  document.body.replaceChildren((root = document.createElement("div")));
  localStorage.clear();
  location.hash = "";
  tasks = [];
  active = [];
});

describe("dashboard running tasks", () => {
  it("lists conversions for downloads beside running tasks, outside the transcode limit", async () => {
    tasks = [scan];
    active = [
      conversion(5, 11, { status: "converting", height: 720, codec: "h264", progress: 0.25 }),
      conversion(4, 12, { height: 1080, codec: "h264" }),
    ];
    await boot(root, { fetch: fetch() });
    await settle();
    expect(rows()).toEqual([
      ["Scan Movies", "50"],
      ["Heat · Download · 720p", "25"],
      ["Pilot · Download · 1080p · Queued", "0"],
    ]);
    expect(root.querySelector("[aria-label='Transcodes']")?.textContent).toContain("0 of 3");
  });

  it("names a conversion of a file in no item as the UI names an unknown title", async () => {
    active = [conversion(7, 14, { height: 720 })];
    const loose = { ...file(14, 0), itemId: undefined };
    await boot(root, { fetch: fetch({ [`GET /media-files/${uid(14)}`]: loose }) });
    await settle();
    expect(rows()).toEqual([["Untitled · Download · 720p · Queued", "0"]]);
  });

  it("says Nothing running with neither", async () => {
    await boot(root, { fetch: fetch() });
    await settle();
    expect(rows()).toEqual([]);
    expect(root.querySelector("[aria-label='Running tasks']")?.textContent).toContain(
      "Nothing running.",
    );
  });

  it("follows the conversions on the poll", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await boot(root, { fetch: fetch() });
      await settle();
      expect(root.querySelector("[aria-label='Running tasks']")?.textContent).toContain(
        "Nothing running.",
      );
      active = [
        conversion(6, 13, {
          status: "converting",
          verdict: "audio_transcode",
          container: "m4a",
          bitrate: 256_000,
          progress: 0.5,
        }),
      ];
      vi.advanceTimersByTime(5000);
      await settle();
      expect(rows()).toEqual([["Blue Monday · Download · 256 kbps", "50"]]);
      active = [];
      vi.advanceTimersByTime(5000);
      await settle();
      expect(root.querySelector("[aria-label='Running tasks']")?.textContent).toContain(
        "Nothing running.",
      );
    } finally {
      vi.useRealTimers();
    }
  });

  const count = (key: string) => calls.filter((c) => c.startsWith(key)).length;

  it("asks for a title, a file and the accounts and devices once, however many polls follow", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      active = [conversion(5, 11, { status: "converting", height: 720, progress: 0.25 })];
      const playing = {
        id: uid(4),
        deviceId: uid(9),
        accountId: uid(1),
        itemId: uid(8),
        mediaFileId: uid(11),
        positionMs: 0,
        currentEntry: 0,
        plan: [],
        encoder: "none",
      };
      await boot(root, { fetch: fetch({ "GET /sessions": page([playing]) }) });
      await settle();
      const polled = count("GET /conversions");
      for (let i = 0; i < 2; i++) {
        vi.advanceTimersByTime(5000);
        await settle();
      }
      expect(count("GET /conversions")).toBe(polled + 2);
      expect(rows()).toEqual([["Heat · Download · 720p", "25"]]);
      expect(root.querySelector("[aria-label='Now playing']")!.textContent).toContain("Ada");
      expect(count("GET /accounts")).toBe(1);
      expect(count("GET /devices")).toBe(1);
      expect(count(`GET /items/${uid(8)}`)).toBe(1);
      expect(count(`GET /media-files/${uid(11)}`)).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reads the accounts and devices again for a session whose account or device it has not seen", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const playing = (id: number, accountId: number) => ({
        id: uid(id),
        deviceId: uid(9),
        accountId: uid(accountId),
        itemId: uid(8),
        mediaFileId: uid(11),
        positionMs: 0,
        currentEntry: 0,
        plan: [],
        encoder: "none",
      });
      let sessions = [playing(4, 1)];
      let accounts = [adult];
      await boot(root, {
        fetch: fetch({
          "GET /sessions": () => page(sessions),
          "GET /accounts": () => page(accounts),
        }),
      });
      await settle();
      accounts = [adult, { ...adult, id: uid(2), name: "Grace" }];
      sessions = [playing(4, 1), playing(5, 2)];
      vi.advanceTimersByTime(5000);
      await settle();
      expect(root.querySelector("[aria-label='Now playing']")!.textContent).toContain("Grace");
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not poll again while the last poll is still waiting", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let held = false;
      let asked = 0;
      let release!: () => void;
      const gate = new Promise<void>((r) => (release = r));
      const inner = fetch();
      await boot(root, {
        fetch: async (req) => {
          if (req.url.includes("/stats")) asked++;
          if (held && req.url.includes("/stats")) await gate;
          return inner(req);
        },
      });
      await settle();
      const opened = asked;
      held = true;
      vi.advanceTimersByTime(20_000);
      await settle();
      expect(asked).toBe(opened + 1);
      release();
      await settle();
      vi.advanceTimersByTime(5000);
      await settle();
      expect(asked).toBe(opened + 2);
    } finally {
      vi.useRealTimers();
    }
  });
});
