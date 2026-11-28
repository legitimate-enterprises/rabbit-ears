// Component tests: the real pages and generated client against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { boot } from "./app";
import { setToken } from "./api";
import { bytes } from "./format";
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
    calls.push({ key, body });
    const r = routes[key];
    if (r === undefined)
      return new Response(JSON.stringify({ code: "not_found", message: `no mock ${key}` }), {
        status: 404,
        headers: { "content-type": "application/json" },
      });
    const out = typeof r === "function" ? (r as Handler)(body, url) : r;
    if (out instanceof Response) return out;
    return new Response(JSON.stringify(out), {
      status: key.startsWith("POST") ? 201 : 200,
      headers: { "content-type": "application/json" },
    });
  };
}

const err = (status: number, code: string, message: string) =>
  new Response(JSON.stringify({ code, message }), {
    status,
    headers: { "content-type": "application/json" },
  });
const health = (setupRequired = false) => ({
  status: "ok",
  version: "1.2.3",
  setupRequired,
  serverName: "Den",
});
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
const me = (isAdmin = true) => ({
  device,
  account: { ...adult, isAdmin },
  activeProfileId: uid(1),
  household: { id: uid(1), name: "Ada", adultId: uid(1), createdAt: "2026-01-01T00:00:00Z" },
  profiles: [adult],
});
const page = (items: unknown[]) => ({ items });
const flush = () => new Promise((r) => setTimeout(r, 0));
const settle = async () => {
  for (let i = 0; i < 8; i++) await flush();
};
const click = (el: Element | null | undefined) => (el as HTMLElement).click();
const byText = (sel: string, text: string) =>
  [...document.querySelectorAll(sel)].find((e) => e.textContent?.trim() === text);
