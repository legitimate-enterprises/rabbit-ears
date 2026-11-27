import { describe, expect, it } from "vitest";
import { createApi } from "../api/client";
import { items, playFraction, resumeFraction, type Item, type WatchState } from "./data";

/** A listing of n items served 200 a page, recording each offset asked and how many were open at once. */
function listing(n: number) {
  const asked: number[] = [];
  let open = 0;
  let peak = 0;
  const fetch = async (req: Request) => {
    const offset = Number(new URL(req.url).searchParams.get("offset"));
    asked.push(offset);
    peak = Math.max(peak, ++open);
    await new Promise((r) => setTimeout(r, 5));
    open--;
    const ids = Array.from(
      { length: Math.max(0, Math.min(200, n - offset)) },
      (_, i) => offset + i,
    );
    const body = {
      items: ids.map((id) => ({ id })),
      nextOffset: offset + 200 < n ? offset + 200 : undefined,
    };
    return new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });
  };
  return { api: createApi({ baseUrl: "http://x/api/v2", fetch }), asked, peak: () => peak };
}

describe("items", () => {
  it("asks once for a listing that fits a page", async () => {
    const l = listing(150);
    expect((await items(l.api, { kind: "movie" })).length).toBe(150);
    expect(l.asked).toEqual([0]);
  });

  it("asks for the pages after the first several at a time, in order", async () => {
    const l = listing(2_850);
    const all = await items(l.api, { kind: "episode" });
    expect(all.map((i) => i.id)).toEqual(Array.from({ length: 2_850 }, (_, i) => i));
    expect(l.peak()).toBe(4);
    expect(l.asked.length).toBeLessThanOrEqual(15 + 3);
  });
});

describe("the resume bar's fraction", () => {
  const ts = "2026-01-01T00:00:00Z";
  const it_ = (id: string, kind: Item["kind"], runtimeMinutes?: number): Item => ({
    id,
    libraryId: "l",
    kind,
    parsedTitle: id,
    addedAt: ts,
    modifiedAt: ts,
    createdAt: ts,
    metadata:
      runtimeMinutes === undefined
        ? undefined
        : { provider: "tmdb", status: "matched", runtimeMinutes },
  });
  const state = (itemId: string, positionMs: number, watched = false, at = ts): WatchState => ({
    itemId,
    positionMs,
    watched,
    playCount: watched ? 1 : 0,
    changedAt: at,
    lastPlayedAt: at,
  });
  const ws = (...s: WatchState[]) => new Map(s.map((w) => [w.itemId, w]));

  it("is the share of the runtime for a started, unfinished movie or episode", () => {
    expect(playFraction(it_("m", "movie", 100), ws(state("m", 25 * 60000)))).toBe(0.25);
    expect(playFraction(it_("e", "episode", 40), ws(state("e", 30 * 60000)))).toBe(0.75);
  });

  it("is none for an unstarted or finished item, or one of unknown runtime", () => {
    expect(playFraction(it_("m", "movie", 100), ws())).toBeUndefined();
    expect(playFraction(it_("m", "movie", 100), ws(state("m", 0, true)))).toBeUndefined();
    expect(playFraction(it_("m", "movie"), ws(state("m", 60000)))).toBeUndefined();
  });

  it("is full, not more, for a position past the runtime", () => {
    expect(playFraction(it_("m", "movie", 1), ws(state("m", 90000)))).toBe(1);
  });

  it("is that of the started episode a show's or season's play resumes", () => {
    const eps = [it_("e1", "episode", 50), it_("e2", "episode", 40)];
    const w = ws(state("e1", 0, true), state("e2", 10 * 60000, false, "2026-01-02T00:00:00Z"));
    expect(playFraction(it_("s", "show"), w, eps)).toBe(0.25);
    expect(playFraction(it_("se", "season"), w, eps)).toBe(0.25);
  });

  it("is none for a show whose play starts its next episode from the beginning", () => {
    const eps = [it_("e1", "episode", 50), it_("e2", "episode", 40)];
    expect(playFraction(it_("s", "show"), ws(state("e1", 0, true)), eps)).toBeUndefined();
    expect(playFraction(it_("s", "show"), ws(), eps)).toBeUndefined();
  });

  it("is none for a show without its episodes, whose resume target is unknown", () => {
    expect(playFraction(it_("s", "show"), ws(state("e2", 60000)))).toBeUndefined();
  });

  it("is the share of a YouTube video's duration at its resume position", () => {
    expect(resumeFraction({ positionMs: 30_000 }, 120_000)).toBe(0.25);
    expect(resumeFraction({ positionMs: 0 }, 120_000)).toBeUndefined();
    expect(resumeFraction({ positionMs: 30_000 }, undefined)).toBeUndefined();
  });
});
