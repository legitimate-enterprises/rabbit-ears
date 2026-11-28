// Component tests: the real pages and generated client against a mocked API.
import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { type Handler, at, calls, json, mockFetch, root, until } from "./testSupport";
import { uid } from "../api/uid";

// hls.js needs MediaSource, which jsdom lacks; the fake records what the player asks of it.
interface FakeHls {
  config: Record<string, unknown>;
  levels: { width?: number; height: number; bitrate: number }[];
  startLevel: number;
  loadLevel: number;
  started?: number;
  destroyed: boolean;
  emit(event: string, data: unknown): void;
}
const hlsMock = vi.hoisted(() => ({ instances: [] as FakeHls[] }));
vi.mock("hls.js", () => {
  class Hls {
    static isSupported = () => true;
    static Events = {
      ERROR: "hlsError",
      MANIFEST_PARSED: "hlsManifestParsed",
      LEVEL_SWITCHED: "hlsLevelSwitched",
    };
    config: Record<string, unknown>;
    handlers: Record<string, (e: string, d: unknown) => void> = {};
    levels: FakeHls["levels"] = [];
    startLevel = -1;
    loadLevel = -1;
    currentLevel = -1;
    bandwidthEstimate = 0;
    started?: number;
    destroyed = false;
    constructor(config: Record<string, unknown>) {
      this.config = config;
      hlsMock.instances.push(this);
    }
    on(e: string, f: (e: string, d: unknown) => void) {
      this.handlers[e] = f;
    }
    emit(e: string, d: unknown) {
      this.handlers[e]?.(e, d);
    }
    loadSource() {}
    attachMedia() {}
    startLoad(at: number) {
      this.started = at;
    }
    destroy() {
      this.destroyed = true;
    }
  }
  return { default: Hls };
});

const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult", extra = {}) => ({
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
  ...extra,
});
const health = { status: "ok", version: "1.0.0", setupRequired: false };
const me = (active: number, kids = true, others: unknown[] = []) => ({
  device: { id: uid(1) },
  account: acct(1, "Helga"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Helga", adultId: uid(1), createdAt: ts },
  profiles: [
    acct(1, "Helga", "adult", { hasPin: true }),
    ...(kids ? [acct(2, "Jason", "kid")] : []),
    ...others,
  ],
});
const page = (items: unknown[]) => ({ items });
const item = (id: number, kind: string, extra = {}) => ({
  id: uid(id),
  libraryId: uid(1),
  kind,
  parsedTitle: `T${id}`,
  addedAt: `2026-01-0${id}T00:00:00Z`,
  modifiedAt: ts,
  ...extra,
});

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  localStorage.clear();
  history.replaceState(null, "", "/");
  location.hash = "";
  // jsdom fires hashchange asynchronously; let it pass before the next boot listens.
  await new Promise((r) => setTimeout(r, 0));
});

describe("sign in and join", () => {
  it("signs in, stores the device token and shows a wrong-password message on 401", async () => {
    const r = root();
    let ok = false;
    const fetch = mockFetch({
      "GET /health": health,
      "GET /me": me(1),
      "POST /auth/sign-in": () =>
        ok ? { token: "tok", me: me(1) } : json({ code: "unauthorized", message: "bad" }, 401),
    });
    await boot(r, { fetch });
    r.querySelector<HTMLInputElement>("input[type=email]")!.value = "h@example.com";
    r.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
    r.querySelector("form")!.requestSubmit();
    await until(() => r.querySelector(".err")?.textContent === "Email or password is wrong.");
    ok = true;
    r.querySelector("form")!.requestSubmit();
    await until(() => localStorage.getItem("rabbit-ears.admin.token") === "tok");
    expect(
      calls.find(
        (c) =>
          c.key === "POST /auth/sign-in" && (c.body as { platform: string }).platform === "web",
      ),
    ).toBeTruthy();
  });

  it("says how long to wait when sign in is rate limited", async () => {
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "POST /auth/sign-in": () =>
          new Response(JSON.stringify({ code: "too_many_requests", message: "slow down" }), {
            status: 429,
            headers: { "content-type": "application/json", "Retry-After": "250" },
          }),
      }),
    });
    r.querySelector<HTMLInputElement>("input[type=email]")!.value = "h@example.com";
    r.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
    r.querySelector("form")!.requestSubmit();
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe("Too many attempts. Try again in 5 minutes.");
  });

  it("formats a typed invite code with its dash", async () => {
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": health }) });
    [...r.querySelectorAll("a")].find((a) => a.textContent === "Have an invite code?")!.click();
    const code = r.querySelector<HTMLInputElement>("input")!;
    code.value = "abcd2345";
    code.dispatchEvent(new Event("input"));
    expect(code.value).toBe("ABCD-2345");
  });

  it("accepts a join link and checks the password locally", async () => {
    history.replaceState(null, "", "/join/ABCD-2345");
    const r = root();
    const fetch = mockFetch({
      "GET /health": health,
      "GET /join/ABCD-2345": { name: "Ada", invitedBy: "Helga", expiresAt: ts },
    });
    await boot(r, { fetch });
    expect(r.querySelector("h1")!.textContent).toBe("Helga invited you to Rabbit Ears");
    const [name, pw, again] = r.querySelectorAll<HTMLInputElement>("input");
    expect(name.value).toBe("Ada");
    again.value = "x";
    pw.value = "short";
    r.querySelector("form")!.requestSubmit();
    expect(r.textContent).toContain("Use at least 12 characters.");
    pw.value = again.value = "correct horse battery";
    pw.value = "correct horse batterz";
    r.querySelector("form")!.requestSubmit();
    expect(r.textContent).toContain("The passwords do not match.");
    expect(calls.some((c) => c.key === "POST /join/ABCD-2345")).toBe(false);
  });

  it("says when the invite is no longer valid", async () => {
    history.replaceState(null, "", "/join/NOPE-NOPE");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /join/NOPE-NOPE": json({ code: "not_found", message: "x" }, 404),
      }),
    });
    expect(r.querySelector("h1")!.textContent).toBe("This invite is no longer valid.");
  });

  it("does not call a valid invite dead when the source is rate limited", async () => {
    history.replaceState(null, "", "/join/ABCD-2345");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /join/ABCD-2345": json({ code: "rate_limited", message: "x" }, 429),
      }),
    });
    expect(r.querySelector("h1")!.textContent).toBe("Too many attempts. Try again in 1 minute.");
    expect(r.textContent).not.toContain("new one");
  });

  describe("which household", () => {
    const households = [
      { id: uid(1), name: "Helga's household" },
      { id: uid(5), name: "Mark's household" },
    ];
    const moved = () => new Response(null, { status: 204 });
    const ask = async (r: HTMLElement) => {
      await until(
        () => r.querySelector("h1")?.textContent === "Which household is this browser in?",
      );
      return [...r.querySelectorAll<HTMLInputElement>("input[type=radio]")];
    };

    it("asks after a sign-in into several, the own one first, and moves this browser to the one picked", async () => {
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          "POST /auth/sign-in": { token: "tok", me: me(1), households },
          "PUT /me/household": moved,
        }),
      });
      r.querySelector<HTMLInputElement>("input[type=email]")!.value = "h@example.com";
      r.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
      r.querySelector("form")!.requestSubmit();
      const [own, other] = await ask(r);
      expect(texts(r, "label b")).toEqual(["Helga's householdYours", "Mark's householdVisiting"]);
      expect(own.checked).toBe(true);
      expect(r.textContent).toContain("Asked once, after signing in. Sign out to change it.");
      expect(location.hash).not.toBe("#/who");
      other.click();
      r.querySelector("form")!.requestSubmit();
      await until(() => location.hash === "#/who");
      expect(calls.find((c) => c.key === "PUT /me/household")!.body).toEqual({
        householdId: uid(5),
      });
    });

    it("moves nothing when the own household is kept, after a single sign-on", async () => {
      history.replaceState(null, "", "/#sso=one-time");
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          "POST /auth/sso/exchange": { token: "tok", me: me(1), households },
        }),
      });
      await ask(r);
      r.querySelector("form")!.requestSubmit();
      await until(() => location.hash === "#/who");
      expect(calls.some((c) => c.key === "PUT /me/household")).toBe(false);
    });

    it("asks after a join too, and shows a refused move", async () => {
      history.replaceState(null, "", "/join/ABCD-2345");
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /join/ABCD-2345": { name: "Ada", invitedBy: "Helga", expiresAt: ts },
          "POST /join/ABCD-2345": { token: "tok", me: me(1), households },
          "PUT /me/household": () => json({ code: "not_found", message: "no such household" }, 404),
        }),
      });
      const [pw, again] = [...r.querySelectorAll<HTMLInputElement>("input")].slice(1);
      pw.value = again.value = "correct horse battery";
      r.querySelector("form")!.requestSubmit();
      const [, other] = await ask(r);
      other.click();
      r.querySelector("form")!.requestSubmit();
      await until(() => r.querySelector(".err:not([hidden])"));
      expect(r.querySelector(".err")!.textContent).toBe("no such household");
    });

    it("goes straight on after a sign-in into one household", async () => {
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          "POST /auth/sign-in": { token: "tok", me: me(1), households: [households[0]] },
        }),
      });
      r.querySelector<HTMLInputElement>("input[type=email]")!.value = "h@example.com";
      r.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
      r.querySelector("form")!.requestSubmit();
      await until(() => location.hash === "#/who");
    });
  });
});

describe("server name", () => {
  const named = { ...health, serverName: "Den" };

  it("names the place by the server's name on sign in and in the page title", async () => {
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": named }) });
    expect(r.querySelector("h1")!.textContent).toBe("Sign in to Den");
    expect(r.querySelector(".brand")!.textContent).toBe("Den");
    expect(document.title).toBe("Den");
  });

  it("shows Rabbit Ears when the server has no name to give", async () => {
    await boot(root(), { fetch: mockFetch({ "GET /health": named }) });
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": health }) });
    expect(r.querySelector("h1")!.textContent).toBe("Sign in to Rabbit Ears");
    expect(r.querySelector(".brand")!.textContent).toBe("Rabbit Ears");
    expect(document.title).toBe("Rabbit Ears");
  });

  it("keeps a long name whole in the brand's title", async () => {
    const long = "The Very Long Name Of A Basement Media Server ".repeat(2).trim();
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": { ...health, serverName: long } }) });
    expect(r.querySelector(".brand")!.getAttribute("title")).toBe(long);
  });

  it("names Who's watching and the main shell by the server's name", async () => {
    setToken("tok");
    await at("#/who");
    const r = root();
    const fetch = mockFetch({
      "GET /health": named,
      "GET /me": me(1),
      "POST /me/lock": () => new Response(null, { status: 204 }),
      "GET /items": page([]),
      "GET /genres": page([]),
      [`GET /profiles/${uid(1)}/watch-state`]: page([]),
      [`GET /profiles/${uid(1)}/show-progress`]: page([]),
    });
    await boot(r, { fetch });
    await until(() => r.querySelector(".profiles"));
    expect(r.querySelector(".brand")!.textContent).toBe("Den");
    await at("#/tv");
    await boot(r, { fetch });
    await until(() => r.querySelector("h1"));
    expect(r.querySelector("a.brand")!.textContent).toBe("Den");
  });
});

describe("single sign-on", () => {
  const providers = [
    { id: uid(3), name: "Keyhole" },
    { id: uid(4), name: "Front door" },
  ];

  it("offers one Sign in with button per provider under the password form", async () => {
    const r = root();
    await boot(r, {
      fetch: mockFetch({ "GET /health": health, "GET /auth/sso-providers": providers }),
    });
    await until(() => r.querySelectorAll("a[href*='/auth/sso/']").length === 2);
    const [a, b] = r.querySelectorAll<HTMLAnchorElement>("a[href*='/auth/sso/']");
    expect(a.textContent).toBe("Sign in with Keyhole");
    expect(a.getAttribute("href")).toBe(`/api/v2/auth/sso/${uid(3)}/start?returnTo=viewer`);
    expect(b.getAttribute("href")).toBe(`/api/v2/auth/sso/${uid(4)}/start?returnTo=viewer`);
    const submit = r.querySelector("button[type=submit]")!;
    expect(submit.compareDocumentPosition(a) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("shows no single sign-on when there are no providers", async () => {
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": health, "GET /auth/sso-providers": [] }) });
    await new Promise((x) => setTimeout(x, 0));
    expect(r.querySelector("a[href*='/auth/sso/']")).toBeNull();
    expect(r.querySelector("form")).toBeTruthy();
  });

  it("exchanges an #sso= code and carries on as a password sign-in does", async () => {
    history.replaceState(null, "", "/#sso=one-time");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "POST /auth/sso/exchange": { token: "tok", me: me(1) },
      }),
    });
    await until(() => localStorage.getItem("rabbit-ears.admin.token") === "tok");
    const body = calls.find((c) => c.key === "POST /auth/sso/exchange")!.body as Record<
      string,
      string
    >;
    expect(body.code).toBe("one-time");
    expect(body.platform).toBe("web");
    expect(body.deviceName).toMatch(/browser$/);
    await until(() => location.hash === "#/who");
    expect(calls.filter((c) => c.key === "POST /auth/sso/exchange")).toHaveLength(1);
  });

  it("explains an #sso_error= on the sign-in page and clears the fragment", async () => {
    history.replaceState(null, "", "/#sso_error=no_account");
    const r = root();
    await boot(r, { fetch: mockFetch({ "GET /health": health }) });
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe(
      "No account here is linked to that sign-in. Ask an admin to add you, or use your password.",
    );
    expect(location.hash).toBe("");
    expect(calls.find((c) => c.key === "POST /auth/sso/exchange")).toBeUndefined();
  });

  it("says when a code no longer works", async () => {
    history.replaceState(null, "", "/#sso=spent");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "POST /auth/sso/exchange": () => json({ code: "unauthorized", message: "no" }, 401),
      }),
    });
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe(
      "That sign-in expired or was started in another browser. Try again.",
    );
    expect(localStorage.getItem("rabbit-ears.admin.token")).toBeNull();
    expect(location.hash).toBe("");
  });
});

describe("who's watching", () => {
  beforeEach(() => setToken("tok"));

  it("lists the household with a PIN badge, then unlocks a PIN profile with four digits", async () => {
    await at("#/who");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`POST /profiles/${uid(1)}/unlock`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector(".profiles"));
    expect([...r.querySelectorAll(".who")].map((a) => a.lastChild!.textContent)).toEqual([
      "Helga",
      "Jason",
      "Add kid",
      "Add adult",
    ]);
    expect(r.querySelectorAll("[aria-label='PIN required']")).toHaveLength(1);
    r.querySelector<HTMLElement>(".who")!.click();
    const dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.getAttribute("aria-labelledby")).toBe("pt");
    const pin = dlg.querySelector<HTMLInputElement>("input")!;
    pin.value = "12a3";
    pin.dispatchEvent(new Event("input"));
    expect(dlg.querySelector("[role=img]")!.getAttribute("aria-label")).toBe(
      "PIN, 3 of 4 digits entered",
    );
    pin.value = "1234";
    pin.dispatchEvent(new Event("input"));
    await until(() => calls.filter((c) => c.key === "GET /me").length === 2);
    expect(calls.find((c) => c.key === `POST /profiles/${uid(1)}/unlock`)!.body).toEqual({
      pin: "1234",
    });
  });

  it("shows the lockout message and clears the dots on a wrong PIN", async () => {
    await at("#/who");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`POST /profiles/${uid(1)}/unlock`]: json({ code: "pin_locked", message: "locked" }, 403),
      }),
    });
    await until(() => r.querySelector(".profiles"));
    r.querySelector<HTMLElement>(".who")!.click();
    const pin = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
    pin.value = "0000";
    pin.dispatchEvent(new Event("input"));
    await until(() =>
      document.querySelector("[role=alert]")?.textContent?.startsWith("Too many tries"),
    );
    expect(pin.value).toBe("");
  });

  it("changes the adult's password and says when the current one is wrong", async () => {
    await at("#/password");
    const r = root();
    let wrong = true;
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "PUT /me/password": () =>
          wrong
            ? json({ code: "forbidden", message: "the current password is wrong" }, 403)
            : new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector("form"));
    const [current, next, again] = r.querySelectorAll<HTMLInputElement>("input");
    current.value = "old password!";
    next.value = again.value = "a much better one";
    r.querySelector("form")!.requestSubmit();
    await until(() => r.textContent!.includes("The current password is wrong."));
    wrong = false;
    r.querySelector("form")!.requestSubmit();
    await until(() => r.textContent!.includes("Password changed."));
    expect(calls.filter((c) => c.key === "PUT /me/password").at(-1)!.body).toEqual({
      currentPassword: "old password!",
      newPassword: "a much better one",
    });
  });

  it("hides invited adults and offers Add adult to the owner only", async () => {
    const others = [acct(3, "Bea"), acct(4, "Cy", "adult", { pending: true })];
    await at("#/who");
    const r = root();
    await boot(r, {
      fetch: mockFetch({ "GET /health": health, "GET /me": me(3, true, others) }),
    });
    await until(() => r.querySelector(".profiles"));
    expect([...r.querySelectorAll(".who")].map((a) => a.lastChild!.textContent)).toEqual([
      "Helga",
      "Jason",
      "Bea",
      "Add kid",
    ]);
    expect(r.textContent).toContain("Helga's household");
  });

  it("adds an adult to the household and shows the code to hand them", async () => {
    await at("#/add-adult");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "POST /household/adults": () =>
          json(
            {
              account: acct(3, "Bea", "adult", { pending: true }),
              joinCode: {
                id: uid(1),
                code: "7KQ4-MNP2",
                link: "/join/7KQ4-MNP2",
                accountId: uid(3),
                expiresAt: ts,
                createdAt: ts,
              },
            },
            201,
          ),
      }),
    });
    await until(() => r.querySelector("form"));
    r.querySelector<HTMLInputElement>("#an")!.value = "Bea";
    r.querySelector<HTMLInputElement>("#ae")!.value = "bea@example.test";
    r.querySelector("form")!.requestSubmit();
    await until(() => r.textContent!.includes("7KQ4-MNP2"));
    expect(calls.find((c) => c.key === "POST /household/adults")!.body).toEqual({
      name: "Bea",
      email: "bea@example.test",
      expiresInDays: 7,
    });
    expect(r.querySelector<HTMLInputElement>("[aria-label='Join link']")!.value).toMatch(
      /\/join\/7KQ4-MNP2$/,
    );
  });

  it("suggests ceilings from the birthday on Add a kid", async () => {
    await at("#/add-kid");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "GET /ratings/suggestion": { age: 9, movieCeiling: "PG", tvCeiling: "TV-PG" },
      }),
    });
    await until(() => r.querySelector("#kb"));
    r.querySelector<HTMLInputElement>("#kn")!.value = "Joan";
    const b = r.querySelector<HTMLInputElement>("#kb")!;
    b.value = "2017-08-19";
    b.dispatchEvent(new Event("change"));
    await until(() => r.textContent!.includes("Suggested: movies up to PG, TV up to TV-PG."));
    expect(r.querySelector<HTMLSelectElement>("#tc")!.value).toBe("TV-PG");
  });

  it("asks for the PIN before adding a kid who needs one, and adds the kid once when the PIN fails", async () => {
    await at("#/add-kid");
    const r = root();
    let pins = 0;
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "POST /profiles": acct(9, "Joan", "kid"),
        [`PUT /profiles/${uid(9)}`]: acct(9, "Joan", "kid"),
        [`PUT /profiles/${uid(9)}/pin`]: () =>
          pins++
            ? new Response(null, { status: 204 })
            : json({ code: "internal", message: "try again" }, 500),
      }),
    });
    await until(() => r.querySelector("#kb"));
    r.querySelector<HTMLInputElement>("#kn")!.value = "Joan";
    r.querySelector<HTMLInputElement>("#kb")!.value = "2017-08-19";
    const need = r.querySelector<HTMLInputElement>("[aria-label='Require a PIN']")!;
    need.checked = true;
    need.dispatchEvent(new Event("change"));
    const form = r.querySelector("form")!;
    form.requestSubmit();
    await new Promise((x) => setTimeout(x, 20));
    expect(calls.some((c) => c.key === "POST /profiles")).toBe(false);
    r.querySelector<HTMLInputElement>("[aria-label=PIN]")!.value = "4321";
    form.requestSubmit();
    await until(() => r.querySelector(".err:not([hidden])")?.textContent === "try again");
    form.requestSubmit();
    await until(() => pins === 2);
    expect(calls.filter((c) => /^(POST|PUT) \/profiles/.test(c.key)).map((c) => c.key)).toEqual([
      "POST /profiles",
      `PUT /profiles/${uid(9)}/pin`,
      `PUT /profiles/${uid(9)}`,
      `PUT /profiles/${uid(9)}/pin`,
    ]);
  });

  it("forgets a half-typed PIN when Require a PIN is unticked, so the form can submit", async () => {
    await at("#/add-kid");
    const r = root();
    await boot(r, {
      fetch: mockFetch({ "GET /health": health, "GET /me": me(1) }),
    });
    await until(() => r.querySelector("#kb"));
    r.querySelector<HTMLInputElement>("#kn")!.value = "Joan";
    r.querySelector<HTMLInputElement>("#kb")!.value = "2017-08-19";
    const need = r.querySelector<HTMLInputElement>("[aria-label='Require a PIN']")!;
    const tick = (on: boolean) => {
      need.checked = on;
      need.dispatchEvent(new Event("change"));
    };
    tick(true);
    r.querySelector<HTMLInputElement>("[aria-label=PIN]")!.value = "12";
    tick(false);
    expect(r.querySelector("form")!.checkValidity()).toBe(true);
  });
});