const type = (label: string, value: string) => {
  const el =
    document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`) ??
    ([...document.querySelectorAll("label")].find((l) => l.textContent === label)
      ?.control as HTMLInputElement);
  el.value = value;
  el.dispatchEvent(new Event("input", { bubbles: true }));
};

let root: HTMLElement;
beforeEach(() => {
  calls.length = 0;
  document.body.replaceChildren((root = document.createElement("div")));
  localStorage.clear();
  location.hash = "";
});

describe("auth guard", () => {
  it("bounces a signed-in non-admin out of the admin UI", async () => {
    setToken("t");
    const leave = vi.fn();
    await boot(root, {
      leave,
      fetch: mockFetch({ "GET /health": health(), "GET /me": me(false) }),
    });
    expect(leave).toHaveBeenCalled();
    expect(root.querySelector("aside")).toBeNull();
  });

  it("bounces an admin whose browser has a kid's profile active, which the server refuses", async () => {
    setToken("t");
    const leave = vi.fn();
    await boot(root, {
      leave,
      fetch: mockFetch({
        "GET /health": health(),
        "GET /me": { ...me(), activeProfileId: uid(2) },
      }),
    });
    expect(leave).toHaveBeenCalled();
    expect(root.querySelector("aside")).toBeNull();
  });

  it("sends a device locked at Who's watching to the viewer's picker instead of an error", async () => {
    setToken("t");
    const leave = vi.fn();
    const base = mockFetch({ "GET /health": health(), "GET /me": me() });
    await boot(root, {
      leave,
      fetch: (req) =>
        /\/(health|me)$/.test(req.url)
          ? base(req)
          : Promise.resolve(err(403, "device_locked", "device locked")),
    });
    await settle();
    expect(leave).toHaveBeenCalledWith("/#/who");
    expect(root.querySelector("[role=alert]")).toBeNull();
  });

  it("shows sign in once a poll finds the device signed out elsewhere", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let out = false;
      const inner = signedIn({
        "GET /attention": { identity: 0, unstreamable: 0, missing: 0, failed: 0 },
      });
      await boot(root, {
        fetch: (req) =>
          out && !/\/health$/.test(req.url)
            ? Promise.resolve(err(401, "unauthorized", "signed out"))
            : inner(req),
      });
      location.hash = "#/settings";
      await settle();
      out = true;
      vi.advanceTimersByTime(5000);
      await settle();
      expect(localStorage.getItem("rabbit-ears.admin.token")).toBeNull();
      expect(root.querySelector("input[type=password]")).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("sends a page to Who's watching once a poll finds the device locked there", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let locked = false;
      const leave = vi.fn();
      const inner = signedIn({
        "GET /attention": { identity: 0, unstreamable: 0, missing: 0, failed: 0 },
      });
      await boot(root, {
        leave,
        fetch: (req) =>
          locked && !/\/(health|me)$/.test(req.url)
            ? Promise.resolve(err(403, "device_locked", "device locked"))
            : inner(req),
      });
      location.hash = "#/settings";
      await settle();
      locked = true;
      vi.advanceTimersByTime(5000);
      await settle();
      expect(leave).toHaveBeenCalledWith("/#/who");
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves for the viewer once a poll finds the browser switched to another profile, such as a kid", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let switched = false;
      const leave = vi.fn();
      const inner = signedIn({
        "GET /attention": { identity: 0, unstreamable: 0, missing: 0, failed: 0 },
      });
      await boot(root, {
        leave,
        fetch: (req) =>
          switched && !/\/(health|me)$/.test(req.url)
            ? Promise.resolve(err(403, "forbidden", "not allowed"))
            : inner(req),
      });
      location.hash = "#/settings";
      await settle();
      switched = true;
      vi.advanceTimersByTime(5000);
      await settle();
      expect(leave).toHaveBeenCalledWith("/");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows sign in without a token and reports a wrong password", async () => {
    await boot(root, {
      fetch: mockFetch({
        "GET /health": health(),
        "POST /auth/sign-in": () => err(401, "unauthorized", "bad"),
      }),
    });
    type("Email", "ada@example.com");
    type("Password", "nope");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(root.querySelector("[role=alert]")?.textContent).toBe("Email or password is wrong.");
    expect(root.querySelector(".brand")?.textContent).toBe("Rabbit EarsAdmin");
  });

  it("heads the signed-in sidebar with the server's name, whole in its title", async () => {
    const name = "The Basement Media Closet Under the Stairs";
    await boot(root, { fetch: signedIn({ "GET /health": { ...health(), serverName: name } }) });
    await settle();
    const mark = root.querySelector<HTMLElement>("aside .brand")!;
    expect(mark.textContent).toBe(`${name}Admin`);
    expect(mark.querySelector(".name")?.getAttribute("title")).toBe(name);
    expect(root.querySelector("aside .foot")?.textContent).toContain("Rabbit Ears 1.2.3");
  });

  it("says how long to wait when sign in is rate limited", async () => {
    await boot(root, {
      fetch: mockFetch({
        "GET /health": health(),
        "POST /auth/sign-in": () =>
          new Response(JSON.stringify({ code: "too_many_requests", message: "slow down" }), {
            status: 429,
            headers: { "content-type": "application/json", "Retry-After": "30" },
          }),
      }),
    });
    type("Email", "ada@example.com");
    type("Password", "nope");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(root.querySelector("[role=alert]")?.textContent).toBe(
      "Too many attempts. Try again in 1 minute.",
    );
  });
});

describe("first run", () => {
  it("validates, creates the admin, names the server and adds a library", async () => {
    const fetch = mockFetch({
      "GET /health": health(true),
      "POST /setup": { token: "tok", me: me() },
      "POST /libraries": {
        id: uid(5),
        name: "Movies",
        kind: "movies",
        scanHour: 3,
        watchFiles: true,
        folders: [],
        stats: { movies: 0, shows: 0, seasons: 0, episodes: 0, files: 0, bytes: 0, unmatched: 0 },
        createdAt: "",
        modifiedAt: "",
      },
      [`POST /libraries/${uid(5)}/folders`]: { id: uid(1), libraryId: uid(5), path: "/m" },
    });
    await boot(root, { fetch });
    const go = byText("button", "Create admin account") as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    type("Name", "Ada");
    type("Email", "ada@example.com");
    type("Password", "short");
    type("Confirm password", "short");
    expect(go.disabled).toBe(true);
    type("Password", "correct horse battery");
    type("Confirm password", "correct horse battery");
    expect(go.disabled).toBe(false);
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    expect(root.querySelector("h1")?.textContent).toBe("Name your server");
    type("Server name", "Den");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.find((c) => c.key === "POST /setup")?.body).toMatchObject({
      serverName: "Den",
      name: "Ada",
      email: "ada@example.com",
      platform: "web",
    });
    expect(localStorage.getItem("rabbit-ears.admin.token")).toBe("tok");
    type("Folder path", "/m");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.map((c) => c.key)).toEqual(
      expect.arrayContaining(["POST /libraries", `POST /libraries/${uid(5)}/folders`]),
    );
  });

  it("keeps the library when its folder is refused and sends only the folder again", async () => {
    setToken("tok");
    let setup = true;
    const fetch = mockFetch({
      "GET /health": () => health(setup),
      "GET /me": me(),
      "POST /setup": () => ((setup = false), { token: "tok", me: me() }),
      "POST /libraries": { id: uid(5) },
      [`POST /libraries/${uid(5)}/folders`]: (b: unknown) =>
        (b as { path: string }).path === "/m"
          ? { id: uid(1), libraryId: uid(5), path: "/m" }
          : err(400, "bad_request", "path: not a folder"),
    });
    await boot(root, { fetch });
    type("Name", "Ada");
    type("Email", "ada@example.com");
    type("Password", "correct horse battery");
    type("Confirm password", "correct horse battery");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    type("Folder path", "/nope");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(root.querySelector("[role=alert]")?.textContent).toBe("path: not a folder");
    type("Folder path", "/m");
    root.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.filter((c) => c.key.startsWith("POST /libraries")).map((c) => c.key)).toEqual([
      "POST /libraries",
      `POST /libraries/${uid(5)}/folders`,
      `POST /libraries/${uid(5)}/folders`,
    ]);
  });
});

const signedIn = (extra: Record<string, Handler | unknown>) => {
  setToken("t");
  return mockFetch({ "GET /health": health(), "GET /me": me(), ...extra });
};

const stats = {
  movies: 3,
  shows: 0,
  seasons: 0,
  episodes: 0,
  files: 3,
  bytes: 4_100_000_000,
  unmatched: 1,
};
const tvStats = {
  movies: 0,
  shows: 5,
  seasons: 5,
  episodes: 6,
  files: 6,
  bytes: 12_345_000_000,
  unmatched: 0,
};

const serverStats = {
  cpuPercent: 18.4,
  memoryUsedBytes: 3_100_000_000,
  memoryTotalBytes: 16_000_000_000,
  storageUsedBytes: 14_200_000_000_000,
  storageTotalBytes: 20_000_000_000_000,
  activeTranscodes: 1,
  maxTranscodes: 3,
};
const serverSettings = {
  serverName: "Den",
  tasksPaused: false,
  dataDir: "/Volumes/Media/.rabbit-ears",
  port: 8477,
  databasePath: "/Volumes/Media/.rabbit-ears/rabbit-ears.db",
  storage: {
    transcodeCacheBytes: 1_200_000_000,
    transcodeCacheCapBytes: 20_000_000_000,
    diskFreeBytes: 5_800_000_000_000,
    diskTotalBytes: 20_000_000_000_000,
  },
  hardware: {
    cpuModel: "Apple M2 Max",
    cores: 12,
    memoryBytes: 32_000_000_000,
    videoToolbox: true,
  },
  metadata: { tmdbConfigured: true },
  maxTranscodes: 3,
  transcodeCacheCapBytes: 20_000_000_000,
  readAheadSeconds: 300,
};
const noContent = () => new Response(null, { status: 204 });

describe("dashboard", () => {
  const session = {
    id: uid(4),
    deviceId: uid(9),
    accountId: uid(1),
    itemId: uid(8),
    mediaFileId: uid(1),
    positionMs: 84_000,
    currentEntry: 0,
    plan: [
      {
        position: 0,
        verdict: "video_transcode",
        videoAction: "convert",
        audioAction: "copy",
        subtitleAction: "none",
        bad: false,
      },
    ],
    encoder: "videotoolbox",
    transcodeReason: "HEVC Main10 is not decodable here",
    startedAt: "",
    modifiedAt: "",
  };
  const routes = {
    "GET /stats": serverStats,
    "GET /libraries": page([]),
    "GET /tasks": page([]),
    "GET /sessions": page([session]),
    "GET /accounts": page([adult]),
    "GET /devices": page([device]),
    [`GET /items/${uid(8)}`]: { id: uid(8), kind: "movie", parsedTitle: "Heat" },
  };

  it("shows four server gauges from the stats endpoint", async () => {
    await boot(root, { fetch: signedIn(routes) });
    await settle();
    const g = (name: string) => root.querySelector(`[aria-label='${name}']`)?.textContent;
    expect(g("CPU")).toContain("18%");
    expect(g("Memory")).toContain("3.1 of 16 GB");
    expect(g("Storage")).toContain("14.2 of 20 TB");
    expect(g("Transcodes")).toContain("1 of 3");
    expect(
      root.querySelector("[aria-label='CPU'] [role=progressbar]")?.getAttribute("aria-valuenow"),
    ).toBe("18");
  });

  it("refreshes the gauges on an interval only while the page is visible", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await boot(root, { fetch: signedIn(routes) });
      await settle();
      const n = () => calls.filter((c) => c.key === "GET /stats").length;
      const before = n();
      vi.advanceTimersByTime(5000);
      await settle();
      expect(n()).toBe(before + 1);
      Object.defineProperty(document, "hidden", { value: true, configurable: true });
      vi.advanceTimersByTime(5000);
      await settle();
      expect(n()).toBe(before + 1);
    } finally {
      Object.defineProperty(document, "hidden", { value: false, configurable: true });
      vi.useRealTimers();
    }
  });

  it("stops polling and routing the old page once the admin signs out", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await boot(root, {
        fetch: signedIn({ ...routes, "POST /auth/sign-out": new Response(null, { status: 204 }) }),
      });
      await settle();
      click(byText("button", "Sign out"));
      await settle();
      const n = () => calls.filter((c) => c.key === "GET /stats").length;
      const before = n();
      vi.advanceTimersByTime(10000);
      await settle();
      expect(n()).toBe(before);
      expect(window.onhashchange).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("neither shows nor polls a page that finished loading after the admin left it", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let open!: () => void;
      const gate = new Promise<void>((r) => (open = r));
      const inner = signedIn(routes);
      await boot(root, {
        fetch: async (req) => {
          if (req.url.endsWith("/stats")) await gate;
          return inner(req);
        },
      });
      location.hash = "#/libraries";
      await settle();
      expect(root.querySelector("h1")?.textContent).toBe("Libraries");
      open();
      await settle();
      expect(root.querySelector("h1")?.textContent).toBe("Libraries");
      const n = calls.filter((c) => c.key === "GET /stats").length;
      vi.advanceTimersByTime(5000);
      await settle();
      expect(calls.filter((c) => c.key === "GET /stats").length).toBe(n);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps Now playing current after two refreshes overlap", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let n = 0;
      let held: (() => void)[] | undefined;
      const inner = signedIn({
        ...routes,
        "GET /sessions": () => page([{ ...session, positionMs: ++n * 60_000 }]),
        [`DELETE /sessions/${uid(4)}`]: new Response(null, { status: 204 }),
      });
      await boot(root, {
        fetch: async (req) => {
          if (held && req.url.includes("/sessions")) await new Promise<void>((r) => held!.push(r));
          return inner(req);
        },
      });
      await settle();
      const card = () => root.querySelector("[aria-label='Now playing']")!.textContent;
      expect(card()).toContain(`${n}:00`);
      held = [];
      // The poll's refresh and the one Stop asks for overlap; polls alone never do.
      vi.advanceTimersByTime(5000);
      await settle();
      click(byText("button", "Stop"));
      await settle();
      vi.advanceTimersByTime(10_000);
      await settle();
      expect(held).toHaveLength(2);
      held.forEach((r) => r());
      held = undefined;
      await settle();
      vi.advanceTimersByTime(5000);
      await settle();
      expect(card()).toContain(`${n}:00`);
    } finally {
      vi.useRealTimers();
    }
  });

  it("now playing shows a position past the hour with its hours", async () => {
    await boot(root, {
      fetch: signedIn({
        ...routes,
        "GET /sessions": page([{ ...session, positionMs: 7_507_000 }]),
      }),
    });
    await settle();
    expect(root.querySelector("[aria-label='Now playing'] tbody tr")!.textContent).toContain(
      "2:05:07",
    );
  });

  it("now playing names a song and a YouTube video, which /items does not find", async () => {
    await boot(root, {
      fetch: signedIn({
        ...routes,
        "GET /sessions": page([
          { ...session, id: uid(2), itemId: uid(12) },
          { ...session, id: uid(3), itemId: uid(13) },
        ]),
        [`GET /music/songs/${uid(12)}`]: { id: uid(12), title: "Roygbiv" },
        [`GET /youtube/videos/${uid(13)}`]: { id: uid(13), title: "Lathe turning basics" },
      }),
    });
    await settle();
    const text = root.querySelector("[aria-label='Now playing']")!.textContent;
    expect(text).toContain("Roygbiv");
    expect(text).toContain("Lathe turning basics");
    expect(text).not.toContain("Item ");
  });

  it("now playing shows the verdict, position, encoder and reason", async () => {
    await boot(root, { fetch: signedIn(routes) });
    await settle();
    const row = root.querySelector("[aria-label='Now playing'] tbody tr")!;
    expect(row.querySelector(".tag.warn")?.textContent).toBe("Video transcode");
    expect(row.textContent).toContain("VideoToolbox");
    expect(row.textContent).toContain("HEVC Main10 is not decodable here");
    expect(row.textContent).toContain("1:24");
    expect(row.textContent).toContain("Heat");
  });
});

describe("libraries", () => {
  it("Scan now requests a run of the library's scan task", async () => {
    location.hash = "#/libraries";
    const lib = {
      id: uid(5),
      name: "Movies",
      kind: "movies",
      scanHour: 3,
      watchFiles: true,
      folders: [{ id: uid(1), libraryId: uid(5), path: "/m" }],
      stats,
      createdAt: "",
      modifiedAt: "",
    };
    const task = {
      id: uid(7),
      name: "Scan Movies",
      kind: `scan:${uid(5)}`,
      enabled: true,
      intervalSeconds: 86400,
      createdAt: "",
    };
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([task]),
        [`POST /tasks/${uid(7)}/runs`]: () => new Response(JSON.stringify(task), { status: 202 }),
      }),
    });
    await settle();
    expect(root.querySelector("[aria-label='Movies library']")?.textContent).toContain(
      "Nightly at 3:00 AM, and when files change",
    );
    expect(root.querySelector("[aria-label='Movies library']")?.textContent).toContain(
      "3 movies · 4.1 GB · 1 unmatched",
    );
    click(byText("button", "Scan now"));
    await settle();
    expect(calls.map((c) => c.key)).toContain(`POST /tasks/${uid(7)}/runs`);
    expect(byText("button", "Queued")).toBeTruthy();
  });

  const lib = {
    id: uid(5),
    name: "Movies",
    kind: "movies",
    scanHour: 3,
    watchFiles: true,
    folders: [{ id: uid(1), libraryId: uid(5), path: "/wrong" }],
    stats,
    createdAt: "",
    modifiedAt: "",
  };
  const card = () => root.querySelector<HTMLElement>("[aria-label='Movies library']")!;
  const inCard = (t: string) =>
    [...card().querySelectorAll("button")].filter((b) => b.textContent === t);

  it("a failed Scan now shows the error and lets the admin try again", async () => {
    location.hash = "#/libraries";
    const task = {
      id: uid(7),
      name: "Scan Movies",
      kind: `scan:${uid(5)}`,
      enabled: true,
      intervalSeconds: 86400,
      createdAt: "",
    };
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([task]),
        [`POST /tasks/${uid(7)}/runs`]: () => err(500, "internal", "queue is full"),
      }),
    });
    await settle();
    click(byText("button", "Scan now"));
    await settle();
    expect(card().querySelector("[role=alert]")?.textContent).toBe("queue is full");
    expect((byText("button", "Scan now") as HTMLButtonElement).disabled).toBe(false);
  });

  const inDialog = (t: string) =>
    [...document.querySelectorAll(".dialog button")].filter((b) => b.textContent === t);
  const save = async () => {
    document
      .querySelector(".dialog form")!
      .dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
  };

  it("the card only lists folders; Edit changes a folder's path and adds another", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([]),
        [`PUT /libraries/${uid(5)}`]: lib,
        [`PUT /libraries/${uid(5)}/folders/${uid(1)}`]: {
          id: uid(1),
          libraryId: uid(5),
          path: "/m",
        },
        [`POST /libraries/${uid(5)}/folders`]: { id: uid(2), libraryId: uid(5), path: "/n" },
      }),
    });
    await settle();
    expect(card().textContent).toContain("/wrong");
    expect([...card().querySelectorAll("button")].map((b) => b.textContent)).toEqual([
      "Scan now",
      "Edit",
    ]);
    click(inCard("Edit")[0]);
    type("Folder path", "/m ");
    type("Add folder", " /n");
    await save();
    expect(calls.find((c) => c.key === `PUT /libraries/${uid(5)}/folders/${uid(1)}`)?.body).toEqual(
      { path: "/m" },
    );
    expect(calls.find((c) => c.key === `POST /libraries/${uid(5)}/folders`)?.body).toEqual({
      path: "/n",
    });
    expect(document.querySelector(".dialog")).toBeNull();
  });

  it("Edit sends nothing for a folder left as it was", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([]),
        [`PUT /libraries/${uid(5)}`]: lib,
      }),
    });
    await settle();
    click(inCard("Edit")[0]);
    await save();
    expect(calls.map((c) => c.key)).toContain(`PUT /libraries/${uid(5)}`);
    expect(calls.filter((c) => c.key.includes("/folders"))).toEqual([]);
    expect(document.querySelector(".dialog")).toBeNull();
  });

  it("Edit keeps the dialog open with the error when a folder is refused", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([]),
        [`PUT /libraries/${uid(5)}`]: lib,
        [`PUT /libraries/${uid(5)}/folders/${uid(1)}`]: () =>
          err(400, "bad_request", "path: not a folder"),
      }),
    });
    await settle();
    click(inCard("Edit")[0]);
    type("Folder path", "/nope");
    await save();
    // The Remove beside the folder has an alert of its own, empty here.
    expect(
      [...document.querySelectorAll(".dialog [role=alert]")].map((e) => e.textContent).join(""),
    ).toBe("path: not a folder");
  });

  it("Remove takes a folder out after confirming in the Edit dialog", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([]),
        [`PUT /libraries/${uid(5)}`]: lib,
        [`DELETE /libraries/${uid(5)}/folders/${uid(1)}`]: () =>
          new Response(null, { status: 204 }),
      }),
    });
    await settle();
    click(inCard("Edit")[0]);
    click(inDialog("Remove")[0]);
    expect(
      calls.find((c) => c.key === `DELETE /libraries/${uid(5)}/folders/${uid(1)}`),
    ).toBeUndefined();
    expect(document.querySelector(".dialog")!.textContent).toContain("marks its files missing");
    click(inDialog("Remove").at(-1));
    await settle();
    expect(
      calls.find((c) => c.key === `DELETE /libraries/${uid(5)}/folders/${uid(1)}`),
    ).toBeTruthy();
    expect(inDialog("Remove")).toEqual([]);
    await save();
    expect(calls.filter((c) => c.key.startsWith(`PUT /libraries/${uid(5)}/folders`))).toEqual([]);
  });

  it("Delete library says what goes and what stays, then deletes it", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([lib]),
        "GET /tasks": page([]),
        [`DELETE /libraries/${uid(5)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await settle();
    click(inCard("Edit")[0]);
    const btn = (t: string) =>
      [...document.querySelectorAll(".dialog button")].filter((b) => b.textContent === t);
    click(btn("Delete library")[0]);
    const text = document.querySelector(".dialog")!.textContent;
    expect(text).toContain("watch state");
    expect(text).toContain("stay on disk");
    expect(calls.find((c) => c.key === `DELETE /libraries/${uid(5)}`)).toBeUndefined();
    click(btn("Delete library").at(-1));
    await settle();
    expect(calls.find((c) => c.key === `DELETE /libraries/${uid(5)}`)).toBeTruthy();
    expect(document.querySelector(".dialog")).toBeNull();
  });
});

