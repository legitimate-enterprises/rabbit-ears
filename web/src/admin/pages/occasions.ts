import type { Ctx } from "../api";
import { ok, paged } from "../api";
import { card, header, table } from "../common";
import { h, sub, tag } from "../dom";
import {
  anchorDate,
  describeWindow,
  longDate,
  range,
  season,
  shortDate,
  startOfDay,
} from "../occasionDates";
import { members, occasionEdit } from "./occasionEdit";

export async function occasions(ctx: Ctx): Promise<HTMLElement[]> {
  const [which] = ctx.path;
  if (which === undefined) return list(ctx);
  if (which === "new") return occasionEdit(ctx);
  return occasionEdit(ctx, which);
}

const fixedDay = (month: number, day: number) =>
  // A leap year, so February 29 reads as itself.
  new Intl.DateTimeFormat("en-US", { month: "long", day: "numeric" }).format(
    new Date(2028, month - 1, day),
  );

async function list(ctx: Ctx): Promise<HTMLElement[]> {
  const { api } = ctx;
  const [all, holidays] = await Promise.all([
    paged((offset) => ok(api.GET("/occasions", { params: { query: { limit: 200, offset } } }))),
    ok(api.GET("/holidays")).then((r) => r.items),
  ]);
  // The server sends no member total, so each occasion's titles are counted by reading them.
  const counts = await Promise.all(all.map((o) => members(api, o.id).then((m) => m.length)));
  const today = startOfDay(new Date());
  const year = today.getFullYear();
  const rows = all
    .map((o, i) => ({ o, count: counts[i], s: season(o, holidays, today) }))
    .sort(
      (a, b) =>
        Number(b.s.active) - Number(a.s.active) ||
        (a.s.window?.first.getTime() ?? Infinity) - (b.s.window?.first.getTime() ?? Infinity) ||
        a.o.name.localeCompare(b.o.name),
    );
  const noDate = `No date for ${year}`;
  const cells = ({ o, count, s }: (typeof rows)[number]) => {
    const date = anchorDate(o, holidays, year);
    return [
      h("b", o.name),
      o.holiday !== undefined
        ? h(
            "span",
            holidays.find((x) => x.key === o.holiday)?.name ?? o.holiday,
            sub(date ? `Holiday · ${longDate(date)}` : noDate),
          )
        : h("span", fixedDay(o.month!, o.day!), sub("Fixed date")),
      h(
        "span",
        describeWindow(o.daysBefore, o.daysAfter),
        sub(s.window ? range(s.window, today) : noDate),
      ),
      h("span.num", String(count)),
      s.active
        ? tag("Active", "ok")
        : sub(s.window ? `Starts ${shortDate(s.window.first, today)}` : noDate),
      h("a.ib", { href: `#/occasions/${o.id}`, "aria-label": `Edit ${o.name}` }, "Edit"),
    ];
  };
  const active = rows.filter((r) => r.s.active).length;
  return [
    header(
      "Occasions",
      `${all.length} occasion${all.length === 1 ? "" : "s"} · ${active} active today, ${longDate(today)}`,
      h("a.ib.p", { href: "#/occasions/new" }, "Add occasion"),
    ),
    h(
      "p.sub",
      "While today falls inside an occasion's window, every Home shows a row of its titles under its name, after Next up. Kids see only the titles their filter allows. Home and this page each use the calendar date of the device they run on.",
    ),
    rows.length
      ? card(
          "Every occasion",
          table(["Name", "Anchor", "Window", "Titles", "Today", ""], rows.map(cells), ""),
        )
      : card(
          "No occasions yet.",
          h("p.sub", "Add one to show seasonal titles on Home while it is in season."),
        ),
  ];
}
