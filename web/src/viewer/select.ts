// Selection mode over a library grid or list: Select toggles it, a click
// toggles an item, Shift-click a range, Cmd/Ctrl-A every loaded item, and
// Escape or Done leaves. The bar applies one bulk request per action.
import { h } from "../admin/dom";
import { ok } from "../admin/api";
import type { Ctx } from "./data";
import { guard, refresh } from "./ui";
import { addAllToPlaylist, addAllToStation } from "./pages/playlists";

export type SelectKind = "movie" | "show" | "channel" | "video" | "album" | "artist" | "song";

const WATCHED: SelectKind[] = ["movie", "show", "channel", "video"];
const KIDS: SelectKind[] = ["movie", "show", "channel"];
const PLAYLIST = {
  movie: "movies",
  show: "tv",
  album: "music",
  artist: "music",
  song: "music",
} as const;

/** A show or season id marks every episode the profile may see. */
export const markWatched = (ctx: Ctx, itemIds: string[], watched: boolean) =>
  ok(
    ctx.api.PUT("/profiles/{profileId}/watch-state/batch", {
      params: { path: { profileId: ctx.profile.id } },
      body: { itemIds, watched },
    }),
  );

/**
 * Selection over the [data-id] elements inside root, whatever root redraws to. Returns the Select
 * button for the filters row and the action bar to place after root.
 */
export function selection(ctx: Ctx, kind: SelectKind, root: HTMLElement) {
  const adult = ctx.profile.kind === "adult";
  const picked = new Set<string>();
  let anchor: string | undefined;
  let on = false;
  const tiles = () => [...root.querySelectorAll<HTMLElement>("[data-id]")];
  const idOf = (el: Element) => (el as HTMLElement).dataset.id!;
  const count = h("span.grow", { role: "status" });
  const ids = () =>
    tiles()
      .map(idOf)
      .filter((id) => picked.has(id));

  const paint = () => {
    for (const t of tiles()) {
      if (on) {
        t.setAttribute("role", "option");
        t.setAttribute("aria-selected", String(picked.has(idOf(t))));
      } else {
        t.removeAttribute("role");
        t.removeAttribute("aria-selected");
      }
    }
    [root, ...root.querySelectorAll(".grid, ol")].forEach((g) => {
      if (!g.matches(".grid, ol")) return;
      if (on) g.setAttribute("role", "listbox");
      else g.removeAttribute("role");
      if (on) g.setAttribute("aria-multiselectable", "true");
      else g.removeAttribute("aria-multiselectable");
    });
    count.textContent = `${picked.size} selected`;
    for (const b of bar.querySelectorAll<HTMLButtonElement>("button.act"))
      b.disabled = !picked.size;
  };
  const watch = new MutationObserver(() => on && paint());

  const click = (e: MouseEvent) => {
    const t = (e.target as Element).closest<HTMLElement>("[data-id]");
    if (!t || !root.contains(t)) return;
    e.preventDefault();
    e.stopPropagation();
    const id = idOf(t);
    const all = tiles().map(idOf);
    if (e.shiftKey && anchor !== undefined && all.includes(anchor)) {
      const [a, b] = [all.indexOf(anchor), all.indexOf(id)].sort((x, y) => x - y);
      for (const x of all.slice(a, b + 1)) picked.add(x);
    } else {
      if (picked.has(id)) picked.delete(id);
      else picked.add(id);
      anchor = id;
    }
    paint();
  };
  const key = (e: KeyboardEvent) => {
    const el = e.target as Element;
    // A dialog the bar opened handles its own keys.
    if (el.closest?.("[role=dialog]")) return;
    if (e.key === "Escape") return leave();
    const typing = el.closest?.("input, textarea, select");
    if (!typing && e.key.toLowerCase() === "a" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      for (const t of tiles()) picked.add(idOf(t));
      paint();
    }
  };

  const enter = () => {
    on = true;
    toggle.textContent = "Done";
    toggle.setAttribute("aria-pressed", "true");
    bar.hidden = false;
    root.classList.add("selecting");
    root.addEventListener("click", click, true);
    document.addEventListener("keydown", key);
    window.addEventListener("hashchange", leave, { once: true });
    watch.observe(root, { childList: true, subtree: true });
    paint();
  };
  function leave() {
    on = false;
    picked.clear();
    anchor = undefined;
    toggle.textContent = "Select";
    toggle.setAttribute("aria-pressed", "false");
    bar.hidden = true;
    root.classList.remove("selecting");
    root.removeEventListener("click", click, true);
    document.removeEventListener("keydown", key);
    window.removeEventListener("hashchange", leave);
    watch.disconnect();
    paint();
  }
  const toggle = h(
    "button.btn.s",
    { type: "button", "aria-pressed": "false", onclick: () => (on ? leave() : enter()) },
    "Select",
  );

  const act = (label: string, run: (itemIds: string[]) => Promise<unknown>) =>
    h(
      "button.btn.s.act",
      {
        type: "button",
        onclick: guard(async () => {
          await run(ids());
          leave();
          refresh();
        }),
      },
      label,
    );
  const watched = (watched: boolean) => (itemIds: string[]) => markWatched(ctx, itemIds, watched);
  const kids = (kidsContent: boolean) => (itemIds: string[]) =>
    ok(ctx.api.PUT("/items/kids-content/batch", { body: { itemIds, kidsContent } }));
  const list = kind in PLAYLIST ? PLAYLIST[kind as keyof typeof PLAYLIST] : undefined;
  const adds = list
    ? [addAllToPlaylist(ctx, ids, list), addAllToStation(ctx, ids, list)].map(
        (b) => (b?.classList.add("act"), b),
      )
    : [];

  const bar = h(
    "div.selbar",
    { role: "toolbar", "aria-label": "Selection", hidden: true },
    count,
    WATCHED.includes(kind) && act("Mark watched", watched(true)),
    WATCHED.includes(kind) && act("Mark unwatched", watched(false)),
    adult && KIDS.includes(kind) && act("Kids' content", kids(true)),
    adult && KIDS.includes(kind) && act("Not kids' content", kids(false)),
    ...adds,
    h("button.btn.p", { type: "button", onclick: () => leave() }, "Done"),
  );
  return { toggle, bar };
}