describe("youtube libraries", () => {
  const yt = {
    id: uid(8),
    name: "YouTube",
    kind: "youtube",
    scanHour: 3,
    watchFiles: false,
    folders: [],
    stats: { ...stats, channels: 14, videos: 2118 },
    createdAt: "",
    modifiedAt: "",
  };

  it("Add library sends name, kind and schedule, then the folder", async () => {
    location.hash = "#/libraries";
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": page([]),
        "GET /tasks": page([]),
        "POST /libraries": yt,
        [`POST /libraries/${uid(8)}/folders`]: { id: uid(1), libraryId: uid(8), path: "/y" },
      }),
    });
    await settle();
    click(byText("button", "Add library"));
    // A checkbox drawn as a text field is stretched to a field's size.
    expect(document.querySelector(".dialog input[type=checkbox]")!.classList.contains("in")).toBe(
      false,
    );
    const kind = root.ownerDocument.querySelector(".dialog select") as HTMLSelectElement;
    kind.value = "youtube";
    kind.dispatchEvent(new Event("change"));
    type("Name", "YouTube");
    type("First folder", "/y");
    const form = document.querySelector(".dialog form")!;
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.find((c) => c.key === "POST /libraries")?.body).toEqual({
      name: "YouTube",
      kind: "youtube",
      scanHour: 3,
      watchFiles: true,
    });
    expect(calls.find((c) => c.key === `POST /libraries/${uid(8)}/folders`)?.body).toEqual({
      path: "/y",
    });
  });

  it("Add library keeps the library when its folder is refused, lists it and sends only the folder again", async () => {
    location.hash = "#/libraries";
    let made = false;
    await boot(root, {
      fetch: signedIn({
        "GET /libraries": () => page(made ? [yt] : []),
        "GET /tasks": page([]),
        "POST /libraries": () => ((made = true), yt),
        [`POST /libraries/${uid(8)}/folders`]: (b: unknown) =>
          (b as { path: string }).path === "/y"
            ? { id: uid(1), libraryId: uid(8), path: "/y" }
            : err(400, "bad_request", "path: not a folder"),
      }),
    });
    await settle();
    click(byText("button", "Add library"));
    type("Name", "YouTube");
    type("First folder", "/nope");
    const form = document.querySelector(".dialog form")!;
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(document.querySelector(".dialog [role=alert]")?.textContent).toBe("path: not a folder");
    expect(root.querySelector(".libcard h2")?.textContent).toContain("YouTube");
    type("First folder", "/y");
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.filter((c) => c.key.startsWith("POST /libraries")).map((c) => c.key)).toEqual([
      "POST /libraries",
      `POST /libraries/${uid(8)}/folders`,
      `POST /libraries/${uid(8)}/folders`,
    ]);
    expect(document.querySelector(".dialog")).toBeNull();
  });

  it("the card shows the library's contents and offers no Channels", async () => {
    location.hash = "#/libraries";
    await boot(root, { fetch: signedIn({ "GET /libraries": page([yt]), "GET /tasks": page([]) }) });
    await settle();
    expect(root.textContent).toContain("14 channels · 2118 videos");
    expect(byText("button", "Channels")).toBeUndefined();
  });
});

describe("library stats", () => {
  const tv = {
    id: uid(6),
    name: "Shows",
    kind: "tv",
    scanHour: 3,
    watchFiles: false,
    folders: [],
    stats: tvStats,
    createdAt: "",
    modifiedAt: "",
  };
  const movies = { ...tv, id: uid(5), name: "Movies", kind: "movies", stats };

  it("the Libraries card shows shows, seasons, episodes and size for TV", async () => {
    location.hash = "#/libraries";
    await boot(root, { fetch: signedIn({ "GET /libraries": page([tv]), "GET /tasks": page([]) }) });
    await settle();
    expect(root.querySelector("[aria-label='Shows library']")?.textContent).toContain(
      "5 shows · 5 seasons · 6 episodes · 12.3 GB",
    );
  });

  it("the Dashboard table shows each library's contents", async () => {
    await boot(root, {
      fetch: signedIn({
        "GET /stats": serverStats,
        "GET /libraries": page([movies, tv]),
        "GET /tasks": page([]),
        "GET /sessions": page([]),
        "GET /accounts": page([]),
        "GET /devices": page([]),
      }),
    });
    await settle();
    const text = root.querySelector("[aria-label='Libraries']")?.textContent ?? "";
    expect(text).toContain("3 movies · 4.1 GB · 1 unmatched");
    expect(text).toContain("5 shows · 5 seasons · 6 episodes · 12.3 GB");
  });
});