describe("device lock", () => {
  beforeEach(() => setToken("tok"));
  const unlocked = () => new Response(null, { status: 204 });

  it("locks the device as Who's watching shows, typed or not, and links nowhere it cannot load", async () => {
    await at("#/who");
    const r = root();
    await boot(r, {
      fetch: mockFetch({ "GET /health": health, "GET /me": me(1), "POST /me/lock": unlocked }),
    });
    await until(() => r.querySelector(".profiles"));
    expect(calls.map((c) => c.key)).toContain("POST /me/lock");
    expect(r.querySelector("a[href='#/']")).toBeNull();
  });

  it("still shows Who's watching when the lock cannot be sent", async () => {
    await at("#/who");
    const r = root();
    const base = mockFetch({ "GET /health": health, "GET /me": me(1) });
    await boot(r, {
      fetch: (req) =>
        req.url.endsWith("/me/lock") ? Promise.reject(new TypeError("offline")) : base(req),
    });
    await until(() => r.querySelector(".profiles"));
    expect(r.querySelector("[role=alert]")).toBeNull();
  });

  it("boots a locked device to Who's watching whatever the hash, and returns there after a pick", async () => {
    await at("#/household");
    const r = root();
    let locked = true;
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": () => ({ ...me(1), locked }),
        "POST /me/lock": unlocked,
        [`POST /profiles/${uid(2)}/unlock`]: () => ((locked = false), unlocked()),
      }),
    });
    await until(() => r.querySelector(".profiles"));
    expect(calls.map((c) => c.key)).toEqual([
      "GET /health",
      "GET /me",
      "GET /libraries",
      "POST /me/lock",
    ]);
    [...r.querySelectorAll<HTMLElement>(".who")]
      .find((a) => a.lastChild!.textContent === "Jason")!
      .click();
    await until(() => location.hash === "#/household");
  });

  it("sends a page the server refuses as locked to Who's watching without an error", async () => {
    await at("#/movies");
    const r = root();
    const base = mockFetch({ "GET /health": health, "GET /me": me(1), "POST /me/lock": unlocked });
    await boot(r, {
      fetch: (req) =>
        /\/(health|me|me\/lock)$/.test(req.url)
          ? base(req)
          : Promise.resolve(json({ code: "device_locked", message: "device locked" }, 403)),
    });
    await until(() => r.querySelector(".profiles"));
    expect(r.querySelector("[role=alert]")).toBeNull();
    expect(location.hash).toBe(`#/who?then=${encodeURIComponent("#/movies")}`);
  });

  describe("other tabs", () => {
    const shell = (m: () => unknown) =>
      mockFetch({
        "GET /health": health,
        "GET /me": m,
        "POST /me/lock": unlocked,
        [`POST /profiles/${uid(2)}/unlock`]: unlocked,
        "GET /items": page([]),
        "GET /genres": page([]),
        [`GET /profiles/${uid(1)}/watch-state`]: page([]),
        [`GET /profiles/${uid(2)}/watch-state`]: page([]),
      });
    const admin = (active: number) => ({
      ...me(active),
      account: acct(1, "Helga", "adult", { isAdmin: true }),
      profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")],
    });
    const tell = (what: string) => {
      const tab = new BroadcastChannel("rabbit-ears.profile");
      tab.postMessage(what);
      tab.close();
    };

    it("follows a switch to a kid made in another tab, Admin and all", async () => {
      await at("#/");
      let active = 1;
      const r = root();
      await boot(r, { fetch: shell(() => admin(active)) });
      await until(() => r.querySelector("header .profile")?.textContent === "HHelga");
      active = 2;
      tell("switched");
      await until(() => r.querySelector("header .profile")?.textContent === "JJason");
      r.querySelector<HTMLElement>("header .profile")!.click();
      expect(texts(r, "header [role=menu] [role=menuitem]")).toEqual([
        "Switch profile",
        "Sign out",
      ]);
    });

    it("follows a lock made in another tab to Who's watching", async () => {
      await at("#/movies");
      let locked = false;
      const r = root();
      await boot(r, { fetch: shell(() => ({ ...admin(1), locked })) });
      await until(() => r.querySelector("header .profile"));
      locked = true;
      tell("locked");
      await until(() => r.querySelector(".profiles"));
      expect(location.hash).toBe(`#/who?then=${encodeURIComponent("#/movies")}`);
    });

    it("tells the other tabs of its lock and its pick, and leaves Who's watching for a pick made elsewhere", async () => {
      await at(`#/who?then=${encodeURIComponent("#/movies")}`);
      const heard: unknown[] = [];
      const tab = new BroadcastChannel("rabbit-ears.profile");
      tab.onmessage = (e) => heard.push(e.data);
      const r = root();
      try {
        await boot(r, { fetch: shell(() => admin(1)) });
        await until(() => r.querySelector(".profiles"));
        await until(() => heard.length === 1);
        [...r.querySelectorAll<HTMLElement>(".who")]
          .find((a) => a.lastChild!.textContent === "Jason")!
          .click();
        await until(() => heard.length === 2);
        await until(() => location.hash === "#/movies");
        await at(`#/who?then=${encodeURIComponent("#/tv")}`);
        await until(() => r.querySelector(".profiles"));
        tell("switched");
        await until(() => location.hash === "#/tv");
      } finally {
        tab.close();
      }
    });
  });

  describe("remember profile", () => {
    const remember = (account: number, v: object) =>
      localStorage.setItem(`rabbit-ears.remember.${uid(account)}`, JSON.stringify(v));
    const shell = (m: unknown, extra = {}) =>
      mockFetch({
        "GET /health": health,
        "GET /me": m,
        "POST /me/lock": unlocked,
        "GET /items": page([]),
        "GET /genres": page([]),
        [`GET /profiles/${uid(1)}/watch-state`]: page([]),
        [`GET /profiles/${uid(2)}/watch-state`]: page([]),
        ...extra,
      });

    it("starts as the last profile with no lock, both settings on by default", async () => {
      await at("#/movies");
      const r = root();
      await boot(r, { fetch: shell(me(1)) });
      await until(() => r.querySelector("header .profile"));
      expect(location.hash).toBe("#/movies");
      expect(calls.map((c) => c.key)).not.toContain("POST /me/lock");
    });

    it("starts at Who's watching when off, and a pick is not a start", async () => {
      remember(1, { profile: false, skipPin: true });
      await at("#/movies");
      const r = root();
      await boot(r, { fetch: shell(me(1), { [`POST /profiles/${uid(2)}/unlock`]: unlocked }) });
      await until(() => r.querySelector(".profiles"));
      expect(calls.filter((c) => c.key === "POST /me/lock")).toHaveLength(1);
      [...r.querySelectorAll<HTMLElement>(".who")]
        .find((a) => a.lastChild!.textContent === "Jason")!
        .click();
      await until(() => location.hash === "#/movies" && r.querySelector("header .profile"));
      expect(calls.filter((c) => c.key === "POST /me/lock")).toHaveLength(1);
    });

    it("asks the last profile's PIN at start when Skip PIN is off", async () => {
      remember(1, { profile: true, skipPin: false });
      await at("#/movies");
      const r = root();
      await boot(r, { fetch: shell(me(1)) });
      await until(() => document.querySelector(".pinbox"));
      expect(document.querySelector(".pinbox h2")!.textContent).toBe("Enter Helga's PIN");
      expect(calls.map((c) => c.key)).toContain("POST /me/lock");
      expect(r.querySelector(".profiles")).not.toBeNull();
    });

    it("unlocks the last profile of a locked device when it has no PIN", async () => {
      await at("#/movies");
      const r = root();
      let locked = true;
      await boot(r, {
        fetch: shell(() => ({ ...me(2), locked }), {
          [`POST /profiles/${uid(2)}/unlock`]: () => ((locked = false), unlocked()),
        }),
      });
      await until(() => location.hash === "#/movies" && r.querySelector("header .profile"));
      expect(calls.map((c) => c.key)).toContain(`POST /profiles/${uid(2)}/unlock`);
      expect(document.querySelector(".pinbox")).toBeNull();
    });

    it("shows Who's watching when the last profile is gone", async () => {
      await at("#/movies");
      const r = root();
      await boot(r, { fetch: shell(me(5)) });
      await until(() => r.querySelector(".profiles"));
      expect(document.querySelector(".pinbox")).toBeNull();
    });

    it("keeps each signed-in adult's settings apart", async () => {
      remember(7, { profile: false, skipPin: false });
      await at("#/movies");
      const r = root();
      await boot(r, { fetch: shell(me(1)) });
      await until(() => r.querySelector("header .profile"));
      expect(calls.map((c) => c.key)).not.toContain("POST /me/lock");
    });

    it("falls back to both on when storage is blocked", async () => {
      remember(1, { profile: false, skipPin: false });
      const real = Storage.prototype.getItem;
      const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(function (
        this: Storage,
        k: string,
      ) {
        if (k.startsWith("rabbit-ears.remember")) throw new Error("blocked");
        return real.call(this, k);
      });
      try {
        await at("#/movies");
        const r = root();
        await boot(r, { fetch: shell(me(1)) });
        await until(() => r.querySelector("header .profile"));
        expect(calls.map((c) => c.key)).not.toContain("POST /me/lock");
      } finally {
        get.mockRestore();
      }
    });

    it("sets both on Preferences for this browser, Skip PIN only while Remember profile is on", async () => {
      await at("#/preferences");
      const r = root();
      await boot(r, { fetch: shell(me(1)) });
      const sw = (name: string) =>
        r.querySelector<HTMLInputElement>(`[role=switch][aria-label="${name}"]`)!;
      await until(() => sw("Remember profile"));
      expect(sw("Remember profile").checked).toBe(true);
      expect(sw("Skip PIN").checked).toBe(true);
      sw("Skip PIN").click();
      expect(JSON.parse(localStorage.getItem(`rabbit-ears.remember.${uid(1)}`)!)).toEqual({
        profile: true,
        skipPin: false,
      });
      sw("Remember profile").click();
      expect(sw("Skip PIN").closest<HTMLElement>(".row")!.hidden).toBe(true);
      expect(JSON.parse(localStorage.getItem(`rabbit-ears.remember.${uid(1)}`)!)).toEqual({
        profile: false,
        skipPin: false,
      });
      expect(calls.map((c) => c.key)).not.toContain(`PUT /profiles/${uid(1)}/preferences`);
    });
  });

  it("unlocks an adult without a PIN before Add kid goes there", async () => {
    await at("#/who");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": { ...me(1), profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")] },
        [`POST /profiles/${uid(1)}/unlock`]: unlocked,
      }),
    });
    await until(() => r.querySelector(".profiles"));
    r.querySelector<HTMLElement>("a[href='#/add-kid']")!.click();
    expect(location.hash).toBe("#/who");
    await until(() => location.hash === "#/add-kid");
    expect(calls.find((c) => c.key === `POST /profiles/${uid(1)}/unlock`)!.body).toEqual({});
  });
});

