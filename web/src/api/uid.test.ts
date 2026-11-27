import { describe, expect, it } from "vitest";
import { tint, uid } from "./uid";
import { color, PALETTE } from "../viewer/data";

// The server and the Apple apps pick by an id's last byte; so must the web.
describe("tint", () => {
  it.each([0, 1, 7, 9, 16, 255, 259])("gives uid(%i) color n %% N", (n) => {
    expect(tint(uid(n), 10)).toBe((n % 256) % 10);
    expect(tint(uid(n), 6)).toBe((n % 256) % 6);
    expect(color(uid(n))).toBe(PALETTE[(n % 256) % PALETTE.length][0]);
  });
});
