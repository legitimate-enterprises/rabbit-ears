import type { Schemas } from "../api/client";

export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "Never";
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 60) return "Just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

/** Whole years from a YYYY-MM-DD birthday, by the calendar. */
export function age(birthday: string, now = new Date()): number {
  const [y, m, d] = birthday.split("-").map(Number);
  const before = now.getMonth() + 1 < m || (now.getMonth() + 1 === m && now.getDate() < d);
  return now.getFullYear() - y - (before ? 1 : 0);
}

export const local = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "-");

export function hour12(h: number): string {
  return `${h % 12 || 12}:00 ${h < 12 ? "AM" : "PM"}`;
}

export function every(sec?: number): string {
  if (!sec) return "On demand";
  if (sec % 86400 === 0) return sec === 86400 ? "Daily" : `Every ${sec / 86400} days`;
  if (sec % 3600 === 0) return sec === 3600 ? "Hourly" : `Every ${sec / 3600} h`;
  return `Every ${Math.round(sec / 60)} min`;
}

const UNITS = ["B", "KB", "MB", "GB", "TB"];
const unitOf = (n: number) => {
  let i = 0;
  while (n >= 1000 ** (i + 1) && i < UNITS.length - 1) i++;
  return i;
};
const scaled = (n: number, i: number) => {
  const v = n / 1000 ** i;
  return i === 0 || v >= 100 ? String(Math.round(v)) : v.toFixed(1).replace(/\.0$/, "");
};

/** Decimal units, as Finder shows them. */
export const bytes = (n: number) => `${scaled(n, unitOf(n))} ${UNITS[unitOf(n)]}`;

/** ["3.1", " of 16 GB"]: the used part in the total's unit, split for a gauge. */
export function share(used: number, total: number): [string, string] {
  const i = unitOf(total);
  return [scaled(used, i), ` of ${scaled(total, i)} ${UNITS[i]}`];
}

const count = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "3 movies · 4.1 GB · 1 unmatched"; TV leads with shows, seasons and episodes. */
export function libraryStats(kind: string, s: Schemas["LibraryStats"]): string {
  const parts =
    kind === "music"
      ? [
          count(s.artists ?? 0, "artist"),
          count(s.albums ?? 0, "album"),
          count(s.songs ?? 0, "song"),
        ]
      : kind === "youtube"
        ? [count(s.channels ?? 0, "channel"), count(s.videos ?? 0, "video")]
        : kind === "tv"
          ? [count(s.shows, "show"), count(s.seasons, "season"), count(s.episodes, "episode")]
          : [count(s.movies, "movie")];
  parts.push(bytes(s.bytes));
  if (s.unmatched) parts.push(`${s.unmatched} unmatched`);
  return parts.join(" · ");
}
