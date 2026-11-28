// Errors that reach the person, sent to the server for the admin's Logs.
// They are held in memory only and go with the next signed-in request that
// succeeds; a page closed before then takes them with it.
import { API_BASE, type Schemas } from "../api/client";

type Entry = Schemas["ClientError"];
type Fetch = (req: Request) => Promise<Response>;

/** The server's batch limit, and how many errors a minute are taken at all. */
const HELD = 25;
const PER_MINUTE = 5;

/** A UUIDv7 for the instant ms; getRandomValues, unlike randomUUID, needs no secure context. */
function uuid7(ms: number): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  for (let i = 0; i < 6; i++) b[i] = Math.floor(ms / 2 ** (8 * (5 - i))) & 0xff;
  b[6] = (b[6] & 0x0f) | 0x70;
  b[8] = (b[8] & 0x3f) | 0x80;
  const x = [...b].map((v) => v.toString(16).padStart(2, "0")).join("");
  return `${x.slice(0, 8)}-${x.slice(8, 12)}-${x.slice(12, 16)}-${x.slice(16, 20)}-${x.slice(20)}`;
}

export class Reporter {
  /** The app's version, the server's own, once /health has said it. */
  version = "";
  private held: Entry[] = [];
  /** Each error taken, by context and message, so none is taken twice. */
  private seen = new Set<string>();
  private taken: number[] = [];
  private sending = false;

  constructor(private now = () => Date.now()) {}

  report(context: string, e: unknown) {
    const err = e instanceof Error ? e : undefined;
    const message = (err ? err.message || err.name : String(e)).slice(0, 1000) || "Unknown error";
    const key = `${context}\n${message}`;
    const now = this.now();
    this.taken = this.taken.filter((t) => now - t < 60_000);
    if (this.seen.has(key) || this.taken.length >= PER_MINUTE || this.held.length >= HELD) return;
    this.seen.add(key);
    this.taken.push(now);
    this.held.push({
      id: uuid7(now),
      at: new Date(now).toISOString(),
      message,
      context: context.slice(0, 100),
      ...(err?.stack && { detail: err.stack.slice(0, 4000) }),
    });
  }

  /** fetch, sending what is held after each signed-in response that succeeds. */
  wrap(fetch?: Fetch): Fetch {
    return async (req) => {
      const auth = req.headers.get("Authorization");
      const res = await (fetch ?? globalThis.fetch)(req);
      if (res.ok && auth && this.held.length && !this.sending)
        void this.send(
          fetch ?? globalThis.fetch,
          new URL(API_BASE + "/client-errors", req.url),
          auth,
        );
      return res;
    };
  }

  private async send(fetch: Fetch, url: URL, auth: string) {
    this.sending = true;
    const entries = this.held.splice(0);
    const body = {
      appVersion: this.version || "unknown",
      osVersion: navigator.userAgent.slice(0, 256) || "unknown",
      entries,
    };
    try {
      const res = await fetch(
        new Request(url, {
          method: "POST",
          headers: { Authorization: auth, "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }),
      );
      // A refusal, as a 400 or 429, would be refused again.
      if (res.status >= 500) this.held.unshift(...entries);
    } catch {
      this.held.unshift(...entries);
    } finally {
      this.held.splice(HELD);
      this.sending = false;
    }
  }
}

export const reporter = new Reporter();
export const report = (context: string, e: unknown) => reporter.report(context, e);

/** Reports what nothing caught. */
export function listen() {
  window.addEventListener("error", (e) => report("uncaught", e.error ?? e.message));
  window.addEventListener("unhandledrejection", (e) => report("unhandled", e.reason));
}
