import { describe, expect, it } from "vitest";
import {
  anchorDate,
  describeWindow,
  longDate,
  range,
  season,
  shortDate,
  startOfDay,
  windowFor,
} from "./occasionDates";

const d = (y: number, m: number, day: number) => new Date(y, m - 1, day);
const holidays = [
  { key: "christmas", name: "Christmas", dates: ["2026-12-25", "2027-12-25", "2028-12-25"] },
  { key: "nye", name: "New Year's Eve", dates: ["2025-12-31", "2026-12-31", "2027-12-31"] },
  { key: "thanksgiving", name: "Thanksgiving (US)", dates: ["2026-11-26"] },
];
const christmas = { holiday: "christmas", daysBefore: 40, daysAfter: 5 };

describe("anchorDate", () => {
  it("takes a holiday's date in the year, and none when the server has none", () => {
    expect(anchorDate({ holiday: "christmas" }, holidays, 2027)).toEqual(d(2027, 12, 25));
    expect(anchorDate({ holiday: "thanksgiving" }, holidays, 2027)).toBeUndefined();
    expect(anchorDate({ holiday: "unknown" }, holidays, 2027)).toBeUndefined();
  });

  it("takes a fixed month and day, and no Feb 29 in a common year", () => {
    expect(anchorDate({ month: 10, day: 31 }, holidays, 2026)).toEqual(d(2026, 10, 31));
    expect(anchorDate({ month: 2, day: 29 }, holidays, 2028)).toEqual(d(2028, 2, 29));
    expect(anchorDate({ month: 2, day: 29 }, holidays, 2027)).toBeUndefined();
  });
});

describe("windowFor", () => {
  it("runs from daysBefore before the anchor through daysAfter after it", () => {
    expect(windowFor(christmas, holidays, 2026)).toEqual({
      anchor: d(2026, 12, 25),
      first: d(2026, 11, 15),
      last: d(2026, 12, 30),
    });
  });
});

describe("season", () => {
  it("is active on the first and last day of the window, not the days around it", () => {
    expect(season(christmas, holidays, d(2026, 11, 14)).active).toBe(false);
    expect(season(christmas, holidays, d(2026, 11, 15)).active).toBe(true);
    expect(season(christmas, holidays, d(2026, 12, 30)).active).toBe(true);
    expect(season(christmas, holidays, d(2026, 12, 31)).active).toBe(false);
  });

  it("names the window it is in, else the next one to start", () => {
    expect(season(christmas, holidays, d(2026, 11, 22)).window?.first).toEqual(d(2026, 11, 15));
    const after = season(christmas, holidays, d(2026, 12, 31));
    expect(after.active).toBe(false);
    expect(after.window?.first).toEqual(d(2027, 11, 15));
  });

  it("is active on both sides of New Year for a fixed anchor", () => {
    const o = { month: 1, day: 5, daysBefore: 10, daysAfter: 10 };
    expect(season(o, [], d(2026, 12, 26)).active).toBe(true);
    expect(season(o, [], d(2026, 12, 25)).active).toBe(false);
    expect(season(o, [], d(2027, 1, 15)).active).toBe(true);
    expect(season(o, [], d(2027, 1, 16)).active).toBe(false);
    expect(season(o, [], d(2027, 1, 2)).window).toEqual({
      anchor: d(2027, 1, 5),
      first: d(2026, 12, 26),
      last: d(2027, 1, 15),
    });
  });

  it("is active on both sides of New Year for a holiday", () => {
    const o = { holiday: "nye", daysBefore: 10, daysAfter: 10 };
    expect(season(o, holidays, d(2026, 12, 25)).active).toBe(true);
    // The window of the 2025 anchor, reaching into 2026.
    expect(season(o, holidays, d(2026, 1, 10)).active).toBe(true);
    expect(season(o, holidays, d(2026, 1, 11)).active).toBe(false);
    expect(season(o, holidays, d(2027, 1, 10)).active).toBe(true);
  });

  it("holds Feb 29 to leap years", () => {
    const o = { month: 2, day: 29, daysBefore: 2, daysAfter: 2 };
    expect(season(o, [], d(2028, 2, 28)).active).toBe(true);
    expect(season(o, [], d(2028, 3, 2)).active).toBe(true);
    expect(season(o, [], d(2027, 2, 28)).active).toBe(false);
    expect(season(o, [], d(2027, 2, 28)).window?.anchor).toEqual(d(2028, 2, 29));
  });

  it("is not active, and has no window, around a holiday the server has no date for", () => {
    const o = { holiday: "thanksgiving", daysBefore: 7, daysAfter: 1 };
    expect(season(o, holidays, d(2026, 11, 22))).toEqual({
      window: { anchor: d(2026, 11, 26), first: d(2026, 11, 19), last: d(2026, 11, 27) },
      active: true,
    });
    expect(season(o, holidays, d(2027, 11, 22))).toEqual({ window: undefined, active: false });
  });

  it("rolls to next year once this year's window has passed", () => {
    const o = { month: 1, day: 15, daysBefore: 14, daysAfter: 30 };
    const s = season(o, [], d(2026, 11, 22));
    expect(s.active).toBe(false);
    expect(s.window?.first).toEqual(d(2027, 1, 1));
  });

  it("ignores the time of day", () => {
    expect(season(christmas, holidays, new Date(2026, 11, 30, 23, 59)).active).toBe(true);
    expect(startOfDay(new Date(2026, 11, 30, 23, 59))).toEqual(d(2026, 12, 30));
  });
});

describe("formatting", () => {
  const today = d(2026, 11, 22);
  it("shows month and day, adding the year only for a date beyond next year", () => {
    expect(shortDate(d(2026, 11, 15), today)).toBe("Nov 15");
    expect(shortDate(d(2027, 1, 1), today)).toBe("Jan 1");
    expect(shortDate(d(2028, 2, 27), today)).toBe("Feb 27, 2028");
  });

  it("writes a range with an en dash", () => {
    const w = windowFor(christmas, holidays, 2026)!;
    expect(range(w, today)).toBe("Nov 15 – Dec 30");
  });

  it("writes a long date with weekday and optionally the year", () => {
    expect(longDate(d(2026, 12, 25))).toBe("Fri, Dec 25");
    expect(longDate(d(2026, 12, 25), true)).toBe("Fri, Dec 25, 2026");
  });

  it("says the window in days", () => {
    expect(describeWindow(40, 5)).toBe("40 days before, 5 after");
    expect(describeWindow(1, 0)).toBe("1 day before, 0 after");
  });
});