describe("users", () => {
  const users = {
    "GET /households": page([{ id: uid(1), name: "Ada", adultId: uid(1), createdAt: "" }]),
    "GET /accounts": page([adult]),
    "GET /devices": page([device, { ...device, id: uid(10), name: "Living room" }]),
    "GET /adult-links": page([]),
  };

  it("adding an adult ends on the join link and code", async () => {
    location.hash = "#/users";
    const acct = {
      ...adult,
      id: uid(2),
      name: "Helga",
      email: "h@example.com",
      isAdmin: false,
      householdId: undefined,
      pending: true,
    };
    const jc = {
      id: uid(3),
      code: "7KQ4-MNP2",
      link: "/join/7KQ4-MNP2",
      accountId: uid(2),
      expiresAt: "2026-12-01T00:00:00Z",
      createdAt: "",
    };
    await boot(root, {
      fetch: signedIn({ ...users, "POST /accounts": acct, "POST /join-codes": jc }),
    });
    await settle();
    click(byText("button", "Add adult"));
    type("Name", "Helga");
    type("Email", "h@example.com");
    document
      .querySelector(".dialog form")!
      .dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(document.querySelector("[role=dialog] h1")?.textContent).toBe(
      "Helga's account is ready",
    );
    expect(
      document.querySelector<HTMLInputElement>("input[aria-label='Join link']")!.value,
    ).toMatch(/\/join\/7KQ4-MNP2$/);
    expect(document.querySelector(".code")?.textContent).toBe("7KQ4-MNP2");
    expect(calls.find((c) => c.key === "POST /join-codes")?.body).toEqual({
      accountId: uid(2),
      expiresInDays: 7,
    });
  });

  const kid = {
    ...adult,
    id: uid(2),
    kind: "kid",
    name: "Mary",
    email: undefined,
    isAdmin: false,
    birthday: "2020-06-02",
    movieCeiling: "G",
    tvCeiling: "TV-Y7",
  };
  const edit = async (name: string, extra: Record<string, Handler | unknown> = {}) => {
    location.hash = "#/users";
    await boot(root, {
      fetch: signedIn({ ...users, "GET /accounts": page([adult, kid]), ...extra }),
    });
    await settle();
    const row = [...root.querySelectorAll("tr")].find((r) => r.textContent?.includes(name))!;
    click([...row.querySelectorAll("button")].find((b) => b.textContent === "Edit"));
    return document.querySelector<HTMLElement>(".sheet")!;
  };
  const choose = (sheet: HTMLElement, label: string, value: string) => {
    const el = [...sheet.querySelectorAll("label")].find((l) => l.textContent === label)!
      .control as HTMLSelectElement | HTMLInputElement;
    el.value = value;
    el.dispatchEvent(new Event("change", { bubbles: true }));
  };
  const submit = (sheet: HTMLElement) =>
    sheet.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));

  it("Edit on a kid saves name, birthday and ceilings through the accounts API", async () => {
    const sheet = await edit("Mary", {
      [`PUT /accounts/${uid(2)}`]: (b: unknown) => ({ ...kid, ...(b as object) }),
      "GET /ratings/suggestion": { age: 9, movieCeiling: "PG", tvCeiling: "TV-PG" },
    });
    expect(sheet.getAttribute("role")).toBe("dialog");
    expect([...sheet.querySelectorAll("label")].map((l) => l.textContent)).not.toContain("Email");
    type("Name", "Mary B");
    choose(sheet, "Birthday", "2017-08-19");
    await settle();
    expect((calls.find((c) => c.key === "GET /ratings/suggestion") ?? {}).key).toBeTruthy();
    choose(sheet, "TV up to", "TV-G");
    submit(sheet);
    await settle();
    expect(calls.find((c) => c.key === `PUT /accounts/${uid(2)}`)?.body).toEqual({
      kind: "kid",
      name: "Mary B",
      birthday: "2017-08-19",
      isAdmin: false,
      filterMode: "ratings",
      movieCeiling: "PG",
      tvCeiling: "TV-G",
    });
    expect(document.querySelector(".sheet")).toBeNull();
  });

  it("Edit on an adult saves email and the admin flag, and shows a server refusal", async () => {
    const sheet = await edit("Ada", {
      [`PUT /accounts/${uid(1)}`]: () => err(409, "conflict", "the last admin cannot be removed"),
    });
    type("Email", "ada@new.example");
    const admin = [...sheet.querySelectorAll("label")]
      .find((l) => l.textContent?.includes("Admin"))!
      .querySelector("input")!;
    admin.checked = false;
    submit(sheet);
    await settle();
    expect(calls.find((c) => c.key === `PUT /accounts/${uid(1)}`)?.body).toEqual({
      kind: "adult",
      name: "Ada",
      email: "ada@new.example",
      isAdmin: false,
      filterMode: "ratings",
    });
    expect(sheet.querySelector("[role=alert]")?.textContent).toBe(
      "the last admin cannot be removed",
    );
  });

  it("Reset password in the Edit sheet sets and shows a temporary password", async () => {
    await edit("Ada", { [`PUT /accounts/${uid(1)}/password`]: noContent });
    click(byText("button", "Reset password"));
    click(
      [...document.querySelectorAll("button")]
        .filter((b) => b.textContent === "Reset password")
        .at(-1),
    );
    await settle();
    const sent = calls.find((c) => c.key === `PUT /accounts/${uid(1)}/password`)?.body as {
      password: string;
    };
    expect(sent.password).toMatch(/^[2-9A-Z]{4}-[2-9A-Z]{4}-[2-9A-Z]{4}$/);
    expect(document.querySelector("[role=dialog] .code")?.textContent).toBe(sent.password);
  });

  it("Remove in the Edit sheet asks in the page, then deletes the account", async () => {
    const confirm = vi.spyOn(window, "confirm");
    const sheet = await edit("Mary", { [`DELETE /accounts/${uid(2)}`]: noContent });
    click(byText("button", "Remove"));
    expect(calls.find((c) => c.key === `DELETE /accounts/${uid(2)}`)).toBeUndefined();
    expect(sheet.textContent).toContain("Remove Mary and their watch history?");
    click([...sheet.querySelectorAll("button")].filter((b) => b.textContent === "Remove").at(-1));
    await settle();
    expect(calls.find((c) => c.key === `DELETE /accounts/${uid(2)}`)).toBeTruthy();
    expect(confirm).not.toHaveBeenCalled();
  });

  it("the owner's Edit sheet offers no Remove", async () => {
    const sheet = await edit("Ada");
    expect([...sheet.querySelectorAll("button")].map((b) => b.textContent)).not.toContain("Remove");
  });

  it("Revoke on a device asks in the page, then revokes it", async () => {
    location.hash = "#/users";
    const confirm = vi.spyOn(window, "confirm");
    await boot(root, { fetch: signedIn({ ...users, [`PUT /devices/${uid(10)}`]: device }) });
    await settle();
    expect(root.textContent).toContain("This browser");
    const row = [...root.querySelectorAll("tr")].find((r) =>
      r.textContent?.includes("Living room"),
    )!;
    click(row.querySelector("button"));
    expect(calls.find((c) => c.key === `PUT /devices/${uid(10)}`)).toBeUndefined();
    expect(row.textContent).toContain("Revoke Living room?");
    click([...row.querySelectorAll("button")].at(-1));
    await settle();
    expect(calls.find((c) => c.key === `PUT /devices/${uid(10)}`)?.body).toEqual({
      name: "Living room",
      revoked: true,
    });
    expect(confirm).not.toHaveBeenCalled();
  });

  it("closes a dialog left open when the admin goes to another page", async () => {
    location.hash = "#/users";
    await boot(root, {
      fetch: signedIn({ ...users, "GET /tasks": page([]), "GET /settings": serverSettings }),
    });
    await settle();
    click(byText("button", "Add adult"));
    expect(document.querySelector(".scrim")).not.toBeNull();
    location.hash = "#/tasks";
    await settle();
    expect(root.querySelector("h1")?.textContent).toBe("Tasks");
    expect(document.querySelector(".scrim")).toBeNull();
  });

  it("a failed Revoke shows the error in the page", async () => {
    location.hash = "#/users";
    await boot(root, {
      fetch: signedIn({
        ...users,
        [`PUT /devices/${uid(10)}`]: () => err(500, "internal", "database is locked"),
      }),
    });
    await settle();
    const row = [...root.querySelectorAll("tr")].find((r) =>
      r.textContent?.includes("Living room"),
    )!;
    click(row.querySelector("button"));
    click([...row.querySelectorAll("button")].at(-1));
    await settle();
    expect(row.querySelector("[role=alert]")?.textContent).toBe("database is locked");
  });

  it("reads every page of devices, so a long-idle one is listed and signed out with its household", async () => {
    location.hash = "#/users";
    const oldest = { ...device, id: uid(11), name: "Attic TV" };
    await boot(root, {
      fetch: signedIn({
        ...users,
        "GET /devices": (_: unknown, url: URL) =>
          url.searchParams.get("offset") === "1"
            ? page([oldest])
            : { items: [device], nextOffset: 1 },
        [`PUT /devices/${uid(9)}`]: device,
        [`PUT /devices/${uid(11)}`]: oldest,
      }),
    });
    await settle();
    expect(root.textContent).toContain("Attic TV");
    click(byText("button", "Sign out everywhere"));
    click(
      [...root.querySelectorAll("button")]
        .filter((b) => b.textContent === "Sign out everywhere")
        .at(-1),
    );
    await settle();
    expect(calls.filter((c) => c.key.startsWith("PUT /devices/")).map((c) => c.key)).toEqual([
      `PUT /devices/${uid(11)}`,
      `PUT /devices/${uid(9)}`,
    ]);
  });

  it("Sign out everywhere revokes this browser's own device after the others have answered, then signs out", async () => {
    location.hash = "#/users";
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    const inner = signedIn({
      ...users,
      [`PUT /devices/${uid(10)}`]: { ...device, id: uid(10) },
      [`PUT /devices/${uid(9)}`]: device,
    });
    await boot(root, {
      fetch: async (req) => {
        const res = await inner(req);
        if (req.url.endsWith(`/devices/${uid(10)}`)) await held;
        return res;
      },
    });
    await settle();
    click(byText("button", "Sign out everywhere"));
    click(
      [...root.querySelectorAll("button")]
        .filter((b) => b.textContent === "Sign out everywhere")
        .at(-1),
    );
    await settle();
    expect(calls.some((c) => c.key === `PUT /devices/${uid(10)}`)).toBe(true);
    expect(calls.some((c) => c.key === `PUT /devices/${uid(9)}`)).toBe(false);
    release();
    await settle();
    expect(calls.filter((c) => c.key.startsWith("PUT /devices/")).map((c) => c.key)).toEqual([
      `PUT /devices/${uid(10)}`,
      `PUT /devices/${uid(9)}`,
    ]);
    expect(localStorage.length).toBe(0);
  });

  it("Revoke on this browser's own device signs out instead of reloading the page", async () => {
    location.hash = "#/users";
    await boot(root, { fetch: signedIn({ ...users, [`PUT /devices/${uid(9)}`]: device }) });
    await settle();
    const row = [...root.querySelectorAll("tr")].find((r) => r.textContent?.includes("Admin web"))!;
    click(row.querySelector("button"));
    click([...row.querySelectorAll("button")].at(-1));
    await settle();
    expect(calls.find((c) => c.key === `PUT /devices/${uid(9)}`)?.body).toEqual({
      name: "Admin web",
      revoked: true,
    });
    expect(localStorage.length).toBe(0);
  });

  describe("shared profiles and connected adults", () => {
    const hhs = page([
      { id: uid(1), name: "Ada", adultId: uid(1), createdAt: "" },
      { id: uid(2), name: "Mark", adultId: uid(3), createdAt: "" },
    ]);
    const joan = { ...kid, name: "Joan", householdIds: [uid(1), uid(2)] };
    const mark = {
      ...adult,
      id: uid(3),
      name: "Mark",
      email: "c@example.com",
      isAdmin: false,
      householdId: uid(2),
      householdIds: [uid(2), uid(1)],
    };
    const ada = { ...adult, householdIds: [uid(1)] };
    const link = { accountId: uid(1), otherAccountId: uid(3), tv: false, createdAt: "" };
    const open = async (extra: Record<string, Handler | unknown> = {}) => {
      location.hash = "#/users";
      await boot(root, {
        fetch: signedIn({
          ...users,
          "GET /households": hhs,
          "GET /accounts": page([ada, joan, mark]),
          "GET /adult-links": page([link]),
          ...extra,
        }),
      });
      await settle();
    };
    const card = (name: string) => root.querySelector<HTMLElement>(`[aria-label="${name}"]`)!;
    const row = (el: Element, name: string) =>
      [...el.querySelectorAll("tbody tr")].find((r) => r.querySelector("b")?.textContent === name)!;
    const cells = (r: Element) => [...r.querySelectorAll("td")].map((c) => c.textContent);
    const buttons = (r: Element) => [...r.querySelectorAll("button")].map((b) => b.textContent);

    it("lists every profile a household holds, with its other households and Visiting", async () => {
      await open();
      const ours = card("Household of Ada");
      expect([...ours.querySelectorAll("th")].map((t) => t.textContent)).toContain("Also in");
      expect(cells(row(ours, "Joan")).slice(0, 3)).toEqual(["JJoan", "Kid", "Mark's household"]);
      expect(cells(row(ours, "Mark")).slice(0, 3)).toEqual([
        "MMark",
        "AdultVisiting",
        "Mark's household",
      ]);
      expect(cells(row(ours, "Ada")).slice(0, 3)).toEqual(["AAda", "OwnerAdmin", "-"]);
      expect(buttons(row(ours, "Joan"))).toEqual(["Edit", "Remove from household"]);
      expect(buttons(row(ours, "Mark"))).toEqual(["Remove from household"]);
      expect(buttons(row(ours, "Ada"))).toEqual(["Edit"]);
      const theirs = card("Household of Mark");
      expect(cells(row(theirs, "Mark")).slice(0, 3)).toEqual(["MMark", "Owner", "Ada's household"]);
      expect(buttons(row(theirs, "Mark"))).toEqual(["Edit"]);
    });

    it("Remove from household asks, then takes the profile out of that household only", async () => {
      await open({ [`DELETE /households/${uid(1)}/members/${uid(2)}`]: noContent });
      const r = row(card("Household of Ada"), "Joan");
      click(
        [...r.querySelectorAll("button")].find((b) => b.textContent === "Remove from household"),
      );
      expect(r.textContent).toContain("Remove Joan from Ada's household?");
      click([...r.querySelectorAll("button")].at(-1));
      await settle();
      expect(calls.some((c) => c.key === `DELETE /households/${uid(1)}/members/${uid(2)}`)).toBe(
        true,
      );
      expect(calls.some((c) => c.key === `DELETE /accounts/${uid(2)}`)).toBe(false);
    });

    it("Add existing profile puts a joined profile not yet there into the household", async () => {
      const pending = { ...adult, id: uid(4), name: "Dee", pending: true, householdId: undefined };
      await open({
        "GET /accounts": page([ada, joan, mark, pending]),
        "GET /join-codes": page([]),
        [`PUT /households/${uid(2)}/members/${uid(1)}`]: noContent,
      });
      click(
        [...card("Household of Mark").querySelectorAll("button")].find(
          (b) => b.textContent === "Add existing profile…",
        ),
      );
      const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
      expect(dlg.querySelector("h1")!.textContent).toBe(
        "Add an existing profile to Mark's household",
      );
      const pick = dlg.querySelector("select")!;
      expect([...pick.options].map((o) => o.textContent)).toEqual(["Ada"]);
      dlg.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
      await settle();
      expect(calls.some((c) => c.key === `PUT /households/${uid(2)}/members/${uid(1)}`)).toBe(true);
      expect(document.querySelector("[role=dialog]")).toBeNull();
    });

    it("lists connected adults with their households, and switches TVs and disconnects", async () => {
      await open({
        [`PUT /adult-links/${uid(1)}/${uid(3)}`]: (b: unknown) => ({ ...link, ...(b as object) }),
        [`DELETE /adult-links/${uid(1)}/${uid(3)}`]: noContent,
      });
      const c = card("Connected adults");
      expect(c.textContent).toContain(
        "Adults in different households never see each other unless you connect them here.",
      );
      const r = c.querySelector("tbody tr")!;
      expect(cells(r).slice(0, 2)).toEqual(["AAdaAda's household", "MMarkMark's household"]);
      const sw = r.querySelector<HTMLInputElement>(
        "[role=switch][aria-label='Ada and Mark see each other on TVs']",
      )!;
      expect(sw.checked).toBe(false);
      click(sw);
      await settle();
      expect(calls.find((c) => c.key === `PUT /adult-links/${uid(1)}/${uid(3)}`)?.body).toEqual({
        tv: true,
      });
      click([...r.querySelectorAll("button")].find((b) => b.textContent === "Disconnect"));
      click([...r.querySelectorAll("button")].at(-1));
      await settle();
      expect(calls.some((c) => c.key === `DELETE /adult-links/${uid(1)}/${uid(3)}`)).toBe(true);
    });

    it("puts the switch back when the server refuses it", async () => {
      await open({
        [`PUT /adult-links/${uid(1)}/${uid(3)}`]: () => err(500, "internal", "database is locked"),
      });
      const c = card("Connected adults");
      const sw = c.querySelector<HTMLInputElement>("[role=switch]")!;
      click(sw);
      await settle();
      expect(sw.checked).toBe(false);
      expect(c.querySelector("[role=alert]")?.textContent).toBe("database is locked");
    });

    it("Connect adults links two joined adults of different households", async () => {
      await open({ "GET /adult-links": page([]), "POST /adult-links": link });
      const c = card("Connected adults");
      expect(c.textContent).toContain("No adults connected yet.");
      click([...c.querySelectorAll("button")].find((b) => b.textContent === "Connect adults"));
      const dlg = document.querySelector<HTMLElement>("[role=dialog]")!;
      const [one, other] = dlg.querySelectorAll("select");
      expect([...one.options].map((o) => o.textContent)).toEqual(["Ada", "Mark"]);
      one.value = uid(1);
      other.value = uid(1);
      const submit = () =>
        dlg.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
      submit();
      await settle();
      expect(dlg.querySelector("[role=alert]")?.textContent).toBe(
        "Pick two adults of different households.",
      );
      expect(calls.some((c) => c.key === "POST /adult-links")).toBe(false);
      other.value = uid(3);
      submit();
      await settle();
      expect(calls.find((c) => c.key === "POST /adult-links")?.body).toEqual({
        accountId: uid(1),
        otherAccountId: uid(3),
        tv: false,
      });
      expect(document.querySelector("[role=dialog]")).toBeNull();
    });
  });
});

