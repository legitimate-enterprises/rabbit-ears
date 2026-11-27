// Watch together: two profiles of one household play a title in sync. The
// server holds the shared state and answers each change at once to a held
// poll; every play, pause and seek takes effect on both at one instant on
// the server's clock.
import "./together.css";
import { ApiError, ok } from "../admin/api";
import { h } from "../admin/dom";
import type { Api, Schemas } from "../api/client";
import type { Ctx } from "./data";
import { countdown } from "./format";
import { icon } from "./ui";

export type Together = Schemas["WatchTogether"];

/** A playing client more than this far from the shared position seeks to it. */
const DRIFT_MS = 500;
/** A viewer's own seek is one at least this far; nearer is the player catching up. */
const SEEK_MS = 2000;
/** After a correcting seek, the player is left this long to buffer before the next. */
const SETTLE_MS = 3000;
/** Clock samples taken before following. */
const SAMPLES = 3;
/** A held poll answered unchanged after at least this long ran out its hold. */
const HELD_MS = 1000;

/** Whether this device takes part: the profile's other device may, in one this device is not in. */
const isMember = (wt: Together, me: string) =>
  wt.members.some((m) => m.accountId === me && m.thisDevice !== false);
/** Who the profile watches with: the other profile by name, or its own other device by that device's name. */
export const otherName = (wt: Together, me: string) =>
  wt.members.find((m) => m.accountId !== me)?.name ??
  wt.members.find((m) => m.thisDevice === false)?.deviceName ??
  "";

/** The open Watch together on itemId that the profile is in. */
export async function findTogether(api: Api, itemId: string, me: string) {
  const p = await ok(api.GET("/watch-together", { params: { query: { itemId } } }));
  return p.items.find((w) => w.status !== "ended" && isMember(w, me));
}

/** One poll; with after, held by the server until the version passes it. rttMs tells it how soon this client can act on a change. */
const get = (api: Api, watchTogetherId: string, after?: number, rttMs?: number) =>
  ok(
    api.GET("/watch-together/{watchTogetherId}", {
      params: {
        path: { watchTogetherId },
        query: {
          ...(after === undefined ? {} : { after }),
          ...(rttMs === undefined ? {} : { rttMs }),
        },
      },
    }),
  );

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The player as Sync sees it; positions in milliseconds. */
export interface Port {
  paused(): boolean;
  ms(): number;
  play(): void;
  pause(): void;
  seek(ms: number): void;
}

/** Keeps a player and the shared state of one Watch together in step. */
export class Sync {
  /** The server's clock less this one's, from the quickest round trip. */
  offset = 0;
  private rtt = Infinity;
  /** The latest round trip of a request that was not held, reported so the server leads no further ahead than needed. */
  private trip?: number;
  private busy = false;
  private stopped = false;
  private due?: ReturnType<typeof setTimeout>;
  private timer?: ReturnType<typeof setInterval>;
  private settled = 0;

  constructor(
    private api: Api,
    public wt: Together,
    private port: Port,
    private changed: (wt: Together) => void,
  ) {}

  get joined() {
    return this.wt.status === "joined";
  }
  serverNow(now = Date.now()) {
    return now + this.offset;
  }
  /** The shared position at a server instant: as of the last change, run on while playing. */
  expected(at = this.serverNow()) {
    const w = this.wt;
    return w.effectivePositionMs + (w.playing ? Math.max(0, at - w.effectiveAtMs) : 0);
  }

  async start() {
    for (let i = 0; i < SAMPLES && !this.stopped; i++) await this.sample();
    if (this.stopped) return;
    this.timer = setInterval(() => this.correct(), 1000);
    while (!this.stopped) {
      const v = this.wt.version;
      const t0 = Date.now();
      const answered = await this.poll();
      // A hold that ran out is asked again at once: a change in the gap would be heard late. A failure, or an answer that came back unchanged and quick, waits.
      if (!this.stopped && this.wt.version === v && !(answered && Date.now() - t0 >= HELD_MS))
        await sleep(1000);
    }
  }
  stop() {
    this.stopped = true;
    clearInterval(this.timer);
    clearTimeout(this.due);
  }

  /** One quick answer, its serverTimeMs taken as of the round trip's midpoint. */
  async sample() {
    await this.request(() => get(this.api, this.wt.id, undefined, this.trip));
  }

  /** One held poll for the next change; whether the server answered. */
  async poll() {
    return this.request(() => get(this.api, this.wt.id, this.wt.version, this.trip), false);
  }

