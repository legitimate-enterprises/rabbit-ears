import { describe, expect, it } from "vitest";
import { chapterAt, scrub } from "./preview";

const frames = {
  intervalMs: 10_000,
  count: 12,
  width: 320,
  height: 180,
  frameUrl: "/api/v1/streams/k/0/previews/{n}.jpg",
  bifUrl: "/api/v1/streams/k/0/previews.bif",
};
const bar = { left: 100, width: 1000 };
const DURATION = 115_000;

describe("scrub", () => {
  it("picks the frame nearest the pointer's time", () => {
    const s = scrub(frames, 100 + 500, bar, DURATION, 260);
    expect(s.ms).toBe(57_500);
    expect(s.index).toBe(6);
    expect(s.url).toBe("/api/v1/streams/k/0/previews/6.jpg");
  });
  it("clamps to the first and last frame", () => {
    expect(scrub(frames, 0, bar, DURATION, 260).index).toBe(0);
    expect(scrub(frames, 5000, bar, DURATION, 260)).toMatchObject({ index: 11, ms: DURATION });
  });
  it("keeps the card inside the bar", () => {
    expect(scrub(frames, 100, bar, DURATION, 260).left).toBe(130);
    expect(scrub(frames, 1100, bar, DURATION, 260).left).toBe(870);
    expect(scrub(frames, 600, bar, DURATION, 260).left).toBe(500);
  });
  it("survives a bar with no width", () => {
    expect(scrub(frames, 50, { left: 0, width: 0 }, DURATION, 260)).toMatchObject({
      index: 0,
      ms: 0,
    });
  });
});

describe("chapterAt", () => {
  const chapters = [
    { id: 1, position: 0, startMs: 0, endMs: 60_000, title: "Cold open" },
    { id: 2, position: 1, startMs: 60_000, endMs: 120_000 },
  ];
  it("names the chapter that holds the time, numbering untitled ones", () => {
    expect(chapterAt(chapters, 30_000)).toBe("Cold open");
    expect(chapterAt(chapters, 60_000)).toBe("Chapter 2");
    expect(chapterAt([], 1)).toBeUndefined();
  });
});
