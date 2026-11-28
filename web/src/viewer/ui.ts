import { h } from "../admin/dom";
import { artKey, placeholderKind, type PlaceholderKind } from "./artwork";
import { ok } from "../admin/api";
import { toast } from "./handoff";
import type { Schemas } from "../api/client";
import { color, PALETTE, title, type Ctx, type Item } from "./data";

const NS = "http://www.w3.org/2000/svg";
const PATHS: Record<string, string> = {
  play: "M7 4v16l13-8z",
  pause: "M6 4h3v16H6zM15 4h3v16h-3z",
  back: "M15 5l-7 7 7 7",
  check: "M5 12l5 5 9-10",
  back10: "M12 5a7 7 0 1 1-7 7M12 1v8L8 5z",
  fwd10: "M12 5a7 7 0 1 0 7 7M12 1v8l4-4z",
  mute: "M4 9v6h4l5 4V5L8 9zM16 9l5 6M21 9l-5 6",
  vol: "M4 9v6h4l5 4V5L8 9zM16 8a5 5 0 0 1 0 8",
  cc: "M3 6h18v12H3zM8 11h3M14 11h3M8 14h3M14 14h3",
  next: "M5 5l9 7-9 7zM17 5v14",
  full: "M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5",
  pip: "M3 5h18v14H3zM12 12h7v5h-7z",
  lock: "M6 11h12v9H6zM8 11V8a4 4 0 0 1 8 0v3",
  plus: "M12 5v14M5 12h14",
  chev: "M6 15l6-6 6 6",
  tv: "M3 5h18v13H3zM8 21h8",
  prev: "M19 5l-9 7 9 7zM7 5v14",
  shuffle: "M3 7h4l10 10h4M3 17h4l10-10h4M18 4l3 3-3 3M18 14l3 3-3 3",
  repeat: "M4 11V9a3 3 0 0 1 3-3h13M17 3l3 3-3 3M20 13v2a3 3 0 0 1-3 3H4M7 21l-3-3 3-3",
  x: "M6 6l12 12M18 6L6 18",
  stop: "M7 7h10v10H7z",
  info: "M12 3a9 9 0 1 0 0 18 9 9 0 1 0 0-18zM12 11v6M12 7.5v.01",
  users:
    "M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 19c0-3 3-5 6-5s6 2 6 5M16 5a3 3 0 0 1 0 6M18 14c2 .5 3 2 3 5",
  // What a tile shows while its image loads, and when it has none.
  movie: "M4 4h16v16H4zM8 4v16M16 4v16M4 9h4M4 15h4M16 9h4M16 15h4",
  show: "M3 5h18v13H3zM8 21h8",
  album: "M12 3a9 9 0 1 0 0 18 9 9 0 1 0 0-18zM12 10a2 2 0 1 0 0 4 2 2 0 1 0 0-4z",
  artist: "M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zM5 11a7 7 0 0 0 14 0M12 18v3",
  song: "M9 18V5l11-2v13M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0zM20 16a3 3 0 1 1-6 0 3 3 0 0 1 6 0z",
  channel: "M3 5h18v11H3zM8 20h8M12 16v4",
  video: "M3 6h18v12H3zM10 9l5 3-5 3z",
  playlist: "M4 6h16M4 11h16M4 16h8M17 20a2 2 0 1 0 0-4 2 2 0 1 0 0 4zM19 18v-6l3 1",
  station:
    "M12 10a2 2 0 1 0 0 4 2 2 0 1 0 0-4zM7.5 8a6 6 0 0 0 0 8M16.5 8a6 6 0 0 1 0 8M4.5 5a10 10 0 0 0 0 14M19.5 5a10 10 0 0 1 0 14",
  collection: "M8 8h12v12H8zM4 16V4h12",
  // A hand-made order, and the handle that moves a row within one.
  order: "M8 20V4M4 8l4-4 4 4M16 4v16M12 16l4 4 4-4",
  grip: "M5 9h14M5 15h14",
  person: "M12 4a4 4 0 1 0 0 8 4 4 0 1 0 0-8zM4 20c0-4 4-6 8-6s8 2 8 6",
  house: "M3 11 12 4l9 7v9H3z",
  sparkle:
    "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8zM19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z",
  star: "M12 3l2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-2.9-5.6 2.9 1.1-6.2L3 9.6l6.2-.9z",
};
export function icon(name: string, size = 20): SVGElement {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("fill", ["play", "pause", "stop"].includes(name) ? "currentColor" : "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "2");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  const p = document.createElementNS(NS, "path");
  p.setAttribute("d", PATHS[name] ?? "");
  svg.append(p);
  return svg;
}

/** The brand mark: rabbit ears over a set. */
export const logo = (size = 24) => {
  const svg = icon("", size);
  svg.querySelector("path")!.setAttribute("d", "M12 13 6 3M12 13l6-10");
  const r = document.createElementNS(NS, "rect");
  for (const [k, v] of Object.entries({ x: 4, y: 13, width: 16, height: 8, rx: 2 }))
    r.setAttribute(k, String(v));
  svg.append(r);
  svg.style.color = "var(--amber)";
  return svg;
};

/** The server's name names the place the person is in; the product's own name stands in until it is known. */
let place = "Rabbit Ears";
export const serverName = () => place;
export const nameServer = (name?: string) => void (document.title = place = name || "Rabbit Ears");

/** The heading that names the place, optionally with the mark and a link home. */
export const brand = (mark = true, home = true) =>
  h(
    home ? "a.brand" : "span.brand",
    { href: home ? "#/" : undefined, title: place },
    mark && logo(),
    h("span", place),
  );

export const art = (i: Item, cls: string) =>
  h(`div.art.${cls}`, {
    "aria-hidden": "true",
    style: `background:${color(i.id)}`,
    "data-art": artKey(i, cls === "thumb" || cls === "w"),
    "data-ph": placeholderKind(i.kind),
  });

/** A play arrow in a translucent circle, centered over a tile's art; the tile plays when activated. */
export const playmark = () => h("span.playmark", icon("play", 20));

/** The icon a tile of this kind shows over its color until its image has loaded, or for good. */
export const placeholder = (kind: PlaceholderKind) => h("span", icon(kind, 40));

/** A poster linking to the item's details, title and caption beneath. */
export const tile = (i: Item, caption: string) =>
  h(
    "a.poster",
    { href: `#/${i.kind}/${i.id}` },
    art(i, "p"),
    h("b", title(i)),
    h("span.sub", caption),
  );

/** A colored circle, painted with the headshot when the provider has one, name and role below. */
export const person = (id: string, name: string, photo: boolean, role?: string) =>
  h(
    "a.person",
    { href: `#/person/${id}` },
    h("span.face", {
      "aria-hidden": "true",
      style: `background:${color(id)}`,
      "data-art": photo ? `person/${id}` : undefined,
      "data-ph": "person",
    }),
    h("b", name),
    role ? h("span.sub", role) : null,
  );

/** A button that is only an icon, named by label. */
export const ib = (label: string, ic: string, on: () => void, size = 24) =>
  h(
    "button.pb",
    { type: "button", "aria-label": label, title: label, onclick: on },
    icon(ic, size),
  );

export const back = (label: string, to: string) =>
  h("a.back", { href: to }, icon("back", 16), label);

export const bar = (pct: number) =>
  h("div.bar", h("div", { style: `width:${Math.min(100, Math.max(0, pct))}%` }));

/** The bar for a resume at fraction f of the runtime, named for assistive tech; none for undefined. */
export const watched = (f?: number) => {
  if (f === undefined) return null;
  const b = bar(f * 100);
  b.setAttribute("role", "img");
  b.setAttribute("aria-label", `${Math.round(f * 100)} percent watched`);
  return b;
};

/** a with watched(f) along its bottom, and exposed then so the bar's name is read. */
export const withWatched = (a: HTMLElement, f?: number) => {
  const b = watched(f);
  if (b) {
    a.removeAttribute("aria-hidden");
    a.append(b);
  }
  return a;
};

/** The profile's initial on its color, painted over with its Gravatar when the server has one. */
export const avatar = (
  p: Pick<Schemas["Account"], "id" | "name" | "color" | "avatarUrl">,
  size: number,
) =>
  h(
    `span.avatar`,
    {
      style: `width:${size}px;height:${size}px;background:${p.color ?? "var(--card)"};font-size:${size * 0.42}px`,
      "aria-hidden": "true",
      "data-art": p.avatarUrl ? `profile/${p.id}` : undefined,
    },
    p.name.slice(0, 1).toUpperCase(),
  );

/** The eight profile colors as one radio group; on gets the picked hex. */
export function swatches(value: string | undefined, on: (hex: string) => void) {
  const name = `sw-${Math.random().toString(36).slice(2, 8)}`;
  return h(
    "div.swatches",
    { role: "radiogroup", "aria-label": "Color" },
    ...PALETTE.map(([hex, label]) =>
      h("input", {
        type: "radio",
        name,
        value: hex,
        "aria-label": label,
        title: label,
        checked: hex === value?.toUpperCase(),
        style: `background:${hex}`,
        onchange: () => on(hex),
      }),
    ),
  );
}

/**
 * Wraps a click handler that writes, so a failure shows as a toast and the action does not
 * look done.
 */
export const guard = (run: (e: Event) => unknown) => async (e: Event) => {
  try {
    await run(e);
  } catch (x) {
    toast((x as Error).message);
  }
};

/** A menu row that goes to href or runs on; checked makes it a checkbox row. */
export const opt = (name: string, to: string | (() => unknown), checked?: boolean) =>
  h(
    typeof to === "string" ? "a.opt" : "button.opt",
    {
      type: typeof to === "string" ? undefined : "button",
      href: typeof to === "string" ? to : undefined,
      role: checked === undefined ? "menuitem" : "menuitemcheckbox",
      "aria-checked": checked === undefined ? undefined : String(checked),
      onclick: typeof to === "string" ? undefined : guard(to),
    },
    checked === undefined ? null : h("span.chk", checked ? icon("check", 14) : null),
    h("span.grow", name),
  );
export const rule = () => h("div.rule", { role: "separator" });

/**
 * Menu rows for the household's kids on a manual allow list, each checked while its list holds
 * itemId; none for a kid active or a household with no such kid.
 */
export async function allowedRows(ctx: Ctx, itemId: string): Promise<HTMLElement[]> {
  const kids =
    ctx.profile.kind === "adult"
      ? ctx.me.profiles.filter((p) => p.kind === "kid" && p.filterMode === "allow_list")
      : [];
  if (!kids.length) return [];
  const { profileIds } = await ok(
    ctx.api.GET("/items/{itemId}/allowed-for", { params: { path: { itemId } } }),
  );
  return [
    h("div.cap", { role: "presentation" }, "Allowed for kids on a list"),
    ...kids.map((k) => {
      let on = profileIds.includes(k.id);
      const o = opt(
        k.name,
        async () => {
          const p = { params: { path: { profileId: k.id, itemId } } };
          await ok(
            on
              ? ctx.api.DELETE("/profiles/{profileId}/allowed-items/{itemId}", p)
              : ctx.api.PUT("/profiles/{profileId}/allowed-items/{itemId}", p),
          );
          on = !on;
          o.setAttribute("aria-checked", String(on));
          o.querySelector(".chk")!.replaceChildren(...(on ? [icon("check", 14)] : []));
        },
        on,
      );
      return o;
    }),
  ];
}

/**
 * trigger opens rows as a menu beneath it. A row that is a plain button, such as Add to
 * playlist, becomes a menu row. A pick, Escape or a click elsewhere closes it; Escape
 * returns focus to trigger, a pick leaves it to whatever the pick opens.
 */
export function menu(trigger: HTMLElement, label: string, rows: (HTMLElement | null | false)[]) {
  const m = h("div.menu.drop", { role: "menu", "aria-label": label, hidden: true });
  for (const r of rows) {
    if (!r) continue;
    if (!r.getAttribute("role")) {
      r.className = "opt";
      r.setAttribute("role", "menuitem");
    }
    m.append(r);
  }
  const wrap = h("div.dropwrap", trigger, m);
  const items = () => [...m.querySelectorAll<HTMLElement>("[role^=menuitem]")];
  const outside = (e: Event) => !wrap.contains(e.target as Node) && close(false);
  const close = (refocus: boolean) => {
    m.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    document.removeEventListener("click", outside);
    if (refocus) trigger.focus();
  };
  trigger.setAttribute("aria-haspopup", "menu");
  trigger.setAttribute("aria-expanded", "false");
  trigger.addEventListener("click", () => {
    if (!m.hidden) return close(true);
    m.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    items()[0]?.focus();
    document.addEventListener("click", outside);
  });
  m.addEventListener("click", (e) => {
    if ((e.target as Element).closest("[role^=menuitem]")) close(false);
  });
  m.addEventListener("keydown", (e) => {
    if (e.key === "Escape") return close(true);
    const by = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (!by) return;
    e.preventDefault();
    const all = items();
    all[
      (all.indexOf(document.activeElement as HTMLElement) + by + all.length) % all.length
    ]?.focus();
  });
  return wrap;
}

/** A dialog form's Cancel and its submit button, labeled. */
export const formEnd = (close: () => void, label: string) =>
  h(
    "div.end",
    h("button.btn.s", { type: "button", onclick: close }, "Cancel"),
    h("button.btn.p", { type: "submit" }, label),
  );

export const btn = (label: string, ic: string, cls: string, on: () => unknown) =>
  h(`button.btn.${cls}`, { type: "button", onclick: guard(on) }, icon(ic, 16), label);

export const meta = (...bits: (string | Node | false | undefined)[]) =>
  h("div.meta", ...bits.filter(Boolean).map((b) => (typeof b === "string" ? h("span", b) : b!)));

export function chip(label: string, pressed: boolean, on: () => void) {
  return h("button.chip", { type: "button", "aria-pressed": String(pressed), onclick: on }, label);
}

/** A page's views as a row of links, each its own route; none for a single view. */
export function viewTabs(views: ([label: string, href: string, current: boolean] | false)[]) {
  const tabs = views.filter((v) => v !== false);
  if (tabs.length < 2) return [];
  return [
    h(
      "nav.vt",
      { "aria-label": "View" },
      ...tabs.map(([label, href, current]) =>
        h("a", { href, "aria-current": current ? "page" : undefined }, label),
      ),
    ),
  ];
}

export function sel(label: string, options: string[], value: string, on: (v: string) => void) {
  const s = h("select.in", { onchange: () => on(s.value) }) as HTMLSelectElement;
  for (const o of options) s.append(h("option", { value: o, selected: o === value }, o));
  return h("label.sel", label, s);
}

/** The mark beside a smart list's name. */
export const smartBadge = () =>
  h("span.smart", { role: "img", "aria-label": "Smart", title: "Smart" }, icon("sparkle", 18));

export const section = (label: string, ...kids: (Node | string)[]) =>
  h("section", { "aria-label": label }, ...kids);

export const empty = (ctx: { me: { account: { isAdmin: boolean } } }) =>
  h(
    "p.sub",
    ctx.me.account.isAdmin
      ? "Nothing here yet. Add a library in the admin console"
      : "Nothing here yet.",
  );

/** Re-runs the current route; the router listens for hashchange. */
export const refresh = () => window.dispatchEvent(new HashChangeEvent("hashchange"));
