import { beforeEach, describe, expect, it } from "vitest";
import { Reporter } from "./report";

type Batch = { appVersion: string; osVersion: string; entries: Record<string, string>[] };
type Sent = { url: string; auth: string | null; body: Batch };

describe("error reporter", () => {
  let now: number;
  let sent: Sent[];
  let answer: number;
  let r: Reporter;
  const inner = async (req: Request) => {
    if (req.url.endsWith("/client-errors")) {
      sent.push({ url: req.url, auth: req.headers.get("Authorization"), body: await req.json() });
      return new Response(null, { status: answer });
    }
    return new Response("{}", { status: req.url.endsWith("/fails") ? 500 : 200 });
  };
  // A send follows the response it rides on, which is not kept waiting for it.
  const get = async (path = "/me", token = "t1") => {
    await r.wrap(inner)(
      new Request(`http://srv/api/v2${path}`, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      }),
    );
    for (let i = 0; i < 8; i++) await new Promise((r) => setTimeout(r, 0));
  };

  beforeEach(() => {
    now = Date.parse("2026-03-01T12:00:00Z");
    sent = [];
    answer = 204;
    r = new Reporter(() => now);
    r.version = "0.26.0";
  });

  it("sends what it holds with the next request that succeeds, as that request's device", async () => {
    const e = new Error("x is undefined");
    r.report("details", e);
    await get("/fails");
    expect(sent).toHaveLength(0);
    await get();
    expect(sent).toHaveLength(1);
    const { url, auth, body } = sent[0];
    expect(url).toBe("http://srv/api/v2/client-errors");
    expect(auth).toBe("Bearer t1");
    expect(body.appVersion).toBe("0.26.0");
    expect(body.osVersion).toBe(navigator.userAgent);
    expect(body.entries).toHaveLength(1);
    const [x] = body.entries;
    expect(x).toMatchObject({
      at: "2026-03-01T12:00:00.000Z",
      message: "x is undefined",
      context: "details",
    });
    expect(x.detail).toBe(e.stack);
    expect(x.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(parseInt(x.id.replace(/-/g, "").slice(0, 12), 16)).toBe(now);
    await get();
    expect(sent).toHaveLength(1);
  });

  it("holds errors until a signed-in request succeeds", async () => {
    r.report("home", "boom");
    await get("/health", "");
    expect(sent).toHaveLength(0);
    await get();
    expect(sent[0].body.entries[0].message).toBe("boom");
  });

  it("reports one error once, however often it happens", async () => {
    for (let i = 0; i < 3; i++) r.report("home", new Error("same"));
    r.report("details", new Error("same"));
    await get();
    expect(sent[0].body.entries.map((x) => x.context)).toEqual(["home", "details"]);
    r.report("home", new Error("same"));
    await get();
    expect(sent).toHaveLength(1);
  });

  it("takes at most five errors a minute, so one broken page cannot flood", async () => {
    for (let i = 0; i < 8; i++) r.report("home", new Error(`e${i}`));
    await get();
    expect(sent[0].body.entries).toHaveLength(5);
    now += 60_000;
    r.report("home", new Error("later"));
    await get();
    expect(sent[1].body.entries.map((x) => x.message)).toEqual(["later"]);
  });

  it("keeps a batch the server failed on for the next try, and drops one it refused", async () => {
    r.report("home", new Error("a"));
    answer = 503;
    await get();
    answer = 204;
    await get();
    expect(sent.map((s) => s.body.entries.length)).toEqual([1, 1]);
    r.report("home", new Error("b"));
    answer = 429;
    await get();
    answer = 204;
    await get();
    expect(sent).toHaveLength(3);
  });

  it("bounds what it sends to the server's limits", async () => {
    const long = new Error("m".repeat(2000));
    long.stack = "s".repeat(5000);
    r.report("c".repeat(200), long);
    await get();
    const [x] = sent[0].body.entries;
    expect([x.message.length, x.context.length, x.detail.length]).toEqual([1000, 100, 4000]);
  });
});