describe("tasks", () => {
  it("Pause all pauses scheduled runs, and Resume all undoes it", async () => {
    location.hash = "#/tasks";
    let paused = false;
    await boot(root, {
      fetch: signedIn({
        "GET /tasks": page([]),
        "GET /settings": () => ({ ...serverSettings, tasksPaused: paused }),
        "PUT /settings": (b: unknown) => {
          paused = (b as { tasksPaused: boolean }).tasksPaused;
          return { ...serverSettings, tasksPaused: paused };
        },
      }),
    });
    await settle();
    click(byText("button", "Pause all"));
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      tasksPaused: true,
    });
    expect(root.querySelector(".head")?.textContent).toContain("Paused");
    click(byText("button", "Resume all"));
    await settle();
    expect(calls.filter((c) => c.key === "PUT /settings").at(-1)?.body).toEqual({
      serverName: "Den",
      tasksPaused: false,
    });
    expect(byText("button", "Pause all")).toBeTruthy();
  });

  it("polls only while visible, and a slow answer never replaces a newer one", async () => {
    location.hash = "#/tasks";
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      let n = 0;
      let slowPoll = Infinity;
      let release!: () => void;
      const slow = new Promise<void>((r) => (release = r));
      const inner = signedIn({
        "GET /tasks": () =>
          page([
            { id: uid(1), name: `Poll ${++n}`, kind: "metadata", enabled: true, createdAt: "" },
          ]),
        [`GET /tasks/${uid(1)}/runs`]: page([]),
        "GET /settings": serverSettings,
      });
      await boot(root, {
        fetch: async (req) => {
          const res = await inner(req);
          if (n === slowPoll && req.url.endsWith("/tasks?limit=200")) await slow;
          return res;
        },
      });
      await settle();
      const first = n;
      expect(root.textContent).toContain(`Poll ${first}`);
      Object.defineProperty(document, "hidden", { value: true, configurable: true });
      vi.advanceTimersByTime(5000);
      await settle();
      expect(n).toBe(first);
      Object.defineProperty(document, "hidden", { value: false, configurable: true });
      // The next poll is asked first and answers last.
      slowPoll = first + 1;
      vi.advanceTimersByTime(5000);
      await settle();
      vi.advanceTimersByTime(5000);
      await settle();
      expect(root.textContent).toContain(`Poll ${first + 2}`);
      release();
      await settle();
      expect(root.textContent).toContain(`Poll ${first + 2}`);
      expect(root.textContent).not.toContain(`Poll ${first + 1}`);
    } finally {
      Object.defineProperty(document, "hidden", { value: false, configurable: true });
      vi.useRealTimers();
    }
  });

  it("a failed task offers Retry with its error", async () => {
    location.hash = "#/tasks";
    const t = {
      id: uid(1),
      name: "Refresh metadata",
      kind: "metadata",
      enabled: true,
      createdAt: "",
      lastRun: {
        id: uid(1),
        taskId: uid(1),
        status: "failed",
        progress: 0,
        error: "TMDB 429",
        startedAt: "2026-01-01T00:00:00Z",
      },
    };
    await boot(root, {
      fetch: signedIn({
        "GET /tasks": page([t]),
        [`GET /tasks/${uid(1)}/runs`]: page([t.lastRun]),
        "GET /settings": serverSettings,
      }),
    });
    await settle();
    expect(byText("button", "Retry")).toBeTruthy();
    expect(root.textContent).toContain("TMDB 429");
  });

  it("shows what a scan did in Scheduled, History and the Dashboard", async () => {
    const run = {
      id: uid(3),
      taskId: uid(2),
      status: "succeeded",
      progress: 1,
      summary: "2 shows, 6 episodes added",
      startedAt: "2026-01-01T00:00:00Z",
    };
    const t = {
      id: uid(2),
      name: "Scan Shows",
      kind: `scan:${uid(6)}`,
      enabled: true,
      createdAt: "",
      lastRun: run,
    };
    const routes = {
      "GET /tasks": page([t]),
      [`GET /tasks/${uid(2)}/runs`]: page([run]),
      "GET /settings": serverSettings,
      "GET /stats": serverStats,
      "GET /libraries": page([]),
      "GET /sessions": page([]),
      "GET /accounts": page([]),
      "GET /devices": page([]),
    };
    location.hash = "#/tasks";
    await boot(root, { fetch: signedIn(routes) });
    await settle();
    const hits = root.textContent!.split("2 shows, 6 episodes added").length - 1;
    expect(hits).toBe(2); // last run and history

    location.hash = "#/dashboard";
    await boot(root, { fetch: signedIn(routes) });
    await settle();
    expect(root.querySelector("[aria-label='Recent activity']")?.textContent).toContain(
      "2 shows, 6 episodes added",
    );
  });
});