describe("grids", () => {
  beforeEach(() => setToken("tok"));
  const shows = [
    item(10, "show", { metadata: { status: "matched", title: "Severance", runtimeMinutes: 60 } }),
  ];
  const seasons = [item(11, "season", { parentId: uid(10), seasonNumber: 1 })];
  const episodes = [21, 22, 23].map((id, i) =>
    item(id, "episode", {
      parentId: uid(11),
      seasonNumber: 1,
      episodeNumber: i + 1,
      metadata: { status: "matched", runtimeMinutes: i < 2 ? 50 : 44 },
    }),
  );
  const progress = (unwatched: number) =>
    page([{ showId: uid(10), seasons: 1, episodes: 3, unwatched, inProgress: false }]);
  const tv = (active: number) => ({
    "GET /health": health,
    "GET /me": me(active),
    [`GET /profiles/${uid(1)}/watch-state`]: page([
      { itemId: uid(21), positionMs: 0, watched: true, playCount: 1, changedAt: ts },
    ]),
    [`GET /profiles/${uid(2)}/watch-state`]: page([]),
    [`GET /profiles/${uid(1)}/show-progress`]: progress(2),
    [`GET /profiles/${uid(2)}/show-progress`]: progress(3),
    "GET /genres": page([]),
    ...Object.fromEntries(
      [...shows, ...seasons, ...episodes].map((i) => [`GET /items/${i.id}`, i]),
    ),
    "GET /items": (_: unknown, u: URL) => {
      const k = u.searchParams.get("kind");
      return page(
        k === "show" ? shows : k === "season" ? seasons : k === "episode" ? episodes : [],
      );
    },
  });

  it("counts unwatched episodes in the badge and leads the caption with runtime", async () => {
    await at("#/tv");
    const r = root();
    await boot(r, { fetch: mockFetch(tv(1)) });
    await until(() => r.querySelector(".grid a"));
    expect(r.querySelector(".badge")!.textContent).toBe("2 new");
    expect(r.querySelector(".grid .sub")!.textContent).toBe("1h - 1 season");
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("1 show");
  });

  it("names season 0 Specials on an episode, as the season page does", async () => {
    await at(`#/episode/${uid(21)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        "GET /media-files": page([]),
        [`GET /items/${uid(11)}`]: item(11, "season", { parentId: uid(10), seasonNumber: 0 }),
      }),
    });
    await until(() => r.querySelector(".crumb"));
    expect(r.querySelector(".crumb")!.textContent).toBe("Severance · Specials");
    expect(r.querySelector("a.back")?.textContent ?? r.textContent).toContain("Specials");
    expect(r.textContent).not.toContain("Season 0");
  });

  it("dismisses an episode in progress, and offers it on no other", async () => {
    await at(`#/episode/${uid(21)}`);
    const r = root();
    const going = {
      itemId: uid(21),
      positionMs: 600000,
      watched: true,
      playCount: 1,
      changedAt: ts,
    };
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        "GET /media-files": page([]),
        [`GET /profiles/${uid(1)}/watch-state/${uid(21)}`]: going,
        [`DELETE /profiles/${uid(1)}/watch-state/${uid(21)}/position`]: () =>
          new Response(null, { status: 204 }),
      }),
    });
    await until(() => button(r, "Dismiss"));
    expect([...r.querySelectorAll(".actions .btn")].map((b) => b.textContent)).toContain(
      "Mark unwatched",
    );
    button(r, "Dismiss").click();
    await until(() =>
      calls.some((c) => c.key === `DELETE /profiles/${uid(1)}/watch-state/${uid(21)}/position`),
    );
    expect(calls.some((c) => c.key.startsWith(`PUT /profiles/${uid(1)}/watch-state`))).toBe(false);

    await at(`#/episode/${uid(22)}`);
    const r2 = root();
    await boot(r2, { fetch: mockFetch({ ...tv(1), "GET /media-files": page([]) }) });
    await until(() => button(r2, "Mark watched"));
    expect(button(r2, "Dismiss")).toBeUndefined();
  });

  it("shows nothing in place of a show's absent cast", async () => {
    await at(`#/show/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({ ...tv(1), [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] } }),
    });
    await until(() => r.querySelector("h1.big"));
    expect(r.querySelector("main")!.textContent).not.toContain("null");
  });

  it("Mark all watched on a show is one batch request with the show's id", async () => {
    await at(`#/show/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] },
        [`PUT /profiles/${uid(1)}/watch-state/batch`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector("h1.big"));
    button(r, "More").click();
    [...r.querySelectorAll<HTMLElement>("[role=menuitem]")]
      .find((e) => e.textContent === "Mark as watched")!
      .click();
    await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/watch-state/batch`));
    expect(calls.filter((c) => c.key.startsWith(`PUT /profiles/${uid(1)}/watch-state`))).toEqual([
      expect.objectContaining({ body: { itemIds: [uid(10)], watched: true } }),
    ]);
  });

  it("Mark all watched on a season is one batch request with the season's id", async () => {
    await at(`#/season/${uid(11)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        [`PUT /profiles/${uid(1)}/watch-state/batch`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => button(r, "Mark all watched"));
    button(r, "Mark all watched").click();
    await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/watch-state/batch`));
    expect(calls.filter((c) => c.key.startsWith(`PUT /profiles/${uid(1)}/watch-state`))).toEqual([
      expect.objectContaining({ body: { itemIds: [uid(11)], watched: true } }),
    ]);
  });

  it("reads only the show's or season's watch states on its page", async () => {
    const r = root();
    for (const [hash, id] of [
      [`#/show/${uid(10)}`, uid(10)],
      [`#/season/${uid(11)}`, uid(11)],
    ]) {
      await at(hash);
      calls.length = 0;
      await boot(r, {
        fetch: mockFetch({ ...tv(1), [`GET /items/${uid(10)}/credits`]: { cast: [], crew: [] } }),
      });
      await until(() => r.querySelector("h1.big"));
      const asked = calls.filter((c) => c.key === `GET /profiles/${uid(1)}/watch-state`);
      expect(asked.map((c) => new URLSearchParams(c.query).get("parentId"))).toEqual([id]);
    }
  });

  it("reads a show added since the catalog was loaded by its own id", async () => {
    let scanned = false;
    await at("#/tv");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        [`GET /items/${uid(40)}`]: () =>
          scanned
            ? item(40, "show", { metadata: { status: "matched", title: "Slow Horses" } })
            : json({ code: "not_found", message: "no item" }, 404),
        [`GET /items/${uid(40)}/credits`]: { cast: [], crew: [] },
        [`GET /items/${uid(41)}/credits`]: { cast: [], crew: [] },
      }),
    });
    await until(() => r.querySelector(".grid a"));
    scanned = true;
    calls.length = 0;
    await at(`#/show/${uid(40)}`);
    await until(() => r.querySelector("h1")?.textContent === "Slow Horses");
    await at(`#/show/${uid(41)}`);
    await until(() => r.querySelector(".err")?.textContent === "Show not found");
    expect(calls.filter((c) => c.query.includes("kind=show"))).toHaveLength(0);
  });

  it("hides the Kids chip on a kid profile and keeps it for an adult", async () => {
    await at("#/tv");
    const adult = root();
    await boot(adult, { fetch: mockFetch(tv(1)) });
    await until(() => adult.querySelector(".chips"));
    expect([...adult.querySelectorAll(".chip")].map((c) => c.textContent)).toEqual([
      "All",
      "Unwatched",
      "In progress",
      "Kids",
    ]);
    document.body.replaceChildren();
    const kid = root();
    await boot(kid, { fetch: mockFetch(tv(2)) });
    await until(() => kid.querySelector(".chips"));
    expect([...kid.querySelectorAll(".chip")].map((c) => c.textContent)).toEqual([
      "All",
      "Unwatched",
      "In progress",
    ]);
  });

  it("lists movies once across Home and Movies and never loads the TV tree for Movies", async () => {
    const kinds: string[] = [];
    await at("#/movies");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...tv(1),
        "GET /items": (_: unknown, u: URL) => {
          // Home's newest few are not the list.
          if (!u.searchParams.has("sort")) kinds.push(u.searchParams.get("kind")!);
          return page(u.searchParams.get("kind") === "movie" ? [item(30, "movie")] : []);
        },
      }),
    });
    await until(() => r.querySelector(".grid a"));
    expect(kinds).toEqual(["movie"]);
    await at("#/");
    await until(() => r.querySelector(".poster"));
    await at("#/movies");
    await until(() => r.querySelector(".grid a"));
    expect(kinds.filter((k) => k === "movie")).toHaveLength(1);
  });

  it("filters to unwatched and shows the empty message", async () => {
    await at("#/movies");
    const r = root();
    const base = tv(1);
    await boot(r, {
      fetch: mockFetch({
        ...base,
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("kind") === "movie"
              ? [item(30, "movie", { metadata: { status: "matched", runtimeMinutes: 166 } })]
              : [],
          ),
        [`GET /profiles/${uid(1)}/watch-state`]: page([
          { itemId: uid(30), positionMs: 0, watched: true, playCount: 1, changedAt: ts },
        ]),
      }),
    });
    await until(() => r.querySelector(".grid a"));
    expect(r.querySelector(".grid .sub")!.textContent).toBe("2h 46m");
    expect(r.querySelector("[aria-label=Unwatched]")).toBeNull();
    [...r.querySelectorAll<HTMLElement>(".chip")]
      .find((c) => c.textContent === "Unwatched")!
      .click();
    expect(r.querySelector(".grid")!.textContent).toBe("No movies match.");
  });

  // A server's answers for an adult who does or does not hide kids' content.
  const kidsLibrary = (hide: boolean) => {
    const rated = (id: number, title: string, rating: string, kidsContent: boolean) =>
      item(id, "movie", {
        parsedTitle: title,
        kidsContent,
        metadata: { status: "matched", contentRating: rating },
      });
    const movies = [
      rated(30, "Cars", "G", true),
      rated(31, "Marked", "", true),
      rated(32, "Family PG", "PG", false),
      rated(33, "Not for kids", "G", false),
    ] as (ReturnType<typeof item> & { kidsContent: boolean })[];
    return mockFetch({
      ...tv(1),
      "GET /me": {
        ...me(1),
        profiles: [
          acct(1, "Helga", "adult", { hasPin: true, hideKidsContent: hide }),
          acct(2, "Jason", "kid"),
        ],
      },
      "GET /items": (_: unknown, u: URL) => {
        if (u.searchParams.get("kind") !== "movie") return page([]);
        const only = u.searchParams.get("kidsContent") === "true";
        return page(movies.filter((m) => (only ? m.kidsContent : !hide || !m.kidsContent)));
      },
    });
  };
  const chipNamed = (r: HTMLElement, name: string) =>
    [...r.querySelectorAll<HTMLElement>(".chip")].find((c) => c.textContent === name)!;
  const shown = (r: HTMLElement) =>
    [...r.querySelectorAll(".grid b")].map((b) => b.textContent).sort();

  it("filters the Kids chip by the server's kidsContent, not by rating, when Hide is off", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: kidsLibrary(false) });
    await until(() => r.querySelector(".grid a"));
    chipNamed(r, "Kids").click();
    expect(shown(r)).toEqual(["Cars", "Marked"]);
    expect(calls.some((c) => c.query.includes("kidsContent"))).toBe(false);
  });

  it("shows kids' titles under the Kids chip when Hide is on, counted and kept out of the other views", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: kidsLibrary(true) });
    await until(() => r.querySelector(".grid a"));
    expect(shown(r)).toEqual(["Family PG", "Not for kids"]);
    chipNamed(r, "Kids").click();
    await until(() => shown(r).includes("Cars"));
    expect(shown(r)).toEqual(["Cars", "Marked"]);
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("2 movies");
    chipNamed(r, "All").click();
    await until(() => shown(r).includes("Family PG"));
    expect(shown(r)).toEqual(["Family PG", "Not for kids"]);
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("2 movies");
  });

  it("does not ask for kids' titles when Hide is on and the Kids chip is not picked", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, { fetch: kidsLibrary(true) });
    await until(() => r.querySelector(".grid a"));
    expect(calls.some((c) => c.query.includes("kidsContent"))).toBe(false);
  });

  it("opens on the Kids chip from the hash when Hide is on", async () => {
    await at("#/movies?filter=Kids");
    const r = root();
    await boot(r, { fetch: kidsLibrary(true) });
    await until(() => r.querySelector(".grid a"));
    expect(shown(r)).toEqual(["Cars", "Marked"]);
  });
});

