// Seek preview geometry: where the pointer is on the bar, which frame of the
// stream's previews shows there, and where the card sits.
import type { Schemas } from "../api/client";

type Previews = Schemas["PreviewFrames"];

/** x is a viewport coordinate; bar is the slider's rectangle. */
export function scrub(
  p: Previews,
  x: number,
  bar: { left: number; width: number },
  durationMs: number,
  cardWidth: number,
) {
  const px = bar.width > 0 ? Math.min(bar.width, Math.max(0, x - bar.left)) : 0;
  const ms = bar.width > 0 ? (px / bar.width) * durationMs : 0;
  // Frame n is the keyframe nearest n × intervalMs.
  const index = Math.min(p.count - 1, Math.max(0, Math.round(ms / p.intervalMs)));
  const half = cardWidth / 2;
  return {
    ms,
    index,
    url: p.frameUrl.replace("{n}", String(index)),
    left: Math.max(half, Math.min(bar.width - half, px)),
  };
}

export function chapterAt(chapters: Schemas["Chapter"][], ms: number): string | undefined {
  const i = chapters.findIndex((c) => ms >= c.startMs && ms < c.endMs);
  return i < 0 ? undefined : chapters[i].title || `Chapter ${i + 1}`;
}
