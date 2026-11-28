// The active profile's favorites, read and written through the API; kids have their own.
import { h } from "../admin/dom";
import { ok, paged } from "../admin/api";
import type { Schemas } from "../api/client";
import type { Ctx } from "./data";
import { guard, icon } from "./ui";

export type FavoriteKind = Schemas["FavoriteKind"];

/** The profile's favorites, most recently starred first; only those of kind when given. */
export const favorites = (ctx: Ctx, kind?: FavoriteKind) =>
  paged((offset) =>
    ok(
      ctx.api.GET("/profiles/{profileId}/favorites", {
        params: { path: { profileId: ctx.profile.id }, query: { kind, limit: 200, offset } },
      }),
    ),
  );

/**
 * The Favorite toggle for item id, outlined off and filled on. It stays disabled until the
 * profile's favorites say which it is, and for good when they cannot be read.
 */
export function star(ctx: Ctx, kind: FavoriteKind, id: string) {
  let on = false;
  const b = h(
    "button.btn.s.star",
    { type: "button", "aria-label": "Favorite", title: "Favorite", disabled: true },
    icon("star", 16),
  ) as HTMLButtonElement;
  const paint = () => {
    b.setAttribute("aria-pressed", String(on));
    b.querySelector("svg")!.setAttribute("fill", on ? "currentColor" : "none");
  };
  paint();
  const p = { params: { path: { profileId: ctx.profile.id, itemId: id } } };
  b.onclick = guard(async () => {
    await ok(
      on
        ? ctx.api.DELETE("/profiles/{profileId}/favorites/{itemId}", p)
        : ctx.api.PUT("/profiles/{profileId}/favorites/{itemId}", p),
    );
    on = !on;
    paint();
  });
  favorites(ctx, kind).then(
    (all) => {
      on = all.some((f) => f.itemId === id);
      paint();
      b.disabled = false;
    },
    () => {},
  );
  return b;
}
