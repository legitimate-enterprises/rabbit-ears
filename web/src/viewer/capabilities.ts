// Browser capability snapshot for the server's playback decision. Every fact
// comes from a browser API; where none exists the fallback errs toward the
// server converting rather than a broken play.
import type { Schemas } from "../api/client";

type Hdr = Schemas["HdrFormat"];
export type Snapshot = Schemas["CapabilitySnapshot"];

interface DecodeResult {
  supported: boolean;
  powerEfficient: boolean;
  smooth: boolean;
}
/** The slice of the browser the snapshot reads, so tests can supply a fake. */
export interface Env {
  decodingInfo(cfg: unknown): Promise<DecodeResult>;
  canPlayType(mime: string): string;
  hlsViaLibrary: boolean;
  screen: { width: number; height: number };
  pixelRatio: number;
  matches(query: string): boolean;
  connection?: { type?: string; saveData?: boolean };
  hostname: string;
  audioChannels(): number;
}

// One probe per codec profile; profile is ffprobe's name, cs the RFC 6381 string.
const PROBES = [
  { codec: "h264", profile: "Baseline", cs: "avc1.42E033", level: 51, depth: 8 },
  { codec: "h264", profile: "Constrained Baseline", cs: "avc1.42E033", level: 51, depth: 8 },
  { codec: "h264", profile: "Main", cs: "avc1.4D4033", level: 51, depth: 8 },
  { codec: "h264", profile: "High", cs: "avc1.640033", level: 51, depth: 8 },
  { codec: "hevc", profile: "Main", cs: "hvc1.1.6.L153.B0", level: 153, depth: 8 },
  { codec: "hevc", profile: "Main 10", cs: "hvc1.2.4.L153.B0", level: 153, depth: 10 },
  { codec: "vp9", profile: "Profile 0", cs: "vp09.00.50.08", level: 0, depth: 8 },
  { codec: "vp9", profile: "Profile 2", cs: "vp09.02.50.10", level: 0, depth: 10 },
  { codec: "av1", profile: "Main", cs: "av01.0.13M.10", level: 0, depth: 10 },
] as const;
const SIZES = [
  [3840, 2160],
  [1920, 1080],
  [1280, 720],
] as const;
const AUDIO: Record<string, string> = {
  aac: 'audio/mp4; codecs="mp4a.40.2"',
  ac3: 'audio/mp4; codecs="ac-3"',
  eac3: 'audio/mp4; codecs="ec-3"',
  flac: 'audio/mp4; codecs="flac"',
  opus: 'audio/webm; codecs="opus"',
  mp3: "audio/mpeg",
  vorbis: 'audio/webm; codecs="vorbis"',
  alac: 'audio/mp4; codecs="alac"',
};
const CONTAINERS: Record<string, string> = {
  mp4: "video/mp4",
  webm: "video/webm",
  mkv: "video/x-matroska",
  flac: "audio/flac",
  mp3: "audio/mpeg",
};
export const HLS_MIME = "application/vnd.apple.mpegurl";

export async function decoder(env: Env): Promise<Schemas["DecoderCapabilities"]> {
  const found = new Map<string, Schemas["VideoDecoder"]>();
  const hdrDisplay = env.matches("(dynamic-range: high)");
  for (const p of PROBES) {
    for (const [w, h] of SIZES) {
      const info = await env
        .decodingInfo({
          type: "file",
          video: {
            contentType: `video/mp4; codecs="${p.cs}"`,
            width: w,
            height: h,
            bitrate: 20_000_000,
            framerate: 60,
          },
        })
        .catch(() => undefined);
      if (!info?.supported) continue;
      const d = found.get(p.codec) ?? {
        codec: p.codec,
        profiles: [],
        maxLevel: 0,
        // shortcut: powerEfficient is the only hardware signal MediaCapabilities gives.
        hardware: false,
        maxWidth: 1,
        maxHeight: 1,
        maxFrameRate: 30,
        maxBitDepth: 8,
        hdrFormats: [] as Hdr[],
      };
      d.profiles!.push(p.profile);
      d.maxLevel = Math.max(d.maxLevel!, p.level) || undefined;
      d.hardware ||= info.powerEfficient;
      d.maxWidth = Math.max(d.maxWidth, w);
      d.maxHeight = Math.max(d.maxHeight, h);
      d.maxFrameRate = info.smooth ? 60 : Math.max(d.maxFrameRate, 30);
      d.maxBitDepth = Math.max(d.maxBitDepth, p.depth);
      // HDR10 output needs a 10-bit decode and an HDR display.
      if (p.depth >= 10 && hdrDisplay && !d.hdrFormats!.includes("hdr10"))
        d.hdrFormats!.push("hdr10");
      found.set(p.codec, d);
      break; // largest size first: the first hit is this profile's ceiling
    }
  }
  // The h264 profile set is deduplicated: Baseline and Constrained Baseline share a probe.
  const videoCodecs = [...found.values()].map((d) => ({
    ...d,
    profiles: [...new Set(d.profiles)],
  }));
  const ok = (m: string) => env.canPlayType(m) !== "";
  return {
    protocols: ["http", ...(env.hlsViaLibrary || ok(HLS_MIME) ? (["hls"] as const) : [])],
    containers: Object.keys(CONTAINERS).filter((k) => ok(CONTAINERS[k])),
    videoCodecs,
    audioCodecs: Object.keys(AUDIO).filter((k) => ok(AUDIO[k])),
    // Sidecars are drawn through <track>, which only reads WebVTT; the player converts SRT.
    subtitleFormats: ["webvtt", "srt"],
  };
}

