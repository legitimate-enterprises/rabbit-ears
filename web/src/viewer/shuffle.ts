// Shuffle of a show, season or collection: one order drawn when it is pressed,
// which the player follows to its end in place of the server's up next. The run
// lives in this page load only, as the music queue does; a play reached any other
// way than through the run ends it.
import { epCode, title, type Ctx, type Item } from "./data";
import { btn } from "./ui";

let run: { id: string; title: string }[] = [];

/** Every playable item of scope once, in a uniformly random order. */
export function shuffled(scope: Item[], random = Math.random): Item[] {
  const out = scope.filter((i) => !i.unavailable);
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}

// pos=0 starts each from the beginning, where an absent position resumes; no edition plays the default.
const href = (n: number) => `#/play/${run[n].id}?pos=0&shuffle=${n}`;

/** Shuffle for scope, its episodes named under show as the server's up next names them; null when nothing in it plays. */
export function shuffleButton(ctx: Ctx, scope: Item[], show?: Item) {
  if (!scope.some((i) => !i.unavailable)) return null;
  return btn("Shuffle", "shuffle", "s", () => {
    run = shuffled(scope).map((i) => ({
      id: i.id,
      title: show ? `${title(show)} · ${epCode(i)} · ${title(i)}` : title(i),
    }));
    ctx.go(href(0));
  });
}

/**
 * What follows the player's item in the run its query names: undefined after the run's last
 * item or once the run is gone. A query outside a run ends the run and answers null.
 */
export function runNext(itemId: string, query: URLSearchParams) {
  if (!query.has("shuffle")) {
    run = [];
    return null;
  }
  const n = Number(query.get("shuffle"));
  return run[n]?.id === itemId && run[n + 1]
    ? { href: href(n + 1), title: run[n + 1].title }
    : undefined;
}