  private async request(f: () => Promise<Together>, timed = true) {
    if (this.stopped) return false;
    const t0 = Date.now();
    let wt: Together;
    try {
      wt = await f();
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) this.take({ ...this.wt, status: "ended" });
      return false;
    }
    if (timed) this.clock(wt, t0, Date.now());
    if (!this.busy) this.take(wt);
    return true;
  }

  private clock(wt: Together, t0: number, t1: number) {
    this.trip = t1 - t0;
    if (t1 - t0 >= this.rtt) return;
    this.rtt = t1 - t0;
    this.offset = wt.serverTimeMs - (t0 + t1) / 2;
  }

  private take(wt: Together) {
    if (this.stopped || wt.version < this.wt.version) return;
    const was = this.wt;
    this.wt = wt;
    if (wt.status === "ended") this.stop();
    // A guest who arrives while this side already plays makes the shared state follow it.
    else if (wt.status === "joined" && was.status !== "joined" && !this.port.paused())
      void this.send(true, this.port.ms());
    else if (wt.status === "joined") this.follow();
    this.changed(wt);
  }

  /** Applies the shared state at its instant, or now if that has passed. */
  follow() {
    clearTimeout(this.due);
    this.due = undefined;
    const wait = this.wt.effectiveAtMs - this.serverNow();
    if (wait > 0) this.due = setTimeout(() => ((this.due = undefined), this.apply()), wait);
    else this.apply();
  }

  private apply() {
    if (this.wt.playing && this.port.paused()) this.port.play();
    else if (!this.wt.playing && !this.port.paused()) this.port.pause();
    this.correct(true);
  }

  private correct(now = false) {
    if (this.stopped || !this.joined || this.due || this.busy) return;
    if (!now && (!this.wt.playing || Date.now() < this.settled)) return;
    if (Math.abs(this.port.ms() - this.expected()) <= DRIFT_MS) return;
    this.port.seek(this.expected());
    this.settled = Date.now() + SETTLE_MS;
  }

  /** Call when the viewer played, paused or seeked; a change that only follows the shared state sends nothing. */
  local() {
    if (this.stopped || this.busy || this.due || !this.joined) return;
    const playing = !this.port.paused();
    if (playing === this.wt.playing && Math.abs(this.port.ms() - this.expected()) <= SEEK_MS)
      return;
    const ms = this.port.ms();
    // A play waits for the instant both start at.
    if (playing && !this.wt.playing) this.port.pause();
    void this.send(playing, ms);
  }

  private async send(playing: boolean, positionMs: number) {
    this.busy = true;
    try {
      const t0 = Date.now();
      const wt = await ok(
        this.api.PUT("/watch-together/{watchTogetherId}/state", {
          params: {
            path: { watchTogetherId: this.wt.id },
            query: this.trip === undefined ? {} : { rttMs: this.trip },
          },
          body: { playing, positionMs: Math.round(positionMs) },
        }),
      );
      this.clock(wt, t0, Date.now());
      this.busy = false;
      this.take(wt);
    } catch {
      // The next poll reads whatever the server holds; sending again at once would never stop.
      return;
    } finally {
      this.busy = false;
    }
    // Another action may have come in while this one was in flight.
    this.local();
  }

  async leave() {
    this.stop();
    await ok(
      this.api.DELETE("/watch-together/{watchTogetherId}", {
        params: { path: { watchTogetherId: this.wt.id } },
      }),
    );
  }
}

/**
 * Watch together on a details page. Polls while the page is shown and opens
 * the player when either member starts playing. For a kid, the server lists
 * only those on titles the kid may see.
 */