describe("player", () => {
  beforeEach(() => {
    setToken("tok");
    // jsdom has no media capability APIs; the snapshot only needs answers.
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener() {},
      removeEventListener() {},
    }));
    HTMLMediaElement.prototype.play = async () => {};
    HTMLMediaElement.prototype.pause = () => {};
    Object.defineProperty(navigator, "mediaCapabilities", {
      configurable: true,
      value: { decodingInfo: async () => ({ supported: false }) },
    });
  });

  it("ends a session that finishes starting after the viewer has left", async () => {
    let started!: () => void;
    const gate = new Promise<void>((r) => (started = r));
    await at(`#/play/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(5)}`]: item(5, "movie"),
        "GET /media-files": page([]),
        "POST /playback/sessions": async () => {
          await gate;
          return json(
            {
              sessionId: uid(7),
              entry: {
                position: 0,
                verdict: "direct_play",
                videoAction: "copy",
                audioAction: "copy",
                subtitleAction: "none",
                bad: false,
              },
              streamUrl: "/api/v2/streams/k/0/file",
              streamKind: "file",
              positionMs: 0,
              negotiated: { verdict: "direct_play" },
              rungs: [],
              sidecars: [],
            },
            201,
          );
        },
        [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
        [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => calls.some((c) => c.key === "POST /playback/sessions"));
    await at("#/");
    started();
    await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
  });

  it("ends the session of a player left and returned to before it finished starting", async () => {
    let started!: () => void;
    const gate = new Promise<void>((r) => (started = r));
    let next = 7;
    await at(`#/play/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(5)}`]: item(5, "movie"),
        "GET /media-files": page([]),
        "POST /playback/sessions": async () => {
          const sessionId = uid(next++);
          await gate;
          return json(
            {
              sessionId,
              entry: {
                position: 0,
                verdict: "direct_play",
                videoAction: "copy",
                audioAction: "copy",
                subtitleAction: "none",
                bad: false,
              },
              streamUrl: "/api/v2/streams/k/0/file",
              streamKind: "file",
              positionMs: 0,
              negotiated: { verdict: "direct_play" },
              rungs: [],
              sidecars: [],
            },
            201,
          );
        },
        [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
        [`PUT /playback/sessions/${uid(8)}/position`]: () => new Response(null, { status: 204 }),
        [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
        [`DELETE /playback/sessions/${uid(8)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => calls.some((c) => c.key === "POST /playback/sessions"));
    await at("#/");
    await at(`#/play/${uid(5)}`);
    await until(() => calls.filter((c) => c.key === "POST /playback/sessions").length === 2);
    started();
    await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
    expect(calls.some((c) => c.key === `DELETE /playback/sessions/${uid(8)}`)).toBe(false);
  });

  // The movie's largest file is another cut; file 9 is the one in play.
  const subtitled = async (hash: string) => {
    const stream = {
      sessionId: uid(7),
      mediaFileId: uid(9),
      entry: {
        position: 0,
        verdict: "direct_play",
        videoAction: "copy",
        audioAction: "copy",
        subtitleAction: "none",
        bad: false,
      },
      streamUrl: "/api/v2/streams/k/0/file",
      streamKind: "file",
      positionMs: 0,
      negotiated: { verdict: "direct_play" },
      rungs: [],
      sidecars: [],
    };
    const sub = (id: number, title: string) => ({
      id: uid(id),
      kind: "subtitle",
      title,
      isForced: false,
    });
    await at(hash);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(5)}`]: item(5, "movie"),
        "GET /media-files": page([
          { id: uid(8), durationMs: 200_000, chapters: [], tracks: [sub(30, "Commentary")] },
          { id: uid(9), durationMs: 120_000, chapters: [], tracks: [sub(31, "English")] },
        ]),
        "POST /playback/sessions": () => json(stream, 201),
        [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
        [`PUT /playback/sessions/${uid(7)}/tracks`]: { action: "continue", stream },
        [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector(".menu.subs"));
    return r;
  };

  it("lists the subtitles and the length of the file in play", async () => {
    const r = await subtitled(`#/play/${uid(5)}`);
    expect([...r.querySelectorAll(".menu.subs .opt b")].map((o) => o.textContent)).toEqual([
      "Off",
      "Forced only",
      "English",
    ]);
    r.querySelector("video")!.dispatchEvent(new Event("timeupdate"));
    expect(r.querySelector(".seek")!.getAttribute("aria-valuemax")).toBe("120");
  });

  it("starts at the volume this device last chose and remembers a new one", async () => {
    localStorage.setItem("rabbit-ears.volume", "0.3");
    const r = await subtitled(`#/play/${uid(5)}`);
    const video = r.querySelector("video")!;
    expect(video.volume).toBe(0.3);
    expect(r.querySelector<HTMLInputElement>("input[aria-label=Volume]")!.value).toBe("0.3");
    const vol = r.querySelector<HTMLInputElement>("input[aria-label=Volume]")!;
    vol.value = "0.65";
    vol.dispatchEvent(new Event("input"));
    await until(() => localStorage.getItem("rabbit-ears.volume") === "0.65");
  });

  it("keeps the audio track chosen at the start when a subtitle is picked", async () => {
    const r = await subtitled(`#/play/${uid(5)}?audio=${uid(12)}`);
    [...r.querySelectorAll<HTMLElement>(".menu.subs .opt")]
      .find((o) => o.textContent === "English")!
      .click();
    await until(() => calls.some((c) => c.key === `PUT /playback/sessions/${uid(7)}/tracks`));
    expect(calls.find((c) => c.key === `PUT /playback/sessions/${uid(7)}/tracks`)!.body).toEqual({
      audioTrackId: uid(12),
      subtitleTrackId: uid(31),
    });
  });

  it("moves to the next subtitles with C and closes with Esc", async () => {
    await at(`#/movie/${uid(5)}`);
    const r = await subtitled(`#/play/${uid(5)}`);
    const key = (k: string, init = {}) =>
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...init }));
    const checked = () => r.querySelector(".menu.subs .opt[aria-checked=true] b")?.textContent;
    expect(checked()).toBe("Forced only");
    key("c");
    await until(() => checked() === "English");
    expect(
      calls.find((c) => c.key === `PUT /playback/sessions/${uid(7)}/tracks`)!.body,
    ).toMatchObject({
      subtitleTrackId: uid(31),
    });
    // Cmd-F is the app's search, not full screen.
    key("f", { metaKey: true });
    key("Escape");
    await until(() => location.hash === `#/movie/${uid(5)}`);
  });

  it("cancels the Up next countdown when the viewer leaves", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await at(`#/play/${uid(21)}`);
      const r = root();
      const ep = (id: number, n: number) =>
        item(id, "episode", { parentId: uid(11), seasonNumber: 1, episodeNumber: n });
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          [`GET /items/${uid(21)}`]: ep(21, 1),
          [`GET /items/${uid(11)}`]: item(11, "season", { parentId: uid(10), seasonNumber: 1 }),
          [`GET /items/${uid(10)}`]: item(10, "show"),
          "GET /media-files": page([]),
          [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: {
            itemId: uid(22),
            kind: "episode",
            title: "T22",
          },
          "POST /playback/sessions": () =>
            json(
              {
                sessionId: uid(7),
                entry: {
                  position: 0,
                  verdict: "direct_play",
                  videoAction: "copy",
                  audioAction: "copy",
                  subtitleAction: "none",
                  bad: false,
                },
                streamUrl: "/api/v2/streams/k/0/file",
                streamKind: "file",
                positionMs: 0,
                negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
                rungs: [],
                sidecars: [],
              },
              201,
            ),
          [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
          [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
        }),
      });
      await until(() => r.querySelector(".player video"));
      r.querySelector("video")!.dispatchEvent(new Event("ended"));
      expect(r.querySelector(".upnext")!.textContent).toContain("Up next: T22 in 10s");
      await at("#/");
      vi.advanceTimersByTime(11_000);
      expect(location.hash).toBe("#/");
    } finally {
      vi.useRealTimers();
    }
  });

  it("starts fading the controls after 2 s idle, or at 3 s with reduced motion", async () => {
    await at(`#/play/${uid(5)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(5)}`]: item(5, "movie"),
        "GET /media-files": page([]),
        "POST /playback/sessions": () =>
          json(
            {
              sessionId: uid(7),
              entry: {
                position: 0,
                verdict: "direct_play",
                videoAction: "copy",
                audioAction: "copy",
                subtitleAction: "none",
                bad: false,
              },
              streamUrl: "/api/v2/streams/k/0/file",
              streamKind: "file",
              positionMs: 0,
              negotiated: { verdict: "direct_play" },
              rungs: [],
              sidecars: [],
            },
            201,
          ),
        [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
        [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector(".player video"));
    const player = r.querySelector(".player")!;
    const video = r.querySelector("video")!;
    let paused = false;
    Object.defineProperty(video, "paused", { configurable: true, get: () => paused });
    const move = () => player.dispatchEvent(new Event("pointermove"));
    const idle = () => player.classList.contains("idle");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      move();
      vi.advanceTimersByTime(1999);
      expect(idle()).toBe(false);
      vi.advanceTimersByTime(1);
      expect(idle()).toBe(true);
      move();
      expect(idle()).toBe(false);
      vi.advanceTimersByTime(1500);
      move();
      vi.advanceTimersByTime(1999);
      expect(idle()).toBe(false);
      paused = true;
      move();
      vi.advanceTimersByTime(5000);
      expect(idle()).toBe(false);
      paused = false;
      r.querySelector<HTMLButtonElement>("[aria-label=Subtitles]")!.click();
      move();
      vi.advanceTimersByTime(5000);
      expect(idle()).toBe(false);
      r.querySelector<HTMLButtonElement>("[aria-label=Subtitles]")!.click();
      vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduced-motion") }));
      move();
      vi.advanceTimersByTime(2999);
      expect(idle()).toBe(false);
      vi.advanceTimersByTime(1);
      expect(idle()).toBe(true);
      // A pause from anywhere but the pointer, as from the other viewer.
      paused = true;
      video.dispatchEvent(new Event("pause"));
      expect(idle()).toBe(false);
    } finally {
      vi.useRealTimers();
    }
    await at("#/");
  });

  it("names an episode by its own show and asks the server what follows", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await at(`#/play/${uid(22)}`);
      const r = root();
      const ep = (id: number, season: number, s: number, n: number) =>
        item(id, "episode", { parentId: uid(season), seasonNumber: s, episodeNumber: n });
      const season = (id: number, n: number) =>
        item(id, "season", { parentId: uid(10), seasonNumber: n });
      const children: Record<string, unknown[]> = {
        [uid(10)]: [season(13, 0), season(11, 1), season(12, 2)],
        [uid(11)]: [ep(21, 11, 1, 1), ep(22, 11, 1, 2)],
        [uid(12)]: [ep(31, 12, 2, 1), ep(32, 12, 2, 2)],
      };
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          [`GET /items/${uid(22)}`]: ep(22, 11, 1, 2),
          [`GET /items/${uid(11)}`]: season(11, 1),
          [`GET /items/${uid(10)}`]: item(10, "show", {
            metadata: { status: "matched", title: "Severance" },
          }),
          "GET /media-files": page([]),
          "GET /items": (_: unknown, u: URL) => page(children[u.searchParams.get("parentId")!]),
          [`GET /profiles/${uid(1)}/up-next/${uid(22)}`]: {
            itemId: uid(31),
            kind: "episode",
            title: "T31",
          },
          "POST /playback/sessions": () =>
            json(
              {
                sessionId: uid(7),
                entry: {
                  position: 0,
                  verdict: "direct_play",
                  videoAction: "copy",
                  audioAction: "copy",
                  subtitleAction: "none",
                  bad: false,
                },
                streamUrl: "/api/v2/streams/k/0/file",
                streamKind: "file",
                positionMs: 0,
                negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
                rungs: [],
                sidecars: [],
              },
              201,
            ),
          [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
          [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
        }),
      });
      await until(() => r.querySelector(".player video"));
      expect(r.querySelector(".ph .t")!.textContent).toBe("Severance");
      expect(calls.filter((c) => c.query.includes("kind="))).toEqual([]);
      r.querySelector("video")!.dispatchEvent(new Event("ended"));
      expect(r.querySelector(".upnext")!.textContent).toContain("Up next: T31 in 10s");
      await at("#/");
    } finally {
      vi.useRealTimers();
    }
  });

  it("plays a playlist row, sends its playlistId and goes to the next row when it ends", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await at(`#/play/${uid(21)}?playlist=${uid(9)}&n=0`);
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          [`GET /items/${uid(21)}`]: item(21, "movie"),
          "GET /media-files": page([]),
          [`GET /profiles/${uid(1)}/playlists/${uid(9)}/play-order`]: page([
            { entryId: uid(1), itemId: uid(21), kind: "movie", item: item(21, "movie") },
            { entryId: uid(2), itemId: uid(22), kind: "movie", item: item(22, "movie") },
          ]),
          [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: {
            itemId: uid(22),
            kind: "movie",
            title: "T22",
            index: 1,
          },
          "POST /playback/sessions": () =>
            json(
              {
                sessionId: uid(7),
                entry: {
                  position: 0,
                  verdict: "direct_play",
                  videoAction: "copy",
                  audioAction: "copy",
                  subtitleAction: "none",
                  bad: false,
                },
                streamUrl: "/api/v2/streams/k/0/file",
                streamKind: "file",
                positionMs: 0,
                negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8e6 },
                rungs: [],
                sidecars: [],
              },
              201,
            ),
          [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
          [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
        }),
      });
      await until(() => r.querySelector(".player video"));
      expect(calls.find((c) => c.key === "POST /playback/sessions")!.body).toMatchObject({
        itemId: uid(21),
        playlistId: uid(9),
      });
      r.querySelector("video")!.dispatchEvent(new Event("ended"));
      expect(r.querySelector(".upnext")!.textContent).toContain("Up next: T22 in 10s");
      vi.advanceTimersByTime(11_000);
      expect(location.hash).toBe(`#/play/${uid(22)}?playlist=${uid(9)}&n=1`);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reads only its own row of the play order", async () => {
    await at(`#/play/${uid(22)}?playlist=${uid(9)}&n=1`);
    const r = root();
    const rows = [21, 22, 23, 24].map((id, i) => ({
      entryId: uid(i + 1),
      itemId: uid(id),
      kind: "movie",
      item: item(id, "movie"),
    }));
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(22)}`]: item(22, "movie"),
        "GET /media-files": page([]),
        [`GET /profiles/${uid(1)}/up-next/${uid(22)}`]: {
          itemId: uid(23),
          kind: "movie",
          title: "T23",
          index: 2,
        },
        [`GET /profiles/${uid(1)}/playlists/${uid(9)}/play-order`]: (_: unknown, u: URL) => {
          const offset = Number(u.searchParams.get("offset"));
          const limit = Number(u.searchParams.get("limit"));
          const more = offset + limit < rows.length;
          return {
            items: rows.slice(offset, offset + limit),
            nextOffset: more ? offset + limit : undefined,
          };
        },
        "POST /playback/sessions": () =>
          json(
            {
              sessionId: uid(7),
              entry: {
                position: 0,
                verdict: "direct_play",
                videoAction: "copy",
                audioAction: "copy",
                subtitleAction: "none",
                bad: false,
              },
              streamUrl: "/api/v2/streams/k/0/file",
              streamKind: "file",
              positionMs: 0,
              negotiated: { verdict: "direct_play" },
              rungs: [],
              sidecars: [],
            },
            201,
          ),
        [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
        [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector(".player video"));
    const reads = calls.filter(
      (c) => c.key === `GET /profiles/${uid(1)}/playlists/${uid(9)}/play-order`,
    );
    expect(reads.map((c) => new URLSearchParams(c.query).toString())).toEqual(["limit=1&offset=1"]);
    expect(r.querySelector("[aria-label=Next]")).not.toBeNull();
    await at("#/");
  });

  describe("at the end", () => {
    const started = {
      sessionId: uid(7),
      entry: {
        position: 0,
        verdict: "direct_play",
        videoAction: "copy",
        audioAction: "copy",
        subtitleAction: "none",
        bad: false,
      },
      streamUrl: "/api/v2/streams/k/0/file",
      streamKind: "file",
      positionMs: 0,
      negotiated: { verdict: "direct_play" },
      rungs: [],
      sidecars: [],
    };
    const ended = async (routes: Record<string, unknown>, active = me(1)) => {
      await at(`#/item/${uid(21)}`);
      await at(`#/play/${uid(21)}`);
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": active,
          [`GET /items/${uid(21)}`]: item(21, "movie"),
          "GET /media-files": page([]),
          "POST /playback/sessions": () => json(started, 201),
          [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
          [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
          ...routes,
        }),
      });
      await until(() => r.querySelector(".player video"));
      r.querySelector("video")!.dispatchEvent(new Event("ended"));
      return r;
    };

    it("closes back to where it was opened with nothing next", async () => {
      await ended({
        [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: () => new Response(null, { status: 204 }),
      });
      await until(() => location.hash === `#/item/${uid(21)}`);
    });

    it("closes back on Cancel", async () => {
      const r = await ended({
        [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: {
          itemId: uid(22),
          kind: "movie",
          title: "T22",
        },
      });
      button(r, "Cancel").click();
      await until(() => location.hash === `#/item/${uid(21)}`);
    });

    it("waits for Play now with Autoplay off, and its checkbox turns Autoplay on", async () => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      try {
        const off = me(1);
        const helga = off.profiles[0] as object;
        off.profiles[0] = { ...helga, autoplay: false };
        const r = await ended(
          {
            [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: {
              itemId: uid(22),
              kind: "movie",
              title: "T22",
            },
            [`PUT /profiles/${uid(1)}/preferences`]: (b: unknown) => ({
              ...helga,
              ...(b as object),
            }),
          },
          off,
        );
        const card = r.querySelector(".upnext")!;
        expect(card.textContent).toContain("Up next: T22");
        expect(card.textContent).not.toContain(" in 10s");
        vi.advanceTimersByTime(11_000);
        expect(location.hash).toBe(`#/play/${uid(21)}`);
        card.querySelector<HTMLInputElement>("input[type=checkbox]")!.click();
        await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/preferences`));
        expect(calls.find((c) => c.key === `PUT /profiles/${uid(1)}/preferences`)!.body).toEqual({
          autoplay: true,
        });
        expect(card.textContent).toContain("Up next: T22 in 10s");
        vi.advanceTimersByTime(11_000);
        expect(location.hash).toBe(`#/play/${uid(22)}`);
      } finally {
        vi.useRealTimers();
      }
    });

    it("drops the Up next card and the ended position when the viewer rewinds after the end", async () => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      try {
        const r = await ended({
          [`GET /profiles/${uid(1)}/up-next/${uid(21)}`]: {
            itemId: uid(22),
            kind: "movie",
            title: "T22",
          },
        });
        const video = r.querySelector("video")!;
        Object.defineProperty(video, "duration", { get: () => 100 });
        Object.defineProperty(video, "currentTime", { get: () => 40, set() {} });
        let atEnd = true;
        Object.defineProperty(video, "ended", { get: () => atEnd });
        expect(r.querySelector<HTMLElement>(".upnext")!.hidden).toBe(false);
        // A seek that leaves the element at its end is not a rewind.
        video.dispatchEvent(new Event("seeking"));
        expect(r.querySelector<HTMLElement>(".upnext")!.hidden).toBe(false);
        atEnd = false;
        video.dispatchEvent(new Event("seeking"));
        expect(r.querySelector<HTMLElement>(".upnext")!.hidden).toBe(true);
        vi.advanceTimersByTime(11_000);
        expect(location.hash).toBe(`#/play/${uid(21)}`);
        calls.length = 0;
        window.dispatchEvent(new Event("pagehide"));
        await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
        expect(calls.find((c) => c.key === `DELETE /playback/sessions/${uid(7)}`)!.query).toBe(
          "?positionMs=40000",
        );
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe("away from home", () => {
    const rung = (height: number, bitrate: number, extra = {}) => ({
      width: (height * 16) / 9,
      height,
      bitrate,
      verdict: "video_transcode",
      ...extra,
    });
    const away = (extra = {}) => ({
      sessionId: uid(7),
      mediaFileId: uid(9),
      entry: {
        position: 0,
        verdict: "video_transcode",
        videoAction: "convert",
        audioAction: "copy",
        subtitleAction: "none",
        bad: false,
      },
      streamUrl: "/api/v2/streams/k/0/file",
      streamKind: "file",
      positionMs: 0,
      negotiated: { verdict: "video_transcode", width: 1920, height: 1080, bitrate: 8_000_000 },
      away: true,
      auto: true,
      rungs: [
        rung(2160, 38_000_000, { verdict: "remux", blocked: true }),
        rung(1080, 8_000_000),
        rung(480, 1_500_000),
      ],
      sidecars: [],
      ...extra,
    });
    const play = async (stream: unknown, extra: Record<string, unknown> = {}) => {
      await at(`#/play/${uid(5)}`);
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          [`GET /items/${uid(5)}`]: item(5, "movie"),
          "GET /media-files": page([
            {
              id: uid(9),
              durationMs: 120_000,
              tracks: [],
              chapters: [
                { id: uid(1), position: 0, startMs: 0, endMs: 120_000, title: "Cold open" },
              ],
            },
          ]),
          "POST /playback/sessions": () => json(stream, 201),
          [`PUT /playback/sessions/${uid(7)}/position`]: () => new Response(null, { status: 204 }),
          [`PUT /playback/sessions/${uid(7)}/network`]: () => ({ action: "continue", stream }),
          [`DELETE /playback/sessions/${uid(7)}`]: () => new Response(null, { status: 204 }),
          ...extra,
        }),
      });
      await until(() => r.querySelector(".menu.quality"));
      return r;
    };
    const net = () =>
      calls.filter((c) => c.key === `PUT /playback/sessions/${uid(7)}/network`).map((c) => c.body);
    const previews = {
      intervalMs: 10_000,
      count: 12,
      width: 320,
      height: 180,
      frameUrl: "/api/v2/streams/k/0/previews/{n}.jpg",
      bifUrl: "/api/v2/streams/k/0/previews.bif",
    };
    const hlsAway = (extra = {}) =>
      away({ streamKind: "hls", streamUrl: "/api/v2/streams/k/0/master.m3u8", ...extra });
    const levels = [
      { width: 854, height: 480, bitrate: 1_500_000 },
      { width: 1920, height: 1080, bitrate: 8_000_000 },
    ];

    it("offers Auto first, selected, disables blocked rungs and sends Auto at the start", async () => {
      const r = await play(away());
      const start = calls.find((c) => c.key === "POST /playback/sessions")!.body as {
        snapshot: { network: unknown };
      };
      expect(start.snapshot.network).toMatchObject({ auto: true });
      const opts = [...r.querySelectorAll<HTMLElement>(".menu.quality .opt")];
      expect(opts.map((o) => o.querySelector("b")!.textContent)).toEqual([
        "Auto",
        "Original",
        "1080p",
        "480p",
      ]);
      expect(opts[0].getAttribute("aria-checked")).toBe("true");
      expect(opts[1].getAttribute("aria-disabled")).toBe("true");
      expect(opts[1].textContent).toContain("Wi-Fi only");
      expect(r.querySelector(".menu.quality")!.textContent).toContain("Away from home");
      expect(r.querySelector(".qb")!.textContent).toBe("1080pAuto8 Mb/s");
      opts[1].click();
      expect(net()).toEqual([]);
    });

    it("sends the rung's cap for a fixed pick and no cap for Original", async () => {
      const r = await play(away({ rungs: [rung(2160, 38e6), rung(1080, 8e6), rung(480, 1.5e6)] }));
      const opts = () => [...r.querySelectorAll<HTMLElement>(".menu.quality .opt")];
      opts()[3].click();
      await until(() => net().length === 1);
      expect(net()[0]).toMatchObject({ maxBitrate: 1_500_000 });
      expect(net()[0]).toMatchObject({ auto: false });
      await until(() => opts()[3].getAttribute("aria-checked") === "true");
      opts()[1].click();
      await until(() => net().length === 2);
      expect(net()[1]).toMatchObject({ auto: false });
      expect(net()[1]).not.toHaveProperty("maxBitrate");
    });

    it("keeps the home menu without Auto", async () => {
      const r = await play(away({ away: false, auto: false }));
      expect(texts(r, ".menu.quality .opt b")).toEqual(["Original", "1080p", "480p"]);
    });

    it("shows the frame under the pointer when the stream has previews, else nothing", async () => {
      const r = await play(away({ previews }));
      const seek = r.querySelector<HTMLElement>(".seek")!;
      seek.dispatchEvent(new PointerEvent("pointermove", { clientX: 0 }));
      const pv = r.querySelector<HTMLElement>(".pv")!;
      expect(pv.hidden).toBe(false);
      expect(pv.querySelector("img")!.getAttribute("src")).toBe(
        "/api/v2/streams/k/0/previews/0.jpg",
      );
      expect(pv.textContent).toBe("0:00Cold open");
      seek.dispatchEvent(new PointerEvent("pointerleave"));
      expect(pv.hidden).toBe(true);
    });

    it("loads one preview frame at a time and ends on the frame under the pointer", async () => {
      const r = await play(away({ previews }));
      const seek = r.querySelector<HTMLElement>(".seek")!;
      seek.getBoundingClientRect = () => ({ left: 0, width: 1000 }) as DOMRect;
      const img = r.querySelector<HTMLImageElement>(".pv img")!;
      for (const x of [0, 300, 500, 1000])
        seek.dispatchEvent(new PointerEvent("pointermove", { clientX: x }));
      expect(img.getAttribute("src")).toBe("/api/v2/streams/k/0/previews/0.jpg");
      expect(r.querySelector(".pvcap span")!.textContent).toBe("2:00");
      img.dispatchEvent(new Event("load"));
      expect(img.getAttribute("src")).toBe("/api/v2/streams/k/0/previews/11.jpg");
      img.dispatchEvent(new Event("error"));
      expect(img.getAttribute("src")).toBe("/api/v2/streams/k/0/previews/11.jpg");
    });

    it("starts hls.js at the negotiated rung without a bandwidth probe, and Auto names the level in use", async () => {
      hlsMock.instances.length = 0;
      const r = await play(hlsAway({ positionMs: 30_000 }));
      await until(() => hlsMock.instances.length === 1);
      const hls = hlsMock.instances[0];
      expect(hls.config).toMatchObject({
        startPosition: 30,
        autoStartLoad: false,
        testBandwidth: false,
      });
      hls.levels = levels;
      hls.emit("hlsManifestParsed", { levels });
      expect([hls.startLevel, hls.loadLevel, hls.started]).toEqual([1, -1, 30]);
      r.querySelector<HTMLElement>(".qb")!.click();
      hls.emit("hlsLevelSwitched", { level: 0 });
      expect(r.querySelector(".qb")!.textContent).toBe("480pAuto1.5 Mb/s");
      expect(r.querySelector(".menu.quality .opt .sub")!.textContent).toBe(
        "Now 480p · adjusts to your connection",
      );
      expect(r.querySelector<HTMLElement>(".menu.quality")!.hidden).toBe(false);
    });

    it("has hls.js hold the forward buffer the server names, by length alone", async () => {
      hlsMock.instances.length = 0;
      await play(hlsAway({ bufferGoalSeconds: 120 }));
      await until(() => hlsMock.instances.length === 1);
      expect(hlsMock.instances[0].config).toMatchObject({
        maxBufferLength: 120,
        maxBufferSize: 0,
      });
    });

    it("leaves hls.js its own buffer when the server names no goal", async () => {
      hlsMock.instances.length = 0;
      await play(hlsAway());
      await until(() => hlsMock.instances.length === 1);
      expect(hlsMock.instances[0].config).not.toHaveProperty("maxBufferLength");
      expect(hlsMock.instances[0].config).not.toHaveProperty("maxBufferSize");
    });

    it("pins a fixed pick's level before hls.js loads anything", async () => {
      hlsMock.instances.length = 0;
      await play(
        hlsAway({
          auto: false,
          negotiated: { verdict: "video_transcode", width: 854, height: 480, bitrate: 1_500_000 },
        }),
      );
      await until(() => hlsMock.instances.length === 1);
      const hls = hlsMock.instances[0];
      hls.levels = levels;
      hls.emit("hlsManifestParsed", { levels });
      expect([hls.startLevel, hls.loadLevel, hls.started]).toEqual([0, 0, 0]);
    });

    it("destroys hls.js when the viewer leaves", async () => {
      hlsMock.instances.length = 0;
      await play(hlsAway());
      await until(() => hlsMock.instances.length === 1);
      await at("#/");
      expect(hlsMock.instances[0].destroyed).toBe(true);
    });

    it("carries the position in telemetry and switches to a stream the answer restarts", async () => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      try {
        const floor = away({ streamUrl: "/api/v2/streams/k2/0/file", positionMs: 12_000 });
        const r = await play(away(), {
          [`PUT /playback/sessions/${uid(7)}/throughput`]: () => ({
            action: "restart",
            stream: floor,
          }),
        });
        const video = r.querySelector("video")!;
        Object.defineProperty(video, "currentTime", { get: () => 12.5, set() {} });
        calls.length = 0;
        vi.advanceTimersByTime(10_000);
        await until(() => video.getAttribute("src") === "/api/v2/streams/k2/0/file");
        expect(calls.map((c) => c.key)).toEqual([`PUT /playback/sessions/${uid(7)}/throughput`]);
        expect(calls[0].body).toMatchObject({ positionMs: 12_500 });
      } finally {
        vi.useRealTimers();
      }
    });

    it("starts again at the position when the server has ended the session", async () => {
      let n = 0;
      const again = away({
        sessionId: uid(8),
        streamUrl: "/api/v2/streams/k2/0/file",
        positionMs: 12_500,
      });
      const r = await play(away(), {
        "POST /playback/sessions": () => json(n++ ? again : away(), 201),
      });
      const video = r.querySelector("video")!;
      Object.defineProperty(video, "currentTime", { get: () => 12.5, set() {} });
      video.dispatchEvent(new Event("error"));
      await until(() => video.getAttribute("src") === "/api/v2/streams/k2/0/file");
      expect(calls.filter((c) => c.key === "POST /playback/sessions")[1].body).toMatchObject({
        itemId: uid(5),
        positionMs: 12_500,
      });
      expect(r.querySelector<HTMLElement>(".pmsg")!.hidden).toBe(true);
    });

    it("sends telemetry at once on a stall", async () => {
      const r = await play(away(), {
        [`PUT /playback/sessions/${uid(7)}/throughput`]: () => ({
          action: "continue",
          stream: away(),
        }),
      });
      const video = r.querySelector("video")!;
      Object.defineProperty(video, "paused", { get: () => false });
      video.dispatchEvent(new Event("waiting"));
      await until(() => calls.some((c) => c.key === `PUT /playback/sessions/${uid(7)}/throughput`));
      expect(
        calls.find((c) => c.key === `PUT /playback/sessions/${uid(7)}/throughput`)!.body,
      ).toMatchObject({ stallCount: 1 });
    });

    it("reports the position when the page is hidden and ends the session at it on pagehide", async () => {
      const r = await play(away());
      const video = r.querySelector("video")!;
      Object.defineProperty(video, "currentTime", { get: () => 12.5, set() {} });
      Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
      document.dispatchEvent(new Event("visibilitychange"));
      Reflect.deleteProperty(document, "visibilityState");
      const reported = () =>
        calls
          .filter((c) => c.key === `PUT /playback/sessions/${uid(7)}/position`)
          .map((c) => c.body);
      await vi.waitFor(() =>
        expect(reported()).toContainEqual({ positionMs: 12_500, paused: true }),
      );
      calls.length = 0;
      window.dispatchEvent(new Event("pagehide"));
      await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
      await new Promise((x) => setTimeout(x, 20));
      const last = calls.filter((c) => /position|DELETE/.test(c.key));
      expect(last.map((c) => [c.key, c.query, c.keepalive])).toEqual([
        [`DELETE /playback/sessions/${uid(7)}`, "?positionMs=12500", true],
      ]);
    });

    it("reports the start position, not 0, until the element has reached it", async () => {
      const r = await play(away({ positionMs: 3_600_000 }), {
        [`PUT /playback/sessions/${uid(7)}/throughput`]: () => ({
          action: "continue",
          stream: away(),
        }),
      });
      const video = r.querySelector("video")!;
      let now = 0;
      Object.defineProperty(video, "currentTime", { get: () => now, set() {} });
      Object.defineProperty(video, "paused", { get: () => false });
      video.dispatchEvent(new Event("waiting"));
      await until(() => calls.some((c) => c.key === `PUT /playback/sessions/${uid(7)}/throughput`));
      expect(
        calls.find((c) => c.key === `PUT /playback/sessions/${uid(7)}/throughput`)!.body,
      ).toMatchObject({ positionMs: 3_600_000 });
      now = 3600.5;
      video.dispatchEvent(new Event("timeupdate"));
      calls.length = 0;
      window.dispatchEvent(new Event("pagehide"));
      await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
      expect(calls.find((c) => c.key === `DELETE /playback/sessions/${uid(7)}`)!.query).toBe(
        "?positionMs=3600500",
      );
    });

    it("ends the session at the start position when the viewer leaves before the first frame", async () => {
      await play(away({ positionMs: 3_600_000 }));
      calls.length = 0;
      window.dispatchEvent(new Event("pagehide"));
      await until(() => calls.some((c) => c.key === `DELETE /playback/sessions/${uid(7)}`));
      expect(calls.find((c) => c.key === `DELETE /playback/sessions/${uid(7)}`)!.query).toBe(
        "?positionMs=3600000",
      );
    });

    it("frees the object URL of a converted SRT sidecar when the viewer leaves", async () => {
      const made: string[] = [];
      const freed: string[] = [];
      const { createObjectURL, revokeObjectURL } = URL;
      vi.stubGlobal("fetch", async () => new Response("1\n00:00:01,000 --> 00:00:02,000\nHi\n"));
      URL.createObjectURL = () => `blob:x/${made.push("") - 1}`;
      URL.revokeObjectURL = (u: string) => void freed.push(u);
      // jsdom's <track> has no TextTrack.
      Object.defineProperty(HTMLTrackElement.prototype, "track", {
        configurable: true,
        get: () => ({ mode: "disabled" }),
      });
      try {
        const sidecar = {
          trackId: uid(9),
          format: "srt",
          forced: false,
          url: "/api/v2/subs/9.srt",
        };
        const r = await play(away({ sidecars: [sidecar] }));
        await until(() => r.querySelector("track"));
        await at("#/");
        expect(freed).toEqual(["blob:x/0"]);
      } finally {
        Object.assign(URL, { createObjectURL, revokeObjectURL });
        Reflect.deleteProperty(HTMLTrackElement.prototype, "track");
        vi.unstubAllGlobals();
      }
    });

    it("moves through the quality menu with the arrow keys and returns focus to the pill", async () => {
      const r = await play(away());
      const pill = r.querySelector<HTMLElement>(".qb")!;
      const video = r.querySelector("video")!;
      pill.focus();
      pill.click();
      const opts = [...r.querySelectorAll<HTMLElement>(".menu.quality .opt")];
      expect(document.activeElement).toBe(opts[0]);
      const key = (k: string) =>
        document.activeElement!.dispatchEvent(
          new KeyboardEvent("keydown", { key: k, bubbles: true }),
        );
      key("ArrowDown");
      expect(document.activeElement).toBe(opts[1]);
      expect(video.volume).toBe(1);
      key("ArrowUp");
      key("ArrowUp");
      expect(document.activeElement).toBe(opts[3]);
      key("Escape");
      expect(r.querySelector<HTMLElement>(".menu.quality")!.hidden).toBe(true);
      expect(document.activeElement).toBe(pill);
    });

    it("shows no preview without a previews block", async () => {
      const r = await play(away());
      r.querySelector(".seek")!.dispatchEvent(new PointerEvent("pointermove", { clientX: 0 }));
      expect(r.querySelector<HTMLElement>(".pv")!.hidden).toBe(true);
    });
  });
});

