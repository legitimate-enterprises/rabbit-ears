import { describe, expect, it, vi } from "vitest";
import {
  browserEnv,
  decoder,
  display,
  network,
  snapshot,
  watchGroups,
  type Env,
} from "./capabilities";

const env = (over: Partial<Env> = {}): Env => ({
  decodingInfo: async (cfg) => {
    const c = (cfg as { video: { contentType: string; width: number } }).video;
    // Hardware H.264 up to 4K, no HEVC, software VP9 up to 1080p.
    if (c.contentType.includes("avc1"))
      return { supported: true, powerEfficient: true, smooth: true };
    if (c.contentType.includes("vp09") && c.width <= 1920)
      return { supported: true, powerEfficient: false, smooth: false };
    return { supported: false, powerEfficient: false, smooth: false };
  },
  canPlayType: (m) => (/^video\/(mp4|webm)|mp4a|opus|audio\/mpeg/.test(m) ? "probably" : ""),
  hlsViaLibrary: false,
  screen: { width: 1512, height: 982 },
  pixelRatio: 2,
  matches: (q) => q.includes("dynamic-range"),
  hostname: "192.168.1.20",
  audioChannels: () => 2,
  ...over,
});

describe("decoder group", () => {
  it("reports each probed codec with its ceiling and hardware flag", async () => {
    const d = await decoder(env());
    const h264 = d.videoCodecs.find((c) => c.codec === "h264")!;
    expect(h264).toMatchObject({
      hardware: true,
      maxWidth: 3840,
      maxHeight: 2160,
      maxFrameRate: 60,
    });
    expect(h264.profiles).toEqual(["Baseline", "Constrained Baseline", "Main", "High"]);
    const vp9 = d.videoCodecs.find((c) => c.codec === "vp9")!;
    expect(vp9).toMatchObject({ hardware: false, maxWidth: 1920, maxHeight: 1080 });
    expect(d.videoCodecs.map((c) => c.codec)).not.toContain("hevc");
  });
  it("lists only what the browser can play and names HLS only when something can read it", async () => {
    const d = await decoder(env());
    // An MP3 file plays as it is; FLAC does not, since this browser has no audio/flac.
    expect(d.containers).toEqual(["mp4", "webm", "mp3"]);
    expect(d.audioCodecs).toEqual(expect.arrayContaining(["aac", "opus", "mp3"]));
    expect(d.audioCodecs).not.toContain("eac3");
    expect(d.protocols).toEqual(["http"]);
    expect((await decoder(env({ hlsViaLibrary: true }))).protocols).toEqual(["http", "hls"]);
  });
  it("marks HDR10 only for 10-bit decodes on an HDR display", async () => {
    const e = env({
      decodingInfo: async () => ({ supported: true, powerEfficient: true, smooth: true }),
    });
    const d = await decoder(e);
    expect(d.videoCodecs.find((c) => c.codec === "hevc")!.hdrFormats).toEqual(["hdr10"]);
    expect(d.videoCodecs.find((c) => c.codec === "h264")!.hdrFormats).toEqual([]);
    const sdr = await decoder({ ...e, matches: () => false });
    expect(sdr.videoCodecs.every((c) => !c.hdrFormats?.length)).toBe(true);
  });
});

describe("other groups", () => {
  it("scales the display by pixel ratio and falls back to 60 Hz", () => {
    expect(display(env())).toEqual({
      width: 3024,
      height: 1964,
      refreshRate: 60,
      hdrFormats: ["hdr10"],
    });
  });
  it.each([
    ["localhost", true],
    ["LocalHost", true],
    ["127.0.0.1", true],
    ["10.1.2.3", true],
    ["172.16.0.1", true],
    ["172.31.255.1", true],
    ["192.168.1.20", true],
    ["169.254.1.1", true],
    ["[::1]", true],
    ["[fd00::1]", true],
    ["[fc00::1]", true],
    ["[fe80::1]", true],
    ["[FEBF::1]", true],
    ["macmini", true],
    ["nas.local", true],
    ["nas.lan", true],
    ["nas.home.arpa", true],
    ["server.internal", true],
    ["10.example.com", false],
    ["127.evil.example", false],
    ["192.168.example.org", false],
    ["localhostfoo.com", false],
    ["10.1.2", false],
    ["172.32.0.1", false],
    ["172.15.0.1", false],
    ["8.8.8.8", false],
    ["[2001:db8::1]", false],
    ["[fec0::1]", false],
    ["example.com", false],
    ["rabbit-ears-demo.legitimate.enterprises", false],
    ["local.example.com", false],
  ])("treats host %s as local: %s", (hostname, local) => {
    expect(network(env({ hostname })).discoveredLocally).toBe(local);
  });
  it("reads the network path and the viewer's pick", () => {
    const path = { discoveredLocally: true, metered: false, lowData: false, vpn: false };
    expect(network(env(), { maxBitrate: 4_000_000 })).toEqual({
      ...path,
      auto: false,
      maxBitrate: 4_000_000,
    });
    expect(network(env(), {})).toEqual({ ...path, auto: false });
    expect(network(env(), { auto: true })).toEqual({ ...path, auto: true });
    expect(network(env())).toEqual({ ...path, auto: true });
    const n = network(
      env({ hostname: "media.example.com", connection: { type: "cellular", saveData: true } }),
    );
    expect(n).toMatchObject({ discoveredLocally: false, metered: true, lowData: true });
    expect(n.maxBitrate).toBeUndefined();
  });
  it("builds a full snapshot with all four groups", async () => {
    const s = await snapshot(env());
    expect(Object.keys(s).sort()).toEqual(["audioRoute", "decoder", "display", "network"]);
    expect(s.audioRoute).toEqual({ channels: 2, passthroughCodecs: [] });
  });
});

describe("watchGroups", () => {
  it("emits only the groups that changed", () => {
    const listeners = new Map<string, () => void>();
    const target = {
      add: (e: string, f: () => void) => listeners.set(e, f),
      remove: (e: string) => listeners.delete(e),
    };
    const e = env();
    const cur = { screen: e.screen, connection: e.connection };
    const seen: unknown[] = [];
    const stop = watchGroups(
      {
        ...e,
        get screen() {
          return cur.screen;
        },
        get connection() {
          return cur.connection;
        },
      } as Env,
      () => ({}),
      (c) => seen.push(Object.keys(c)),
      target,
    );
    listeners.get("resize")!();
    expect(seen).toEqual([]);
    cur.screen = { width: 1920, height: 1080 };
    listeners.get("resize")!();
    cur.connection = { saveData: true };
    listeners.get("connection")!();
    expect(seen).toEqual([["display"], ["network"]]);
    stop();
    expect(listeners.size).toBe(0);
  });
});

describe("browser env", () => {
  it("reads the screen and pixel ratio when asked, so a display change is seen", () => {
    vi.stubGlobal("screen", { width: 1512, height: 982 });
    vi.stubGlobal("devicePixelRatio", 2);
    const e = browserEnv(false);
    vi.stubGlobal("screen", { width: 3840, height: 2160 });
    vi.stubGlobal("devicePixelRatio", 1);
    expect([e.screen, e.pixelRatio]).toEqual([{ width: 3840, height: 2160 }, 1]);
    vi.unstubAllGlobals();
  });
});
