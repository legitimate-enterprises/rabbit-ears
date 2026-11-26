import type { Schemas } from "../api/client";

export const runtime = (min?: number) =>
  !min
    ? ""
    : min < 60
      ? `${min}m`
      : min % 60
        ? `${Math.floor(min / 60)}h ${min % 60}m`
        : `${min / 60}h`;

/** runtime() of a duration in milliseconds. */
export const minutes = (ms = 0) => runtime(Math.round(ms / 60000));

export const clock = (sec: number) => {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
};

export const left = (totalMin: number | undefined, positionMs: number) =>
  totalMin ? runtime(Math.max(1, Math.round(totalMin - positionMs / 60000))) + " left" : "";

export const plural = (n: number, one: string, many = one + "s") =>
  `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** "a moment ago", "5 minutes ago", "3 hours ago". */
export const ago = (iso: string, now = Date.now()) => {
  const min = Math.floor((now - Date.parse(iso)) / 60000);
  return min < 1
    ? "a moment ago"
    : min < 60
      ? `${plural(min, "minute")} ago`
      : `${plural(Math.floor(min / 60), "hour")} ago`;
};

/** "today", "yesterday", "5 days ago", "last week", "last month" for a publish date (YYYY-MM-DD, UTC). */
export const since = (date: string, now = Date.now()) => {
  const d = Math.floor(now / 864e5) - Math.floor(Date.parse(date) / 864e5);
  if (d <= 0) return "today";
  if (d === 1) return "yesterday";
  if (d < 7) return `${d} days ago`;
  if (d < 14) return "last week";
  if (d < 30) return `${Math.floor(d / 7)} weeks ago`;
  if (d < 60) return "last month";
  if (d < 365) return `${Math.floor(d / 30)} months ago`;
  const y = Math.floor(d / 365);
  return y === 1 ? "last year" : `${y} years ago`;
};

/** m:ss for a countdown in milliseconds, never negative. */
export const countdown = (ms: number) => clock(Math.ceil(Math.max(0, ms) / 1000));

const names = new Intl.DisplayNames(["en"], { type: "language", fallback: "none" });
/** English name of a probed language code, any region dropped; "Unknown" when missing or und. */
const language = (code?: string) => {
  const base = code?.split(/[-_]/)[0];
  if (!base || base.toLowerCase() === "und") return "Unknown";
  try {
    return names.of(base) ?? base.toUpperCase();
  } catch {
    return base.toUpperCase();
  }
};
const english = (t: Schemas["Track"]) => /^(eng|en)([-_]|$)/i.test(t.language ?? "");
const label = (t: Schemas["Track"]) => {
  const n = language(t.language);
  const named = !t.title
    ? n
    : t.title.toLowerCase().includes(n.toLowerCase())
      ? t.title
      : `${n} ${t.title}`;
  return t.isForced ? `${named} (forced)` : named;
};
const labels = (ts: Schemas["Track"][]) => [...new Set(ts.map(label))].join(", ");

/**
 * The details Subtitles line, the same on every client: each track labeled, or past three
 * tracks the English ones and a count of the other languages.
 */
export const subtitles = (tracks: Schemas["Track"][]) => {
  const subs = tracks.filter((t) => t.kind === "subtitle");
  if (subs.length <= 3) return labels(subs);
  const en = subs.filter(english);
  const others = new Set(subs.filter((t) => !english(t)).map((t) => language(t.language))).size;
  if (!en.length) return others === 1 ? labels(subs) : plural(others, "language");
  return others ? `${labels(en)}, +${plural(others, "other language")}` : labels(en);
};

export const VERDICT = {
  direct_play: "Direct play",
  remux: "Remux",
  audio_transcode: "Audio transcode",
  video_transcode: "Video transcode",
} as const;

export interface DetailRow {
  label: string;
  value: string;
  lossless?: boolean;
  tone?: "ok" | "warn";
}

const kbps = (bps: number) => `${Math.round(bps / 1000).toLocaleString("en-US")} kb/s`;
const channels = (n: number) => (n === 1 ? "Mono" : n === 2 ? "Stereo" : `${n} channels`);

/** The song details panel: the source file and what is delivered, a row left out when absent. */
export function audioDetails({ source: s, delivered: d }: Schemas["AudioDetails"]) {
  const row = <T>(
    label: string,
    v: T | undefined,
    show: (v: T) => string,
    more = {},
  ): DetailRow[] => (v === undefined ? [] : [{ label, value: show(v), ...more }]);
  const groups: { title: string; rows: DetailRow[] }[] = [
    {
      title: "Source",
      rows: [
        ...row("Format", s.codec, (c) => c.toUpperCase(), s.lossless ? { lossless: true } : {}),
        ...row("Bitrate", s.bitrate, kbps),
        ...row("Sample rate", s.sampleRateHz, (hz) => `${hz / 1000} kHz`),
        ...row("Bit depth", s.bitDepth, (b) => `${b}-bit`),
        ...row("Channels", s.channels, channels),
        ...row("Size", s.sizeBytes, (b) => `${(b / 1e6).toFixed(1)} MB`),
      ],
    },
    {
      title: "Delivered",
      rows: [
        ...row("Playback", d.verdict, (v) => VERDICT[v], {
          tone: d.verdict === "direct_play" || d.verdict === "remux" ? "ok" : "warn",
        }),
        ...row("Codec", d.codec, (c) => c.toUpperCase()),
        ...row("Bitrate", d.bitrate, kbps),
      ],
    },
  ];
  return groups.filter((g) => g.rows.length);
}

export const KIND: Record<Schemas["PlaylistKind"], string> = {
  movies: "Movies",
  tv: "TV",
  music: "Music",
};
export const TIMING: Record<Schemas["StationTiming"], string> = {
  continuous: "Back to back",
  half_hour: "On the hour or half hour",
  hour: "On the hour",
};
export const VIS: Record<Schemas["StationVisibility"], string> = {
  private: "Private",
  household: "Household",
  server: "Server",
};
