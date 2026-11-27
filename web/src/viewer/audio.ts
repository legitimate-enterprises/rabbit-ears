// The music engine and its mini player. One engine per active profile; it
// outlives page renders so music keeps playing while the viewer browses.
// Each song is its own playback session through the same negotiation video
// uses; the order of songs is the client's queue.
import "./music.css";
import type Hls from "hls.js";
import { ApiError } from "../admin/api";
import { h } from "../admin/dom";
import type { Api } from "../api/client";
import { AUTO, browserEnv, browserTarget, HLS_MIME, watchGroups } from "./capabilities";
import { color, type Ctx } from "./data";
import { deviceSnapshot, hlsLibrarySupported, keepVolume } from "./device";
import { clock } from "./format";
import { Playback, type Stream } from "./playback";
import { Queue, type Song } from "./queue";
import { caption, tuneIn, untilMs, type OnAir, type Station, type Tuned } from "./stations";
import { bar, ib, icon, logo } from "./ui";

export class Music {
  readonly q = new Queue();
  // Never in the document: page renders replace the chrome around it.
  readonly audio = new Audio();
  pb?: Playback;
  /** Why the current song is not playing; cleared by the next one. */
  error = "";
  miniEl?: HTMLElement;
  /** The station tuned in to and what it is doing; the queue waits beneath it, untouched. */
  station?: { s: Station; onAir: OnAir };
  // Bumped by every song start and by stop; work begun under an older value is dropped.
  private gen = 0;
  private hls?: Hls;
  private subs = new Set<() => void>();
  private timer: ReturnType<typeof setInterval>;
  private unwatch?: () => void;
  // When the station's program ends, or its next starts.
  private wait?: ReturnType<typeof setTimeout>;

  constructor(
    private api: Api,
    private profileId: string,
  ) {
    const a = this.audio;
    keepVolume(a);
    a.addEventListener("ended", () => void (this.station ? this.retune() : this.next()));
    a.addEventListener("error", () => a.getAttribute("src") && void this.fail(a.error?.message));
    a.addEventListener("play", () => this.changed());
    a.addEventListener("pause", () => {
      this.changed();
      if (!a.ended) void this.pb?.position(this.ms(), true).catch(() => {});
    });
    this.timer = setInterval(
      () => !a.paused && void this.pb?.position(this.ms()).catch(() => {}),
      10000,
    );
    window.addEventListener("pagehide", this.gone);
  }

  /** The page going away ends what plays, as leave does; the queue waits for Play. */
  private gone = () => this.leave();

  ms() {
    return this.audio.currentTime * 1000;
  }
  get playing() {
    return !this.audio.paused;
  }
  /** Calls fn on every change of song, queue or play state; returns the unsubscribe. */
  on(fn: () => void) {
    this.subs.add(fn);
    return () => void this.subs.delete(fn);
  }
  changed() {
    this.subs.forEach((f) => f());
  }

  play(songs: Song[], start: number, from: string, fromHref: string, o: Opts = {}) {
    this.q.shuffle = !!o.shuffle;
    this.q.load(songs, start, from, fromHref, o.playlistId);
    return this.start();
  }
  jump(i: number) {
    this.q.jump(i);
    return this.start();
  }
  next() {
    this.q.next();
    return this.start();
  }
  /** Early in a song, the song before; later, the start of this one. */
  prev() {
    if (this.audio.currentTime > 3 || this.q.index === 0) return void (this.audio.currentTime = 0);
    this.q.prev();
    return this.start();
  }
  toggle() {
    // After a station stops, the queue's song has no stream until it starts again.
    if (!this.pb && this.q.current) return void this.start();
    if (this.audio.paused) void this.audio.play().catch(() => {});
    else this.audio.pause();
  }
  /** A station cannot pause, so pausing one stops it. */
  pause() {
    if (this.station) this.leave();
    else this.audio.pause();
  }

  /** Tunes in to a music station at its live position; the queue keeps its place. */
  tune(s: Station) {
    this.station = { s, onAir: s.onAir };
    return this.retune();
  }

  /** Stops the station: its session ends and the queue is back, not playing. */
  leave() {
    this.gen++;
    clearTimeout(this.wait);
    void this.pb?.end(this.ms(), true).catch(() => {});
    this.pb = undefined;
    this.detach();
    this.station = undefined;
    this.error = "";
    this.changed();
  }

  /** Stops what plays, a station too, and empties the queue, which hides the mini player. */
  end() {
    this.q.load([], 0, "", "#/music");
    this.leave();
  }