describe("logs", () => {
  const entries = [
    {
      id: uid(3),
      at: "2026-03-01T12:02:00Z",
      level: "info",
      source: "playback",
      message: "Session 7 started: video_transcode",
      sessionId: uid(7),
      ffmpegCommand: "ffmpeg -i /m/a.mkv out.m3u8",
      transcodeReason: "HEVC Main10 is not decodable here",
    },
    {
      id: uid(2),
      at: "2026-03-01T12:01:00Z",
      level: "warn",
      source: "tmdb",
      message: "TMDB returned 429",
    },
    {
      id: uid(1),
      at: "2026-03-01T12:00:00Z",
      level: "error",
      source: "task",
      message: "Task failed",
    },
  ];
  const boot_ = async (routes: Record<string, Handler | unknown> = {}) => {
    location.hash = "#/logs";
    await boot(root, { fetch: signedIn({ "GET /logs": page(entries), ...routes }) });
    await settle();
  };
  const select = (label: string, value: string) => {
    const el = root.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
    el.value = value;
    el.dispatchEvent(new Event("change", { bubbles: true }));
  };

  it("lists entries newest first with level tags, and hides details until asked", async () => {
    await boot_();
    const rows = [...root.querySelectorAll("tbody tr")];
    expect(rows).toHaveLength(3);
    expect(rows[0].textContent).toContain("Session 7 started");
    expect(rows[1].querySelector(".tag.warn")?.textContent).toBe("Warning");
    expect(rows[2].querySelector(".tag.err")?.textContent).toBe("Error");
    expect(root.querySelector("pre")).toBeNull();

    click(byText("button", "Show details"));
    expect(root.querySelector("pre")?.textContent).toBe("ffmpeg -i /m/a.mkv out.m3u8");
    expect(root.querySelector("tr.detail")?.textContent).toContain("HEVC Main10 is not decodable");
    const toggle = byText("button", "Hide details")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    click(toggle);
    expect(root.querySelector("pre")).toBeNull();
  });

  it("dates the rows of a window longer than a day, and only times the shorter ones", async () => {
    await boot_();
    const at = () => root.querySelector("tbody tr td")?.textContent;
    expect(at()).toBe(new Date(entries[0].at).toLocaleTimeString());
    select("Time", "week");
    await settle();
    expect(at()).toBe(new Date(entries[0].at).toLocaleString());
  });

  it("sends the level, source and search filters and says when nothing matches", async () => {
    let query = new URL("http://x");
    await boot_({
      "GET /logs": (_b: unknown, url: URL) => {
        query = url;
        return page(url.searchParams.get("q") === "zzz" ? [] : entries);
      },
    });
    select("Level", "warn");
    select("Source", "tmdb");
    await settle();
    expect(query.searchParams.get("level")).toBe("warn");
    expect(query.searchParams.get("source")).toBe("tmdb");
    expect(query.searchParams.get("since")).toBeTruthy();
    type("Search messages", "zzz");
    await new Promise((r) => setTimeout(r, 400));
    await settle();
    expect(query.searchParams.get("q")).toBe("zzz");
    expect(root.textContent).toContain("No entries match.");
  });

  const reported = {
    id: uid(4),
    at: "2026-03-01T12:03:00Z",
    level: "error",
    source: "client",
    message: "<img src=x onerror=alert(1)> Home failed",
    client: {
      platform: "ios",
      appVersion: "1.4.7",
      osVersion: "iOS 27.1",
      context: "home",
      detail: "TypeError: x is undefined\n  at render",
      deviceId: uid(9),
      deviceName: "Kitchen iPad",
      profileId: uid(5),
      profileName: "Kid",
    },
  };

  it("shows a client's entry by platform, with where it came from in its details, as text", async () => {
    await boot_({ "GET /logs": page([reported, ...entries]) });
    const row = root.querySelector("tbody tr")!;
    expect(row.children[2].textContent).toBe("iOS app");
    expect(row.textContent).toContain("<img src=x onerror=alert(1)> Home failed");
    expect(root.querySelector("tbody img")).toBeNull();
    click(byText("button", "Show details"));
    const detail = root.querySelector("tr.detail")!;
    expect(detail.textContent).toContain("Rabbit Ears 1.4.7 on iOS 27.1");
    expect(detail.textContent).toContain("Kitchen iPad");
    expect(detail.textContent).toContain("Kid");
    expect(detail.textContent).toContain("home");
    expect(detail.querySelector("pre")?.textContent).toBe("TypeError: x is undefined\n  at render");
  });

  it("says a client's device and profile are gone once they are", async () => {
    const { platform, appVersion, osVersion, context } = reported.client;
    await boot_({
      "GET /logs": page([{ ...reported, client: { platform, appVersion, osVersion, context } }]),
    });
    click(byText("button", "Show details"));
    const detail = root.querySelector("tr.detail")!;
    expect(detail.textContent).toContain("Device: removed");
    expect(detail.textContent).toContain("Profile: removed");
    expect(detail.querySelector("pre")).toBeNull();
  });

  it("filters to clients' entries and to one platform", async () => {
    let query = new URL("http://x");
    await boot_({
      "GET /logs": (_b: unknown, url: URL) => ((query = url), page([reported])),
    });
    select("Source", "client");
    select("Platform", "android");
    await settle();
    expect(query.searchParams.get("source")).toBe("client");
    expect(query.searchParams.get("platform")).toBe("android");
    select("Platform", "");
    await settle();
    expect(query.searchParams.has("platform")).toBe(false);
  });

  it("a slow answer for an older filter never replaces the newer one", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const inner = signedIn({
      "GET /logs": (_b: unknown, url: URL) =>
        page(entries.filter((e) => e.level === url.searchParams.get("level"))),
    });
    location.hash = "#/logs";
    await boot(root, {
      fetch: async (req) => {
        if (req.url.includes("level=warn")) await gate;
        return inner(req);
      },
    });
    await settle();
    select("Level", "warn");
    select("Level", "error");
    await settle();
    expect(root.querySelector("tbody")?.textContent).toContain("Task failed");
    release();
    await settle();
    expect(root.querySelector("tbody")?.textContent).toContain("Task failed");
    expect(root.querySelector("tbody")?.textContent).not.toContain("TMDB returned 429");
  });

  it("Load older fetches the next page, and Download asks for the filtered text", async () => {
    const urls: URL[] = [];
    let downloaded = new URL("http://x");
    URL.createObjectURL = () => "blob:x";
    URL.revokeObjectURL = () => {};
    await boot_({
      "GET /logs": (_b: unknown, url: URL) => {
        urls.push(url);
        return url.searchParams.get("offset") === "100"
          ? page([
              {
                id: uid(0),
                at: "2026-03-01T11:00:00Z",
                level: "info",
                source: "scan",
                message: "Older entry",
              },
            ])
          : { items: entries, nextOffset: 100 };
      },
      "GET /logs/download": (_b: unknown, url: URL) => {
        downloaded = url;
        return new Response("line\n", { status: 200, headers: { "content-type": "text/plain" } });
      },
    });
    click(byText("button", "Load older"));
    await settle();
    expect(root.textContent).toContain("Older entry");
    expect(byText("button", "Load older")).toBeUndefined();
    select("Level", "error");
    await settle();
    click(byText("button", "Download"));
    await settle();
    const dl = calls.filter((c) => c.key === "GET /logs/download");
    expect(dl).toHaveLength(1);
    expect(downloaded.searchParams.get("level")).toBe("error");
  });
});