// jsdom has no media capability APIs; the snapshot only needs answers.
const stubMedia = () => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {} }));
  Object.defineProperty(navigator, "mediaCapabilities", {
    configurable: true,
    value: { decodingInfo: async () => ({ supported: false }) },
  });
};
const texts = (r: Element, sel: string) => [...r.querySelectorAll(sel)].map((e) => e.textContent);
const button = (r: Element, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent === label)!;

describe("link a TV", () => {
  beforeEach(() => setToken("tok"));
  const preview = (extra = {}) => ({
    deviceName: "Living room Roku",
    platform: "roku",
    createdAt: new Date(Date.now() - 5000).toISOString(),
    expiresAt: new Date(Date.now() + 600_000).toISOString(),
    ...extra,
  });
  const linkApi = (extra: Record<string, unknown> = {}, active = 1) => ({
    "GET /health": health,
    "GET /me": me(active),
    "GET /link/7KQ4-MNP2": () => preview(),
    ...extra,
  });
  const open = (qs = "?code=7KQ4-MNP2") => history.replaceState(null, "", `/link${qs}`);
  const lookups = () => calls.filter((c) => c.key.startsWith("GET /link/")).length;
  const gone = (status: number) => () =>
    json({ code: status === 429 ? "too_many_requests" : "not_found", message: "no" }, status);

  it("shows the device and when it asked, then approves for this adult", async () => {
    open();
    const r = root();
    await boot(r, {
      fetch: mockFetch(linkApi({ "POST /link/7KQ4-MNP2/approve": () => ({ id: uid(1) }) })),
    });
    await until(() => button(r, "Link this TV"));
    expect(r.textContent).toContain("Living room Roku");
    expect(r.textContent).toContain("Asked to sign in a moment ago");
    expect(r.textContent).toContain("The TV will show Helga's household. PINs still apply.");
    expect(r.querySelector<HTMLInputElement>("input")!.value).toBe("7KQ4-MNP2");
    button(r, "Link this TV").click();
    await until(() => r.querySelector("h1")?.textContent === "TV linked");
    expect(r.textContent).toContain("Living room Roku");
    expect(document.activeElement).toBe(r.querySelector("h1"));
  });

  it("reads a code from the link the way it would be typed", async () => {
    open("?code=7kq4mnp2");
    const r = root();
    await boot(r, { fetch: mockFetch(linkApi()) });
    await until(() => button(r, "Link this TV"));
    expect(r.querySelector<HTMLInputElement>("input")!.value).toBe("7KQ4-MNP2");
  });

  it("shows only the device for the code in the field when lookups finish out of order", async () => {
    open();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        linkApi({
          "GET /link/7KQ4-MNP2": async () => (await gate, preview()),
          "GET /link/AAAA-BBBB": () => preview({ deviceName: "Bedroom Roku" }),
        }),
      ),
    });
    await until(() => lookups() === 1);
    const code = r.querySelector<HTMLInputElement>("input")!;
    code.value = "AAAABBBB";
    code.dispatchEvent(new Event("input"));
    await until(() => button(r, "Link this TV"));
    release();
    await new Promise((x) => setTimeout(x, 20));
    expect(r.textContent).toContain("Bedroom Roku");
    expect(r.textContent).not.toContain("Living room Roku");
  });

  it("denies the link on Cancel", async () => {
    open();
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        linkApi({ "POST /link/7KQ4-MNP2/deny": () => new Response(null, { status: 204 }) }),
      ),
    });
    await until(() => button(r, "Cancel"));
    button(r, "Cancel").click();
    await until(() => r.querySelector("h1")?.textContent === "Not linked");
  });

  it("looks the code up once it is typed in full, adding the dash", async () => {
    open("");
    const r = root();
    await boot(r, { fetch: mockFetch(linkApi()) });
    const code = r.querySelector<HTMLInputElement>("input")!;
    expect(r.textContent).toContain("Enter the code shown on the TV.");
    code.value = "7kq4mn";
    code.dispatchEvent(new Event("input"));
    expect(lookups()).toBe(0);
    code.value = "7kq4mnp2";
    code.dispatchEvent(new Event("input"));
    expect(code.value).toBe("7KQ4-MNP2");
    await until(() => button(r, "Link this TV"));
    expect(lookups()).toBe(1);
  });

  it("states an unknown, expired or used code plainly, and a lockout", async () => {
    open();
    const r = root();
    await boot(r, { fetch: mockFetch(linkApi({ "GET /link/7KQ4-MNP2": gone(404) })) });
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe(
      "That code is unknown, has expired or was already used. Check the code on the TV or ask it for a new one.",
    );
    expect(r.textContent).not.toContain("Link this TV");

    r.replaceChildren();
    await boot(r, { fetch: mockFetch(linkApi({ "GET /link/7KQ4-MNP2": gone(429) })) });
    await until(() => r.querySelector(".err:not([hidden])"));
    expect(r.querySelector(".err")!.textContent).toBe(
      "Too many tries. Wait a few minutes and try again.",
    );
  });

  it("asks a signed-out visitor to sign in first, then returns to the same code", async () => {
    open();
    setToken();
    const r = root();
    let signedIn = false;
    await boot(r, {
      fetch: mockFetch({
        ...linkApi(),
        "POST /auth/sign-in": () => ((signedIn = true), { token: "tok", me: me(1) }),
      }),
    });
    expect(r.querySelector("h1")!.textContent).toBe("Sign in to Rabbit Ears");
    r.querySelector<HTMLInputElement>("input[type=email]")!.value = "h@example.com";
    r.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
    r.querySelector("form")!.requestSubmit();
    await until(() => signedIn && button(r, "Link this TV"));
    expect(location.pathname).toBe("/link");
  });

  it("returns a single sign-on started here to the same code", async () => {
    open();
    setToken();
    const r = root();
    const fetch = mockFetch({
      ...linkApi(),
      "GET /auth/sso-providers": [{ id: uid(3), name: "Keyhole" }],
      "POST /auth/sso/exchange": { token: "tok", me: me(1) },
    });
    await boot(r, { fetch });
    await until(() => r.querySelector("a[href*='/auth/sso/']"));
    const sso = r.querySelector<HTMLElement>("a[href*='/auth/sso/']")!;
    // jsdom cannot leave the page for the provider.
    sso.addEventListener("click", (e) => e.preventDefault());
    sso.click();
    // The server's callback comes back to the viewer's root.
    history.replaceState(null, "", "/#sso=one-time");
    await boot(r, { fetch });
    await until(() => button(r, "Link this TV"));
    expect(location.pathname + location.search).toBe("/link?code=7KQ4-MNP2");
  });

  it("tells a kid profile that an adult must link the TV", async () => {
    open();
    const r = root();
    await boot(r, { fetch: mockFetch(linkApi({}, 2)) });
    await until(() =>
      r.textContent?.includes("Only the adult who signed in on this device can link a TV."),
    );
    expect(lookups()).toBe(0);
    expect(r.querySelector("input")).toBeNull();
  });

  it("falls back to the same message when the server refuses the active profile", async () => {
    open();
    const r = root();
    await boot(r, { fetch: mockFetch(linkApi({ "GET /link/7KQ4-MNP2": gone(403) })) });
    await until(() =>
      r.textContent?.includes("Only the adult who signed in on this device can link a TV."),
    );
  });
});

describe("search", () => {
  beforeEach(() => setToken("tok"));

  it("searches every library at once from the top bar, a section per kind found", async () => {
    await at("#/search?q=toy");
    const r = root();
    const none = {
      movies: [],
      shows: [],
      episodes: [],
      people: [],
      artists: [],
      albums: [],
      songs: [],
      channels: [],
      videos: [],
    };
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "GET /search": (_: unknown, u: URL) =>
          u.searchParams.get("q") === "toy"
            ? {
                ...none,
                movies: [item(30, "movie", { parsedTitle: "Toy Story" })],
                shows: [item(10, "show", { parsedTitle: "Toy Shop" })],
                episodes: [
                  item(21, "episode", {
                    parentId: uid(11),
                    seasonNumber: 1,
                    episodeNumber: 2,
                    metadata: { status: "matched", title: "Toys" },
                  }),
                ],
                people: [{ id: uid(7), name: "Toy Maker" }],
                albums: [
                  {
                    id: uid(100),
                    libraryId: uid(3),
                    artistId: uid(50),
                    artistName: "Toy Band",
                    title: "Toybox",
                    songCount: 1,
                    durationMs: 1,
                    lossless: false,
                    genres: [],
                    addedAt: ts,
                  },
                ],
                artists: [
                  {
                    id: uid(50),
                    libraryId: uid(3),
                    name: "Toy Band",
                    sortName: "Toy Band",
                    albumCount: 1,
                    songCount: 1,
                    genres: [],
                    addedAt: ts,
                  },
                ],
                channels: [
                  {
                    id: uid(40),
                    libraryId: uid(5),
                    title: "Toy Reviews",
                    videoCount: 1,
                    unwatchedCount: 0,
                    addedAt: ts,
                  },
                ],
              }
            : none,
      }),
    });
    await until(() => r.querySelector("[aria-label=People] a"));
    expect(r.querySelector<HTMLInputElement>("input[type=search]")!.value).toBe("toy");
    expect(texts(r, "section h2")).toEqual(["Movies", "TV", "Music", "YouTube", "People"]);
    expect([...r.querySelectorAll("section a")].map((a) => a.getAttribute("href"))).toEqual([
      `#/movie/${uid(30)}`,
      `#/episode/${uid(21)}`,
      `#/show/${uid(10)}`,
      `#/album/${uid(100)}`,
      `#/artist/${uid(50)}`,
      `#/channel/${uid(40)}`,
      `#/person/${uid(7)}`,
    ]);
    expect(calls.some((c) => c.key === "GET /items" || c.key === "GET /people")).toBe(false);
    const field = r.querySelector<HTMLInputElement>("input[type=search]")!;
    field.value = "zzz";
    field.form!.requestSubmit();
    await until(() => r.textContent!.includes('Nothing matches "zzz".'));
    expect(location.hash).toBe("#/search?q=zzz");
  });

  it("searches as the viewer types, keeping the field and its focus", async () => {
    await at("#/");
    const r = root();
    const none = {
      movies: [],
      shows: [],
      episodes: [],
      people: [],
      artists: [],
      albums: [],
      songs: [],
      channels: [],
      videos: [],
    };
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "GET /home": { continueWatching: [], rows: [] },
        "GET /search": (_: unknown, u: URL) =>
          u.searchParams.get("q") === "toy"
            ? { ...none, movies: [item(30, "movie", { parsedTitle: "Toy Story" })] }
            : none,
      }),
    });
    const field = r.querySelector<HTMLInputElement>("input[type=search]")!;
    field.focus();
    field.value = "to";
    field.dispatchEvent(new Event("input"));
    field.value = "toy";
    field.dispatchEvent(new Event("input"));
    await until(() => r.querySelector(`a[href='#/movie/${uid(30)}']`));
    expect(location.hash).toBe("#/search?q=toy");
    expect(calls.filter((c) => c.key === "GET /search").map((c) => c.query)).toEqual(["?q=toy"]);
    expect(document.activeElement).toBe(field);
    field.value = "toyz";
    field.form!.requestSubmit();
    await until(() => r.textContent!.includes('Nothing matches "toyz".'));
    expect(document.activeElement).toBe(field);
  });
});

