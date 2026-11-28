import { describe, expect, it } from "vitest";
import { washBackground } from "./wash";

const christmas = ["#1F6B45", "#B3263E", "#E0B252"];
const glow = (at: string, r: string, rgb: string, stops: [number, number][]) =>
  `radial-gradient(circle ${r} at ${at}, ${stops.map(([a, p]) => `rgba(${rgb}, ${a}) ${p}%`).join(", ")})`;

describe("washBackground", () => {
  it("brightens each color and glows it from its corner of a triangle pointing down, color 2 on top", () => {
    const bg = washBackground(christmas, {
      strength: 200,
      brightness: 15,
      sharpness: 0,
      reach: 100,
    });
    expect(bg).toBe(
      [
        glow("50% 100%", "max(80vw, 50vh)", "190, 71, 91", [
          [0.6, 0],
          [0.6, 0],
          [0.24, 50],
          [0, 100],
        ]),
        glow("0% 0%", "max(80vw, 50vh)", "65, 129, 97", [
          [0.48, 0],
          [0.48, 0],
          [0.192, 50],
          [0, 100],
        ]),
        glow("100% 0%", "max(80vw, 50vh)", "229, 190, 108", [
          [0.48, 0],
          [0.48, 0],
          [0.192, 50],
          [0, 100],
        ]),
      ].join(", "),
    );
  });

  it("caps the opacity at 1, holds it to the sharpness, and scales the radius by the reach", () => {
    const bg = washBackground(
      christmas,
      { strength: 400, brightness: 0, sharpness: 40, reach: 140 },
      ["cqw", "cqh"],
    );
    expect(bg!.split(", radial-gradient")[0]).toBe(
      glow("50% 100%", "max(112cqw, 70cqh)", "179, 38, 62", [
        [1, 0],
        [1, 40],
        [0.4, 70],
        [0, 100],
      ]),
    );
    expect(bg).toContain(
      glow("0% 0%", "max(112cqw, 70cqh)", "31, 107, 69", [
        [0.96, 0],
        [0.96, 40],
        [0.384, 70],
        [0, 100],
      ]),
    );
  });

  it("draws nothing without exactly three valid colors", () => {
    const wash = { strength: 200, brightness: 15, sharpness: 0, reach: 100 };
    expect(washBackground(undefined, wash)).toBeUndefined();
    expect(washBackground([], wash)).toBeUndefined();
    expect(washBackground(christmas.slice(0, 2), wash)).toBeUndefined();
    expect(washBackground(["#1F6B45", "red", "#E0B252"], wash)).toBeUndefined();
  });
});
