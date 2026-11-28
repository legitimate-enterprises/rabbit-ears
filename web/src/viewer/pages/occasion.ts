// An occasion's own page, and the rows of its other members on a title's details.
import { h } from "../../admin/dom";
import { ok } from "../../admin/api";
import { items, today, type Ctx, type Item } from "../data";
import { plural, runtime } from "../format";
import { back, section, tile } from "../ui";
import { washBackground } from "../wash";

const year = (i: Item) => (i.parsedYear ? String(i.parsedYear) : "");

/** Every member of the occasion the profile can see, under its name, washed in its colors when it has them. */
export async function occasion(ctx: Ctx, [id]: string[]): Promise<HTMLElement[]> {
  const occasionId = id;
  const [o, members] = await Promise.all([
    ok(ctx.api.GET("/occasions/{occasionId}", { params: { path: { occasionId } } })),
    items(ctx.api, { occasionId }),
  ]);
  const wash = washBackground(o.colors, o.wash);
  return [
    ...(wash ? [h("div.wash", { "aria-hidden": "true", style: `--wash:${wash}` })] : []),
    back("Home", "#/"),
    h("div.titlerow", h("h1", o.name), h("span.sub", plural(members.length, "title"))),
    section(
      o.name,
      members.length
        ? h("div.grid", ...members.map((i) => tile(i, year(i))))
        : h("p.sub", "Nothing here for you yet."),
    ),
  ];
}

/**
 * A row per occasion in season that the title belongs to, of its other members, linked to the
 * occasion's page; none when it has no others, and none at all when the server errs.
 */
export async function occasionRows(ctx: Ctx, itemId: string): Promise<HTMLElement[]> {
  try {
    const { items: active } = await ok(
      ctx.api.GET("/profiles/{profileId}/occasions", {
        params: { path: { profileId: ctx.profile.id }, query: { date: today(), itemId } },
      }),
    );
    const rows = await Promise.all(
      active.map(async (o) => {
        const others = (
          await ok(ctx.api.GET("/items", { params: { query: { occasionId: o.id, limit: 15 } } }))
        ).items.filter((i) => i.id !== itemId);
        return others.length
          ? h(
              "section",
              { "aria-label": o.name },
              h("h2.sec", h("a", { href: `#/occasion/${o.id}` }, o.name)),
              h("div.strip", ...others.map((i) => tile(i, runtime(i.metadata?.runtimeMinutes)))),
            )
          : null;
      }),
    );
    return rows.filter((r): r is HTMLElement => !!r);
  } catch {
    return [];
  }
}