describe("settings", () => {
  const routes = {
    "GET /settings": serverSettings,
    "GET /attribution": {
      provider: "TMDB",
      text: "This product uses the TMDB API but is not endorsed or certified by TMDB.",
      url: "https://www.themoviedb.org",
    },
    "PUT /settings": (b: unknown) => ({ ...serverSettings, ...(b as object) }),
    "PUT /me/password": noContent,
    "DELETE /transcode-cache": noContent,
    "POST /metadata/refresh": () => new Response(null, { status: 202 }),
  };
  const open = async (extra: Record<string, Handler | unknown> = {}) => {
    location.hash = "#/settings";
    await boot(root, { fetch: signedIn({ ...routes, ...extra }) });
    await settle();
  };
  const cardText = (name: string) => root.querySelector(`[aria-label='${name}']`)?.textContent;

  it("links the attribution only for an http or https address", async () => {
    await open();
    expect(root.querySelector("a[href='https://www.themoviedb.org']")?.textContent).toBe("TMDB");
    root.replaceChildren();
    await open({
      "GET /attribution": { provider: "TMDB", text: "Uses TMDB.", url: "javascript:alert(1)" },
    });
    expect(root.querySelector("a[href^='javascript']")).toBeNull();
    expect(cardText("Metadata sources")).toContain("Uses TMDB. TMDB");
  });

  it("opens Settings in a browser that has no URL.parse", async () => {
    const parse = URL.parse;
    // Chrome and Firefox before 126 and Safari before 18 lack the static URL.parse.
    Object.defineProperty(URL, "parse", { value: undefined, configurable: true });
    try {
      await open();
      expect(root.querySelector("a[href='https://www.themoviedb.org']")?.textContent).toBe("TMDB");
    } finally {
      Object.defineProperty(URL, "parse", { value: parse, configurable: true });
    }
  });

  it("orders the cards, ending with the server's facts, none of them editable", async () => {
    await open();
    expect([...root.querySelectorAll("main section.card")].map((c) => c.ariaLabel)).toEqual([
      "Server",
      "Profiles",
      "Transcoding",
      "Metadata sources",
      "Admin account",
      "Danger zone",
      "About this server",
    ]);
    expect(cardText("Server")).not.toContain("Version");
    expect(cardText("Metadata sources")).toContain("Key set");
    expect(cardText("Metadata sources")).toContain("not endorsed or certified by TMDB");
    const about = root.querySelector("[aria-label='About this server']")!;
    expect(about.querySelector("input, select, button, textarea")).toBeNull();
    expect(about.querySelector("p.sub")?.textContent).toBe(
      "Nothing here changes from this page. A row that names a variable is read once, when the server starts: set it in the env file in the server's folder, then restart.",
    );
    const rows = [...about.querySelectorAll(".set")].map((r) => [
      r.querySelector("b")?.textContent,
      [...r.children].slice(1).map((c) => c.textContent),
    ]);
    expect(rows).toEqual([
      ["Version", ["Rabbit Ears 1.2.3"]],
      ["Port", ["RABBIT_EARS_PORT", "8477"]],
      ["Database", ["RABBIT_EARS_DB", "/Volumes/Media/.rabbit-ears/rabbit-ears.db"]],
      ["Data folder", ["RABBIT_EARS_DATA", "/Volumes/Media/.rabbit-ears"]],
      ["Disk", ["5.8 TB free of 20 TB"]],
      ["Processor", ["Apple M2 Max · 12 cores"]],
      ["Memory", ["32 GB"]],
      ["Video encoder", ["Apple VideoToolbox"]],
      ["Subtitles", []],
    ]);
    expect([...about.querySelectorAll("span.path")].map((e) => e.textContent)).toEqual([
      "env",
      "RABBIT_EARS_PORT",
      "RABBIT_EARS_DB",
      "RABBIT_EARS_DATA",
    ]);
    expect(about.textContent).toContain("Detected when the server starts");
    expect(about.textContent).toContain(
      "A subtitle shows on its own only when the file marks it forced; viewers choose any other in the player. Text subtitles are sent beside the video; bitmap ones are converted, or burned in as a last resort.",
    );
    expect(cardText("YouTube")).toBeUndefined();
  });

  it("sets the transcode limit from 1 to 8", async () => {
    const menu = () =>
      root.querySelector<HTMLSelectElement>("select[aria-label='Simultaneous transcodes']")!;
    await open();
    expect([...menu().options].map((o) => o.textContent)).toEqual([
      "1",
      "2",
      "3",
      "4",
      "5",
      "6",
      "7",
      "8",
    ]);
    expect(menu().value).toBe("3");
    pickMeta("Simultaneous transcodes", "5");
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      maxTranscodes: 5,
    });
    expect(cardText("Transcoding")).toContain("Saved");
  });

  it("sets the cache limit in the page's own units, and shows one the menu lacks", async () => {
    const menu = () => root.querySelector<HTMLSelectElement>("select[aria-label='Cache limit']")!;
    await open();
    expect([...menu().options].map((o) => o.textContent)).toEqual(
      [5e9, 10e9, 20e9, 50e9, 100e9].map(bytes),
    );
    expect(menu().selectedOptions[0].textContent).toBe("20 GB");
    expect(cardText("Transcoding")).toContain("oldest idle streams are deleted. 1.2 GB used now.");
    pickMeta("Cache limit", "50000000000");
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      transcodeCacheCapBytes: 50_000_000_000,
    });
    root.replaceChildren();
    await open({ "GET /settings": { ...serverSettings, transcodeCacheCapBytes: 7_500_000_000 } });
    expect(menu().selectedOptions[0].textContent).toBe("7.5 GB");
  });

  it("puts a transcode limit back and says why when the server refuses it", async () => {
    await open({ "PUT /settings": () => err(400, "bad_request", "maxTranscodes is out of range") });
    pickMeta("Simultaneous transcodes", "8");
    await settle();
    expect(
      root.querySelector<HTMLSelectElement>("select[aria-label='Simultaneous transcodes']")!.value,
    ).toBe("3");
    expect(cardText("Transcoding")).toContain("maxTranscodes is out of range");
  });

  it("says when no TMDB key is set", async () => {
    await open({ "GET /settings": { ...serverSettings, metadata: { tmdbConfigured: false } } });
    expect(root.querySelector("[aria-label='Metadata sources'] .tag.err")?.textContent).toBe(
      "Not configured",
    );
    expect(cardText("Metadata sources")).not.toContain("environment");
    expect(byText("[aria-label='Metadata sources'] button", "Remove key")).toBeUndefined();
  });

  const keyField = () => root.querySelector<HTMLInputElement>("input[aria-label='TMDB API key']")!;
  const metadataButton = (t: string) => byText("[aria-label='Metadata sources'] button", t);

  it("saves a TMDB key, and never shows it", async () => {
    await open({
      "GET /settings": {
        ...serverSettings,
        tasksPaused: true,
        metadata: { tmdbConfigured: false },
      },
      "PUT /settings": () => ({ ...serverSettings, tasksPaused: true }),
    });
    expect(keyField().type).toBe("password");
    expect(keyField().value).toBe("");
    type("TMDB API key", " abc123 ");
    click(metadataButton("Save"));
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      tmdbKey: "abc123",
    });
    expect(keyField().value).toBe("");
    expect(cardText("Metadata sources")).toContain("Key set");
    expect(cardText("Metadata sources")).toContain("Saved");
    expect(metadataButton("Remove key")).toBeTruthy();
  });

  it("removes the stored TMDB key", async () => {
    await open({
      "PUT /settings": () => ({ ...serverSettings, metadata: { tmdbConfigured: false } }),
    });
    click(metadataButton("Remove key"));
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      tmdbKey: "",
    });
    expect(cardText("Metadata sources")).toContain("Not configured");
    expect(metadataButton("Remove key")).toBeUndefined();
  });

  it("shows a rejected TMDB key's error on the field", async () => {
    await open({
      "PUT /settings": () =>
        new Response(
          JSON.stringify({
            code: "bad_request",
            message: "invalid request",
            fields: [{ field: "tmdbKey", message: "TMDB rejected this key" }],
          }),
          { status: 400, headers: { "content-type": "application/json" } },
        ),
    });
    type("TMDB API key", "wrong");
    click(metadataButton("Save"));
    await settle();
    const described = document.getElementById(keyField().getAttribute("aria-describedby")!);
    expect(described?.textContent).toBe("TMDB rejected this key");
    expect(described?.hidden).toBe(false);
    expect(keyField().getAttribute("aria-invalid")).toBe("true");
    expect(cardText("Metadata sources")).toContain("Key set");
  });

  const pickMeta = (label: string, value: string) => {
    const sel = root.querySelector<HTMLSelectElement>(`select[aria-label='${label}']`)!;
    sel.value = value;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  };
  const keyless = { tmdbConfigured: false };

  it("sets the metadata source and mode side by side", async () => {
    let metadata = { tmdbConfigured: true, source: "local", mode: "only" };
    await open({
      "GET /settings": { ...serverSettings, metadata },
      "PUT /settings": (b: unknown) => {
        const { metadataSource: source, metadataMode: mode } = b as Record<string, string>;
        metadata = { tmdbConfigured: true, source, mode };
        return { ...serverSettings, metadata };
      },
    });
    expect(
      root.querySelector<HTMLSelectElement>("select[aria-label='Metadata source']")!.value,
    ).toBe("local");
    expect(root.querySelector<HTMLSelectElement>("select[aria-label='Metadata mode']")!.value).toBe(
      "only",
    );
    pickMeta("Metadata source", "remote");
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      metadataSource: "remote",
      metadataMode: "only",
    });
    expect(cardText("Metadata sources")).toContain("Saved");
    expect(
      root.querySelector<HTMLSelectElement>("select[aria-label='Metadata source']")!.value,
    ).toBe("remote");
  });

  it("sets the read-ahead, and shows one the menu lacks", async () => {
    const ahead = () => root.querySelector<HTMLSelectElement>("select[aria-label='Read-ahead']")!;
    await open();
    expect(ahead().selectedOptions[0].textContent).toBe("5 minutes");
    pickMeta("Read-ahead", "600");
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      readAheadSeconds: 600,
    });
    expect(cardText("Transcoding")).toContain("Saved");
    root.replaceChildren();
    await open({ "GET /settings": { ...serverSettings, readAheadSeconds: 45 } });
    expect(ahead().selectedOptions[0].textContent).toBe("45 seconds");
  });

  it("says a missing TMDB key leaves movies and TV local, unless metadata is Local only", async () => {
    const note =
      "No TMDB key is set, so movies and TV have only local data. Music and YouTube need no key.";
    for (const [source, mode, shown] of [
      ["local", "only", false],
      ["local", "preferred", true],
      ["remote", "only", true],
      ["remote", "preferred", true],
    ] as const) {
      root.replaceChildren();
      await open({
        "GET /settings": { ...serverSettings, metadata: { ...keyless, source, mode } },
      });
      expect(cardText("Metadata sources")?.includes(note)).toBe(shown);
    }
    root.replaceChildren();
    await open({
      "GET /settings": {
        ...serverSettings,
        metadata: { tmdbConfigured: true, source: "remote", mode: "only" },
      },
    });
    expect(cardText("Metadata sources")).not.toContain("No TMDB key is set");
  });

  it("says the TMDB key is not used while metadata is Local only, and drops it once that changes", async () => {
    const note = "Not used while metadata is Local only.";
    await open({
      "GET /settings": {
        ...serverSettings,
        metadata: { tmdbConfigured: true, source: "local", mode: "only" },
      },
      "PUT /settings": () => ({
        ...serverSettings,
        metadata: { tmdbConfigured: true, source: "local", mode: "preferred" },
      }),
    });
    expect(cardText("Metadata sources")).toContain(note);
    pickMeta("Metadata mode", "preferred");
    await settle();
    expect(cardText("Metadata sources")).not.toContain(note);
  });

  it("does not send an empty TMDB key as a removal", async () => {
    await open();
    click(metadataButton("Save"));
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")).toBeUndefined();
  });

  it("turns Gravatar pictures on from the Profiles card", async () => {
    await open({ "GET /settings": { ...serverSettings, gravatar: false } });
    expect(cardText("Profiles")).toContain("never contact gravatar.com");
    const sw = root.querySelector<HTMLInputElement>("[role=switch][aria-label='Gravatar']")!;
    expect(sw.checked).toBe(false);
    click(sw);
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Den",
      gravatar: true,
    });
  });

  it("saves the server name as it changes, without sending the pause", async () => {
    await open({ "GET /settings": { ...serverSettings, tasksPaused: true } });
    const name = root.querySelector<HTMLInputElement>("input[aria-label='Server name']")!;
    expect(name.value).toBe("Den");
    name.value = "Basement";
    name.dispatchEvent(new Event("change", { bubbles: true }));
    await settle();
    expect(calls.find((c) => c.key === "PUT /settings")?.body).toEqual({
      serverName: "Basement",
    });
    expect(cardText("Server")).toContain("Saved");
    expect(root.querySelector("aside .brand")?.textContent).toBe("BasementAdmin");
  });

  it("changes the admin's own password and refuses a mismatch", async () => {
    await open();
    const rows = [...root.querySelectorAll("[aria-label='Admin account'] .set")];
    expect(rows.map((r) => r.querySelector(".l")?.textContent)).toEqual([
      "PasswordSigned in as ada@example.com",
    ]);
    click(byText("button", "Change password"));
    type("Current password", "old password here");
    type("New password", "correct horse battery");
    type("Confirm new password", "correct horse batterx");
    const form = root.querySelector<HTMLFormElement>("[aria-label='Admin account'] form")!;
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.find((c) => c.key === "PUT /me/password")).toBeUndefined();
    expect(form.querySelector("[role=alert]")?.textContent).toBe("The passwords do not match.");
    type("Confirm new password", "correct horse battery");
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await settle();
    expect(calls.find((c) => c.key === "PUT /me/password")?.body).toEqual({
      currentPassword: "old password here",
      newPassword: "correct horse battery",
    });
    expect(cardText("Admin account")).toContain("Password changed");
  });

  it("danger zone actions confirm in the page before acting", async () => {
    const confirm = vi.spyOn(window, "confirm");
    await open();
    const zone = root.querySelector<HTMLElement>("[aria-label='Danger zone']")!;
    const btn = (t: string) =>
      [...zone.querySelectorAll("button")].filter((b) => b.textContent === t);
    click(btn("Clear cache")[0]);
    expect(calls.find((c) => c.key === "DELETE /transcode-cache")).toBeUndefined();
    click(btn("Cancel")[0]);
    expect(btn("Cancel")).toHaveLength(0);
    click(btn("Clear cache")[0]);
    click(btn("Clear cache").at(-1));
    await settle();
    expect(calls.find((c) => c.key === "DELETE /transcode-cache")).toBeTruthy();
    click(btn("Refresh all")[0]);
    click(btn("Refresh all").at(-1));
    await settle();
    expect(calls.find((c) => c.key === "POST /metadata/refresh")).toBeTruthy();
    expect(zone.textContent).toContain("Queued");
    expect(confirm).not.toHaveBeenCalled();
  });
});