export function display(env: Env): Schemas["DisplayCapabilities"] {
  return {
    width: Math.max(1, Math.round(env.screen.width * env.pixelRatio)),
    height: Math.max(1, Math.round(env.screen.height * env.pixelRatio)),
    // No browser API reports the refresh rate; 60 is the common floor.
    refreshRate: 60,
    // The platform exposes only HDR yes or no; HDR10 is the format every HDR panel takes.
    hdrFormats: env.matches("(dynamic-range: high)") ? ["hdr10"] : [],
  };
}

export function audioRoute(env: Env): Schemas["AudioRouteCapabilities"] {
  // Browsers never expose passthrough, so every codec is decoded to PCM.
  return { channels: Math.min(8, Math.max(1, env.audioChannels())), passthroughCodecs: [] };
}

/** The viewer's pick: Auto, Original (no cap) or a rung's bitrate as the cap. */
export type Quality = { auto: true } | { auto?: false; maxBitrate?: number };
/** Sent until the viewer picks Original or a rung. */
export const AUTO: Quality = { auto: true };

// Whole-host match: loopback, RFC 1918 and link-local literals (IPv6 as bracketed by
// location.hostname), single-label names, and the suffixes reserved for home networks.
const PRIVATE =
  /^(localhost|(127|10)(\.\d{1,3}){3}|192\.168(\.\d{1,3}){2}|169\.254(\.\d{1,3}){2}|172\.(1[6-9]|2\d|3[01])(\.\d{1,3}){2}|\[(::1|f[cd][\da-f]{2}:.*|fe[89ab][\da-f]:.*)\]|[\w-]+|.+\.(local|lan|home\.arpa|internal))\.?$/i;
export function network(env: Env, q: Quality = AUTO): Schemas["NetworkPathCapabilities"] {
  return {
    // The page was fetched from the server, so a private address stands in for local discovery.
    discoveredLocally: PRIVATE.test(env.hostname),
    metered: env.connection?.type === "cellular",
    lowData: env.connection?.saveData === true,
    // A VPN is invisible to a page.
    vpn: false,
    // auto is always sent: the server treats a snapshot without it as a v1 client.
    ...(q.auto
      ? { auto: true }
      : { auto: false, ...(q.maxBitrate ? { maxBitrate: q.maxBitrate } : {}) }),
  };
}

export async function snapshot(env: Env, q?: Quality): Promise<Snapshot> {
  return {
    decoder: await decoder(env),
    display: display(env),
    audioRoute: audioRoute(env),
    network: network(env, q),
  };
}

type NavConn = EventTarget & { type?: string; saveData?: boolean };
export function browserEnv(hlsViaLibrary: boolean): Env {
  const video = document.createElement("video");
  const conn = (navigator as unknown as { connection?: NavConn }).connection;
  return {
    decodingInfo: (cfg) =>
      navigator.mediaCapabilities.decodingInfo(cfg as MediaDecodingConfiguration),
    canPlayType: (m) => (m.startsWith("audio/") ? new Audio() : video).canPlayType(m),
    hlsViaLibrary,
    // Read when asked: the window may since have moved to another display.
    get screen() {
      return { width: screen.width, height: screen.height };
    },
    get pixelRatio() {
      return window.devicePixelRatio || 1;
    },
    matches: (q) => window.matchMedia(q).matches,
    connection: conn,
    hostname: location.hostname,
    audioChannels: () => {
      try {
        const ctx = new AudioContext();
        const n = ctx.destination.maxChannelCount;
        void ctx.close();
        return n || 2;
      } catch {
        return 2;
      }
    },
  };
}

export interface Groups {
  display: Schemas["DisplayCapabilities"];
  audioRoute: Schemas["AudioRouteCapabilities"];
  network: Schemas["NetworkPathCapabilities"];
}
type Changed = Partial<Groups>;

/**
 * Calls onChange with only the groups whose value differs after a browser
 * event. Returns the unsubscribe.
 */
export function watchGroups(
  env: Env,
  getQuality: () => Quality,
  onChange: (c: Changed) => void,
  target: { add(ev: string, fn: () => void): void; remove(ev: string, fn: () => void): void },
): () => void {
  const read = (): Groups => ({
    display: display(env),
    audioRoute: audioRoute(env),
    network: network(env, getQuality()),
  });
  let last = read();
  const check = () => {
    const now = read();
    const c: Changed = {};
    for (const k of ["display", "audioRoute", "network"] as const)
      if (JSON.stringify(now[k]) !== JSON.stringify(last[k]))
        (c as Record<string, unknown>)[k] = now[k];
    last = now;
    if (Object.keys(c).length) onChange(c);
  };
  const events = ["resize", "dynamic-range", "connection", "devicechange"];
  events.forEach((e) => target.add(e, check));
  return () => events.forEach((e) => target.remove(e, check));
}

export function browserTarget() {
  const conn = (navigator as unknown as { connection?: EventTarget }).connection;
  const hdr = window.matchMedia("(dynamic-range: high)");
  const map: Record<string, EventTarget | undefined> = {
    resize: window,
    "dynamic-range": hdr,
    connection: conn,
    devicechange: navigator.mediaDevices,
  };
  const name = (e: string) =>
    e === "dynamic-range" ? "change" : e === "connection" ? "change" : e;
  return {
    add: (e: string, fn: () => void) => map[e]?.addEventListener(name(e), fn),
    remove: (e: string, fn: () => void) => map[e]?.removeEventListener(name(e), fn),
  };
}
