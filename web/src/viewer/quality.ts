// The quality menu, pill and rung telemetry as plain data, so the player only
// paints them. Away from home the server offers Auto, and the player's own
// ABR moves between the rungs of the Auto ladder.
import { AUTO, type Quality } from "./capabilities";
import { VERDICT } from "./format";
import { mbps, resolutionLabel, type Stream } from "./playback";

type Rung = Stream["rungs"][number];
/** A rung the player is on, as hls.js reports its variants. */
export interface Level {
  width?: number;
  height: number;
  bitrate: number;
}
/** "auto", or an index into the stream's rungs where 0 is Original. */
export type Pick = "auto" | number;

export interface Entry {
  pick: Pick;
  name: string;
  detail: string;
  verdict: string;
  green: boolean;
  blocked: boolean;
  checked: boolean;
  /** A divider follows this entry. */
  rule?: boolean;
}

export const initialPick = (s: Stream): Pick => (s.auto ? "auto" : 0);

const verdict = (r: Rung) =>
  r.blocked
    ? "Wi-Fi only"
    : r.verdict === "direct_play"
      ? "Direct play"
      : r.verdict === "remux"
        ? "Remux"
        : "Transcode";

/** level is the variant hls.js is on, which the Auto row names once known. */
export function qualityMenu(
  s: Stream,
  pick: Pick,
  level?: Level,
): { caption: string; entries: Entry[] } {
  const entries: Entry[] = s.rungs.map((r, i) => ({
    pick: i,
    name: i === 0 ? "Original" : resolutionLabel(r.width, r.height),
    detail: `${r.width}×${r.height} - ${mbps(r.bitrate)}`,
    verdict: verdict(r),
    green: r.verdict === "direct_play" && !r.blocked,
    blocked: !!r.blocked,
    checked: pick === i,
  }));
  if (s.away)
    entries.unshift({
      pick: "auto",
      name: "Auto",
      detail: `Now ${resolutionLabel(level?.width ?? s.negotiated.width, level?.height ?? s.negotiated.height)} · adjusts to your connection`,
      verdict: "Transcode",
      green: false,
      blocked: false,
      checked: pick === "auto",
      rule: true,
    });
  return { caption: s.away ? "Away from home" : "Playing on this browser", entries };
}

/** The network group's quality for a pick: Auto, Original without a cap, or the rung's bitrate. */
export const networkQuality = (pick: Pick, rungs: Rung[]): Quality =>
  pick === "auto" ? AUTO : pick === 0 ? {} : { maxBitrate: rungs[pick].bitrate };

/** In Auto the rung in use is the level hls.js is on, else what the server negotiated. */
export function pillText(s: Stream, level?: Level) {
  const n = s.negotiated;
  const auto = !!s.auto;
  const l = auto ? level : undefined;
  const res = resolutionLabel(l?.width ?? n.width, l?.height ?? n.height);
  const hdr = n.hdrFormat ? "HDR" : "";
  const label = auto ? "Auto" : VERDICT[n.verdict];
  const bitrate = mbps(l?.bitrate ?? n.bitrate);
  return {
    res,
    hdr,
    label,
    ok: !auto && n.verdict === "direct_play",
    bitrate,
    aria: `Playback quality: ${[res, hdr].filter(Boolean).join(" ")}, ${label.toLowerCase()}, ${bitrate}`.replace(
      "Mb/s",
      "megabits per second",
    ),
  };
}

export function rungTelemetry(s: Stream, levels: Level[] | undefined, current: number) {
  const l = levels?.[current] ?? s.negotiated;
  return { rungHeight: l.height, rungBitrate: l.bitrate };
}

/**
 * The variant of the negotiated rung: same height, nearest bitrate; -1 when none.
 * A fixed pick pins it; Auto only starts there.
 */
export function levelIndex(levels: Level[], s: Stream): number {
  const n = s.negotiated;
  let best = -1;
  levels.forEach((l, i) => {
    if (
      l.height === n.height &&
      (best < 0 || Math.abs(l.bitrate - n.bitrate) < Math.abs(levels[best].bitrate - n.bitrate))
    )
      best = i;
  });
  return best;
}
