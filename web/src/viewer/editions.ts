// A title's editions as the viewer offers them. A title of one edition offers
// no choice and sends no editionId, so it behaves as if editions did not exist.
import { ApiError } from "../admin/api";
import { dialog, h } from "../admin/dom";
import type { Schemas } from "../api/client";
import { inProgress, type WatchState } from "./data";
import { minutes } from "./format";

export type Edition = Schemas["Edition"];

/** "Extended · 3h 48m · 4K, 1080p". */
export const editionLabel = (e: Edition) =>
  [
    e.name,
    minutes(e.runtimeMs),
    [...new Set(e.copies.map((c) => c.quality).filter(Boolean))].join(", "),
  ]
    .filter(Boolean)
    .join(" · ");

export const defaultEdition = (eds: Edition[]) => eds.find((e) => e.isDefault) ?? eds[0];

// A position without an editionId belongs to the default edition.
const savedIn = (eds: Edition[], w?: WatchState) =>
  !inProgress(w)
    ? undefined
    : w!.editionId === undefined
      ? defaultEdition(eds)
      : eds.find((e) => e.id === w!.editionId);

/** The edition the saved position belongs to, else the default. */
export const preselect = (eds: Edition[], w?: WatchState) => savedIn(eds, w) ?? defaultEdition(eds);

/** Resume is offered only for the edition the saved position belongs to. */
export const resumes = (eds: Edition[], w: WatchState | undefined, editionId?: number) =>
  inProgress(w) && (eds.length < 2 || savedIn(eds, w)?.id === editionId);

/** The player route for itemId, resuming a saved position in the edition it belongs to. */
export const resumeHref = (itemId: number, w?: WatchState) =>
  `#/play/${itemId}${inProgress(w) && w!.editionId ? `?edition=${w!.editionId}` : ""}`;

/** A playlist row's title, followed by its edition when the server names one. */
export const named = (title: string, editionName?: string) =>
  editionName ? `${title} · ${editionName}` : title;

/** The server's answer to adding a title of several editions without naming one. */
export const needsEdition = (x: unknown) =>
  x instanceof ApiError && x.status === 400 && x.fields.some((f) => f.field === "editionId");

/** Asks which edition; undefined when closed without a pick. */
export const askEdition = (eds: Edition[]) =>
  new Promise<number | undefined>((done) => {
    const pick = (id?: number) => (d.close(), done(id));
    const d = dialog(
      "Which edition?",
      h(
        "div.choices",
        ...eds.map((e) =>
          h("button.btn.s.wide", { type: "button", onclick: () => pick(e.id) }, editionLabel(e)),
        ),
      ),
      h("div.end", h("button.btn.s", { type: "button", onclick: () => pick() }, "Cancel")),
    );
    d.box.parentElement!.addEventListener("keydown", (e) => e.key === "Escape" && done(undefined));
  });
