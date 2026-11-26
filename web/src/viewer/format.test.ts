import { describe, expect, it } from "vitest";
import type { Schemas } from "../api/client";
import { ago, audioDetails, countdown, minutes, since, subtitles } from "./format";

describe("minutes", () => {
  it("shows milliseconds as a runtime, rounded to the minute", () => {
    expect(minutes(29_999)).toBe("");
    expect(minutes(30_000)).toBe("1m");
    expect(minutes(59 * 60_000 + 31_000)).toBe("1h");
    expect(minutes((2 * 60 + 5) * 60_000)).toBe("2h 5m");
  });
  it("shows nothing for an unknown or empty duration", () => {
    expect(minutes(undefined)).toBe("");
    expect(minutes(0)).toBe("");
  });
});

describe("ago", () => {
  const now = Date.parse("2026-01-01T12:00:00Z");
  const at = (s: number) => new Date(now - s * 1000).toISOString();
  it("rounds to a moment, minutes or hours", () => {
    expect(ago(at(5), now)).toBe("a moment ago");
    expect(ago(at(60), now)).toBe("1 minute ago");
    expect(ago(at(5 * 60 + 20), now)).toBe("5 minutes ago");
    expect(ago(at(3 * 3600), now)).toBe("3 hours ago");
  });
  it("treats a clock a little ahead of the server as just now", () => {
    expect(ago(at(-30), now)).toBe("a moment ago");
  });
});

describe("since", () => {
  const now = Date.parse("2026-11-14T20:00:00Z");
  it("names a publish date in days, weeks and months", () => {
    expect(since("2026-11-14", now)).toBe("today");
    expect(since("2026-11-13", now)).toBe("yesterday");
    expect(since("2026-11-09", now)).toBe("5 days ago");
    expect(since("2026-11-05", now)).toBe("last week");
    expect(since("2026-10-25", now)).toBe("2 weeks ago");
    expect(since("2026-10-05", now)).toBe("last month");
    expect(since("2026-09-01", now)).toBe("2 months ago");
    expect(since("2024-11-01", now)).toBe("2 years ago");
  });
});

describe("countdown", () => {
  it("rounds up to the second as m:ss and stops at zero", () => {
    expect(countdown(252_000)).toBe("4:12");
    expect(countdown(251_100)).toBe("4:12");
    expect(countdown(-5)).toBe("0:00");
  });
});

describe("subtitles", () => {
  const t = (language?: string, title?: string, isForced = false) =>
    ({ kind: "subtitle", language, title, isForced }) as Schemas["Track"];
  const line = (...tracks: Schemas["Track"][]) => subtitles(tracks);

  it("names ISO 639-2 codes, bibliographic and terminological alike, through the platform", () => {
    const names = new Intl.DisplayNames(["en"], { type: "language", fallback: "none" });
    expect(["eng", "fre", "fra", "ger", "deu"].map((c) => names.of(c))).toEqual([
      "English",
      "French",
      "French",
      "German",
      "German",
    ]);
    expect(names.of("xyz")).toBeUndefined();
    expect(names.of("und")).toBeUndefined();
  });

  it("labels each track by language, then a title that adds to it, then forced", () => {
    expect(line(t("eng"))).toBe("English");
    expect(line(t("eng"), t("eng", "SDH"), t("spa"))).toBe("English, English SDH, Spanish");
    expect(line(t("eng", "English SDH"))).toBe("English SDH");
    expect(line(t("eng", undefined, true))).toBe("English (forced)");
    expect(line(t("xyz"), t(), t("und"))).toBe("XYZ, Unknown");
    expect(line(t("ENG"), t("en-US", "SDH"))).toBe("English, English SDH");
  });

  it("past three tracks, lists the English ones and counts the other languages", () => {
    expect(line(t("eng"), t("eng", "SDH"), t("spa"), t("fre"), t("ger"))).toBe(
      "English, English SDH, +3 other languages",
    );
    expect(line(t("eng"), t("spa"), t("spa", "SDH"), t("spa", undefined, true))).toBe(
      "English, +1 other language",
    );
    expect(line(t("eng"), t("eng"), t("eng"), t("eng"))).toBe("English");
    expect(line(t("en"), t("fre"), t("fra"), t(), t("und"))).toBe("English, +2 other languages");
  });

  it("past three tracks and none English, counts the languages unless there is one", () => {
    expect(line(t("spa"), t("fre"), t("ger"), t("ita"))).toBe("4 languages");
    expect(line(t("spa"), t("spa", "SDH"), t("spa"), t("spa"))).toBe("Spanish, Spanish SDH");
  });

  it("leaves out tracks that are not subtitles", () => {
    expect(subtitles([{ ...t("eng"), kind: "audio" }, t("spa")])).toBe("Spanish");
  });
});

describe("audioDetails", () => {
  const audio = (
    source: Partial<Schemas["AudioSource"]>,
    delivered: Partial<Schemas["AudioDelivered"]> = {},
  ): Schemas["AudioDetails"] => ({
    source: { lossless: false, ...source },
    delivered: { verdict: "direct_play", codec: "flac", lossless: false, ...delivered },
  });

  it("groups a lossless direct play, formatted for reading", () => {
    expect(
      audioDetails(
        audio(
          {
            codec: "flac",
            lossless: true,
            bitrate: 2_115_600,
            sampleRateHz: 96_000,
            bitDepth: 24,
            channels: 2,
            sizeBytes: 67_400_000,
          },
          { bitrate: 2_115_600, lossless: true },
        ),
      ),
    ).toEqual([
      {
        title: "Source",
        rows: [
          { label: "Format", value: "FLAC", lossless: true },
          { label: "Bitrate", value: "2,116 kb/s" },
          { label: "Sample rate", value: "96 kHz" },
          { label: "Bit depth", value: "24-bit" },
          { label: "Channels", value: "Stereo" },
          { label: "Size", value: "67.4 MB" },
        ],
      },
      {
        title: "Delivered",
        rows: [
          { label: "Playback", value: "Direct play", tone: "ok" },
          { label: "Codec", value: "FLAC" },
          { label: "Bitrate", value: "2,116 kb/s" },
        ],
      },
    ]);
  });

  it("leaves out rows that have no value, and marks a transcode as a caution", () => {
    expect(
      audioDetails(
        audio(
          { codec: "opus", sampleRateHz: 44_100, channels: 6 },
          { verdict: "audio_transcode", codec: "aac", bitrate: 256_000 },
        ),
      ),
    ).toEqual([
      {
        title: "Source",
        rows: [
          { label: "Format", value: "OPUS" },
          { label: "Sample rate", value: "44.1 kHz" },
          { label: "Channels", value: "6 channels" },
        ],
      },
      {
        title: "Delivered",
        rows: [
          { label: "Playback", value: "Audio transcode", tone: "warn" },
          { label: "Codec", value: "AAC" },
          { label: "Bitrate", value: "256 kb/s" },
        ],
      },
    ]);
  });

  it("names one channel Mono, and a remux is a success", () => {
    const [src, out] = audioDetails(audio({ channels: 1 }, { verdict: "remux" }));
    expect(src.rows).toEqual([{ label: "Channels", value: "Mono" }]);
    expect(out.rows[0]).toEqual({ label: "Playback", value: "Remux", tone: "ok" });
  });
});
