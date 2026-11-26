import type { Schemas } from "../../api/client";
import type { Ctx } from "../api";
import { ok, paged } from "../api";
import { card, saveSwitch, table } from "../common";
import { errorLine, h, sub, tag } from "../dom";
import { KIND, VIS } from "../../viewer/format";

type Station = Schemas["Station"];

export async function stations(ctx: Ctx): Promise<HTMLElement[]> {
  const { api } = ctx;
  const q = (offset: number) => ({ params: { query: { limit: 200, offset } } });
  const [list, accounts, households] = await Promise.all([
    paged((o) => ok(api.GET("/admin/stations", q(o)))),
    paged((o) => ok(api.GET("/accounts", q(o)))),
    paged((o) => ok(api.GET("/households", q(o)))),
  ]);
  const owner = new Map(accounts.map((a) => [a.id, a.name]));
  const household = new Map(households.map((x) => [x.id, x.name]));
  const caption = sub("");
  const count = () =>
    (caption.textContent = `${list.length} station${list.length === 1 ? "" : "s"} · ${
      list.filter((s) => s.visibility === "server").length
    } server wide`);
  count();
  const err = errorLine();
  const row = (s: Station) => {
    const vis = h("span", tag(VIS[s.visibility], s.visibility === "server" ? "warn" : ""));
    const sw = saveSwitch(
      `Server wide: ${s.name}`,
      s.visibility === "server",
      err,
      async (serverWide) => {
        const out = await ok(
          api.PUT("/admin/stations/{stationId}/server-wide", {
            params: { path: { stationId: s.id } },
            body: { serverWide },
          }),
        );
        s.visibility = out.visibility;
        vis.replaceChildren(tag(VIS[s.visibility], s.visibility === "server" ? "warn" : ""));
        count();
      },
    );
    return [
      h("b", s.name),
      KIND[s.kind],
      owner.get(s.ownerId) ?? "",
      household.get(s.householdId) ?? "",
      s.rating ?? sub("None"),
      vis,
      sw,
    ];
  };
  return [
    h("div.head", h("h1", "Stations"), caption),
    h(
      "p.sub",
      "An adult makes a station and shows it to themselves or to their household. Only an admin makes a station server wide: every household then sees it, kids within its rating.",
    ),
    err,
    card(
      "Every station",
      tag("Ratings recalculated daily"),
      table(
        ["Station", "Type", "Owner", "Household", "Rating", "Visibility", "Server wide"],
        list.map(row),
        "No stations yet.",
      ),
    ),
  ];
}
