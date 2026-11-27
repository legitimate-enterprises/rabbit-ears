import type { Api, Schemas } from "../../api/client";
import { watchArt } from "../../viewer/artwork";
import type { Ctx } from "../api";
import { ok, paged } from "../api";
import { confirming, table } from "../common";
import { errorLine, field, h, input, showError, sub, tag } from "../dom";
import { anchorDate, far, longDate, range, season, startOfDay } from "../occasionDates";

type Occasion = Schemas["Occasion"];
type Member = Schemas["OccasionMember"];
type Item = Schemas["Item"];
type Child = Node | string | null | false;

const PREVIEW = 10;
const MAX_KEYWORDS = 100;
const MAX_PHRASES = 200;
const MAX_PHRASE = 60;
// What Add colors starts from without a holiday's own: the viewer's palette, for the admin to change.
const NEW_COLORS = ["#2e6b8a", "#6b3a2e", "#5b6b3f"];
const MONTHS = Array.from({ length: 12 }, (_, i) =>
  new Intl.DateTimeFormat("en-US", { month: "long" }).format(new Date(2028, i, 1)),
);

/** Every member of an occasion, by title. */
export const members = (api: Api, occasionId: string) =>
  paged((offset) =>
    ok(
      api.GET("/occasions/{occasionId}/members", {
        params: { path: { occasionId }, query: { limit: 200, offset } },
      }),
    ),
  );

const put = (el: HTMLElement, ...kids: Child[]) =>
  el.replaceChildren(...kids.filter((k): k is Node | string => !!k));
const nameOf = (i: Item) => i.metadata?.title || i.parsedTitle;
const kindOf = (i: Item) => (i.kind === "show" ? "Show" : "Movie");
const yearOf = (i: Item) => i.metadata?.releaseDate?.slice(0, 4) ?? String(i.parsedYear ?? "");
const thumb = (i: Item) => h("i.th", { "data-art": `${i.id}/poster`, "aria-hidden": "true" });
// A leap year, so February 29 is a day of February.
const dayOfMonth = (month: number, day: number) =>
  new Date(2028, month - 1, day).getMonth() === month - 1;
const whole = (el: HTMLInputElement, min: number, max: number) => {
  const v = el.value.trim();
  const n = Number(v);
  return v !== "" && Number.isInteger(n) && n >= min && n <= max ? n : undefined;
};

