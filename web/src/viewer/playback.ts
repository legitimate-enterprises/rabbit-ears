// One playback session against the server: starts it with the current
// snapshot, forwards group changes, reports position, and walks the plan on
// failure. The DOM player drives it; nothing here touches the document.
import type { Api, Schemas } from "../api/client";
import { ok, ApiError } from "../admin/api";
import { AUTO, type Groups, type Quality, type Snapshot } from "./capabilities";
import { movedTo } from "./handoff";

export type Stream = Schemas["PlaybackStream"];
type Update = Schemas["PlaybackUpdate"];

interface Common {
  snapshot: () => Promise<Snapshot>;
  audioTrackId?: number;
  subtitleTrackId?: number;
}
interface ItemOptions extends Common {
  profileId: number;
  itemId: number;
  /** Absent plays the default edition. */
  editionId?: number;
  positionMs?: number;
  playlistId?: number;
}
/** Tunes in: the server picks the program and the live position. */
interface StationOptions extends Common {
  stationId: number;
  /** Asks for the station's continuous stream, which a video station has. */
  live?: boolean;
}
type StartOptions = ItemOptions | StationOptions;

// Tabs share one device, whose other session the server ends when one starts.
const tabs = new BroadcastChannel("rabbit-ears.playback");
let heard = 0;
tabs.addEventListener("message", () => heard++);

export class Playback {
  stream!: Stream;
  /** What the viewer last picked; sent again with every network group. */
  choice: Quality = AUTO;
  /** What a station is doing, from tuning in. */
  onAir?: Schemas["StationOnAir"];
  // Set by starting again after the server ended the session; an answer from the new one clears it.
  private again = false;
  // Starts heard from other tabs when this one last started.
  private heard = 0;

  constructor(
    private api: Api,
    private o: StartOptions,
  ) {}

  async start(positionMs?: number): Promise<Stream> {
    const o = this.o;
    this.heard = heard;
    tabs.postMessage("start");
    if ("stationId" in o) {
      const t = await ok(
        this.api.POST("/stations/{stationId}/playback", {
          params: { path: { stationId: o.stationId } },
          body: {
            audioTrackId: o.audioTrackId,
            subtitleTrackId: o.subtitleTrackId,
            snapshot: await o.snapshot(),
            live: o.live,
          },
        }),
      );
      this.onAir = t.onAir;
      this.stream = t.stream;
    } else
      this.stream = await ok(
        this.api.POST("/playback/sessions", {
          body: {
            profileId: o.profileId,
            itemId: o.itemId,
            editionId: o.editionId,
            audioTrackId: o.audioTrackId,
            subtitleTrackId: o.subtitleTrackId,
            positionMs: positionMs ?? o.positionMs,
            playlistId: o.playlistId,
            snapshot: await o.snapshot(),
          },
        }),
      );
    return this.stream;
  }

  private get path() {
    return { params: { path: { sessionId: this.stream.sessionId } } };
  }
  private take(u: Update): Update {
    this.stream = u.stream;
    return u;
  }

  /** Reports the position first: group updates carry none, so the server resumes from it. keepalive outlives the page. */
  async position(positionMs: number, paused = false, keepalive = false) {
    const { response } = await this.api.PUT("/playback/sessions/{sessionId}/position", {
      ...this.path,
      body: { positionMs: Math.max(0, Math.round(positionMs)), paused },
      keepalive,
    });
    if (response.ok) this.again = false;
  }

  /** Re-sends only the groups that changed, at the given position. */
  async groups(c: Partial<Groups>, positionMs: number): Promise<Update | undefined> {
    await this.position(positionMs);
    let last: Update | undefined;
    if (c.display)
      last = this.take(
        await ok(
          this.api.PUT("/playback/sessions/{sessionId}/display", { ...this.path, body: c.display }),
        ),
      );
    if (c.audioRoute)
      last = this.take(
        await ok(
          this.api.PUT("/playback/sessions/{sessionId}/audio-route", {
            ...this.path,
            body: c.audioRoute,
          }),
        ),
      );
    if (c.network) {
      this.choice = c.network.auto ? AUTO : { maxBitrate: c.network.maxBitrate };
      last = this.take(
        await ok(
          this.api.PUT("/playback/sessions/{sessionId}/network", { ...this.path, body: c.network }),
        ),
      );
    }
    return last;
  }

  /** The viewer picked a quality: the network group saying so, resumed at positionMs. */
  async quality(network: Schemas["NetworkPathCapabilities"], positionMs: number) {
    return this.groups({ network }, positionMs);
  }

  /** The pick replaces the session's whole selection, and a session started again keeps it. */
  async tracks(sel: Schemas["TrackSelection"], positionMs: number) {
    this.o.audioTrackId = sel.audioTrackId;
    this.o.subtitleTrackId = sel.subtitleTrackId;
    await this.position(positionMs);
    return this.take(
      await ok(this.api.PUT("/playback/sessions/{sessionId}/tracks", { ...this.path, body: sel })),
    );
  }

  /** Telemetry; the answer is restart when the slow loop gave Auto its floor rung. */
  async throughput(t: Schemas["ThroughputTelemetry"]) {
    return this.take(
      await ok(
        this.api.PUT("/playback/sessions/{sessionId}/throughput", { ...this.path, body: t }),
      ),
    );
  }

  /**
   * A client failure marks this entry bad; undefined when the plan has no entry left.
   * A session the server ended starts again at positionMs, once until the new one answers,
   * unless another tab has started one since.
   */
  async failed(positionMs: number, reason: string): Promise<Update | undefined> {
    try {
      return this.take(
        await ok(
          this.api.POST("/playback/sessions/{sessionId}/events", {
            ...this.path,
            body: {
              kind: "client_failure",
              positionMs: Math.round(positionMs),
              reason: reason.slice(0, 500),
            },
          }),
        ),
      );
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) return undefined;
      if (!(e instanceof ApiError) || e.status !== 404 || this.again) throw e;
      if (heard !== this.heard) throw new Error("Another tab started playing.", { cause: e });
      // A session another device took with Move here is not started again.
      const to = await movedTo(this.api, this.stream.sessionId);
      if (to) throw new Error(`Moved to ${to}`, { cause: e });
      this.again = true;
      return { action: "restart", stream: await this.start(Math.max(0, Math.round(positionMs))) };
    }
  }

  async end(positionMs?: number, keepalive = false) {
    if (!this.stream) return;
    const query =
      positionMs === undefined ? {} : { positionMs: Math.max(0, Math.round(positionMs)) };
    await this.api.DELETE("/playback/sessions/{sessionId}", {
      params: { path: { sessionId: this.stream.sessionId }, query },
      keepalive,
    });
  }
}

export const resolutionLabel = (w: number, h: number) =>
  w >= 3200 || h >= 2000
    ? "4K"
    : h >= 1000 || w >= 1800
      ? "1080p"
      : h >= 700
        ? "720p"
        : h >= 460
          ? "480p"
          : `${h}p`;

export const mbps = (bps: number) => `${+(bps / 1e6).toFixed(bps >= 1e7 ? 0 : 1)} Mb/s`;

/** SRT to WebVTT, since <track> reads only the latter. */
export const srtToVtt = (srt: string) =>
  "WEBVTT\n\n" + srt.replace(/\r/g, "").replace(/(\d\d:\d\d:\d\d),(\d{3})/g, "$1.$2");