describe("genres and people", () => {
  beforeEach(() => {
    setToken("tok");
    stubMedia();
  });
  const base = {
    "GET /health": health,
    "GET /me": me(1),
    [`GET /profiles/${uid(1)}/watch-state`]: page([]),
  };

  it("filters the Movies grid by a genre of its library", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base,
        "GET /genres": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("libraryId") === uid(1)
              ? [
                  { id: uid(3), name: "Comedy" },
                  { id: uid(4), name: "Drama" },
                ]
              : [],
          ),
        "GET /items": (_: unknown, u: URL) =>
          u.searchParams.get("kind") !== "movie"
            ? page([])
            : u.searchParams.get("genreId") === uid(3)
              ? page([item(30, "movie")])
              : page([item(30, "movie"), item(31, "movie")]),
      }),
    });
    await until(() => r.querySelector(".grid a"));
    const select = [...r.querySelectorAll<HTMLSelectElement>("select")].find((s) =>
      s.parentElement!.textContent!.startsWith("Genre"),
    )!;
    expect([...select.options].map((o) => o.textContent)).toEqual([
      "All genres",
      "Comedy",
      "Drama",
    ]);
    select.value = uid(3);
    select.dispatchEvent(new Event("change"));
    await until(() => r.querySelectorAll(".grid a").length === 1);
    expect(r.querySelector(".grid b")!.textContent).toBe("T30");
  });

  it("filters by a genre named like the All option", async () => {
    await at("#/movies");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base,
        "GET /genres": page([{ id: uid(3), name: "All genres" }]),
        "GET /items": (_: unknown, u: URL) =>
          u.searchParams.get("kind") !== "movie"
            ? page([])
            : u.searchParams.get("genreId") === uid(3)
              ? page([item(30, "movie")])
              : page([item(30, "movie"), item(31, "movie")]),
      }),
    });
    await until(() => r.querySelector(".grid a"));
    const select = [...r.querySelectorAll<HTMLSelectElement>("select")].find((s) =>
      s.parentElement!.textContent!.startsWith("Genre"),
    )!;
    select.value = uid(3);
    select.dispatchEvent(new Event("change"));
    await until(() => r.querySelectorAll(".grid a").length === 1);
  });

  it("shows genres, cast, key crew and the server's playability on Movie details", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base,
        [`GET /items/${uid(30)}`]: item(30, "movie", { genres: [{ id: uid(3), name: "Comedy" }] }),
        "GET /media-files": page([
          {
            id: uid(1),
            itemId: uid(30),
            path: "/m/toy.mkv",
            sizeBytes: 1e9,
            container: "matroska",
            tracks: [
              { id: uid(1), kind: "video", index: 0, codec: "h264", width: 1920, height: 1080 },
            ],
          },
        ]),
        [`GET /items/${uid(30)}/credits`]: {
          cast: [
            { personId: uid(7), name: "Tom Hanks", character: "Woody", profilePath: "/a.jpg" },
          ],
          crew: [
            { personId: uid(8), name: "John Lasseter", job: "Director" },
            { personId: uid(9), name: "Joss Whedon", job: "Screenplay" },
          ],
        },
        [`POST /items/${uid(30)}/playability`]: {
          verdict: "video_transcode",
          negotiated: { verdict: "video_transcode" },
        },
      }),
    });
    await until(() => r.querySelector("[aria-label=Cast] a"));
    expect(r.querySelector(".meta")!.textContent).toContain("Comedy");
    const cast = r.querySelector("[aria-label=Cast] a")!;
    expect(cast.getAttribute("href")).toBe(`#/person/${uid(7)}`);
    expect(cast.textContent).toBe("Tom HanksWoody");
    expect(cast.querySelector("[data-art]")!.getAttribute("data-art")).toBe(`person/${uid(7)}`);
    expect(r.textContent).toContain("Directed by John Lasseter · Written by Joss Whedon");
    await until(() => r.querySelector(".warn")?.textContent === "Converted for this device");
    const sent = calls.find((c) => c.key === `POST /items/${uid(30)}/playability`)!.body as {
      decoder: unknown;
      network: unknown;
    };
    expect(sent.decoder).toBeTruthy();
    expect(sent.network).toBeTruthy();
    await until(() => calls.some((c) => c.key === `GET /people/${uid(7)}/artwork`));
  });

  it("lists a person's titles", async () => {
    await at(`#/person/${uid(7)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base,
        [`GET /people/${uid(7)}`]: { id: uid(7), name: "Tom Hanks" },
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("personId") === uid(7)
              ? [item(30, "movie"), item(10, "show", { parsedTitle: "Band" })]
              : [],
          ),
      }),
    });
    await until(() => r.querySelector(".grid a"));
    expect(r.querySelector("h1")!.textContent).toBe("Tom Hanks");
    expect([...r.querySelectorAll(".grid a")].map((a) => a.getAttribute("href"))).toEqual([
      `#/movie/${uid(30)}`,
      `#/show/${uid(10)}`,
    ]);
  });
});