export async function occasionEdit(ctx: Ctx, id?: string): Promise<HTMLElement[]> {
  const { api } = ctx;
  const [holidays, saved, found] = await Promise.all([
    ok(api.GET("/holidays")).then((r) => r.items),
    id === undefined
      ? undefined
      : ok(api.GET("/occasions/{occasionId}", { params: { path: { occasionId: id } } })),
    id === undefined ? [] : members(api, id),
  ]);
  const today = startOfDay(new Date());
  const year = today.getFullYear();
  let current = saved;

  const title = h("h1", saved?.name ?? "New occasion");
  const status = h("span");
  const paintHead = (o: Occasion) => {
    const s = season(o, holidays, today);
    put(
      status,
      s.active && tag("Active today", "ok"),
      sub(s.window ? `Shows ${range(s.window, today)}` : `No date for ${year}`),
    );
  };
  if (saved) paintHead(saved);

  const name = input("text", { value: saved?.name ?? "", maxLength: 100 });
  const fixed = saved !== undefined && saved.holiday === undefined;
  const holidayRadio = h("input", { type: "radio", name: "anchor", checked: !fixed });
  const fixedRadio = h("input", {
    type: "radio",
    name: "anchor",
    checked: fixed,
  }) as HTMLInputElement;
  const pick = h(
    "select.in",
    { "aria-label": "Choose a holiday" },
    ...holidays.map((x) =>
      h("option", { value: x.key, selected: x.key === saved?.holiday }, x.name),
    ),
  ) as HTMLSelectElement;
  const holidayDate = sub("");
  const month = h(
    "select.in",
    { "aria-label": "Month" },
    ...MONTHS.map((m, i) =>
      h("option", { value: String(i + 1), selected: saved?.month === i + 1 }, m),
    ),
  ) as HTMLSelectElement;
  const day = input("number", {
    "aria-label": "Day",
    min: 1,
    max: 31,
    value: String(saved?.day ?? ""),
    style: "width:80px",
  });
  const holidayBox = h(
    "div.opt",
    h("label", holidayRadio, "Holiday"),
    h("div.optbody", pick, holidayDate),
  );
  const fixedBox = h(
    "div.opt",
    h("label", fixedRadio, "Fixed date"),
    h("div.optbody", month, day, sub("Every year")),
  );
  const before = input("number", { min: 0, max: 90, value: String(saved?.daysBefore ?? 0) });
  const after = input("number", { min: 0, max: 90, value: String(saved?.daysAfter ?? 0) });
  const shows = sub("");

  const update = () => {
    const fixedNow = fixedRadio.checked;
    pick.disabled = fixedNow;
    month.disabled = day.disabled = !fixedNow;
    holidayBox.classList.toggle("on", !fixedNow);
    fixedBox.classList.toggle("on", fixedNow);
    const date = anchorDate({ holiday: pick.value }, holidays, year);
    holidayDate.textContent = `This year: ${date ? longDate(date, true) : `No date for ${year}`}`;
    const b = whole(before, 0, 90);
    const f = whole(after, 0, 90);
    const m = Number(month.value);
    const d = whole(day, 1, 31);
    // Nothing to show while a field is not yet a valid one.
    const known =
      b !== undefined && f !== undefined && (!fixedNow || (d !== undefined && dayOfMonth(m, d)));
    const w = known
      ? season(
          {
            ...(fixedNow ? { month: m, day: d } : { holiday: pick.value }),
            daysBefore: b,
            daysAfter: f,
          },
          holidays,
          today,
        ).window
      : undefined;
    shows.textContent = !known
      ? ""
      : w
        ? `Shows from ${longDate(w.first, far(w.first, today))} through ${longDate(w.last, far(w.last, today))}.`
        : `No date for ${year}.`;
  };

  let keywords = [...(saved?.keywordIds ?? [])];
  const chips = h("div.chips", { role: "list", "aria-labelledby": "o-kw" });
  const drawChips = () =>
    put(
      chips,
      ...keywords.map((k) =>
        h(
          "span.kw",
          { role: "listitem" },
          String(k),
          h(
            "button.x",
            {
              type: "button",
              "aria-label": `Remove keyword ${k}`,
              onclick: () => {
                keywords = keywords.filter((x) => x !== k);
                drawChips();
              },
            },
            "×",
          ),
        ),
      ),
    );
  drawChips();
  const keyword = h("input.in", {
    "aria-label": "Keyword id to add",
    inputMode: "numeric",
    placeholder: "Keyword id",
    style: "width:160px",
  }) as HTMLInputElement;
  const keywordErr = errorLine();
  const addKeyword = () => {
    const v = keyword.value.trim();
    const n = Number(v);
    if (!/^\d+$/.test(v) || !Number.isSafeInteger(n) || n < 1)
      return showError(keywordErr, "A keyword id is a whole number above 0.");
    if (keywords.includes(n)) return showError(keywordErr, `Keyword ${n} is already listed.`);
    if (keywords.length >= MAX_KEYWORDS)
      return showError(keywordErr, `An occasion holds at most ${MAX_KEYWORDS} keywords.`);
    keywords.push(n);
    keyword.value = "";
    showError(keywordErr);
    drawChips();
  };
  keyword.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    // Enter here adds the keyword; it must not save the form.
    e.preventDefault();
    addKeyword();
  });

  let phrases = [...(saved?.phrases ?? [])];
  const phraseChips = h("div.chips", { role: "list", "aria-labelledby": "o-ph" });
  const drawPhrases = () =>
    put(
      phraseChips,
      ...phrases.map((p) =>
        h(
          "span.phrase",
          { role: "listitem" },
          p,
          h(
            "button.x",
            {
              type: "button",
              "aria-label": `Remove phrase ${p}`,
              onclick: () => {
                phrases = phrases.filter((x) => x !== p);
                drawPhrases();
              },
            },
            "×",
          ),
        ),
      ),
    );
  drawPhrases();
  const phrase = h("input.in", {
    "aria-label": "Phrase to add",
    placeholder: "Ho, Ho, Ho!",
    style: "width:240px",
  }) as HTMLInputElement;
  const phraseErr = errorLine();
  const addPhrase = () => {
    const v = phrase.value.trim();
    if (!v || [...v].length > MAX_PHRASE)
      return showError(phraseErr, `A phrase is 1 to ${MAX_PHRASE} characters.`);
    if (phrases.some((p) => p.toLowerCase() === v.toLowerCase()))
      return showError(phraseErr, `“${v}” is already listed.`);
    if (phrases.length >= MAX_PHRASES)
      return showError(phraseErr, `An occasion holds at most ${MAX_PHRASES} phrases.`);
    phrases.push(v);
    phrase.value = "";
    showError(phraseErr);
    drawPhrases();
  };
  phrase.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    // Enter here adds the phrase; it must not save the form.
    e.preventDefault();
    addPhrase();
  });

  let colors = [...(saved?.colors ?? [])];
  const swatches = h("div.row");
  const drawColors = () =>
    put(
      swatches,
      ...(colors.length
        ? [
            ...colors.map((c, i) =>
              h("input.swatch", {
                type: "color",
                value: c,
                "aria-label": `Color ${i + 1}`,
                oninput: (e: Event) => (colors[i] = (e.target as HTMLInputElement).value),
              }),
            ),
            h(
              "button.ib",
              { type: "button", onclick: () => ((colors = []), drawColors()) },
              "Clear colors",
            ),
          ]
        : [
            sub("None: the occasion's page keeps the plain background."),
            h(
              "button.ib",
              {
                type: "button",
                onclick: () => {
                  const own = fixedRadio.checked
                    ? undefined
                    : holidays.find((x) => x.key === pick.value)?.colors;
                  // Lowercase, as a color input reports what it holds.
                  colors = (own?.length === 3 ? own : NEW_COLORS).map((c) => c.toLowerCase());
                  drawColors();
                },
              },
              "Add colors",
            ),
          ]),
    );
  drawColors();

  const err = errorLine();
  const note = h("span.sub.note");
  const save = h("button.ib.p", { type: "submit" }, "Save") as HTMLButtonElement;
  const submit = async (e: Event) => {
    e.preventDefault();
    note.textContent = "";
    const n = name.value.trim();
    const b = whole(before, 0, 90);
    const f = whole(after, 0, 90);
    const m = Number(month.value);
    const d = whole(day, 1, 31);
    const isFixed = fixedRadio.checked;
    const bad =
      !n || [...n].length > 100
        ? "Name must be 1 to 100 characters."
        : b === undefined
          ? "Days before must be a whole number from 0 to 90."
          : f === undefined
            ? "Days after must be a whole number from 0 to 90."
            : !isFixed && !pick.value
              ? "Choose a holiday."
              : isFixed && (d === undefined || !dayOfMonth(m, d))
                ? "Day must be a day of that month."
                : undefined;
    showError(err, bad);
    if (bad) return;
    const body = {
      name: n,
      ...(isFixed ? { month: m, day: d } : { holiday: pick.value }),
      daysBefore: b!,
      daysAfter: f!,
      keywordIds: [...keywords],
      // A new occasion left without them takes its holiday's own; a replace sets them as shown.
      ...(id === undefined && !phrases.length ? {} : { phrases: [...phrases] }),
      ...(id === undefined && !colors.length ? {} : { colors: [...colors] }),
    };
    save.disabled = true;
    try {
      const out = await ok(
        id === undefined
          ? api.POST("/occasions", { body })
          : api.PUT("/occasions/{occasionId}", { params: { path: { occasionId: id } }, body }),
      );
      if (id === undefined) {
        location.hash = `#/occasions/${out.id}`;
        return;
      }
      current = out;
      title.textContent = out.name;
      paintHead(out);
      note.textContent = "Saved";
    } catch (x) {
      showError(err, (x as Error).message);
    } finally {
      save.disabled = false;
    }
  };

  const form = h(
    "form.stack",
    { noValidate: true, onsubmit: submit },
    h(
      "section.card",
      { "aria-label": "Occasion" },
      h("h2", "Occasion"),
      h(
        "div.occgrid",
        h(
          "div.col",
          field("Name", name, "The occasion page's title, and the Home row's without phrases."),
          h(
            "div.field",
            { role: "radiogroup", "aria-labelledby": "o-anchor" },
            h("span.lb", { id: "o-anchor" }, "Anchor"),
            holidayBox,
            fixedBox,
          ),
        ),
        h(
          "div.col",
          h("div.pair", field("Days before", before), field("Days after", after)),
          shows,
          h(
            "div.field",
            h("span.lb", { id: "o-kw" }, "TMDB keyword ids"),
            chips,
            h("div.row", keyword, h("button.ib", { type: "button", onclick: addKeyword }, "Add")),
            keywordErr,
            sub(
              "Movies and shows tagged with any of these on TMDB join automatically, on the next metadata refresh.",
            ),
          ),
        ),
      ),
      h(
        "div.field",
        h("span.lb", { id: "o-ph" }, "Phrases"),
        phraseChips,
        h("div.row", phrase, h("button.ib", { type: "button", onclick: addPhrase }, "Add phrase")),
        phraseErr,
        sub(
          "Home titles the row with one of these at random, or with the name when there are none.",
        ),
      ),
      h("div.field", h("span.lb", "Colors"), swatches, sub("Washed behind the occasion's page.")),
      id === undefined &&
        sub("A holiday occasion saved without phrases or colors starts with the holiday's own."),
    ),
    saved && titlesCard(api, saved.id, found),
    h(
      "div.row.actions",
      saved &&
        confirming(
          "Delete occasion",
          `Delete ${current!.name}? Its titles added or removed by hand are forgotten with it.`,
          async () => {
            await ok(
              api.DELETE("/occasions/{occasionId}", { params: { path: { occasionId: saved.id } } }),
            );
            location.hash = "#/occasions";
          },
        ),
      note,
      h("a.ib", { href: "#/occasions" }, "Cancel"),
      save,
    ),
    err,
  );
  form.addEventListener("input", update);
  form.addEventListener("change", update);
  update();

  const root = h(
    "div.stack",
    h("a.back", { href: "#/occasions" }, "‹ Occasions"),
    h("div.head", title, status),
    form,
  );
  watchArt(root, api);
  return [root];
}

