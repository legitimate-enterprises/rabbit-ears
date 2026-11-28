// Stations: continuous programming from one playlist. The server decides the
// order and what is on; these pages show onAir as given and play what tuning
// in hands back.
import { ApiError, ok, paged } from "../../admin/api";
import { dialog, errorLine, h, input, showError } from "../../admin/dom";
import type { Schemas } from "../../api/client";
import { music as engine } from "../audio";
import { imageButtons, imageKey } from "../coverImage";
import { color, type Ctx } from "../data";
import { deviceSnapshot } from "../device";
import { KIND, minutes, plural, TIMING, VIS } from "../format";
import {
  stationKey,
  caption,
  clockAt,
  fieldOf,
  formOf,
  inputOf,
  localDate,
  onNowFraction,
  onNowName,
  programTitle,
  timingOf,
  tuneIn,
  untilMs,
  type OnAir,
  type Station,
  type Weekday,
} from "../stations";
import { back, bar, brand, playmark, chip, guard, icon, logo, refresh, section } from "../ui";
import { rulesDialog } from "../rules";
import { cannotPlay, player } from "./player";
import { askReschedule, confirmDelete, editable, entryRows } from "./playlists";

type Kind = Schemas["PlaylistKind"];
const DAYS: [Weekday, string][] = [
  ["monday", "Mon"],
  ["tuesday", "Tue"],
  ["wednesday", "Wed"],
  ["thursday", "Thu"],
  ["friday", "Fri"],
  ["saturday", "Sat"],
  ["sunday", "Sun"],
];
const stationsHref = (k: Kind) => `#/${k}?view=stations`;
const path = (stationId: string) => ({ params: { path: { stationId } } });

/**
 * The station's own image, else the art of what is on, else of what is next, else its own tile
 * with its name.
 */
const stationArt = (s: Station, cls: string, ...over: (HTMLElement | null)[]) => {
  const key = stationKey(s);
  return key
    ? h(
        `div.art.${cls}`,
        {
          "aria-hidden": "true",
          style: `background:${color(s.id)}`,
          "data-art": key,
          "data-ph": "station",
        },
        ...over,
      )
    : h(`div.art.${cls}.ident`, { "aria-hidden": "true" }, logo(28), h("span", s.name), ...over);
};

/** 16:9, square for music, captioned with what is on; on Home, named with its kind and barred. */
const stationTile = (s: Station, home = false) => {
  const done = home ? onNowFraction(s.onAir) : undefined;
  return h(
    s.kind === "music" ? "a.poster" : "a.wide",
    home
      ? {
          href: `#/tune/${s.id}`,
          "aria-label": `${s.kind === "music" ? "Listen to" : "Watch"} ${onNowName(s)}`,
        }
      : { href: `#/station/${s.id}` },
    stationArt(
      s,
      s.kind === "music" ? "sq" : "st",
      ...(home ? [playmark()] : []),
      done === undefined ? null : bar(done * 100),
    ),
    h("b", home ? onNowName(s) : s.name),
    ...caption(s).map((l) => h("span.sub", l)),
  );
};

const list = (ctx: Ctx, kind?: Kind) =>
  paged((offset) =>
    ok(ctx.api.GET("/stations", { params: { query: { kind, limit: 200, offset } } })),
  );

/** A kind's stations as a grid, and for an adult a New station link. */
export async function stationTiles(ctx: Ctx, kind: Kind) {
  const all = await list(ctx, kind);
  return {
    count: plural(all.length, "station"),
    grid: all.length
      ? h(kind === "music" ? "div.grid.sq" : "div.grid.st", ...all.map((s) => stationTile(s)))
      : h("p.sub", "No stations yet."),
    create:
      ctx.profile.kind === "adult"
        ? h("a.btn.s", { href: `#/station-new?kind=${kind}` }, icon("plus", 16), "New station")
        : null,
  };
}