describe("household", () => {
  beforeEach(() => setToken("tok"));
  const later = "2026-01-08T00:00:00Z";
  const members = [
    acct(1, "Helga", "adult", { hasPin: true }),
    acct(3, "Bea"),
    acct(4, "Cy", "adult", { pending: true, inviteExpiresAt: later }),
    acct(2, "Jason", "kid", { movieCeiling: "PG", tvCeiling: "TV-PG", birthday: "2017-08-19" }),
  ];
  const code = {
    id: uid(9),
    code: "7KQ4-MNP2",
    link: "/join/7KQ4-MNP2",
    accountId: uid(4),
    expiresAt: later,
    createdAt: ts,
  };
  const noOffers = { incoming: [], outgoing: [] };
  const rows = (r: Element) =>
    Object.fromEntries(
      [...r.querySelectorAll(".members li")].map((li) => [
        li.querySelector("b")!.textContent,
        texts(li, "button, a.btn"),
      ]),
    );

  it("offers Add kid and Add adult on Who's watching to an adult, none to a kid, and no Manage household", async () => {
    await at("#/who");
    const noPin = { ...me(1), profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")] };
    const links = async (m: unknown) => {
      document.body.replaceChildren();
      const r = root();
      await boot(r, { fetch: mockFetch({ "GET /health": health, "GET /me": m }) });
      await until(() => r.querySelector(".profiles"));
      return ["#/add-kid", "#/add-adult", "#/household"].filter((href) =>
        r.querySelector(`a[href='${href}']`),
      );
    };
    const all = ["#/add-kid", "#/add-adult"];
    expect(await links(noPin)).toEqual(all);
    expect(await links(me(1))).toEqual(all);
    expect(await links(me(2))).toEqual([]);
  });

  it("sends a kid who types an adult's form to Home", async () => {
    for (const hash of ["#/password", "#/add-kid", "#/add-adult", `#/kid/${uid(2)}`]) {
      document.body.replaceChildren();
      await at(hash);
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(2),
          "GET /items": page([]),
          "GET /genres": page([]),
          [`GET /profiles/${uid(2)}/watch-state`]: page([]),
        }),
      });
      await until(() => location.hash === "#/");
      await until(() => r.querySelector("header .profile"));
      expect(r.querySelector("main form")).toBeNull();
    }
  });

  describe("management behind the active adult's PIN", () => {
    const open = async (unlock: () => Response) => {
      await at("#/who");
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": me(1),
          [`POST /profiles/${uid(1)}/unlock`]: unlock,
        }),
      });
      await until(() => r.querySelector(".profiles"));
      return r;
    };
    const type = (pin: string) => {
      const field = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
      field.value = pin;
      field.dispatchEvent(new Event("input"));
    };

    it("asks for the PIN first and goes nowhere until it is right", async () => {
      const r = await open(() => json({ code: "forbidden", message: "wrong" }, 403));
      r.querySelector<HTMLElement>("a[href='#/add-kid']")!.click();
      expect(document.querySelector("[role=dialog]")).not.toBeNull();
      expect(location.hash).toBe("#/who");
      type("0000");
      await until(() => document.querySelector("[role=alert]")?.textContent === "Wrong PIN.");
      expect(location.hash).toBe("#/who");
      button(document.body, "Cancel").click();
      expect(document.querySelector("[role=dialog]")).toBeNull();
      expect(location.hash).toBe("#/who");
    });

    it("goes to the target once the PIN is right", async () => {
      const r = await open(() => new Response(null, { status: 204 }));
      r.querySelector<HTMLElement>("a[href='#/add-kid']")!.click();
      type("1234");
      await until(() => location.hash === "#/add-kid");
      expect(calls.find((c) => c.key === `POST /profiles/${uid(1)}/unlock`)!.body).toEqual({
        pin: "1234",
      });
    });
  });

  it("gives the owner every action on others and only PIN actions on themselves, then resends a code", async () => {
    await at("#/household");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1, true, [members[1], members[2]]),
        "GET /profiles": page(members),
        "GET /household/offers": noOffers,
        [`POST /profiles/${uid(4)}/join-code`]: { account: members[2], joinCode: code },
      }),
    });
    await until(() => r.querySelector(".members li"));
    expect(rows(r)).toEqual({
      Helga: ["Change PIN", "Remove PIN", "Share with another household…"],
      Bea: ["Share with another household…", "Remove"],
      Cy: ["Resend code", "Remove"],
      Jason: ["Edit", "Share with another household…", "Change to adult", "Remove"],
    });
    expect(texts(r, ".members li .sub")).toEqual([
      "Owner · PIN",
      "Adult",
      `Awaiting sign-in · Code expires ${new Date(later).toLocaleDateString()}`,
      "Kid · Movies up to PG, TV up to TV-PG",
    ]);
    button(r.querySelectorAll(".members li")[2], "Resend code").click();
    await until(() => r.textContent!.includes("7KQ4-MNP2"));
  });

  it("lets another adult edit and remove kids only", async () => {
    await at("#/household");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(3, true, [members[1]]),
        "GET /profiles": page([members[0], members[1], members[3]]),
        "GET /household/offers": noOffers,
        [`DELETE /profiles/${uid(2)}`]: () => new Response(null, { status: 204 }),
        [`PUT /profiles/${uid(3)}/pin`]: () => new Response(null, { status: 204 }),
      }),
    });
    await until(() => r.querySelector(".members li"));
    const share = "Share with another household…";
    expect(rows(r)).toEqual({
      Helga: [share],
      Bea: ["Set PIN", share],
      Jason: ["Edit", share, "Remove"],
    });
    button(r, "Set PIN").click();
    const pinDlg = document.querySelector("[role=dialog]")!;
    pinDlg.querySelector<HTMLInputElement>("input[type=password]")!.value = "4321";
    pinDlg.querySelector("form")!.requestSubmit();
    await until(() => calls.filter((c) => c.key === "GET /me").length === 2);
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(3)}/pin`)!.body).toEqual({
      pin: "4321",
    });
    await until(() => r.querySelector(".members li"));
    button(r, "Remove").click();
    const dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.textContent).toContain("Remove Jason?");
    button(dlg, "Remove").click();
    await until(() => calls.filter((c) => c.key === "GET /me").length === 2);
    expect(calls.some((c) => c.key === `DELETE /profiles/${uid(2)}`)).toBe(true);
  });

  it("changes a kid to an adult with an email", async () => {
    await at("#/household");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1, true, [members[1]]),
        "GET /profiles": page(members),
        "GET /household/offers": noOffers,
        [`POST /profiles/${uid(2)}/make-adult`]: { account: members[3], joinCode: code },
      }),
    });
    await until(() => r.querySelector(".members li"));
    button(r.querySelectorAll(".members li")[3], "Change to adult").click();
    const dlg = document.querySelector("[role=dialog]")!;
    dlg.querySelector<HTMLInputElement>("input[type=email]")!.value = "jason@example.test";
    dlg.querySelector("form")!.requestSubmit();
    await until(() => r.textContent!.includes("7KQ4-MNP2"));
    expect(calls.find((c) => c.key === `POST /profiles/${uid(2)}/make-adult`)!.body).toEqual({
      email: "jason@example.test",
      expiresInDays: 7,
    });
  });

  it("edits a kid: name, PIN cleared, and titles added to and removed from the allow list", async () => {
    await at(`#/kid/${uid(2)}`);
    const r = root();
    const kid = acct(2, "Jason", "kid", {
      hasPin: true,
      birthday: "2017-08-19",
      movieCeiling: "PG",
      tvCeiling: "TV-PG",
      color: "#3C4170",
    });
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /profiles/${uid(2)}`]: kid,
        [`PUT /profiles/${uid(2)}`]: (b: unknown) => ({ ...kid, ...(b as object) }),
        [`DELETE /profiles/${uid(2)}/pin`]: () => new Response(null, { status: 204 }),
        [`GET /profiles/${uid(2)}/allowed-items`]: { itemIds: [uid(30), uid(40)] },
        [`GET /items/${uid(30)}`]: item(30, "movie", { parsedTitle: "Toy Story" }),
        [`GET /youtube/channels/${uid(40)}`]: { id: uid(40), title: "Toy Reviews" },
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("q") === "mat" && u.searchParams.get("kind") === "movie"
              ? [item(31, "movie", { parsedTitle: "Matrix" })]
              : [],
          ),
        [`PUT /profiles/${uid(2)}/allowed-items/${uid(31)}`]: () =>
          new Response(null, { status: 204 }),
        [`DELETE /profiles/${uid(2)}/allowed-items/${uid(30)}`]: () =>
          new Response(null, { status: 204 }),
        "GET /profiles": page(members),
      }),
    });
    await until(() => r.querySelector("form"));
    expect(r.querySelector("h1")!.textContent).toBe("Edit Jason");
    expect(r.querySelector<HTMLInputElement>("#kb")!.value).toBe("2017-08-19");
    expect(r.querySelector<HTMLSelectElement>("#mc")!.value).toBe("PG");
    const pin = r.querySelector<HTMLInputElement>("[role=switch]")!;
    expect(pin.checked).toBe(true);
    expect(r.querySelector<HTMLInputElement>("input[aria-label=PIN]")!.hidden).toBe(false);

    expect(r.querySelector<HTMLInputElement>("input[aria-label=Indigo]")!.checked).toBe(true);
    r.querySelector<HTMLInputElement>("input[aria-label=Rust]")!.click();
    r.querySelector<HTMLInputElement>("input[name=filter]:not(:checked)")!.click();
    await until(() => r.textContent!.includes("Toy Reviews"));
    expect(r.textContent).toContain("Toy Story");
    const find = r.querySelector<HTMLInputElement>("input[type=search]")!;
    find.value = "mat";
    find.dispatchEvent(new Event("input"));
    await until(() => r.textContent!.includes("Matrix"));
    button(r, "Add").click();
    await until(() =>
      calls.some((c) => c.key === `PUT /profiles/${uid(2)}/allowed-items/${uid(31)}`),
    );
    [...r.querySelectorAll<HTMLElement>(".allow li")]
      .find((li) => li.textContent!.startsWith("Toy Story"))!
      .querySelector("button")!
      .click();
    await until(() =>
      calls.some((c) => c.key === `DELETE /profiles/${uid(2)}/allowed-items/${uid(30)}`),
    );

    r.querySelector<HTMLInputElement>("#kn")!.value = "Jason";
    pin.click();
    r.querySelector("form")!.requestSubmit();
    await until(() => calls.some((c) => c.key === `DELETE /profiles/${uid(2)}/pin`));
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(2)}`)!.body).toEqual({
      name: "Jason",
      birthday: "2017-08-19",
      filterMode: "allow_list",
      color: "#6B3A2E",
    });
    await until(() => location.hash === "#/household");
  });

  it("keeps the allow list on screen through an add and a remove, without asking for any title again", async () => {
    await at(`#/kid/${uid(2)}`);
    const r = root();
    const kid = acct(2, "Jason", "kid", { birthday: "2017-08-19", filterMode: "allow_list" });
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /profiles/${uid(2)}`]: kid,
        // 50 is gone from both the items and the channels: an unknown title.
        [`GET /profiles/${uid(2)}/allowed-items`]: { itemIds: [uid(30), uid(50)] },
        [`GET /items/${uid(30)}`]: item(30, "movie", { parsedTitle: "Toy Story" }),
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("q") === "mat" && u.searchParams.get("kind") === "movie"
              ? [item(31, "movie", { parsedTitle: "Matrix" })]
              : [],
          ),
        [`PUT /profiles/${uid(2)}/allowed-items/${uid(31)}`]: () =>
          new Response(null, { status: 204 }),
        [`DELETE /profiles/${uid(2)}/allowed-items/${uid(30)}`]: () =>
          new Response(null, { status: 204 }),
        "GET /profiles": page(members),
      }),
    });
    const titles = () =>
      [...r.querySelectorAll(".allow ul:first-of-type b")].map((b) => b.textContent);
    await until(() => titles().length === 2);
    expect(titles()).toEqual(["Toy Story", "Untitled"]);
    const find = r.querySelector<HTMLInputElement>("input[type=search]")!;
    find.value = "mat";
    find.dispatchEvent(new Event("input"));
    await until(() => r.textContent!.includes("Matrix"));
    button(r, "Add").click();
    await until(() => titles().length === 3);
    expect(titles()).toEqual(["Toy Story", "Untitled", "Matrix"]);
    [...r.querySelectorAll<HTMLElement>(".allow li")]
      .find((li) => li.textContent!.startsWith("Toy Story"))!
      .querySelector("button")!
      .click();
    await until(() => titles().length === 2);
    expect(titles()).toEqual(["Untitled", "Matrix"]);
    const count = (key: string) => calls.filter((c) => c.key === key).length;
    expect(count(`GET /profiles/${uid(2)}/allowed-items`)).toBe(1);
    expect(count(`GET /items/${uid(30)}`)).toBe(1);
    expect(count(`GET /items/${uid(50)}`)).toBe(1);
  });

  describe("shared with other households", () => {
    const offer = (id: number, extra = {}) => ({
      id: uid(id),
      profileId: uid(7),
      profileName: "Mia",
      profileKind: "kid",
      householdId: uid(5),
      householdName: "Mark",
      fromProfileId: uid(8),
      fromName: "Mark",
      toProfileId: uid(1),
      toName: "Helga",
      createdAt: ts,
      ...extra,
    });
    const shared = acct(2, "Jason", "kid", {
      movieCeiling: "PG",
      tvCeiling: "TV-PG",
      households: 2,
    });
    const jo = acct(6, "Jo", "adult", { households: 2, present: true });
    const open = async (extra: Record<string, unknown>, m: unknown = me(1, true, [jo])) => {
      await at("#/household");
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": m,
          "GET /profiles": page([members[0], shared, jo]),
          "GET /household/offers": noOffers,
          ...extra,
        }),
      });
      await until(() => r.querySelector(".members li"));
      return r;
    };

    it("lists offers to and from the household, and Accept, Decline and Withdraw act on them", async () => {
      const r = await open({
        "GET /household/offers": {
          incoming: [offer(10), offer(11, { profileName: "Ty", profileKind: "adult" })],
          outgoing: [offer(12, { profileId: uid(2), profileName: "Jason", toName: "Mark" })],
        },
        [`POST /household/offers/${uid(10)}/accept`]: () => new Response(null, { status: 204 }),
        [`DELETE /household/offers/${uid(11)}`]: () => new Response(null, { status: 204 }),
        [`DELETE /household/offers/${uid(12)}`]: () => new Response(null, { status: 204 }),
      });
      expect(texts(r, "h2")).toEqual(["Offers", "Profiles"]);
      const [mia, ty, jason] = r.querySelectorAll(".members")[0].querySelectorAll("li");
      expect(mia.querySelector(".sub")!.textContent).toBe(
        "Kid · Mark offers to share Mia with your household",
      );
      expect(texts(mia, "button")).toEqual(["Decline", "Accept"]);
      expect(ty.querySelector(".sub")!.textContent).toBe(
        "Adult · Mark offers to share Ty with your household",
      );
      expect(jason.querySelector(".sub")!.textContent).toBe("Kid · Offered to Mark, waiting");
      expect(texts(jason, "button")).toEqual(["Withdraw"]);
      button(mia, "Accept").click();
      button(ty, "Decline").click();
      button(jason, "Withdraw").click();
      for (const k of [
        `POST /household/offers/${uid(10)}/accept`,
        `DELETE /household/offers/${uid(11)}`,
        `DELETE /household/offers/${uid(12)}`,
      ])
        await until(() => calls.some((c) => c.key === k));
      await until(() => calls.filter((c) => c.key === "GET /me").length > 1);
    });

    it("shows no Offers section without offers", async () => {
      const r = await open({});
      expect(texts(r, "h2")).toEqual([]);
    });

    it("marks shared and visiting profiles, and removing a shared one only takes it out of here", async () => {
      const r = await open({
        [`DELETE /profiles/${uid(2)}`]: () => new Response(null, { status: 204 }),
      });
      expect(texts(r, ".members li .sub")).toEqual([
        "Owner · PIN",
        "Kid · Movies up to PG, TV up to TV-PG · In 2 households",
        "Adult · Visiting from another household",
      ]);
      expect(r.querySelectorAll(".members li .sub svg")).toHaveLength(2);
      expect(rows(r)).toEqual({
        Helga: ["Change PIN", "Remove PIN", "Share with another household…"],
        Jason: [
          "Edit",
          "Share with another household…",
          "Change to adult",
          "Remove from this household…",
        ],
        Jo: ["Remove from this household…"],
      });
      expect(r.textContent).toContain(
        "A shared profile is the same in every household it is in: one watch history, one set of playlists and limits. Adults in each of its households can change a kid's settings.",
      );
      button(r.querySelectorAll(".members li")[1], "Remove from this household…").click();
      const dlg = document.querySelector("[role=dialog]")!;
      expect(dlg.querySelector("h1")!.textContent).toBe("Remove Jason from this household?");
      expect(dlg.textContent).toContain(
        "Jason stays in their other household. Nothing of theirs is deleted.",
      );
      button(dlg, "Remove").click();
      await until(() => calls.some((c) => c.key === `DELETE /profiles/${uid(2)}`));
    });

    it("keeps today's removal wording and no caption for a profile in this household only", async () => {
      const r = await open({ "GET /profiles": page([members[0], members[3]]) });
      expect(rows(r).Jason).toContain("Remove");
      expect(r.textContent).not.toContain("A shared profile");
    });

    it("offers a profile to a connected adult from the Share sheet", async () => {
      const r = await open({
        "GET /household/linked-adults": page([acct(8, "Mark"), acct(9, "Marcus")]),
        "POST /household/offers": offer(13),
      });
      button(r.querySelectorAll(".members li")[1], "Share with another household…").click();
      await until(() => document.querySelector("[role=dialog]"));
      const dlg = document.querySelector("[role=dialog]")!;
      expect(dlg.querySelector("h1")!.textContent).toBe("Share Jason");
      expect(dlg.textContent).toContain(
        "Offer Jason to an adult in another household. Once they accept, Jason appears in Who's watching there with the same watch history, playlists and limits, and the adults there can change Jason's settings as you can.",
      );
      expect(dlg.textContent).toContain(
        "Only adults the server's admin has connected you with appear here.",
      );
      expect(texts(dlg, "label b")).toEqual(["Mark", "Marcus"]);
      const send = button(dlg, "Send offer") as HTMLButtonElement;
      expect(send.disabled).toBe(true);
      dlg.querySelectorAll<HTMLInputElement>("input[type=radio]")[1].click();
      expect(send.disabled).toBe(false);
      send.click();
      await until(() => calls.some((c) => c.key === "POST /household/offers"));
      expect(calls.find((c) => c.key === "POST /household/offers")!.body).toEqual({
        profileId: uid(2),
        toProfileId: uid(9),
      });
      await until(() => !document.querySelector("[role=dialog]"));
    });

    it("shows a refused offer in the Share sheet", async () => {
      const r = await open({
        "GET /household/linked-adults": page([acct(8, "Mark")]),
        "POST /household/offers": () =>
          json({ code: "conflict", message: "already offered to Mark" }, 409),
      });
      button(r.querySelectorAll(".members li")[1], "Share with another household…").click();
      await until(() => document.querySelector("[role=dialog] input[type=radio]"));
      const dlg = document.querySelector("[role=dialog]")!;
      dlg.querySelector<HTMLInputElement>("input[type=radio]")!.click();
      button(dlg, "Send offer").click();
      await until(() => dlg.querySelector("[role=alert]:not([hidden])"));
      expect(dlg.querySelector("[role=alert]")!.textContent).toBe("already offered to Mark");
    });

    it("gives a visiting adult nothing to manage, only Leave this household", async () => {
      const visiting = { ...me(6, true, [jo]), account: jo };
      const r = await open(
        { [`DELETE /profiles/${uid(6)}`]: () => new Response(null, { status: 204 }) },
        visiting,
      );
      expect(rows(r)).toEqual({ Helga: [], Jason: [], Jo: [] });
      expect(texts(r, ".end a, .end button")).toEqual(["Done", "Leave this household…"]);
      expect(calls.some((c) => c.key === "GET /household/offers")).toBe(false);
      button(r, "Leave this household…").click();
      const dlg = document.querySelector("[role=dialog]")!;
      expect(dlg.textContent).toContain("Nothing of yours is deleted.");
      button(dlg, "Leave").click();
      await until(() => calls.some((c) => c.key === `DELETE /profiles/${uid(6)}`));
      await until(() => location.hash.startsWith("#/who"));
    });

    it("offers a visiting adult no Add kid or Add adult on Who's watching", async () => {
      await at("#/who");
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          "GET /health": health,
          "GET /me": { ...me(6, true, [jo]), account: jo },
          "POST /me/lock": () => new Response(null, { status: 204 }),
        }),
      });
      await until(() => r.querySelector(".profiles"));
      expect(r.querySelector("a[href='#/add-kid']")).toBeNull();
      expect(r.querySelector("a[href='#/add-adult']")).toBeNull();
    });
  });
});

describe("More on details", () => {
  beforeEach(() => {
    setToken("tok");
    stubMedia();
  });
  const listKid = acct(5, "Ivy", "kid", { filterMode: "allow_list" });
  const household = { ...me(1), profiles: [...me(1).profiles, listKid] };
  const station = (id: number, name: string, ownerId = 1) => ({
    id: uid(id),
    name,
    kind: "movies",
    ownerId: uid(ownerId),
    householdId: uid(1),
    playlistId: uid(id * 10),
    order: "playlist",
    visibility: "private",
    days: [],
    times: [],
    onAir: { status: "off_schedule", upNext: [] },
    createdAt: ts,
    modifiedAt: ts,
  });
  const routes = (m: unknown, movie: object, more: Record<string, unknown> = {}) =>
    mockFetch({
      ...more,
      "GET /health": health,
      "GET /me": m,
      [`GET /profiles/${uid(1)}/watch-state`]: page([]),
      [`GET /profiles/${uid(2)}/watch-state`]: page([]),
      [`GET /items/${uid(30)}`]: item(30, "movie", movie),
      "GET /media-files": page([]),
      [`GET /items/${uid(30)}/credits`]: { cast: [], crew: [] },
      [`GET /items/${uid(30)}/allowed-for`]: { profileIds: [uid(5)] },
      [`PUT /items/${uid(30)}/kids-content`]: (b: unknown) => item(30, "movie", b as object),
      [`DELETE /items/${uid(30)}/kids-content`]: () => new Response(null, { status: 204 }),
      [`DELETE /profiles/${uid(5)}/allowed-items/${uid(30)}`]: () =>
        new Response(null, { status: 204 }),
      "GET /stations": page([station(4, "Mine"), station(6, "Someone else's", 3)]),
      [`POST /profiles/${uid(1)}/playlists/${uid(40)}/entries`]: { id: uid(1) },
    });
  const rows = (r: Element) =>
    [...r.querySelectorAll("[role=menu][aria-label=More] > *")].map((e) =>
      e.getAttribute("role") === "separator"
        ? "---"
        : e.getAttribute("aria-checked")
          ? `${e.textContent} [${e.getAttribute("aria-checked")}]`
          : e.textContent,
    );

  it("holds watched, kids' content, the allow lists, then Add to playlist, station and collection", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: routes(household, { kidsContent: true, kidsContentOverride: true }),
    });
    await until(() => button(r, "More"));
    button(r, "More").click();
    expect(rows(r)).toEqual([
      "Mark as watched",
      "Kids' content [true]",
      "Use rating",
      "Allowed for kids on a list",
      "Ivy [true]",
      "---",
      "Add to playlist",
      "Add to station",
      "Add to collection",
    ]);
    const opt = (t: string) =>
      [...r.querySelectorAll<HTMLElement>("[role=menu] [role^=menuitem]")].find((e) =>
        e.textContent!.startsWith(t),
      )!;
    opt("Ivy").click();
    await until(() =>
      calls.some((c) => c.key === `DELETE /profiles/${uid(5)}/allowed-items/${uid(30)}`),
    );
    await until(() => opt("Ivy").getAttribute("aria-checked") === "false");
    opt("Add to station").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    const dlg = document.querySelector("[role=dialog]")!;
    expect(texts(dlg, ".choices button")).toEqual(["Mine"]);
    expect(calls.find((c) => c.key === "GET /stations")!.query).toContain("kind=movies");
    button(dlg as HTMLElement, "Mine").click();
    await until(() =>
      calls.some((c) => c.key === `POST /profiles/${uid(1)}/playlists/${uid(40)}/entries`),
    );
    expect(
      calls.find((c) => c.key === `POST /profiles/${uid(1)}/playlists/${uid(40)}/entries`)!.body,
    ).toEqual({
      itemId: uid(30),
    });
    opt("Kids' content").click();
    await until(() => calls.some((c) => c.key === `PUT /items/${uid(30)}/kids-content`));
    expect(calls.find((c) => c.key === `PUT /items/${uid(30)}/kids-content`)!.body).toEqual({
      kidsContent: false,
    });
    opt("Use rating").click();
    await until(() => calls.some((c) => c.key === `DELETE /items/${uid(30)}/kids-content`));
  });

  it("dismisses a movie in progress beside its watched mark", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: routes(
        me(1),
        { kidsContent: false },
        {
          [`GET /profiles/${uid(1)}/watch-state/${uid(30)}`]: {
            itemId: uid(30),
            positionMs: 600000,
            watched: false,
            playCount: 0,
            changedAt: ts,
          },
          [`DELETE /profiles/${uid(1)}/watch-state/${uid(30)}/position`]: () =>
            new Response(null, { status: 204 }),
        },
      ),
    });
    await until(() => button(r, "More"));
    button(r, "More").click();
    expect(rows(r).slice(0, 2)).toEqual(["Mark as watched", "Dismiss"]);
    [...r.querySelectorAll<HTMLElement>("[role=menuitem]")]
      .find((e) => e.textContent === "Dismiss")!
      .click();
    await until(() =>
      calls.some((c) => c.key === `DELETE /profiles/${uid(1)}/watch-state/${uid(30)}/position`),
    );
  });

  it("a More option whose write fails says so in a toast", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: async (req: Request) =>
        req.method === "PUT"
          ? json({ code: "internal", message: "disk full" }, 500)
          : routes(household, { kidsContent: true })(req),
    });
    await until(() => button(r, "More"));
    button(r, "More").click();
    [...r.querySelectorAll<HTMLElement>("[role^=menuitem]")]
      .find((e) => e.textContent === "Kids' content")!
      .click();
    await until(() => document.querySelector(".toast"));
    expect(document.querySelector(".toast")!.textContent).toBe("disk full");
  });

  it("leaves out the allow lists when no kid is on one, and all but watched for a kid", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, { fetch: routes(me(1), { kidsContent: false }) });
    await until(() => button(r, "More"));
    button(r, "More").click();
    expect(rows(r)).toEqual([
      "Mark as watched",
      "Kids' content [false]",
      "---",
      "Add to playlist",
      "Add to station",
      "Add to collection",
    ]);
    expect(calls.some((c) => c.key === `GET /items/${uid(30)}/allowed-for`)).toBe(false);

    document.body.replaceChildren();
    const kid = root();
    await boot(kid, { fetch: routes(me(2), { kidsContent: true }) });
    await until(() => button(kid, "More"));
    button(kid, "More").click();
    expect(rows(kid)).toEqual(["Mark as watched"]);
  });
});

describe("related titles and neighbors on details", () => {
  beforeEach(() => {
    setToken("tok");
    stubMedia();
  });
  const film = (id: number, name: string) =>
    item(id, "movie", { metadata: { status: "matched", title: name } });
  const collection = (id: number, name: string) => ({
    id: uid(id),
    name,
    source: "tmdb",
    movieCount: 2,
    createdAt: ts,
    modifiedAt: ts,
  });
  const movieRoutes = (related: unknown, extra = {}) =>
    mockFetch({
      "GET /health": health,
      "GET /me": me(1),
      [`GET /items/${uid(30)}`]: film(30, "The Two Towers"),
      "GET /media-files": page([]),
      [`GET /items/${uid(30)}/credits`]: {
        cast: [{ personId: uid(7), name: "Elijah Wood", character: "Frodo" }],
        crew: [],
      },
      [`GET /items/${uid(30)}/related`]: related,
      ...extra,
    });
  // The rows between the film's header and its cast.
  const rowsAbove = (r: Element) => {
    const out: Element[] = [];
    for (let e = r.querySelector(".detail")?.nextElementSibling; e; e = e.nextElementSibling) {
      if (e.getAttribute("aria-label") === "Cast") break;
      out.push(e);
    }
    return out;
  };
  const actions = (r: Element) =>
    [...r.querySelectorAll(".actions a.btn.s")].map((a) => [
      a.textContent,
      a.getAttribute("aria-label"),
      a.getAttribute("href"),
    ]);

  it("gives each collection its own row of its films, in order, directly above Cast", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: movieRoutes({
        collections: [
          {
            collection: collection(2, "The Lord of the Rings"),
            items: [film(52, "Return"), film(41, "Fellowship")],
          },
          { collection: collection(9, "Long ones"), items: [film(47, "Heat")] },
        ],
        similar: [film(33, "Willow")],
      }),
    });
    await until(() => r.querySelector("[aria-label=Cast]"));
    expect(
      rowsAbove(r).map((row) => [
        row.getAttribute("aria-label"),
        row.querySelector("h2")!.textContent,
        [...row.querySelectorAll(".strip a.poster")].map((a) => a.getAttribute("href")),
      ]),
    ).toEqual([
      [
        "The Lord of the Rings",
        "The Lord of the Rings",
        [`#/movie/${uid(52)}`, `#/movie/${uid(41)}`],
      ],
      ["Long ones", "Long ones", [`#/movie/${uid(47)}`]],
    ]);
    expect(rowsAbove(r)[0]!.querySelector("a.poster b")!.textContent).toBe("Return");
    expect(actions(r)).toEqual([]);
  });

  it("gives each occasion in season it belongs to a row of the others, after its collections", async () => {
    const active = (id: number, name: string) => ({
      id: uid(id),
      name,
      headline: "Not this",
      anchorDate: "2026-12-25",
      firstDate: "2026-11-25",
      lastDate: "2026-12-31",
    });
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: movieRoutes(
        {
          collections: [{ collection: collection(2, "Heists"), items: [film(47, "Heat")] }],
          similar: [],
        },
        {
          [`GET /profiles/${uid(1)}/occasions`]: page([active(7, "Christmas"), active(8, "Alone")]),
          "GET /items": (_: unknown, u: URL) =>
            page(
              u.searchParams.get("occasionId") === uid(7)
                ? [film(30, "The Two Towers"), film(70, "Elf")]
                : [film(30, "The Two Towers")],
            ),
        },
      ),
    });
    await until(() => r.querySelector("section[aria-label=Christmas]"));
    expect(
      rowsAbove(r).map((row) => [
        row.getAttribute("aria-label"),
        row.querySelector("h2")!.textContent,
        row.querySelector("h2 a")?.getAttribute("href"),
        [...row.querySelectorAll(".strip a.poster")].map((a) => a.getAttribute("href")),
      ]),
    ).toEqual([
      ["Heists", "Heists", undefined, [`#/movie/${uid(47)}`]],
      ["Christmas", "Christmas", `#/occasion/${uid(7)}`, [`#/movie/${uid(70)}`]],
    ]);
    const q = new URLSearchParams(
      calls.find((c) => c.key === `GET /profiles/${uid(1)}/occasions`)!.query,
    );
    expect(q.get("itemId")).toBe(uid(30));
    expect(q.get("date")).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it("shows no row and no neighbors for an empty answer, a failed read or no connection", async () => {
    const r = root();
    for (const answer of [
      { collections: [], similar: [] },
      json({ code: "internal", message: "boom" }, 500),
      () => Promise.reject(new TypeError("Failed to fetch")),
    ]) {
      await at("#/");
      await at(`#/movie/${uid(30)}`);
      await boot(r, { fetch: movieRoutes(answer) });
      await until(() => r.querySelector("[aria-label=Cast]"));
      expect(rowsAbove(r)).toEqual([]);
      expect(r.querySelector(".err")).toBeNull();
      expect(actions(r)).toEqual([]);
    }
  });

  it("draws nothing of the row while it loads, then the page with it", async () => {
    let answer: (body: unknown) => void = () => {};
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: movieRoutes(
        () =>
          new Promise((done) => {
            answer = done;
          }),
      ),
    });
    await until(() => calls.some((c) => c.key === `GET /items/${uid(30)}/related`));
    expect(r.querySelector("main")!.textContent).toBe("Loading");
    answer({
      collections: [{ collection: collection(2, "Heists"), items: [film(47, "Heat")] }],
      similar: [],
    });
    await until(() => r.querySelector("section[aria-label=Heists]"));
  });

  it("names a film's franchise neighbors by title among the secondary actions", async () => {
    await at(`#/movie/${uid(30)}`);
    const r = root();
    await boot(r, {
      fetch: movieRoutes({
        collections: [],
        similar: [],
        previous: film(41, "The Fellowship of the Ring"),
        next: film(52, "The Return of the King"),
      }),
    });
    await until(() => actions(r).length);
    expect(rowsAbove(r)).toEqual([]);
    expect(actions(r)).toEqual([
      [
        "Previous: The Fellowship of the Ring",
        "Previous film, The Fellowship of the Ring",
        `#/movie/${uid(41)}`,
      ],
      ["Next: The Return of the King", "Next film, The Return of the King", `#/movie/${uid(52)}`],
    ]);
  });

  const ep = (id: number, season: number, n: number) =>
    item(id, "episode", {
      parentId: season === 0 ? uid(12) : uid(11),
      seasonNumber: season,
      episodeNumber: n,
    });
  const tvRoutes = (related: unknown) =>
    mockFetch({
      "GET /health": health,
      "GET /me": me(1),
      [`GET /items/${uid(10)}`]: item(10, "show"),
      [`GET /items/${uid(11)}`]: item(11, "season", { parentId: uid(10), seasonNumber: 1 }),
      [`GET /items/${uid(22)}`]: ep(22, 1, 2),
      [`GET /items/${uid(23)}`]: ep(23, 0, 1),
      "GET /items": (_: unknown, u: URL) =>
        page(
          u.searchParams.get("kind") === "season"
            ? [item(11, "season", { parentId: uid(10), seasonNumber: 1 })]
            : u.searchParams.get("kind") === "episode"
              ? [ep(21, 1, 1), ep(22, 1, 2)]
              : [],
        ),
      "GET /media-files": page([]),
      [`GET /profiles/${uid(1)}/watch-state`]: page([]),
      [`GET /items/${uid(22)}/related`]: related,
    });

  it("names an episode's neighbors by season and episode and goes in place of the page", async () => {
    await at(`#/season/${uid(11)}`);
    await at(`#/episode/${uid(22)}`);
    const r = root();
    await boot(r, {
      fetch: tvRoutes({ collections: [], similar: [], previous: ep(21, 1, 1), next: ep(23, 0, 1) }),
    });
    await until(() => actions(r).length > 1);
    expect(actions(r).filter(([t]) => /^(Previous|Next)/.test(t!))).toEqual([
      ["Previous: S1 E1", "Previous episode, S1 E1", `#/episode/${uid(21)}`],
      ["Next: S0 E1", "Next episode, S0 E1", `#/episode/${uid(23)}`],
    ]);
    const depth = history.length;
    [...r.querySelectorAll<HTMLElement>(".actions a")]
      .find((a) => a.textContent === "Next: S0 E1")!
      .click();
    await until(() => location.hash === `#/episode/${uid(23)}`);
    expect(history.length).toBe(depth);
    history.back();
    await until(() => location.hash === `#/season/${uid(11)}`);
  });

  it("gives a show a row of each in-season occasion's other members, above its cast", async () => {
    await at(`#/show/${uid(10)}`);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        [`GET /items/${uid(10)}`]: item(10, "show"),
        [`GET /items/${uid(10)}/credits`]: {
          cast: [{ personId: uid(7), name: "Ann", character: "Mom" }],
          crew: [],
        },
        [`GET /profiles/${uid(1)}/watch-state`]: page([]),
        [`GET /profiles/${uid(1)}/occasions`]: (_: unknown, u: URL) =>
          page(
            u.searchParams.get("itemId") === uid(10)
              ? [
                  {
                    id: uid(7),
                    name: "Christmas",
                    headline: "Not this",
                    anchorDate: "2026-12-25",
                    firstDate: "2026-11-25",
                    lastDate: "2026-12-31",
                  },
                ]
              : [],
          ),
        "GET /items": (_: unknown, u: URL) =>
          page(
            u.searchParams.get("occasionId") === uid(7)
              ? [item(10, "show"), item(4, "show"), item(5, "movie")]
              : [],
          ),
      }),
    });
    await until(() => r.querySelector("section[aria-label=Christmas]"));
    const row = r.querySelector("section[aria-label=Christmas]")!;
    expect(row.nextElementSibling!.getAttribute("aria-label")).toBe("Cast");
    expect(row.querySelector("h2 a")!.getAttribute("href")).toBe(`#/occasion/${uid(7)}`);
    expect(row.querySelector("h2")!.textContent).toBe("Christmas");
    expect([...row.querySelectorAll(".strip a.poster")].map((a) => a.getAttribute("href"))).toEqual(
      [`#/show/${uid(4)}`, `#/movie/${uid(5)}`],
    );
  });

  it("shows no neighbors on an episode whose read fails", async () => {
    await at(`#/episode/${uid(22)}`);
    const r = root();
    await boot(r, { fetch: tvRoutes(json({ code: "internal", message: "boom" }, 500)) });
    await until(() => r.querySelector("h1.big"));
    expect(actions(r).filter(([t]) => /^(Previous|Next)/.test(t!))).toEqual([]);
    expect(r.querySelector(".err")).toBeNull();
  });
});

