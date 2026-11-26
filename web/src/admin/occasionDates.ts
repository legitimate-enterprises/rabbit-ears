// An occasion's calendar, in this browser's local days. The server stores no instants for it; each
// client reads the window against its own today.
import type { Schemas } from "../api/client";

type Holiday = Schemas["Holiday"];
export type Anchor = { holiday?: string; month?: number; day?: number };
export type Window = { anchor: Date; first: Date; last: Date };
export type Season = { window?: Window; active: boolean };

// Feb 29 recurs within 8 years; the scan looks that far for an anchor that has a date.
const YEARS_AHEAD = 8;

export const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
const shift = (d: Date, days: number) =>
  new Date(d.getFullYear(), d.getMonth(), d.getDate() + days);

/** The anchor's date in a year: none for a holiday the server has no date for, or Feb 29 in a common year. */
export function anchorDate(a: Anchor, holidays: Holiday[], year: number): Date | undefined {
  if (a.holiday !== undefined) {
    const s = holidays
      .find((h) => h.key === a.holiday)
      ?.dates.find((x) => x.startsWith(`${year}-`));
    if (!s) return undefined;
    const [y, m, d] = s.split("-").map(Number);
    return new Date(y, m - 1, d);
  }
  if (!a.month || !a.day) return undefined;
  const d = new Date(year, a.month - 1, a.day);
  return d.getMonth() === a.month - 1 ? d : undefined;
}

export function windowFor(
  o: Anchor & { daysBefore: number; daysAfter: number },
  holidays: Holiday[],
  year: number,
): Window | undefined {
  const anchor = anchorDate(o, holidays, year);
  return (
    anchor && { anchor, first: shift(anchor, -o.daysBefore), last: shift(anchor, o.daysAfter) }
  );
}

/**
 * The window of today's activation if today is inside one, else the next to start. The scan
 * begins a year back so a window that crosses New Year is active on its January side too.
 */
export function season(
  o: Anchor & { daysBefore: number; daysAfter: number },
  holidays: Holiday[],
  today: Date,
): Season {
  const t = startOfDay(today).getTime();
  const year = today.getFullYear();
  for (let y = year - 1; y <= year + YEARS_AHEAD; y++) {
    const window = windowFor(o, holidays, y);
    if (window && window.last.getTime() >= t)
      return { window, active: window.first.getTime() <= t };
  }
  return { active: false };
}

const month = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric" });
const withYear = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  year: "numeric",
});
const weekday = new Intl.DateTimeFormat("en-US", {
  weekday: "short",
  month: "short",
  day: "numeric",
});
const weekdayYear = new Intl.DateTimeFormat("en-US", {
  weekday: "short",
  month: "short",
  day: "numeric",
  year: "numeric",
});

/** A date beyond next year, such as the next Feb 29, says its year. */
export const far = (d: Date, today: Date) => d.getFullYear() > today.getFullYear() + 1;

export const shortDate = (d: Date, today: Date) => (far(d, today) ? withYear : month).format(d);

export const range = (w: Window, today: Date) =>
  `${shortDate(w.first, today)} – ${shortDate(w.last, today)}`;

export const longDate = (d: Date, year = false) => (year ? weekdayYear : weekday).format(d);

const days = (n: number) => `${n} ${n === 1 ? "day" : "days"}`;
export const describeWindow = (before: number, after: number) =>
  `${days(before)} before, ${after} after`;
