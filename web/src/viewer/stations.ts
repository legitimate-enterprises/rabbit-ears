// What the viewer derives from a station. The server decides the order, the
// schedule and what is on; nothing here computes any of it.
import { ApiError, ok } from "../admin/api";
import type { Api, Schemas } from "../api/client";
import { artKey, artOf } from "./artwork";
import type { Snapshot } from "./capabilities";
import { imageKey } from "./coverImage";
import { epCode, title } from "./data";
import { named } from "./editions";
import { KIND } from "./format";
import { Playback } from "./playback";

export type Station = Schemas["Station"];
export type OnAir = Schemas["StationOnAir"];
export type Program = Schemas["StationProgram"];
export type Weekday = Schemas["Weekday"];

export const programTitle = (p: Program): string =>
  p.song
    ? p.song.title
    : p.show && p.item
      ? `${title(p.show)} · ${epCode(p.item)}`
      : named(p.item ? title(p.item) : "", p.editionName);

/** "4:00 PM" in the browser's locale. */
export const clockAt = (iso: string) =>
  new Date(iso).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });

/** "today at 9:00 PM", else "Friday at 10:00 PM". */
const dayAt = (iso: string, now: number) => {
  const d = new Date(iso);
  const day =
    d.toDateString() === new Date(now).toDateString()
      ? "today"
      : d.toLocaleDateString(undefined, { weekday: "long" });
  return `${day} at ${clockAt(iso)}`;
};

/** What a tile or the off-air screen says, one line each. */
export function caption(s: Pick<Station, "kind" | "onAir">, now = Date.now()): string[] {
  const { status, now: on, next } = s.onAir;
  const nextLine =
    next &&
    `Next: ${programTitle(next)}${s.kind === "music" ? "" : ` at ${clockAt(next.startsAt)}`}`;
  if (status === "on_air" && on)
    return [`On now: ${programTitle(on)}`, ...(nextLine ? [nextLine] : [])];
  if (!next) return ["Nothing scheduled"];
  return [
    status === "off_schedule"
      ? `Back ${dayAt(next.startsAt, now)}`
      : `Up next at ${clockAt(next.startsAt)}: ${programTitle(next)}`,
  ];
}

/** Home's On now tile names a station with its kind: "Movies: Marvel". */
export const onNowName = (s: Pick<Station, "kind" | "name">) => `${KIND[s.kind]}: ${s.name}`;

/** How far through the program on, 0 to 1, as the server answered; none without a program or length. */
export function onNowFraction(a: OnAir): number | undefined {
  const length = a.now?.durationMs;
  return a.positionMs === undefined || !length || length <= 0
    ? undefined
    : Math.min(1, a.positionMs / length);
}

/** The data-art key of what is on, else of what is next; none shows the station's own tile. */
export function artFor(a: OnAir): string | undefined {
  const p = a.now ?? a.next;
  if (p?.song) return artOf(p.song.posterUrl);
  return p?.item ? artKey(p.item, true) : undefined;
}

/** The data-art key of the station's own image, else artFor's. */
export const stationKey = (s: Pick<Station, "imageUrl" | "onAir">) =>
  s.imageUrl ? imageKey(s.imageUrl) : artFor(s.onAir);

// The server's clock less the browser's, as of the last tuning in.
let skew = 0;

const serverNow = () => Date.now() + skew;

/**
 * Milliseconds from now, by the server's clock, to iso; undefined without an instant. Never
 * under a second, so an instant just past cannot ask the server again and again.
 */
export const untilMs = (iso: string | undefined, now = serverNow()) =>
  iso === undefined ? undefined : Math.max(1000, Date.parse(iso) - now);

/** How far through a program the server's clock is, 0 to 1; 0 with none on. */
export const progress = (p: Program | undefined, now = serverNow()) => {
  if (!p) return 0;
  const start = Date.parse(p.startsAt);
  const length = Date.parse(p.endsAt) - start;
  return length > 0 ? Math.min(1, Math.max(0, (now - start) / length)) : 0;
};

/** YYYY-MM-DD of the browser's day. */
export const localDate = (now = Date.now()) => {
  const d = new Date(now);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

export type Tuned =
  | { state: "on_air"; pb: Playback; onAir: OnAir }
  | { state: "between_programs" | "off_schedule"; onAir: OnAir; resumesAt?: string };

/**
 * Tunes in to a video station: a session on its live stream, whose onAir has no now while an
 * ident plays, or, when the station is off the air, what it shows instead and when to ask again.
 */
export async function tuneIn(
  api: Api,
  stationId: string,
  snapshot: () => Promise<Snapshot>,
): Promise<Tuned> {
  const pb = new Playback(api, { stationId, snapshot, live: true });
  try {
    await pb.start();
    const { now, positionMs } = pb.onAir!;
    // The live position is how far the server's clock was into the program when it answered.
    if (now && positionMs !== undefined) skew = Date.parse(now.startsAt) + positionMs - Date.now();
    return { state: "on_air", pb, onAir: pb.onAir! };
  } catch (e) {
    if (!(e instanceof ApiError && e.code === "off_air")) throw e;
    const r = await api.GET("/stations/{stationId}", { params: { path: { stationId } } });
    const date = Date.parse(r.response.headers.get("Date") ?? "");
    if (date) skew = date - Date.now();
    const { onAir } = await ok(Promise.resolve(r));
    // A program that started since the refusal is next, joined by the call at resumesAt.
    if (onAir.status === "on_air")
      return {
        state: "between_programs",
        onAir: {
          status: "between_programs",
          next: onAir.now,
          upNext: [onAir.now!, ...onAir.upNext].slice(0, 10),
        },
        resumesAt: e.resumesAt,
      };
    return { state: onAir.status, onAir, resumesAt: e.resumesAt };
  }
}

interface Form {
  name: string;
  kind: Schemas["PlaylistKind"];
  order: Schemas["StationOrder"];
  visibility: Schemas["StationVisibility"];
  timing: Schemas["StationTiming"];
  /** Chosen days and times; false is continuous. */
  chosen: boolean;
  days: Weekday[];
  times: { from: string; to: string }[];
}

/** A new station's timing: the hour for movies, the half hour for tv, back to back for music. */
export const timingOf = (kind: Schemas["PlaylistKind"]): Schemas["StationTiming"] =>
  kind === "movies" ? "hour" : kind === "tv" ? "half_hour" : "continuous";

export const formOf = (s?: Station, kind: Schemas["PlaylistKind"] = "movies"): Form => ({
  name: s?.name ?? "",
  kind: s?.kind ?? kind,
  order: s?.order ?? "playlist",
  visibility: s?.visibility ?? "private",
  timing: s?.timing ?? timingOf(s?.kind ?? kind),
  chosen: !!s && (s.days.length > 0 || s.times.length > 0),
  days: [...(s?.days ?? [])],
  times: (s?.times ?? []).map((t) => ({ ...t })),
});

export function inputOf(f: Form): Schemas["StationInput"] {
  return {
    name: f.name.trim(),
    // A station keeps the playlist it has; a new one gets its own of this kind.
    kind: f.kind,
    order: f.order,
    // A server station's visibility is the admin's; leaving it out keeps it.
    ...(f.visibility === "server" ? {} : { visibility: f.visibility }),
    timing: f.timing,
    days: f.chosen ? f.days : [],
    times: f.chosen ? f.times.filter((t) => t.from && t.to) : [],
  };
}

/** The form field a 400's field names: "times[0].to" is times. */
export const fieldOf = (f: string) => f.replace(/^\W+/, "").split(/\W/)[0];
