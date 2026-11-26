import { describe, expect, it } from "vitest";
import { createApi } from "../admin/api";
import { mbps, Playback, resolutionLabel, srtToVtt } from "./playback";
import type { Snapshot } from "./capabilities";

const stream = (over = {}) => ({
  sessionId: 7,
  entry: {
    position: 0,
    verdict: "direct_play",
    videoAction: "copy",
    audioAction: "copy",
    subtitleAction: "none",
    bad: false,
  },
  streamUrl: "/api/v1/streams/k/0/file",
  streamKind: "file",
  positionMs: 90_000,
  negotiated: { verdict: "direct_play", width: 1920, height: 1080, bitrate: 8_000_000 },
  rungs: [],
  sidecars: [],
  ...over,
});
const snap = { decoder: {}, display: {}, audioRoute: {}, network: {} } as unknown as Snapshot;

function setup(answers: Record<string, () => Response>) {
  const calls: { key: string; body: unknown }[] = [];
  const api = createApi({
    fetch: async (req) => {
      const key = `${req.method} ${new URL(req.url, "http://x").pathname.replace("/api/v1", "")}`;
      calls.push({
        key,
        body: req.method === "GET" || req.method === "DELETE" ? undefined : await req.json(),
      });
      return (
        answers[key]?.() ??
        new Response("{}", { status: 404, headers: { "content-type": "application/json" } })
      );
    },
  });
  return { calls, pb: new Playback(api, { profileId: 2, itemId: 5, snapshot: async () => snap }) };
}
const json =
  (b: unknown, status = 200) =>
  () =>
    new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });

describe("Playback", () => {
  it("starts with the full snapshot and keeps the server's start position", async () => {
    const { pb, calls } = setup({ "POST /playback/sessions": json(stream(), 201) });
    const s = await pb.start();
    expect(s.positionMs).toBe(90_000);
    expect(calls[0].body).toEqual({ profileId: 2, itemId: 5, snapshot: snap });
  });

  it("reports the position before a group change and sends only that group", async () => {
    const { pb, calls } = setup({
      "POST /playback/sessions": json(stream(), 201),
      "PUT /playback/sessions/7/position": () => new Response(null, { status: 204 }),
      "PUT /playback/sessions/7/display": json({
        action: "restart",
        stream: stream({ positionMs: 61_000, streamKind: "hls" }),
      }),
    });
    await pb.start();
    const u = await pb.groups(
      { display: { width: 1920, height: 1080, refreshRate: 60, hdrFormats: [] } },
      61_234.6,
    );
    expect(calls.slice(1).map((c) => c.key)).toEqual([
      "PUT /playback/sessions/7/position",
      "PUT /playback/sessions/7/display",
    ]);
    expect(calls[1].body).toEqual({ positionMs: 61_235, paused: false });
    expect(u?.action).toBe("restart");
    expect(pb.stream.streamKind).toBe("hls");
  });

  it("remembers the pick for later snapshots, Auto until the viewer chooses", async () => {
    const { pb } = setup({
      "POST /playback/sessions": json(stream(), 201),
      "PUT /playback/sessions/7/position": () => new Response(null, { status: 204 }),
      "PUT /playback/sessions/7/network": json({ action: "restart", stream: stream() }),
    });
    await pb.start();
    expect(pb.choice).toEqual({ auto: true });
    const net = { discoveredLocally: true, metered: false, lowData: false, vpn: false };
    await pb.quality({ ...net, maxBitrate: 4e6 }, 0);
    expect(pb.choice).toEqual({ maxBitrate: 4e6 });
    await pb.quality({ ...net, auto: true }, 0);
    expect(pb.choice).toEqual({ auto: true });
    await pb.quality(net, 0);
    expect(pb.choice).toEqual({ maxBitrate: undefined });
  });

  it("takes the stream the slow loop restarts on a throughput report", async () => {
    const { pb } = setup({
      "POST /playback/sessions": json(stream(), 201),
      "PUT /playback/sessions/7/throughput": json({
        action: "restart",
        stream: stream({ streamUrl: "/api/v1/streams/k2/0/master.m3u8", positionMs: 30_000 }),
      }),
    });
    await pb.start();
    const u = await pb.throughput({ measuredBitrate: 1, bufferMs: 0, stallCount: 3 });
    expect(u.action).toBe("restart");
    expect(pb.stream.streamUrl).toBe("/api/v1/streams/k2/0/master.m3u8");
  });

  it("moves down the plan on a client failure and gives up on 409", async () => {
    const down = setup({
      "POST /playback/sessions": json(stream(), 201),
      "POST /playback/sessions/7/events": json({
        action: "restart",
        stream: stream({ streamKind: "hls" }),
      }),
    });
    await down.pb.start();
    expect((await down.pb.failed(5000, "decode error"))?.stream.streamKind).toBe("hls");
    expect(down.calls[1].body).toEqual({
      kind: "client_failure",
      positionMs: 5000,
      reason: "decode error",
    });

    const none = setup({
      "POST /playback/sessions": json(stream(), 201),
      "POST /playback/sessions/7/events": json({ code: "conflict", message: "no entry left" }, 409),
    });
    await none.pb.start();
    expect(await none.pb.failed(0, "x")).toBeUndefined();
  });

  it("starts a session the server ended again at the position, once until it answers", async () => {
    let n = 0;
    const { pb, calls } = setup({
      "POST /playback/sessions": () =>
        json(stream({ sessionId: ++n === 1 ? 7 : 8, positionMs: n === 1 ? 0 : 42_000 }), 201)(),
      "PUT /playback/sessions/8/position": () => new Response(null, { status: 204 }),
    });
    await pb.start();
    const u = await pb.failed(42_000.4, "404");
    expect(u).toMatchObject({ action: "restart", stream: { sessionId: 8, positionMs: 42_000 } });
    expect(calls.map((c) => c.key)).toEqual([
      "POST /playback/sessions",
      "POST /playback/sessions/7/events",
      "GET /playback/sessions/7",
      "POST /playback/sessions",
    ]);
    expect(calls[3].body).toEqual({ profileId: 2, itemId: 5, positionMs: 42_000, snapshot: snap });
    await expect(pb.failed(42_000, "404")).rejects.toMatchObject({ status: 404 });
    expect(n).toBe(2);
    await pb.position(43_000);
    expect((await pb.failed(43_000, "404"))?.action).toBe("restart");
  });

  it("starts a session the server ended again with the tracks the viewer picked", async () => {
    let n = 0;
    const { pb, calls } = setup({
      "POST /playback/sessions": () => json(stream({ sessionId: ++n === 1 ? 7 : 8 }), 201)(),
      "PUT /playback/sessions/7/position": () => new Response(null, { status: 204 }),
      "PUT /playback/sessions/7/tracks": json({ action: "continue", stream: stream() }),
    });
    await pb.start();
    await pb.tracks({ audioTrackId: 3, subtitleTrackId: 9 }, 1000);
    await pb.failed(42_000, "404");
    expect(calls.filter((c) => c.key === "POST /playback/sessions")[1].body).toMatchObject({
      audioTrackId: 3,
      subtitleTrackId: 9,
    });
  });

  it("does not start again a session another device took with Move here", async () => {
    const { pb, calls } = setup({
      "POST /playback/sessions": json(stream(), 201),
      "GET /playback/sessions/7": json({ id: 7, movedToDeviceName: "Kitchen iPad" }),
    });
    await pb.start();
    await expect(pb.failed(42_000, "404")).rejects.toThrow("Moved to Kitchen iPad");
    expect(calls.filter((c) => c.key === "POST /playback/sessions").length).toBe(1);
  });

  it("leaves a session ended by another tab's start to that tab", async () => {
    const { pb, calls } = setup({ "POST /playback/sessions": json(stream(), 201) });
    await pb.start();
    const tab = new BroadcastChannel("rabbit-ears.playback");
    tab.postMessage("start");
    await new Promise((r) => setTimeout(r, 20));
    tab.close();
    await expect(pb.failed(42_000, "404")).rejects.toThrow("Another tab started playing.");
    expect(calls.map((c) => c.key)).toEqual([
      "POST /playback/sessions",
      "POST /playback/sessions/7/events",
    ]);
  });
});

describe("helpers", () => {
  it("converts SRT cues to WebVTT", () => {
    expect(srtToVtt("1\r\n00:00:01,500 --> 00:00:02,000\r\nHi\r\n")).toBe(
      "WEBVTT\n\n1\n00:00:01.500 --> 00:00:02.000\nHi\n",
    );
  });
  it("formats bitrates without a trailing zero", () => {
    expect([mbps(8e6), mbps(1.5e6), mbps(38e6)]).toEqual(["8 Mb/s", "1.5 Mb/s", "38 Mb/s"]);
  });
  it("labels resolutions", () => {
    expect([
      resolutionLabel(3840, 2160),
      resolutionLabel(1920, 800),
      resolutionLabel(1280, 720),
      resolutionLabel(854, 480),
    ]).toEqual(["4K", "1080p", "720p", "480p"]);
  });
});