/** Every station the profile sees, a section per kind. */
export async function stations(ctx: Ctx) {
  const all = await list(ctx);
  const kinds = (Object.keys(KIND) as Kind[]).filter((k) => all.some((s) => s.kind === k));
  return [
    h("div.titlerow", h("h1", "Stations"), h("span.sub", plural(all.length, "station"))),
    ...(kinds.length
      ? kinds.map((k) =>
          section(
            KIND[k],
            h("h2.sec", KIND[k]),
            h(
              k === "music" ? "div.grid.sq" : "div.grid.st",
              ...all.filter((s) => s.kind === k).map((s) => stationTile(s)),
            ),
          ),
        )
      : [h("p.sub", "No stations yet.")]),
  ];
}

/** Home's On now: every station with something on, kinds mixed; none when the call fails. */
export const onNowTiles = (ctx: Ctx) =>
  list(ctx).then(
    (all) => all.filter((s) => s.onAir.status === "on_air").map((s) => stationTile(s, true)),
    () => [],
  );

export async function stationPage(ctx: Ctx, [id]: string[]) {
  const stationId = id;
  const s = await ok(ctx.api.GET("/stations/{stationId}", path(stationId)));
  // shortcut: a music station's day is hundreds of songs and the schedule has no "from now"
  // filter, so its page shows only what is on and next; list it once the server can page from now.
  const music = s.kind === "music";
  const programs = music
    ? []
    : await paged((offset) =>
        ok(
          ctx.api.GET("/stations/{stationId}/schedule", {
            params: { path: { stationId }, query: { date: localDate(), limit: 200, offset } },
          }),
        ),
      );
  // Only the owner reads the station's playlist: it lives under their profile, theirs or shared
  // with them, and they edit it as that playlist allows.
  const own = { params: { path: { profileId: ctx.profile.id, playlistId: s.playlistId } } };
  const pl =
    s.ownerId === ctx.profile.id
      ? await ok(ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}", own))
      : undefined;
  const contents = pl
    ? await entryRows(
        ctx,
        s.playlistId,
        (_e, href) => href && ctx.go(href),
        editable(ctx, pl),
        () => askReschedule(ctx, s.id, refresh),
      )
    : undefined;
  // Rules belong to the playlist's owner, so a station on a playlist shared with its owner has none to set.
  const rules = (current?: Schemas["SmartRules"]) =>
    rulesDialog({
      heading: "Station rules",
      kind: s.kind,
      rules: current,
      save: async (d) => {
        await ok(
          ctx.api.PUT("/profiles/{profileId}/playlists/{playlistId}/rules", {
            ...own,
            body: d.rules,
          }),
        );
        askReschedule(ctx, s.id, refresh);
      },
    });
  const useRules = () => {
    const d = dialog(
      "Use rules?",
      h("p.sub", "Replaces this station's titles with those the rules match."),
      h(
        "div.end",
        h("button.btn.s", { type: "button", onclick: () => d.close() }, "Cancel"),
        h("button.btn.p", { type: "button", onclick: () => (d.close(), rules()) }, "Continue"),
      ),
    );
  };
  const rulesButton =
    pl?.access !== "owner"
      ? null
      : s.smart
        ? h(
            "button.btn.s",
            {
              type: "button",
              onclick: guard(async () =>
                rules(
                  await ok(ctx.api.GET("/profiles/{profileId}/playlists/{playlistId}/rules", own)),
                ),
              ),
            },
            "Edit rules…",
          )
        : h("button.btn.s", { type: "button", onclick: useRules }, "Use rules…");
  const on = s.onAir.now;
  const rows = programs.map((p) => {
    const current = !!on && p.startsAt === on.startsAt && p.itemId === on.itemId;
    return h(
      "li",
      { "aria-current": current ? "true" : undefined },
      h("span.when", clockAt(p.startsAt)),
      h("b", programTitle(p)),
      current ? h("span.tag.on", "On now") : null,
      h("span.t", minutes(p.durationMs)),
    );
  });
  return [
    back("Stations", stationsHref(s.kind)),
    h(
      "div.detail",
      stationArt(s, s.kind === "music" ? "sq240" : "st320"),
      h(
        "div.info",
        h("h1.big", s.name),
        h(
          "div.meta",
          h("span", `${KIND[s.kind]} station`),
          h("span.tag", s.rating ?? "None"),
          h("span", VIS[s.visibility]),
        ),
        h(
          "div.actions",
          h("a.btn.p", { href: `#/tune/${s.id}` }, icon("play", 16), "Tune in"),
          s.ownerId === ctx.profile.id
            ? h("a.btn.s", { href: `#/station-edit/${s.id}` }, "Edit station")
            : null,
          rulesButton,
          ...(s.ownerId === ctx.profile.id
            ? imageButtons(
                ctx.api,
                { station: { stationId } },
                { profileId: ctx.profile.id, playlistId: s.playlistId },
                s.imageUrl,
                refresh,
              )
            : []),
        ),
        h("p.sub", caption(s).join(" · ")),
      ),
    ),
    music
      ? null
      : section(
          "Today",
          h("h2.sec", "Today"),
          rows.length ? h("ol.sched", ...rows) : h("p.sub", "Nothing scheduled today."),
        ),
    contents ? section("Contents", h("h2.sec", "Contents"), contents.err, contents.body) : null,
  ].filter((e): e is HTMLElement => !!e);
}

