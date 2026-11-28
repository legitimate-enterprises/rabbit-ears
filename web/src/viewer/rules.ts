// The rule editor of smart playlists, collections and station content: and/or groups of
// conditions over the fields the server's catalog gives each kind.
import { dialog, errorLine, field, h, input, showError } from "../admin/dom";
import { ApiError } from "../admin/api";
import { MOVIE_RATINGS, TV_RATINGS, type Schemas } from "../api/client";
import { formEnd } from "./ui";

type Rules = Schemas["SmartRules"];
type Condition = Schemas["SmartCondition"];
type Field = Schemas["SmartField"];
type Operator = Schemas["SmartOperator"];
type Match = Schemas["SmartMatch"];
type Type = "text" | "number" | "bool";
/** A playlist kind, or "collection": movie fields without the owner's. */
export type RulesKind = Schemas["PlaylistKind"] | "collection";
export interface Draft {
  name: string;
  kind: RulesKind;
  rules: Rules;
}

const OPERATORS: Record<Type, [Operator, string][]> = {
  text: [
    ["is", "is"],
    ["isNot", "is not"],
    ["contains", "contains"],
    ["notContains", "does not contain"],
    ["startsWith", "starts with"],
    ["endsWith", "ends with"],
  ],
  number: [
    ["is", "="],
    ["isNot", "≠"],
    ["lt", "less than"],
    ["le", "at most"],
    ["gt", "more than"],
    ["ge", "at least"],
  ],
  bool: [
    ["isTrue", "Yes"],
    ["isFalse", "No"],
  ],
};
const FIELDS: Record<Field, { label: string; type: Type; unit?: string }> = {
  title: { label: "Title", type: "text" },
  show: { label: "Show", type: "text" },
  season: { label: "Season", type: "number", unit: "season" },
  episode: { label: "Episode", type: "number", unit: "episode" },
  artist: { label: "Artist", type: "text" },
  album: { label: "Album", type: "text" },
  year: { label: "Year", type: "number" },
  runtime: { label: "Runtime", type: "number", unit: "min" },
  duration: { label: "Duration", type: "number", unit: "s" },
  contentRating: { label: "Content rating", type: "text" },
  genre: { label: "Genre", type: "text" },
  cast: { label: "Cast", type: "text" },
  director: { label: "Director", type: "text" },
  hdr: { label: "HDR", type: "bool" },
  uhd: { label: "4K", type: "bool" },
  explicit: { label: "Explicit", type: "bool" },
  lossless: { label: "Lossless", type: "bool" },
  watched: { label: "Watched", type: "bool" },
  playCount: { label: "Plays", type: "number" },
  favorite: { label: "Favorite", type: "bool" },
};
/** The owner's own data, which collections, shared by a household, never read. */
const PERSONAL: Field[] = ["watched", "playCount", "favorite"];
const MOVIE: Field[] = [
  "title",
  "year",
  "runtime",
  "contentRating",
  "genre",
  "cast",
  "director",
  "hdr",
  "uhd",
  ...PERSONAL,
];
const CATALOG: Record<RulesKind, Field[]> = {
  movies: MOVIE,
  tv: [
    "title",
    "show",
    "season",
    "episode",
    "year",
    "runtime",
    "contentRating",
    "genre",
    "cast",
    "hdr",
    "uhd",
    ...PERSONAL,
  ],
  music: [
    "title",
    "artist",
    "album",
    "year",
    "genre",
    "duration",
    "explicit",
    "lossless",
    "playCount",
    "favorite",
  ],
  collection: MOVIE.filter((f) => !PERSONAL.includes(f)),
};
const KINDS: [Schemas["PlaylistKind"], string][] = [
  ["movies", "Movies"],
  ["tv", "TV"],
  ["music", "Music"],
];
const MAX_GROUPS = 20;
const MAX_CONDITIONS = 50;

interface Row {
  field: Field;
  operator: Operator;
  value: string;
  err: HTMLElement;
}
interface Group {
  match: Match;
  rows: Row[];
}

const blank = (kind: RulesKind): Row => {
  const f = CATALOG[kind][0];
  return { field: f, operator: OPERATORS[FIELDS[f].type][0][0], value: "", err: errorLine() };
};

