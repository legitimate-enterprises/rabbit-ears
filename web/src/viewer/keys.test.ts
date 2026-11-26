import { describe, expect, it } from "vitest";
import { command } from "./keys";

const press = (key: string, init: KeyboardEventInit = {}, target?: HTMLElement) => {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, ...init });
  if (target) Object.defineProperty(e, "target", { value: target });
  return e;
};

describe("shortcuts", () => {
  it("takes Cmd on a Mac and Ctrl elsewhere", () => {
    expect(command(press("f", { metaKey: true }), true)).toBe("search");
    expect(command(press("f", { ctrlKey: true }), true)).toBeUndefined();
    expect(command(press("f", { ctrlKey: true }), false)).toBe("search");
    expect(command(press("f", { metaKey: true }), false)).toBeUndefined();
  });

  it("goes back, forward and to each section by number", () => {
    expect(command(press("[", { metaKey: true }), true)).toBe("back");
    expect(command(press("]", { metaKey: true }), true)).toBe("forward");
    expect(
      ["1", "2", "3", "4", "5", "6", "7"].map((k) => command(press(k, { ctrlKey: true }), false)),
    ).toEqual(["home", "movies", "tv", "music", "youtube", "stations", undefined]);
    expect(command(press("1", { metaKey: true, altKey: true }), true)).toBeUndefined();
    expect(command(press("f", { metaKey: true, shiftKey: true }), true)).toBeUndefined();
  });

  it("opens the sheet with ?, except while typing", () => {
    expect(command(press("?", { shiftKey: true }, document.body), true)).toBe("help");
    expect(command(press("?", {}, document.createElement("input")), true)).toBeUndefined();
    expect(command(press("?", {}, document.createElement("textarea")), true)).toBeUndefined();
    const box = document.createElement("input");
    box.type = "checkbox";
    expect(command(press("?", {}, box), true)).toBe("help");
    // Mod shortcuts work from the search field itself.
    expect(command(press("f", { metaKey: true }, document.createElement("input")), true)).toBe(
      "search",
    );
  });
});