describe("stations", () => {
  const st = (id: number, name: string, extra = {}) => ({
    id: uid(id),
    name,
    kind: "movies",
    ownerId: uid(1),
    householdId: uid(1),
    playlistId: uid(9),
    order: "playlist",
    visibility: "household",
    days: [],
    times: [],
    onAir: { status: "off_schedule" },
    createdAt: "",
    modifiedAt: "",
    ...extra,
  });

  it("lists every station after Libraries, each with a Server wide switch", async () => {
    location.hash = "#/stations";
    await boot(root, {
      fetch: signedIn({
        "GET /admin/stations": page([
          st(4, "The Big Screen", { rating: "PG-13" }),
          st(5, "Kitchen Radio", { kind: "music", visibility: "server" }),
        ]),
        "GET /accounts": page([adult]),
        "GET /households": page([
          { id: uid(1), name: "Ada's household", adultId: uid(1), createdAt: "" },
        ]),
        [`PUT /admin/stations/${uid(4)}/server-wide`]: st(4, "The Big Screen", {
          visibility: "server",
        }),
      }),
    });
    await settle();
    const nav = [...root.querySelectorAll("nav a")].map((a) => a.textContent);
    expect(nav.indexOf("Stations")).toBe(nav.indexOf("Libraries") + 1);
    expect(root.textContent).toContain("2 stations · 1 server wide");
    const rows = [...root.querySelectorAll("tbody tr")].map((tr) =>
      [...tr.querySelectorAll("td")].map((td) => td.textContent),
    );
    expect(rows[0].slice(0, 6)).toEqual([
      "The Big Screen",
      "Movies",
      "Ada",
      "Ada's household",
      "PG-13",
      "Household",
    ]);
    expect(rows[1][4]).toBe("None");
    const sw = root.querySelector<HTMLInputElement>(
      "[role=switch][aria-label='Server wide: The Big Screen']",
    )!;
    expect(sw.checked).toBe(false);
    expect(
      root.querySelector<HTMLInputElement>(
        "[role=switch][aria-label='Server wide: Kitchen Radio']",
      )!.checked,
    ).toBe(true);
    click(sw);
    await settle();
    expect(calls.find((c) => c.key === `PUT /admin/stations/${uid(4)}/server-wide`)!.body).toEqual({
      serverWide: true,
    });
    expect(root.querySelector("tbody tr")!.textContent).toContain("Server");
  });
});

describe("single sign-on", () => {
  it("offers the providers on the sign-in page, returning to the admin UI", async () => {
    await boot(root, {
      fetch: mockFetch({
        "GET /health": health(),
        "GET /auth/sso-providers": [{ id: uid(3), name: "Keyhole" }],
      }),
    });
    await settle();
    const a = byText("a", "Sign in with Keyhole") as HTMLAnchorElement;
    expect(a.getAttribute("href")).toBe(`/api/v2/auth/sso/${uid(3)}/start?returnTo=admin`);
    expect(root.querySelector("form")!.contains(a)).toBe(true);
  });

  it("exchanges an #sso= code as the admin web device and opens the shell", async () => {
    history.replaceState(null, "", "/admin/#sso=one-time");
    await boot(root, {
      fetch: mockFetch({
        "GET /health": health(),
        "POST /auth/sso/exchange": { token: "tok", me: me() },
        "GET /me": me(),
        "GET /stats": {},
      }),
    });
    await settle();
    expect(calls.find((c) => c.key === "POST /auth/sso/exchange")?.body).toEqual({
      code: "one-time",
      deviceName: "Admin web",
      platform: "web",
    });
    expect(localStorage.getItem("rabbit-ears.admin.token")).toBe("tok");
    expect(root.querySelector("aside")).toBeTruthy();
  });

  it("explains an #sso_error= and clears the fragment", async () => {
    history.replaceState(null, "", "/admin/#sso_error=not_allowed");
    await boot(root, { fetch: mockFetch({ "GET /health": health() }) });
    await settle();
    expect(root.querySelector("[role=alert]")?.textContent).toBe(
      "This account is not allowed to sign in here.",
    );
    expect(location.hash).toBe("");
  });

  const provider = (id: number, name: string, extra = {}) => ({
    id: uid(id),
    name,
    issuer: "https://id.example.com",
    clientId: "rabbit-ears",
    clientSecretSet: true,
    linksByEmail: false,
    enabled: true,
    createdAt: "",
    modifiedAt: "",
    ...extra,
  });
  const open = async (extra: Record<string, Handler | unknown> = {}) => {
    location.hash = "#/sso";
    await boot(root, {
      fetch: signedIn({
        "GET /admin/sso-providers": page([
          provider(3, "Keyhole", { requiredGroup: "media", linksByEmail: true }),
          provider(4, "Attic", { enabled: false }),
        ]),
        ...extra,
      }),
    });
    await settle();
  };
  const dialogForm = () => document.querySelector<HTMLFormElement>(".dialog form")!;
  const submit = () => dialogForm().dispatchEvent(new Event("submit", { cancelable: true }));
  const check = (label: string, on: boolean) => {
    const el = [...document.querySelectorAll("label")].find((l) => l.textContent === label)!
      .control as HTMLInputElement;
    el.checked = on;
  };

  it("lists the providers and shows the callback URL to register", async () => {
    await open();
    const nav = [...root.querySelectorAll("nav a")].map((a) => a.textContent);
    expect(nav.indexOf("Single sign-on")).toBe(nav.indexOf("Users & profiles") + 1);
    expect(root.querySelector<HTMLInputElement>("input[aria-label='Callback URL']")!.value).toBe(
      `${location.origin}/api/v2/auth/sso/callback`,
    );
    const rows = [...root.querySelectorAll("tbody tr")].map((tr) => tr.textContent);
    expect(rows[0]).toContain("Keyhole");
    expect(rows[0]).toContain("media");
    expect(
      root.querySelector<HTMLInputElement>("[role=switch][aria-label='Enabled: Attic']")!.checked,
    ).toBe(false);
  });

  it("adds a provider with a write-only secret", async () => {
    await open({ "POST /admin/sso-providers": provider(5, "Front door") });
    click(byText("button", "Add provider"));
    type("Name", "Front door");
    type("Issuer URL", "https://id.example.com ");
    type("Client ID", "rabbit-ears");
    type("Client secret", "s3cret");
    check("Accounts from this provider are mine to control", true);
    expect(document.querySelector<HTMLInputElement>(".dialog input[type=password]")).toBeTruthy();
    expect(document.querySelector(".dialog")!.textContent).toContain("same email");
    submit();
    await settle();
    expect(calls.find((c) => c.key === "POST /admin/sso-providers")?.body).toEqual({
      name: "Front door",
      issuer: "https://id.example.com",
      clientId: "rabbit-ears",
      clientSecret: "s3cret",
      linksByEmail: true,
      enabled: true,
    });
    expect(document.querySelector(".dialog")).toBeNull();
  });

  it("edits a provider keeping its secret, and shows an issuer refusal on the field", async () => {
    let refuse = true;
    await open({
      [`PUT /admin/sso-providers/${uid(3)}`]: () =>
        refuse
          ? new Response(
              JSON.stringify({
                code: "bad_request",
                message: "invalid request",
                fields: [{ field: "issuer", message: "no OpenID provider answers as this issuer" }],
              }),
              { status: 400, headers: { "content-type": "application/json" } },
            )
          : provider(3, "Keyhole"),
    });
    const row = [...root.querySelectorAll("tr")].find((r) => r.textContent?.includes("Keyhole"))!;
    click([...row.querySelectorAll("button")].find((b) => b.textContent === "Edit"));
    expect(document.querySelector<HTMLInputElement>(".dialog input[type=password]")!.value).toBe(
      "",
    );
    submit();
    await settle();
    expect(document.querySelector(".dialog [role=alert]")?.textContent).toBe(
      "no OpenID provider answers as this issuer",
    );
    refuse = false;
    submit();
    await settle();
    expect(
      calls.filter((c) => c.key === `PUT /admin/sso-providers/${uid(3)}`).at(-1)?.body,
    ).toEqual({
      name: "Keyhole",
      issuer: "https://id.example.com",
      clientId: "rabbit-ears",
      requiredGroup: "media",
      linksByEmail: true,
      enabled: true,
    });
  });

  it("enables, disables and deletes a provider", async () => {
    await open({
      [`PUT /admin/sso-providers/${uid(4)}`]: (b: unknown) => provider(4, "Attic", b as object),
      [`DELETE /admin/sso-providers/${uid(4)}`]: noContent,
    });
    click(root.querySelector("[role=switch][aria-label='Enabled: Attic']"));
    await settle();
    const put = calls.find((c) => c.key === `PUT /admin/sso-providers/${uid(4)}`)!.body as Record<
      string,
      unknown
    >;
    expect(put.enabled).toBe(true);
    expect(put).not.toHaveProperty("clientSecret");
    const row = [...root.querySelectorAll("tr")].find((r) => r.textContent?.includes("Attic"))!;
    click([...row.querySelectorAll("button")].find((b) => b.textContent === "Delete"));
    click([...row.querySelectorAll("button")].filter((b) => b.textContent === "Delete").at(-1));
    await settle();
    expect(calls.find((c) => c.key === `DELETE /admin/sso-providers/${uid(4)}`)).toBeTruthy();
  });

  it("unlinks an adult's single sign-on identity from their Edit sheet", async () => {
    location.hash = "#/users";
    await boot(root, {
      fetch: signedIn({
        "GET /households": page([{ id: uid(1), name: "Ada", adultId: uid(1), createdAt: "" }]),
        "GET /accounts": page([adult]),
        "GET /devices": page([device]),
        "GET /adult-links": page([]),
        [`GET /accounts/${uid(1)}/identities`]: page([
          {
            id: uid(7),
            accountId: uid(1),
            providerId: uid(3),
            subject: "u1",
            email: "ada@id.example.com",
            createdAt: "",
          },
        ]),
        "GET /admin/sso-providers": page([provider(3, "Keyhole")]),
        [`DELETE /accounts/${uid(1)}/identities/${uid(7)}`]: noContent,
      }),
    });
    await settle();
    const row = [...root.querySelectorAll("tr")].find((r) => r.textContent?.includes("Ada"))!;
    click([...row.querySelectorAll("button")].find((b) => b.textContent === "Edit"));
    await settle();
    const sheet = document.querySelector<HTMLElement>(".sheet")!;
    expect(sheet.textContent).toContain("Keyhole");
    expect(sheet.textContent).toContain("ada@id.example.com");
    click([...sheet.querySelectorAll("button")].find((b) => b.textContent === "Unlink"));
    click([...sheet.querySelectorAll("button")].filter((b) => b.textContent === "Unlink").at(-1));
    await settle();
    expect(
      calls.find((c) => c.key === `DELETE /accounts/${uid(1)}/identities/${uid(7)}`),
    ).toBeTruthy();
  });
});