  /** Ends the song on and asks the station again: the next song, or when it is back. */
  private async retune() {
    const st = this.station;
    if (!st) return;
    const mine = ++this.gen;
    clearTimeout(this.wait);
    void this.pb?.end(this.ms()).catch(() => {});
    this.pb = undefined;
    this.detach();
    this.error = "";
    let t: Tuned;
    try {
      t = await tuneIn(this.api, st.s.id, () => deviceSnapshot());
    } catch (e) {
      if (mine !== this.gen) return;
      this.error = (e as Error).message;
      return this.changed();
    }
    if (mine !== this.gen) {
      if (t.state === "on_air") void t.pb.end().catch(() => {});
      return;
    }
    st.onAir = t.onAir;
    const again = untilMs(t.state === "on_air" ? t.onAir.now?.endsAt : t.resumesAt);
    if (again !== undefined) this.wait = setTimeout(() => void this.retune(), again);
    if (t.state === "on_air") {
      this.pb = t.pb;
      if (await this.watch(mine)) await this.attach(t.pb.stream);
    }
    this.changed();
  }

  /** Ends the session and silences the engine for good. */
  stop() {
    this.gen++;
    clearTimeout(this.wait);
    clearInterval(this.timer);
    window.removeEventListener("pagehide", this.gone);
    this.unwatch?.();
    void this.pb?.end(this.ms(), true).catch(() => {});
    this.pb = undefined;
    this.detach();
    this.subs.clear();
  }

  private async start() {
    const mine = ++this.gen;
    // Playing from the queue leaves the station.
    clearTimeout(this.wait);
    this.station = undefined;
    // The song that was playing ends where it stopped: a finished one at its end.
    void this.pb?.end(this.ms()).catch(() => {});
    this.pb = undefined;
    this.detach();
    this.error = "";
    const song = this.q.current;
    this.changed();
    if (!song) return;
    const pb = new Playback(this.api, {
      profileId: this.profileId,
      itemId: song.id,
      positionMs: 0,
      playlistId: this.q.playlistId,
      snapshot: () => deviceSnapshot(),
    });
    try {
      await pb.start();
    } catch (e) {
      if (mine !== this.gen) return;
      this.error =
        e instanceof ApiError && e.status === 409
          ? `${song.title} can't play on this device.`
          : (e as Error).message;
      return this.changed();
    }
    if (mine !== this.gen) return void pb.end().catch(() => {});
    this.pb = pb;
    if (!(await this.watch(mine))) return;
    await this.attach(pb.stream);
    this.changed();
  }

  /** Follows display, audio route and network changes once; false when stale. */
  private async watch(mine: number) {
    if (this.unwatch) return true;
    const env = browserEnv(await hlsLibrarySupported());
    // A stop during the wait would never remove listeners added after it.
    if (mine !== this.gen) return false;
    this.unwatch = watchGroups(
      env,
      () => AUTO,
      (c) => void this.regroup(c),
      browserTarget(),
    );
    return true;
  }

  private async attach(s: Stream) {
    const mine = this.gen;
    this.detach();
    const a = this.audio;
    const at = s.positionMs / 1000;
    // Safari keeps its own HLS, as the video player does.
    const native =
      a.canPlayType(HLS_MIME) !== "" &&
      ("WebKitPlaybackTargetAvailabilityEvent" in window || !(await hlsLibrarySupported()));
    if (mine !== this.gen) return;
    if (s.streamKind === "hls" && !native) {
      const { default: Hls } = await import("hls.js");
      if (mine !== this.gen) return;
      const x = new Hls({ startPosition: at });
      this.hls = x;
      x.on(Hls.Events.ERROR, (_e, d) => d.fatal && void this.fail(`hls: ${d.type} ${d.details}`));
      x.loadSource(s.streamUrl);
      x.attachMedia(a);
    } else {
      a.src = s.streamUrl;
      if (at) a.addEventListener("loadedmetadata", () => (a.currentTime = at), { once: true });
    }
    void a.play().catch(() => {});
  }

  private detach() {
    this.hls?.destroy();
    this.hls = undefined;
    if (!this.audio.paused) this.audio.pause();
    this.audio.removeAttribute("src");
  }

  /** A client failure moves to the plan's next entry; none left stops on this song with a message. */
  private async fail(reason = "media error") {
    const pb = this.pb;
    const mine = this.gen;
    if (!pb) return;
    const u = await pb.failed(this.ms(), reason).catch((e: Error) => e);
    if (mine !== this.gen) return;
    if (u && !(u instanceof Error)) return this.attach(u.stream);
    const what = this.station?.s.name ?? this.q.current?.title;
    this.error =
      u?.message ??
      (what ? `${what} can't play on this device.` : "No way to play this on this device.");
    this.changed();
  }

