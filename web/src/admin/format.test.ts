import { describe, expect, it } from "vitest";
import { age } from "./format";

describe("age", () => {
  it("counts whole years by the calendar, so the birthday itself is the new age", () => {
    expect(age("2020-06-02", new Date(2026, 5, 1, 23, 59))).toBe(5);
    expect(age("2020-06-02", new Date(2026, 5, 2, 0, 0))).toBe(6);
    expect(age("2020-02-29", new Date(2026, 1, 28))).toBe(5);
    expect(age("2020-02-29", new Date(2026, 2, 1))).toBe(6);
  });
});