export async function watchTogether(
  ctx: Ctx,
  item: { id: string; title: string },
  play: HTMLElement,
  /** Told on every redraw whether the viewer is opening or inside one. */
  inside?: (yes: boolean) => void,
) {
  const me = ctx.profile.id;
  const api = ctx.api;
  const slot = h("span.tg");
  const note = h("p.tgnote");
  const label = play.lastChild as Text;
  let playLabel = label.data;

  let wt: Together | undefined;
  let at = Date.now();
  let key = "";
  let tick = () => {};
  // Bumped by every action; an answer that began before it is dropped.
  let gen = 0;
  let inflight = false;

  const left = () => countdown((wt?.joinMsLeft ?? 0) - (Date.now() - at));
  const fail = (e: Error) => {
    note.textContent = e.message;
    note.setAttribute("role", "alert");
  };
  const act = async (f: () => Promise<Together | undefined>) => {
    gen++;
    try {
      set(await f());
    } catch (e) {
      fail(e as Error);
    }
  };
  const leave = () =>
    act(async () => {
      await ok(
        api.DELETE("/watch-together/{watchTogetherId}", {
          params: { path: { watchTogetherId: wt!.id } },
        }),
      );
      return undefined;
    });

  const draw = () => {
    note.removeAttribute("role");
    inside?.(!!wt && isMember(wt, me));
    // The page may have relabelled Play since the last redraw.
    if (label.data !== "Play together") playLabel = label.data;
    label.data = playLabel;
    play.onclick = null;
    const host = wt?.members.find((m) => m.role === "host")?.name ?? "";
    const counts: HTMLElement[] = [];
    const count = () => {
      const e = h("span");
      counts.push(e);
      return e;
    };
    tick = () => counts.forEach((e) => (e.textContent = left()));
    if (!wt) {
      note.textContent = "";
      slot.replaceChildren(
        h(
          "button.btn.s",
          {
            type: "button",
            onclick: () =>
              void act(() => ok(api.POST("/watch-together", { body: { itemId: item.id } }))),
          },
          icon("users", 16),
          "Watch together",
        ),
      );
    } else if (wt.status === "joined") {
      label.data = "Play together";
      play.onclick = (ev) => {
        ev.preventDefault();
        void playTogether();
      };
      const dev = wt.members.find((m) => m.accountId !== me);
      note.textContent = `Pause and play on either device apply to both.${dev ? ` ${dev.name} is on ${dev.deviceName}.` : ""}`;
      slot.replaceChildren(
        h(
          "span.pill.sync",
          icon("users", 16),
          `In sync with ${otherName(wt, me)}`,
          h("button.leave", { type: "button", onclick: () => void leave() }, "Leave"),
        ),
      );
    } else if (isMember(wt, me)) {
      note.textContent = `Someone else who opens ${item.title} and taps Watch together in the next 5 minutes will play in sync with you. Pause and play on either device apply to both.`;
      slot.replaceChildren(
        h(
          "span.pill.wait",
          h("i"),
          h("span", "Waiting for someone to join · ", count()),
          h(
            "button",
            { type: "button", "aria-label": "Cancel", onclick: () => void leave() },
            icon("x", 14),
          ),
        ),
      );
    } else {
      note.replaceChildren(
        `${host} wants to watch this together. Join in the next `,
        count(),
        " to play in sync.",
      );
      slot.replaceChildren(
        h(
          "button.btn.join",
          {
            type: "button",
            onclick: () =>
              void act(() =>
                ok(
                  api.POST("/watch-together/{watchTogetherId}/join", {
                    params: { path: { watchTogetherId: wt!.id } },
                  }),
                ),
              ),
          },
          icon("users", 16),
          `Join ${host} · `,
          count(),
        ),
      );
    }
    tick();
  };

  function set(next: Together | undefined) {
    if (next?.status === "ended") next = undefined;
    const was = wt;
    wt = next;
    at = Date.now();
    // The guest's Play on the other device opens the player here too.
    if (
      was &&
      next &&
      was.id === next.id &&
      next.status === "joined" &&
      !was.playing &&
      next.playing
    )
      ctx.go(`#/play/${item.id}`);
    const k = next
      ? `${next.id}:${next.status}:${next.members.map((m) => `${m.accountId}${m.name}`).join()}`
      : "";
    if (k !== key) {
      key = k;
      draw();
    } else tick();
    if (next && isMember(next, me)) void follow();
  }

  /** Play together: a play for both at the shared position, then this side's player. */
  const playTogether = async () => {
    const w = wt!;
    const positionMs = Math.round(w.positionMs + (w.playing ? Date.now() - at : 0));
    await act(() =>
      ok(
        api.PUT("/watch-together/{watchTogetherId}/state", {
          params: { path: { watchTogetherId: w.id } },
          body: { playing: true, positionMs },
        }),
      ),
    );
    if (wt?.status === "joined" && wt.playing && !gone) ctx.go(`#/play/${item.id}`);
  };

  // Set once the page is left; an answer that lands after it must not open the player.
  let gone = false;
  /** Lists the title's, or reads the one this profile is in; a held read waits for its next change. */
  const poll = async (held = false) => {
    if (!held && inflight) return;
    if (!held) inflight = true;
    const mine = ++gen;
    try {
      const next =
        wt && isMember(wt, me)
          ? await get(api, wt.id, held ? wt.version : undefined).catch((e: unknown) => {
              if (e instanceof ApiError && e.status === 404) return undefined;
              throw e;
            })
          : (
              await ok(api.GET("/watch-together", { params: { query: { itemId: item.id } } }))
            ).items.find((w) => w.status !== "ended");
      if (mine === gen && !gone) set(next);
    } catch {
      // Keep what is shown; the next poll tries again.
    } finally {
      if (!held) inflight = false;
    }
  };
  let following = false;
  async function follow() {
    if (following) return;
    following = true;
    while (!gone && wt && isMember(wt, me)) {
      const v = wt.version;
      await poll(true);
      if (wt?.version === v) await sleep(1000);
    }
    following = false;
  }
  // Listening before the first answer, so leaving while it is in flight still stops the polling.
  window.addEventListener("hashchange", () => (gone = true), { once: true });
  await poll();
  draw();
  if (gone) return { actions: slot, note };
  // Every five seconds until in one, which is then followed change by change.
  let n = 0;
  const timer = setInterval(() => {
    if (gone) return clearInterval(timer);
    tick();
    if (++n % 5 === 0 && !(wt && isMember(wt, me))) void poll();
  }, 1000);
  return { actions: slot, note };
}