/** The station's own screen between programs and off schedule. */
const offAir = (s: Station, onAir: OnAir) =>
  h(
    "div.player.offair",
    h(
      "div.ph",
      h(
        "button.pb",
        {
          type: "button",
          "aria-label": "Back",
          title: "Back",
          onclick: () => (history.length > 1 ? history.back() : (location.hash = "#/")),
        },
        icon("back", 24),
      ),
      h("b.t", s.name),
    ),
    h(
      "div.idbody",
      s.imageUrl
        ? h("div.art.sq240", { "aria-hidden": "true", "data-art": imageKey(s.imageUrl) })
        : logo(96),
      h("h1", s.name),
      h("p", caption({ kind: s.kind, onAir })[0]),
    ),
  );

/**
 * Tuned in. A video station plays its live stream in the player; off the air it shows its own
 * screen and asks again when the next program starts. A music station plays in the music engine.
 */
export async function tune(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const stationId = id;
  let left = false;
  window.addEventListener("hashchange", () => (left = true), { once: true });
  const s = await ok(ctx.api.GET("/stations/{stationId}", path(stationId)));
  if (left) return [];
  if (s.kind === "music") {
    void engine()?.tune(s);
    // In place of this entry: Back from Now playing must not land here and tune in again.
    location.replace("#/now-playing");
    return [];
  }
  let t;
  try {
    t = await tuneIn(ctx.api, stationId, () => deviceSnapshot());
  } catch (e) {
    return cannotPlay(e);
  }
  // Left while tuning in: the session would play unseen, and the wait for resumesAt would redraw another page.
  if (left) {
    if (t.state === "on_air") void t.pb.end().catch(() => {});
    return [];
  }
  if (t.state === "on_air") return player(ctx, "", new URLSearchParams(), { station: s, pb: t.pb });
  const wait = untilMs(t.resumesAt);
  if (wait !== undefined) {
    const timer = setTimeout(refresh, wait);
    // A hidden tab's timer fires late, and a sleeping device's not at all: back in view, a
    // start already passed asks at once.
    const seen = () =>
      document.visibilityState === "visible" && untilMs(t.resumesAt)! <= 1000 && refresh();
    document.addEventListener("visibilitychange", seen);
    window.addEventListener(
      "hashchange",
      () => (clearTimeout(timer), document.removeEventListener("visibilitychange", seen)),
      { once: true },
    );
  }
  return [offAir(s, t.onAir)];
}