/** Every member with its source, a search that adds titles by hand, and a remove per row. */
function titlesCard(api: Api, occasionId: string, first: Member[]) {
  let list = first;
  let all = false;
  const heading = h("h2");
  const body = h("div");
  const results = h("div");
  const err = errorLine();
  const path = (i: Item) => ({ params: { path: { occasionId, itemId: i.id } } });

  const draw = () => {
    const byKeyword = list.filter((m) => m.via === "keyword").length;
    put(
      heading,
      "Titles",
      tag(String(list.length)),
      sub(`${byKeyword} by keyword · ${list.length - byKeyword} added`),
    );
    const shown = all ? list : list.slice(0, PREVIEW);
    put(
      body,
      table(
        ["Title", "Type", "Year", "Source", ""],
        shown.map((m) => [
          h("span.who", thumb(m.item), h("b", nameOf(m.item))),
          kindOf(m.item),
          yearOf(m.item),
          m.via === "keyword" ? tag("Keyword") : tag("Added", "warn"),
          h(
            "button.x",
            {
              type: "button",
              "aria-label": `Remove ${nameOf(m.item)}`,
              onclick: () =>
                change(() => api.DELETE("/occasions/{occasionId}/members/{itemId}", path(m.item))),
            },
            "×",
          ),
        ]),
        "No titles yet.",
      ),
      list.length > shown.length &&
        h(
          "button.ib",
          {
            type: "button",
            onclick: () => {
              all = true;
              draw();
            },
          },
          `Show all ${list.length}`,
        ),
      sub(
        "A title removed here stays out even if it carries a keyword, and is listed nowhere. Search for it to add it back.",
      ),
    );
  };
  // After any change the list and its counts are read again.
  const change = async (run: () => Promise<{ response: Response; error?: unknown }>) => {
    showError(err);
    try {
      await ok(run());
      list = await members(api, occasionId);
      draw();
      drawHits();
    } catch (x) {
      showError(err, (x as Error).message);
    }
  };

  // Results stay as searched; only each row's Add or In this occasion follows the list.
  let hits: Item[] | undefined;
  const drawHits = () => {
    const have = new Set(list.map((m) => m.item.id));
    if (!hits) return put(results);
    put(
      results,
      hits.length
        ? h(
            "ul.found",
            ...hits.map((i) =>
              h(
                "li",
                thumb(i),
                h("span", h("b", nameOf(i)), sub(`${yearOf(i)} · ${kindOf(i)}`)),
                have.has(i.id)
                  ? tag("In this occasion")
                  : h(
                      "button.ib",
                      {
                        type: "button",
                        "aria-label": `Add ${nameOf(i)}`,
                        onclick: () =>
                          change(() =>
                            api.PUT("/occasions/{occasionId}/members/{itemId}", path(i)),
                          ),
                      },
                      "Add",
                    ),
              ),
            ),
          )
        : h("p.sub.found", "No movie or show matches."),
    );
  };

  let seq = 0;
  let timer: number | undefined;
  const search = h("input.in.search", {
    type: "search",
    placeholder: "Add a movie or show",
    "aria-label": "Add a movie or show",
    oninput: () => {
      clearTimeout(timer);
      const text = search.value.trim();
      const mine = ++seq;
      if (!text) {
        hits = undefined;
        return put(results);
      }
      timer = window.setTimeout(async () => {
        try {
          const r = await ok(
            api.GET("/admin/search", { params: { query: { q: text, limit: 10 } } }),
          );
          if (mine !== seq) return;
          hits = [...r.movies, ...r.shows];
          drawHits();
        } catch (x) {
          if (mine === seq) showError(err, (x as Error).message);
        }
      }, 200);
    },
    // Enter here is not a save of the form.
    onkeydown: (e: KeyboardEvent) => e.key === "Enter" && e.preventDefault(),
  }) as HTMLInputElement;

  draw();
  return h("section.card", { "aria-label": "Titles" }, heading, search, results, err, body);
}