  private async regroup(c: Parameters<Playback["groups"]>[0]) {
    const mine = this.gen;
    const u = await this.pb?.groups(c, this.ms()).catch(() => undefined);
    if (mine === this.gen && u?.action === "restart") await this.attach(u.stream);
    this.changed();
  }
}
interface Opts {
  shuffle?: boolean;
  playlistId?: string;
}

let engine: Music | undefined;
export const music = () => engine;
export function resetMusic(ctx?: Ctx) {
  engine?.stop();
  engine = ctx && new Music(ctx.api, ctx.profile.id);
}

export const albumArt = (albumId: string, cls: string) =>
  h(`div.art.${cls}`, {
    "aria-hidden": "true",
    style: `background:${color(albumId)}`,
    "data-art": `${albumId}/poster`,
    "data-ph": "album",
  });

/** The bar under every page while a song is loaded; built once per engine. */
export function mini(m: Music): HTMLElement {
  if (m.miniEl) return m.miniEl;
  const a = m.audio;
  const art = h("span");
  const name = h("b");
  const sub = h("span.sub");
  const play = ib("Pause", "pause", () => m.toggle(), 18);
  const elapsed = h("span.tm", "0:00");
  const total = h("span.tm", "0:00");
  const seek = h("input.seekbar", {
    type: "range",
    min: "0",
    max: "0",
    step: "1",
    value: "0",
    "aria-label": "Seek",
    oninput: () => (a.currentTime = Number(seek.value)),
  }) as HTMLInputElement;
  const mute = ib("Mute", "vol", () => (a.muted = !a.muted), 18);
  const vol = h("input.volume", {
    type: "range",
    min: "0",
    max: "1",
    step: "0.05",
    value: String(a.volume),
    "aria-label": "Volume",
    oninput: () => ((a.volume = Number(vol.value)), (a.muted = false)),
  }) as HTMLInputElement;
  const stop = ib("Stop", "stop", () => m.end(), 18);
  // A station has only Stop, and a bar with no knob.
  const queueCtl = [
    ib("Previous", "prev", () => void m.prev(), 18),
    play,
    ib("Next", "next", () => void m.next(), 18),
    stop,
  ];
  const progress = bar(0);
  const ctl = h("div.mctl", ...queueCtl);
  const mtime = h("div.mtime", elapsed, seek, total);
  let onStation = false;
  const el = h(
    "div.mini",
    { role: "region", "aria-label": "Music player", hidden: true },
    h("a.np", { href: "#/now-playing", title: "Now playing" }, art, h("span", name, sub)),
    ctl,
    mtime,
    mute,
    vol,
  );
  const song = () => (m.station ? m.station.onAir.now?.song : m.q.current);
  const time = () => {
    const s = song();
    const d = Number.isFinite(a.duration) ? a.duration : (s?.durationMs ?? 0) / 1000;
    (progress.firstChild as HTMLElement).style.width = d ? `${(a.currentTime / d) * 100}%` : "0";
    seek.max = String(Math.round(d));
    seek.value = String(Math.round(a.currentTime));
    seek.setAttribute("aria-valuetext", `${clock(a.currentTime)} of ${clock(d)}`);
    seek.style.setProperty("--p", d ? `${(a.currentTime / d) * 100}%` : "0%");
    elapsed.textContent = clock(a.currentTime);
    total.textContent = clock(d);
  };
  const paint = () => {
    const st = m.station;
    const s = song();
    el.hidden = !s && !st;
    if (el.hidden) return;
    if (!!st !== onStation) {
      onStation = !!st;
      ctl.replaceChildren(...(st ? [stop] : queueCtl));
      (st ? seek : progress).replaceWith(st ? progress : seek);
    }
    art.replaceChildren(s ? albumArt(s.albumId, "sq40") : h("div.art.sq40.ident", logo(20)));
    name.textContent = s?.title ?? st!.s.name;
    sub.textContent =
      m.error ||
      (st
        ? s
          ? `${s.artistName} · ${st.s.name}`
          : caption({ kind: "music", onAir: st.onAir })[0]
        : `${s!.artistName} · ${s!.albumTitle}`);
    sub.classList.toggle("err", !!m.error);
    play.replaceChildren(icon(m.playing ? "pause" : "play", 18));
    play.setAttribute("aria-label", m.playing ? "Pause" : "Play");
    play.title = play.getAttribute("aria-label")!;
    time();
  };
  a.addEventListener("timeupdate", time);
  a.addEventListener("loadedmetadata", time);
  a.addEventListener("volumechange", () => {
    mute.setAttribute("aria-label", a.muted ? "Unmute" : "Mute");
    vol.value = String(a.muted ? 0 : a.volume);
  });
  m.on(paint);
  paint();
  m.miniEl = el;
  return el;
}