function choose(
  label: string,
  options: [string, string][],
  value: string,
  on: (v: string) => void,
) {
  const s = h(
    "select.in",
    { "aria-label": label, onchange: () => on(s.value) },
    ...options.map(([v, l]) => h("option", { value: v, selected: v === value }, l)),
  ) as HTMLSelectElement;
  return s;
}
const matchPick = (value: Match, on: (m: Match) => void) =>
  choose(
    "Match",
    [
      ["all", "all"],
      ["any", "any"],
    ],
    value,
    (v) => on(v as Match),
  );

/** The message for a row that would be refused, or undefined. */
function check(r: Row): string | undefined {
  const t = FIELDS[r.field].type;
  if (t === "text" && !r.value.trim()) return "Enter a value.";
  if (t === "number" && !/^-?\d+$/.test(r.value.trim())) return "Enter a whole number.";
}

function condition(r: Row): Condition {
  const t = FIELDS[r.field].type;
  const c: Condition = { field: r.field, operator: r.operator };
  if (t === "text") c.text = r.value.trim();
  if (t === "number") c.number = Number(r.value.trim());
  return c;
}

/**
 * The editor sheet. save gets the draft once every row passes the client checks; an ApiError
 * it throws with fields such as groups.0.conditions.2.text shows under that row, and the
 * sheet stays open until save resolves.
 */
