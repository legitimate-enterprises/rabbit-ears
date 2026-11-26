import { ok, paged } from "../admin/api";
import type { Ctx } from "./data";

/** The library kinds that have a section of their own in the top navigation. */
export const SECTION_KINDS = ["movies", "tv", "music", "youtube"];

const KEY = "rabbit-ears.sections";

/** The sections to show for the server's library kinds; every one while the kinds are unknown. */
export const visibleSections = (kinds?: string[]): Set<string> =>
  new Set(kinds ? SECTION_KINDS.filter((k) => kinds.includes(k)) : SECTION_KINDS);

/** The last answer on this browser, so a later load does not show entries and then take them away. */
export function rememberedSections(): Set<string> {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "null");
    return visibleSections(Array.isArray(v) ? v : undefined);
  } catch {
    return visibleSections();
  }
}

/** Asks the server which kinds of library it has and remembers the answer; a failure keeps the last. */
export async function loadSections(ctx: Ctx): Promise<Set<string>> {
  try {
    const libraries = await paged((offset) =>
      ok(ctx.api.GET("/libraries", { params: { query: { limit: 200, offset } } })),
    );
    const kinds: string[] = libraries.map((l) => l.kind);
    try {
      localStorage.setItem(KEY, JSON.stringify(kinds));
    } catch {
      /* storage blocked: the answer lasts until reload */
    }
    return visibleSections(kinds);
  } catch {
    return rememberedSections();
  }
}