describe("primary navigation", () => {
  beforeEach(() => setToken("tok"));
  const lib = (kind: string) => ({ id: uid(1), kind });
  const labels = (r: HTMLElement) =>
    [...r.querySelectorAll("header nav a")].map((a) => a.textContent);
  const start = async (libraries: Handler | unknown, hash = "#/") => {
    await at(hash);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "GET /libraries": libraries,
        "GET /stations": page([]),
      }),
    });
    return r;
  };

  it("shows only the sections the server has libraries for, and remembers them", async () => {
    const r = await start(page([lib("movies"), lib("music")]));
    await until(() => labels(r).join() === "Home,Movies,Music,Stations");
    expect(localStorage.getItem("rabbit-ears.sections")).toBe('["movies","music"]');
    // The next load shows the remembered answer before the server answers.
    const again = await start(() => new Promise(() => {}));
    await until(() => labels(again).length);
    expect(labels(again).join()).toBe("Home,Movies,Music,Stations");
  });

  it("shows every section until the server answers, and keeps the last set when it fails", async () => {
    const r = await start(json({ code: "internal", message: "x" }, 500));
    await until(() => labels(r).length);
    expect(labels(r)).toEqual(["Home", "TV", "Movies", "Music", "YouTube", "Stations"]);
  });

  it("sends a direct link to a hidden section to Home", async () => {
    await start(page([lib("movies")]), "#/youtube");
    await until(() => location.hash === "#/");
  });

  it("leaves the player alone when the sections change under it", async () => {
    let answer!: (body: unknown) => void;
    await start(() => new Promise((r) => (answer = r)), `#/play/${uid(1)}`);
    const redrawn = vi.fn();
    window.addEventListener("hashchange", redrawn);
    answer(page([lib("movies")]));
    await until(() => localStorage.getItem("rabbit-ears.sections") === '["movies"]');
    await new Promise((r) => setTimeout(r, 0));
    window.removeEventListener("hashchange", redrawn);
    expect(redrawn).not.toHaveBeenCalled();
  });

  it("leaves the bar alone under an open menu when the sections change, until it closes", async () => {
    let answer!: (body: unknown) => void;
    const r = await start(() => new Promise((res) => (answer = res)));
    await until(() => r.querySelector("header .profile"));
    r.querySelector<HTMLElement>("header .profile")!.click();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      answer(page([lib("movies")]));
      await vi.advanceTimersByTimeAsync(1000);
      expect(r.querySelector("header [role=menu]:not([hidden])")).not.toBeNull();
      r.querySelector<HTMLElement>("header .profile")!.click();
      await vi.advanceTimersByTimeAsync(2500);
    } finally {
      vi.useRealTimers();
    }
    answer(page([lib("movies")]));
    await until(() => labels(r).join() === "Home,Movies,Stations");
  });
});

describe("keyboard shortcuts", () => {
  beforeEach(() => setToken("tok"));
  // jsdom's navigator is not a Mac's, so Ctrl is the modifier.
  const key = (k: string, init: KeyboardEventInit = {}, at: Element = document.body) => {
    const e = new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init });
    at.dispatchEvent(e);
    return e;
  };
  const start = async (hash = "#/search") => {
    await at(hash);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        "GET /health": health,
        "GET /me": me(1),
        "GET /stations": page([]),
      }),
    });
    await until(() => r.querySelector("h1"));
    return r;
  };

  it("focuses search with Ctrl-F in place of the browser's find", async () => {
    const r = await start();
    expect(key("f", { ctrlKey: true }).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(r.querySelector("input[type=search]"));
  });

  it("goes to each section by number, Stations among them", async () => {
    const r = await start();
    key("2", { ctrlKey: true });
    expect(location.hash).toBe("#/movies");
    key("6", { ctrlKey: true });
    await until(() => r.querySelector("h1")?.textContent === "Stations");
    expect(r.querySelector("nav a[aria-current=page]")!.textContent).toBe("Stations");
  });

  it("lists every shortcut on ?, but not while typing", async () => {
    const r = await start();
    key("?", {}, r.querySelector("input[type=search]")!);
    expect(document.querySelector(".keysheet")).toBeNull();
    key("?");
    const sheet = document.querySelector(".keysheet")!;
    expect([...sheet.querySelectorAll("dd")].map((d) => d.textContent)).toContain("Search");
    expect(sheet.querySelector("kbd")!.textContent).toBe("Ctrl+F");
    key("Escape", {}, sheet);
    expect(document.querySelector(".keysheet")).toBeNull();
  });
});

describe("profile menu and preferences", () => {
  beforeEach(() => setToken("tok"));
  const shell = (m: unknown, extra = {}) =>
    mockFetch({
      "GET /health": health,
      "GET /me": m,
      [`GET /profiles/${uid(1)}/watch-state`]: page([]),
      [`GET /profiles/${uid(2)}/watch-state`]: page([]),
      "GET /items": page([]),
      [`GET /profiles/${uid(1)}/avatar`]: () => new Response(new Blob(["x"]), { status: 200 }),
      ...extra,
    });
  const menuOf = async (m: unknown) => {
    document.body.replaceChildren();
    const r = root();
    await boot(r, { fetch: shell(m) });
    await until(() => r.querySelector("header .profile"));
    r.querySelector<HTMLElement>("header .profile")!.click();
    return r;
  };

  it("shows the profile's color, initial and name on one button whose menu holds the rest", async () => {
    await at("#/");
    const admin = {
      ...me(1),
      account: acct(1, "Helga", "adult", { isAdmin: true }),
      profiles: [acct(1, "Helga", "adult", { color: "#2E6B5B" }), acct(2, "Jason", "kid")],
    };
    const r = await menuOf(admin);
    const b = r.querySelector<HTMLElement>("header .profile")!;
    expect(b.textContent).toBe("HHelga");
    expect(b.querySelector<HTMLElement>(".avatar")!.style.background).toBe("rgb(46, 107, 91)");
    expect(b.getAttribute("aria-expanded")).toBe("true");
    expect(texts(r, "header [role=menu] [role=menuitem]")).toEqual([
      "Switch profile",
      "Preferences",
      "Manage household",
      "Admin",
      "Password",
      "Sign out",
    ]);
    expect(r.querySelector("header nav")!.textContent).not.toContain("Admin");
    expect(r.querySelector("header a[href='#/password']:not([role=menuitem])")).toBeNull();
    (r.querySelector("header [role=menu] [role=menuitem]") as HTMLElement).click();
    await until(() => location.hash.startsWith("#/who"));
  });

  it("offers a kid only Switch profile and Sign out", async () => {
    await at("#/");
    const r = await menuOf(me(2));
    expect(texts(r, "header [role=menu] [role=menuitem]")).toEqual(["Switch profile", "Sign out"]);
  });

  it("paints an adult's Gravatar from the server over the initial", async () => {
    await at("#/");
    const r = await menuOf({
      ...me(1),
      profiles: [acct(1, "Helga", "adult", { avatarUrl: `/api/v2/profiles/${uid(1)}/avatar` })],
    });
    expect(r.querySelector(".profile .avatar")!.getAttribute("data-art")).toBe(`profile/${uid(1)}`);
    await until(() => calls.some((c) => c.key === `GET /profiles/${uid(1)}/avatar`));
  });

  it("saves the color, Hide kids' content and Autoplay on Preferences", async () => {
    await at("#/preferences");
    const r = root();
    const helga = acct(1, "Helga", "adult", { color: "#3C4170", hideKidsContent: false });
    await boot(r, {
      fetch: shell(
        { ...me(1), profiles: [helga] },
        {
          [`PUT /profiles/${uid(1)}/preferences`]: (b: unknown) => ({ ...helga, ...(b as object) }),
        },
      ),
    });
    await until(() => r.querySelector("input[aria-label=Teal]"));
    expect(r.querySelectorAll("[role=radiogroup] input[type=radio]")).toHaveLength(8);
    r.querySelector<HTMLInputElement>("input[aria-label=Teal]")!.click();
    await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/preferences`));
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(1)}/preferences`)!.body).toEqual({
      color: "#2E6B5B",
    });
    await until(() => r.querySelector("[role=switch]"));
    calls.length = 0;
    r.querySelector<HTMLInputElement>('[role=switch][aria-label="Hide kids\' content"]')!.click();
    await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/preferences`));
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(1)}/preferences`)!.body).toEqual({
      hideKidsContent: true,
    });
    await until(() => r.querySelector("[role=switch][aria-label=Autoplay]"));
    calls.length = 0;
    const auto = r.querySelector<HTMLInputElement>("[role=switch][aria-label=Autoplay]")!;
    expect(auto.checked).toBe(true);
    auto.click();
    await until(() => calls.some((c) => c.key === `PUT /profiles/${uid(1)}/preferences`));
    expect(calls.find((c) => c.key === `PUT /profiles/${uid(1)}/preferences`)!.body).toEqual({
      autoplay: false,
    });
  });

  it("deletes the adult's own account from Preferences once they give the password, then signs out", async () => {
    await at("#/preferences");
    const r = root();
    let wrong = true;
    await boot(r, {
      fetch: shell(me(1), {
        "GET /me/deletion": { outcome: "household", kidNames: ["Jason"] },
        "POST /me/deletion": () =>
          wrong
            ? json({ code: "forbidden", message: "the current password is wrong" }, 403)
            : new Response(null, { status: 204 }),
        "POST /auth/sign-out": () => json({ code: "unauthorized", message: "x" }, 401),
      }),
    });
    await until(() => r.querySelector("button.danger"));
    r.querySelector<HTMLElement>("button.danger")!.click();
    await until(() => document.querySelector("[role=dialog]"));
    const d = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(d.textContent).toContain("It cannot be undone.");
    expect(d.textContent).toContain("the household goes too, with Jason and everything of theirs.");
    d.querySelector<HTMLInputElement>("input[type=password]")!.value = "pw";
    d.querySelector("form")!.requestSubmit();
    await until(() => d.textContent!.includes("The password is wrong."));
    wrong = false;
    d.querySelector("form")!.requestSubmit();
    await until(() => r.querySelector("button[type=submit]")?.textContent === "Sign in");
    expect(calls.find((c) => c.key === "POST /me/deletion")!.body).toEqual({ password: "pw" });
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("tells the server's only admin why their account stays, with nothing to submit", async () => {
    await at("#/preferences");
    const r = root();
    await boot(r, {
      fetch: shell(me(1), { "GET /me/deletion": { outcome: "refused", kidNames: [] } }),
    });
    await until(() => r.querySelector("button.danger"));
    r.querySelector<HTMLElement>("button.danger")!.click();
    await until(() => document.querySelector("[role=dialog]"));
    const d = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(d.textContent).toContain("server's only admin");
    expect(d.querySelector("input, form")).toBeNull();
  });
});

describe("styles", () => {
  it("lets the hidden attribute hide what the viewer hides", () => {
    const css = readdirSync(import.meta.dirname)
      .filter((f) => f.endsWith(".css"))
      .map((f) => readFileSync(resolve(import.meta.dirname, f), "utf8"))
      .join("\n");
    document.head.append(Object.assign(document.createElement("style"), { textContent: css }));
    // Every element the viewer creates hidden or toggles hidden, by its classes.
    const shown = [
      "span.pill.sync.small",
      "div.pnote",
      "label.sel",
      "div.row",
      "div.allow",
      "button.btn.s.more",
      "span.adjusting",
      "p.err.pmsg",
      "div.menu",
      "div.pv",
      "div.upnext",
      "div.chosen",
      "div.mini",
      "input.in",
    ].filter((sel) => {
      const [tag, ...cls] = sel.split(".");
      const el = document.createElement(tag);
      el.className = cls.join(" ");
      el.hidden = true;
      document.body.append(el);
      return getComputedStyle(el).display !== "none";
    });
    expect(shown).toEqual([]);
  });
});