export const newStation = async (ctx: Ctx, _a: string[], q: URLSearchParams) =>
  editor(ctx, undefined, (q.get("kind") ?? "movies") as Kind);

export async function editStation(ctx: Ctx, [id]: string[]) {
  const s = await ok(ctx.api.GET("/stations/{stationId}", path(id)));
  return editor(ctx, s, s.kind);
}

/** The station editor; an adult's only. A new station's kind is chosen here, once. */
function editor(ctx: Ctx, s: Station | undefined, kind: Kind) {
  const f = formOf(s, kind);
  const back = s ? `#/station/${s.id}` : stationsHref(kind);
  const errs: Record<string, HTMLElement> = {};
  const err = errorLine();
  const fieldErr = (name: string) =>
    (errs[name] = h("p.err", { role: "alert", hidden: true, "data-for": name }));
  const labelled = (label: string, name: string, el: HTMLElement, hint?: string) => {
    el.id = `st-${name}`;
    return h(
      "div.field",
      h("label", { htmlFor: el.id }, label),
      el,
      hint ? h("span.sub.hint", hint) : null,
      fieldErr(name),
    );
  };
  const select = (name: string, options: [string, string][], value?: string | number) =>
    h(
      "select.in",
      { name },
      ...options.map(([v, l]) => h("option", { value: v, selected: String(value) === v }, l)),
    ) as HTMLSelectElement;

  const name = input("text", { name: "name", required: true, maxLength: 100, value: f.name });
  const kindSel = s
    ? undefined
    : select(
        "kind",
        (Object.keys(KIND) as Kind[]).map((k) => [k, KIND[k]]),
        f.kind,
      );
  const order = select(
    "order",
    [
      ["playlist", "In order"],
      ["random", "Shuffled"],
    ],
    f.order,
  );
  const timing = select(
    "timing",
    (Object.keys(TIMING) as (keyof typeof TIMING)[]).map((t) => [t, TIMING[t]]),
    f.timing,
  );
  // A new station takes its kind's timing until one is picked.
  kindSel?.addEventListener("change", () => {
    if (!timing.dataset.picked) timing.value = timingOf(kindSel.value as Kind);
  });
  timing.addEventListener("change", () => (timing.dataset.picked = "1"));
  const server = f.visibility === "server";
  const visibility = server
    ? input("text", { name: "visibility", readOnly: true, value: "Server" })
    : select(
        "visibility",
        [
          ["private", "Private"],
          ["household", "Household"],
        ],
        f.visibility,
      );

  const days = h("div.chips", { role: "group", "aria-label": "Days" });
  const drawDays = () =>
    days.replaceChildren(
      ...DAYS.map(([d, label]) =>
        chip(label, f.days.includes(d), () => {
          const on = new Set(f.days);
          if (on.has(d)) on.delete(d);
          else on.add(d);
          f.days = DAYS.map(([x]) => x).filter((x) => on.has(x));
          drawDays();
        }),
      ),
    );
  const times = h("div.ranges");
  const drawTimes = () =>
    times.replaceChildren(
      ...f.times.map((t, i) => {
        const at = (k: "from" | "to", label: string) =>
          input("time", {
            value: t[k],
            "aria-label": label,
            oninput: (e: Event) => (t[k] = (e.target as HTMLInputElement).value),
          });
        return h(
          "div.range",
          at("from", "From"),
          h("span.sub", "to"),
          at("to", "To"),
          h(
            "button.pb",
            {
              type: "button",
              "aria-label": "Remove time range",
              title: "Remove",
              onclick: () => (f.times.splice(i, 1), drawTimes()),
            },
            icon("x", 16),
          ),
        );
      }),
      h(
        "button.link",
        { type: "button", onclick: () => (f.times.push({ from: "", to: "" }), drawTimes()) },
        "Add a time range",
      ),
    );
  drawDays();
  drawTimes();
  const chosen = h(
    "div.chosen",
    { hidden: !f.chosen },
    days,
    fieldErr("days"),
    times,
    fieldErr("times"),
  );
  const radio = (value: "continuous" | "chosen", title: string, sub: string, extra?: HTMLElement) =>
    h(
      "label.opt",
      h("input", {
        type: "radio",
        name: "schedule",
        value,
        checked: (value === "chosen") === f.chosen,
        onchange: () => ((f.chosen = value === "chosen"), (chosen.hidden = !f.chosen)),
      }),
      h("span", h("b", title), h("span.sub", sub), extra),
    );

  // Reads what the browser holds for the fields not tracked as they change.
  const read = () => {
    f.name = name.value;
    if (kindSel) f.kind = kindSel.value as Kind;
    f.order = order.value as typeof f.order;
    f.timing = timing.value as typeof f.timing;
    if (!server) f.visibility = visibility.value as typeof f.visibility;
  };
  /** Saves the form; undefined when refused, with the reasons shown on their fields. */
  const save = async (): Promise<Station | undefined> => {
    read();
    Object.values(errs).forEach((e) => showError(e));
    showError(err);
    const body = inputOf(f);
    try {
      return await ok(
        s
          ? ctx.api.PUT("/stations/{stationId}", { ...path(s.id), body })
          : ctx.api.POST("/stations", { body }),
      );
    } catch (x) {
      const rest: string[] = [];
      if (x instanceof ApiError && x.fields.length)
        for (const fe of x.fields) {
          const el = errs[fieldOf(fe.field)];
          if (el) showError(el, fe.message);
          else rest.push(`${fe.field}: ${fe.message}`);
        }
      else rest.push((x as Error).message);
      if (rest.length) showError(err, rest.join("; "));
    }
  };

  const form = h(
    "form.card.station",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        const st = await save();
        if (!st) return;
        const go = () => ctx.go(`#/station/${st.id}`);
        if (s) askReschedule(ctx, st.id, go);
        else go();
      },
    },
    h("h1", s ? "Edit station" : "New station"),
    h(
      "div.cols",
      h(
        "div.col",
        labelled("Name", "name", name),
        kindSel
          ? labelled(
              "Kind",
              "kind",
              kindSel,
              "Chosen once. Fill it from any title's Add to station.",
            )
          : null,
        h(
          "div.row",
          labelled("Order", "order", order),
          labelled("Visibility", "visibility", visibility),
        ),
        h(
          "span.sub",
          server
            ? "Set by a server admin. Every household sees this station."
            : "Only a server admin can make a station server wide.",
        ),
        s
          ? h(
              "div.rating",
              h(
                "span",
                h("b", "Rating"),
                h("span.sub", "The highest rating in the station. Recalculated daily."),
              ),
              h("span.tag", s.rating ?? "None"),
            )
          : null,
      ),
      h(
        "div.col",
        h(
          "fieldset",
          h("legend", "Schedule"),
          radio("continuous", "Continuous", "On every day, all day."),
          radio(
            "chosen",
            "Chosen days and times",
            "Server time. A program still playing when a time range ends plays to its end.",
            chosen,
          ),
        ),
        labelled("Timing", "timing", timing, "When each program starts after the one before ends."),
      ),
    ),
    err,
    h(
      "div.row",
      s
        ? h(
            "button.btn.s.danger",
            {
              type: "button",
              onclick: () =>
                confirmDelete(
                  `Delete ${s.name}?`,
                  "The station and its schedule go. What it plays stays in your library.",
                  () =>
                    ok(ctx.api.DELETE("/stations/{stationId}", path(s.id))).then(() =>
                      ctx.go(stationsHref(s.kind)),
                    ),
                ),
            },
            "Delete station",
          )
        : h("span"),
      h(
        "div.end",
        h("a.btn.s", { href: back }, "Cancel"),
        h("button.btn.p", { type: "submit" }, "Save"),
      ),
    ),
  );
  return [h("header.top", brand(false)), h("div.whopage", form)];
}
