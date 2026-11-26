import { describe, expect, it } from "vitest";
import { createApi } from "../api/client";
import { items } from "./data";

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
  return { api: createApi({ baseUrl: "http://x/api/v1", fetch }), asked, peak: () => peak };
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
