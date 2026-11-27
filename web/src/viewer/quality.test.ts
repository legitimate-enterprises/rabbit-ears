import { describe, expect, it } from "vitest";
import { AUTO } from "./capabilities";
import {
  initialPick,
  levelIndex,
  networkQuality,
  pillText,
  qualityMenu,
  rungTelemetry,
} from "./quality";
import type { Stream } from "./playback";
import { uid } from "../api/uid";

const rung = (height: number, bitrate: number, over = {}) => ({
  width: Math.round((height * 16) / 9),
  height,
  bitrate,
  verdict: "video_transcode",
  ...over,
});
const stream = (over = {}): Stream =>
  ({
    sessionId: uid(7),
    negotiated: { verdict: "video_transcode", width: 1920, height: 1080, bitrate: 8_000_000 },
    rungs: [
      rung(2160, 38_000_000, { verdict: "remux" }),
      rung(1080, 8_000_000),
      rung(720, 4_000_000),
      rung(480, 1_500_000),
    ],
    ...over,
  }) as Stream;

describe("qualityMenu", () => {
  it("at home lists Original and the rungs under the unchanged caption", () => {
    const m = qualityMenu(
      stream({
        rungs: [rung(1080, 8e6, { verdict: "direct_play" }), rung(720, 4e6)],
      }),
      0,
    );
    expect(m.caption).toBe("Playing on this browser");
    expect(m.entries.map((e) => [e.name, e.verdict, e.checked])).toEqual([
      ["Original", "Direct play", true],
      ["720p", "Transcode", false],
    ]);
    expect(m.entries[0].green).toBe(true);
  });

  it("away puts Auto first and selected, then a rule, then Original labeled by its verdict", () => {
    const s = stream({ away: true, auto: true });
    const m = qualityMenu(s, initialPick(s));
    expect(m.caption).toBe("Away from home");
    expect(m.entries.map((e) => [e.name, e.verdict, e.checked, !!e.rule])).toEqual([
      ["Auto", "Transcode", true, true],
      ["Original", "Remux", false, false],
      ["1080p", "Transcode", false, false],
      ["720p", "Transcode", false, false],
      ["480p", "Transcode", false, false],
    ]);
    expect(m.entries[0].detail).toBe("Now 1080p · adjusts to your connection");
  });

  it("names the level hls.js is on in the Auto row once it reports one", () => {
    const s = stream({ away: true, auto: true });
    const m = qualityMenu(s, "auto", { width: 854, height: 480, bitrate: 1_500_000 });
    expect(m.entries[0].detail).toBe("Now 480p · adjusts to your connection");
  });

  it("selects the rung picked and unselects Auto", () => {
    const m = qualityMenu(stream({ away: true, auto: false }), 2);
    expect(m.entries.filter((e) => e.checked).map((e) => e.name)).toEqual(["720p"]);
  });

  it("renders blocked entries as Wi-Fi only in place of the verdict", () => {
    const m = qualityMenu(
      stream({
        away: true,
        rungs: [
          rung(2160, 38e6, { verdict: "remux", blocked: true }),
          rung(1080, 8e6, { blocked: true }),
          rung(480, 1.5e6),
        ],
      }),
      "auto",
    );
    const by = Object.fromEntries(m.entries.map((e) => [e.name, e]));
    expect(by.Original).toMatchObject({ blocked: true, verdict: "Wi-Fi only" });
    expect(by["1080p"]).toMatchObject({ blocked: true, verdict: "Wi-Fi only" });
    expect(by["480p"]).toMatchObject({ blocked: false, verdict: "Transcode" });
    expect(by.Auto.blocked).toBe(false);
  });
});

describe("initialPick", () => {
  it("is Auto when the server says Auto is in effect, else Original", () => {
    expect(initialPick(stream({ auto: true }))).toBe("auto");
    expect(initialPick(stream())).toBe(0);
  });
});

describe("networkQuality", () => {
  const rungs = stream().rungs;
  it("sends auto for Auto, no cap for Original and the rung's bitrate for a rung", () => {
    expect(networkQuality("auto", rungs)).toEqual(AUTO);
    expect(networkQuality(0, rungs)).toEqual({});
    expect(networkQuality(2, rungs)).toEqual({ maxBitrate: 4_000_000 });
  });
});

describe("pillText", () => {
  it("reads resolution, Auto and the bitrate of the rung in use", () => {
    const s = stream({ away: true, auto: true });
    expect(pillText(s)).toMatchObject({ res: "1080p", label: "Auto", bitrate: "8 Mb/s" });
    const low = pillText(s, { width: 854, height: 480, bitrate: 1_500_000 });
    expect(low).toMatchObject({ res: "480p", label: "Auto", bitrate: "1.5 Mb/s" });
    expect(low.aria).toBe("Playback quality: 480p, auto, 1.5 megabits per second");
  });
  it("keeps the verdict for a fixed pick and flags HDR", () => {
    const p = pillText(
      stream({
        negotiated: {
          verdict: "remux",
          width: 3840,
          height: 2160,
          bitrate: 38_000_000,
          hdrFormat: "hdr10",
        },
      }),
    );
    expect(p).toMatchObject({
      res: "4K",
      hdr: "HDR",
      label: "Remux",
      bitrate: "38 Mb/s",
      ok: false,
    });
    expect(p.aria).toBe("Playback quality: 4K HDR, remux, 38 megabits per second");
  });
});

describe("rungTelemetry", () => {
  const levels = [
    { height: 480, bitrate: 1_500_000 },
    { height: 1080, bitrate: 8_000_000 },
  ];
  it("reports the level hls.js is on", () => {
    expect(rungTelemetry(stream(), levels, 0)).toEqual({ rungHeight: 480, rungBitrate: 1_500_000 });
  });
  it("falls back to the negotiated stream without a current level", () => {
    expect(rungTelemetry(stream(), levels, -1)).toEqual({
      rungHeight: 1080,
      rungBitrate: 8_000_000,
    });
    expect(rungTelemetry(stream(), undefined, 0)).toEqual({
      rungHeight: 1080,
      rungBitrate: 8_000_000,
    });
  });
});

describe("levelIndex", () => {
  const levels = [
    { height: 480, bitrate: 1_500_000 },
    { height: 720, bitrate: 4_000_000 },
    { height: 720, bitrate: 2_000_000 },
  ];
  it("finds the negotiated rung in Auto too, where it is only the start level", () => {
    const s = stream({ auto: true, negotiated: { height: 720, bitrate: 4_000_000 } });
    expect(levelIndex(levels, s)).toBe(1);
  });
  it("finds the level nearest the negotiated rung", () => {
    const s = stream({ negotiated: { height: 720, bitrate: 2_000_000 } });
    expect(levelIndex(levels, s)).toBe(2);
    expect(levelIndex(levels, stream({ negotiated: { height: 1080, bitrate: 8e6 } }))).toBe(-1);
  });
});
