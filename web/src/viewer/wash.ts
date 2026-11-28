// An occasion's wash: its three colors as radial glows behind its screen,
// drawn as OccasionWash in the API spec says every client draws them.
import type { Schemas } from "../api/client";

/** Where colors 1, 2 and 3 glow: a triangle pointing down. Pointing up is ["0% 100%", "50% 0%", "100% 100%"]. */
const AT = ["0% 0%", "50% 100%", "100% 0%"];
/** Each color's peak opacity at a strength of 100. */
const BASE = [0.24, 0.3, 0.24];
/** The middle glow is listed first, so it is drawn over the corners'. */
const ORDER = [1, 0, 2];
const HEX = /^#[0-9a-f]{6}$/i;

const num = (n: number) => String(+n.toFixed(3));

/**
 * The CSS background of the wash, or undefined unless there are exactly three valid colors. The
 * units are the area's width and height: the viewport's, or a container's for a preview.
 */
export function washBackground(
  colors: string[] | undefined,
  wash: Schemas["OccasionWash"],
  [w, h]: [string, string] = ["vw", "vh"],
): string | undefined {
  if (colors?.length !== 3 || !colors.every((c) => HEX.test(c))) return undefined;
  const b = wash.brightness / 100;
  const s = wash.sharpness;
  const k = wash.reach / 100;
  const radius = `max(${num(80 * k)}${w}, ${num(50 * k)}${h})`;
  return ORDER.map((i) => {
    const rgb = [1, 3, 5]
      .map((p) => Math.round(parseInt(colors[i].slice(p, p + 2), 16) * (1 - b) + 255 * b))
      .join(", ");
    const a = Math.min(1, (BASE[i] * wash.strength) / 100);
    const stop = (alpha: number, at: number) => `rgba(${rgb}, ${num(alpha)}) ${num(at)}%`;
    return `radial-gradient(circle ${radius} at ${AT[i]}, ${[
      stop(a, 0),
      stop(a, s),
      stop(0.4 * a, s + (100 - s) / 2),
      stop(0, 100),
    ].join(", ")})`;
  }).join(", ");
}