export function rulesDialog(o: {
  heading: string;
  kind: RulesKind;
  /** The list's name; undefined leaves the field out, as for a station's own playlist. */
  name?: string;
  /** choose: a new playlist picks its kind; locked: shown, fixed; none: not shown. */
  kindChoice?: "choose" | "locked" | "none";
  rules?: Rules;
  save: (d: Draft) => Promise<unknown>;
}) {
  let kind = o.kind;
  let match: Match = o.rules?.match ?? "all";
  const groups: Group[] = o.rules?.groups.map((g) => ({
    match: g.match,
    rows: g.conditions.map((c) => ({
      field: c.field,
      operator: c.operator,
      value: c.text ?? (c.number === undefined ? "" : String(c.number)),
      err: errorLine(),
    })),
  })) ?? [{ match: "all", rows: [blank(kind)] }];
  const general = errorLine();
  const nameErr = errorLine();
  const name =
    o.name === undefined
      ? undefined
      : input("text", { name: "name", maxLength: 100, value: o.name });
  const choice = o.kindChoice ?? "none";
  const kindSel =
    choice === "none"
      ? undefined
      : choose("Kind", KINDS, kind, (v) => {
          kind = v as RulesKind;
          for (const g of groups)
            g.rows = g.rows.map((r) => (CATALOG[kind].includes(r.field) ? r : blank(kind)));
          draw();
        });
  if (kindSel) kindSel.disabled = choice === "locked";
  const ratings = `r-${Math.random().toString(36).slice(2, 8)}`;
  const suggestions = h(
    "datalist",
    { id: ratings },
    ...[...MOVIE_RATINGS, ...TV_RATINGS].map((r) => h("option", r)),
  );
  const head = h("p.matchline");
  const cards = h("div.rulecards");
  const note = h("p.sub");
  const addGroup = h(
    "button.btn.s",
    {
      type: "button",
      onclick: () => (groups.push({ match: "all", rows: [blank(kind)] }), draw()),
    },
    "+ Group",
  ) as HTMLButtonElement;
  const total = () => groups.reduce((n, g) => n + g.rows.length, 0);

  const rowEl = (g: Group, r: Row): HTMLElement => {
    const t = FIELDS[r.field].type;
    const fieldSel = choose(
      "Field",
      CATALOG[kind].map((f) => [f, FIELDS[f].label]),
      r.field,
      (v) => {
        const was = FIELDS[r.field].type;
        r.field = v as Field;
        const now = FIELDS[r.field].type;
        r.operator = OPERATORS[now][0][0];
        if (now !== was) r.value = "";
        const next = rowEl(g, r);
        el.replaceWith(next);
        next.querySelector("select")!.focus();
        paintNote();
      },
    );
    const value =
      t === "bool"
        ? null
        : (input("text", {
            "aria-label": "Value",
            value: r.value,
            inputMode: t === "number" ? "numeric" : undefined,
            maxLength: t === "text" ? 200 : undefined,
            list: r.field === "contentRating" ? ratings : undefined,
            oninput: () => (r.value = value!.value),
          }) as HTMLInputElement);
    const unit = FIELDS[r.field].unit;
    const remove = h(
      "button.pb",
      {
        type: "button",
        "aria-label": "Remove condition",
        title: "Remove condition",
        onclick: () => {
          g.rows.splice(g.rows.indexOf(r), 1);
          if (!g.rows.length) groups.splice(groups.indexOf(g), 1);
          draw();
        },
      },
      "−",
    );
    remove.hidden = total() === 1;
    const el = h(
      "div.rulerow",
      h(
        "div.conds",
        { role: "group", "aria-label": `Condition ${g.rows.indexOf(r) + 1}` },
        fieldSel,
        choose("Operator", OPERATORS[t], r.operator, (v) => (r.operator = v as Operator)),
        value,
        unit ? h("span.unit.sub", unit) : null,
        remove,
      ),
      r.err,
    );
    return el;
  };
  const paintNote = () => {
    const used = groups.some((g) => g.rows.some((r) => PERSONAL.includes(r.field)));
    note.textContent = used ? "Watched, plays and favorites are yours, the owner's." : "";
    note.hidden = !used;
  };
  const draw = () => {
    const one = groups.length === 1;
    head.replaceChildren(
      "Match ",
      one
        ? matchPick(groups[0].match, (m) => (groups[0].match = m))
        : matchPick(match, (m) => (match = m)),
      one ? " of these conditions" : " of these groups",
    );
    cards.replaceChildren(
      ...groups.map((g, i) =>
        h(
          "div.rulecard",
          { role: "group", "aria-label": `Group ${i + 1}` },
          one
            ? null
            : h(
                "p.cardline",
                matchPick(g.match, (m) => (g.match = m)),
                " of:",
              ),
          ...g.rows.map((r) => rowEl(g, r)),
          total() < MAX_CONDITIONS
            ? h(
                "button.btn.s",
                { type: "button", onclick: () => (g.rows.push(blank(kind)), draw()) },
                "+ Condition",
              )
            : null,
        ),
      ),
    );
    addGroup.hidden = groups.length >= MAX_GROUPS || total() >= MAX_CONDITIONS;
    paintNote();
  };
  draw();

  const submit = h(
    "form.rules",
    {
      onsubmit: (ev: Event) => {
        ev.preventDefault();
        showError(general);
        showError(nameErr, name && !name.value.trim() ? "Enter a name." : undefined);
        let bad = !!name && !name.value.trim();
        for (const g of groups)
          for (const r of g.rows) {
            const m = check(r);
            showError(r.err, m);
            bad ||= !!m;
          }
        if (bad) return;
        const one = groups.length === 1;
        o.save({
          name: name?.value.trim() ?? "",
          kind,
          rules: {
            match: one ? "all" : match,
            groups: groups.map((g) => ({ match: g.match, conditions: g.rows.map(condition) })),
          },
        }).then(d.close, (x: Error) => {
          const rest: string[] = [];
          for (const f of x instanceof ApiError ? x.fields : []) {
            const at = /^groups\.(\d+)\.conditions\.(\d+)\b/.exec(f.field);
            const r = at && groups[Number(at[1])]?.rows[Number(at[2])];
            if (r) showError(r.err, f.message);
            else rest.push(`${f.field}: ${f.message}`);
          }
          if (!(x instanceof ApiError) || !x.fields.length) rest.push(x.message);
          showError(general, rest.join("; ") || undefined);
        });
      },
    },
    name ? field("Name", name) : null,
    name ? nameErr : null,
    kindSel ? h("label.sel", "Kind", kindSel) : null,
    head,
    cards,
    addGroup,
    note,
    suggestions,
    general,
    formEnd(() => d.close(), "Save"),
  );
  // The rules are written online only.
  if (!navigator.onLine)
    submit.querySelector<HTMLButtonElement>("button[type=submit]")!.disabled = true;
  const d = dialog(o.heading, submit);
  return d;
}

/**
 * Makes a list, then gives it rules; a list whose rules fail is deleted again, so the editor
 * stays open on nothing half made.
 */
export async function createSmart<T extends { id: string }>(
  make: () => Promise<T>,
  rules: (id: string) => Promise<unknown>,
  remove: (id: string) => Promise<unknown>,
): Promise<T> {
  const made = await make();
  try {
    await rules(made.id);
  } catch (x) {
    await remove(made.id).catch(() => {});
    throw x;
  }
  return made;
}
