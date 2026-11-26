// Live updates: while signed in and in view, one held poll of the server's
// event stream tells the pages and the player that something they show has
// changed. An event names a kind and ids; what changed is read again
// through the ordinary operations.
import type { Api, Schemas } from "../api/client";

export type LiveEvent = Schemas["Event"];
/** Told each batch of events; an empty batch says to reload everything shown. */
type Listener = (events: LiveEvent[]) => void;

const listeners = new Set<Listener>();
/** Hears every batch until the function returned is called. */
export const onLive = (l: Listener) => (listeners.add(l), () => void listeners.delete(l));
const tell = (events: LiveEvent[]) => [...listeners].forEach((l) => l(events));

const pause = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
const hidden = () => document.visibilityState === "hidden";
/** The kinds followed besides the first five, which the server sends only to a client that names them. */
const kinds: LiveEvent["kind"][] = ["library", "household"];
/** The longest wait between two tries of a failing poll. */
const MAX_WAIT = 30_000;

/**
 * Follows the stream until the function returned is called. A failed poll is
 * tried again after a growing wait; a server without the stream, or a device
 * signed out or locked, is not asked again. A device signed out, as by its
 * account's deletion elsewhere, calls signedOut. In the background nothing
 * is asked, and on return everything shown is reloaded once.
 */
export function follow(api: Api, sleep = pause, signedOut = () => {}): () => void {
  let stopped = false;
  let running = false;
  let abort: AbortController | undefined;
  const run = async () => {
    if (running) return;
    running = true;
    let cursor: string | undefined;
    let wait = 1000;
    while (!stopped && !hidden()) {
      abort = new AbortController();
      const r = await api
        .GET("/events", {
          params: { query: cursor === undefined ? { kinds } : { kinds, after: cursor } },
          signal: abort.signal,
        })
        .catch(() => undefined);
      if (stopped || hidden()) break;
      if (r?.data) {
        if (r.data.resync) tell([]);
        else if (r.data.events.length) tell(r.data.events);
        cursor = r.data.cursor;
        wait = 1000;
      } else if (r && r.response.status < 500 && ![408, 429].includes(r.response.status)) {
        stopped = true;
        if (r.response.status === 401) signedOut();
      } else {
        await sleep(wait);
        wait = Math.min(wait * 2, MAX_WAIT);
      }
    }
    running = false;
  };
  const seen = () => {
    if (stopped) return;
    if (hidden()) return abort?.abort();
    tell([]);
    void run();
  };
  document.addEventListener("visibilitychange", seen);
  void run();
  return () => {
    stopped = true;
    abort?.abort();
    document.removeEventListener("visibilitychange", seen);
  };
}
