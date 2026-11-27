import type { Ctx } from "../api";
import { ok, paged } from "../api";
import { card, confirming, header, table } from "../common";
import { errorLine, h, showError, sub, tag } from "../dom";
import { local } from "../format";
import { runNow, scanTasks } from "../scan";
import { playingTitle, target } from "./dashboard";
import type { Schemas } from "../../api/client";

type Review = Schemas["IdentityReview"];
type Evidence = Schemas["IdentityEvidence"];
type File = Schemas["MediaFile"];

const REASON: Record<Review["reason"], [string, string]> = {
  conflicting_identity: ["Conflicting ID", "warn"],
  invalid_identity: ["Invalid ID", "warn"],
  tied_candidates: ["Tied match", "warn"],
  conflicting_years: ["Conflicting years", "warn"],
  ambiguous_move: ["Ambiguous move", "warn"],
  no_match: ["No match", ""],
  unrecognized: ["Unrecognized file", ""],
};
// What a review with no evidence rests on.
const BARE: Partial<Record<Review["reason"], string>> = {
  no_match: "Nothing on TMDB by this name.",
  unrecognized: "Its name places it under no title.",
};
// The reasons a match made by hand settles; the rest an admin can only dismiss.
const MATCHABLE = new Set<Review["reason"]>([
  "conflicting_identity",
  "invalid_identity",
  "tied_candidates",
  "no_match",
]);
const PROVIDER: Record<NonNullable<Evidence["provider"]>, string> = {
  tmdb: "TMDB",
  tvdb: "TVDB",
  imdb: "IMDb",
  musicbrainz: "MusicBrainz",
};

const base = (path?: string) => path?.split("/").pop() ?? "";

/** A card whose heading carries how many rows it lists. */
function counted(title: string, n: number, body: HTMLElement) {
  const c = card(title, body);
  c.querySelector("h2")!.append(" ", tag(String(n)));
  return c;
}

/** The reason ffprobe gave, which ends its output after the path it could not read. */
const reason = (error: string) => error.trim().split("\n").pop()!.replace(/^.*: /, "");

/** "Crash (1996, CA)": a title with its year and country, as tied candidates differ. */
function named(e: Evidence) {
  const more = [e.year, e.country].filter(Boolean).join(", ");
  return e.title ? `${e.title}${more ? ` (${more})` : ""}` : more;
}

function said(e: Evidence) {
  const id = e.provider ? `${PROVIDER[e.provider]} ${e.externalId ?? ""}`.trim() : "";
  const facts = [id, named(e)].filter(Boolean).join(", ");
  if (e.role === "current") return `Matched now to ${facts}.`;
  if (e.role === "candidate") return `Could be ${facts || e.path}.`;
  return facts ? `${e.path ? base(e.path) : "Its name"} says ${facts}.` : `Was ${e.path}.`;
}

async function load(api: Ctx["api"]) {
  const q = (offset: number, query: Record<string, unknown> = {}) => ({
    params: { query: { limit: 200, offset, ...query } },
  });
  const files = (problem: Schemas["MediaFileProblem"]) =>
    paged((o) => ok(api.GET("/media-files", q(o, { problem }))));
  const [reviews, noDuration, unreadable, missing, tasks, conversions, libs, scans] =
    await Promise.all([
      paged((o) => ok(api.GET("/identity-reviews", q(o, { dismissed: false })))),
      files("no_duration"),
      files("unreadable"),
      files("missing"),
      ok(api.GET("/tasks", { params: { query: { limit: 200 } } })),
      paged((o) => ok(api.GET("/conversions", q(o, { failed: true })))),
      ok(api.GET("/libraries")),
      scanTasks(api),
    ]);
  const converted = await Promise.all(
    conversions.map(async (c) => {
      const f = await ok(
        api.GET("/media-files/{mediaFileId}", { params: { path: { mediaFileId: c.mediaFileId } } }),
      );
      return { c, title: (await playingTitle(api, f.itemId)) ?? base(f.path) };
    }),
  );
  return {
    reviews,
    unstreamable: [...noDuration, ...unreadable],
    missing,
    tasks: tasks.items.filter((t) => t.lastRun?.status === "failed"),
    converted,
    library: new Map(libs.items.map((l) => [l.id, l.name])),
    scans,
  };
}

export async function attention(ctx: Ctx): Promise<HTMLElement[]> {
  const root = h("div.stack");
  await fill(ctx, root);
  return [root];
}

// Only the newest request may draw: an older answer can land after a newer one.
let latest = 0;

async function fill(ctx: Ctx, root: HTMLElement) {
  const mine = ++latest;
  const { api } = ctx;
  const d = await load(api);
  if (mine !== latest) return;
  const err = errorLine();
  const reload = () => {
    ctx.recount();
    void fill(ctx, root).catch((x) => showError(err, (x as Error).message));
  };
  // A button whose failure shows above the cards and whose success reloads them.
  const act = (label: string, run: () => Promise<unknown>, primary = false) =>
    h(
      primary ? "button.ib.p" : "button.ib",
      {
        type: "button",
        onclick: async (e: Event) => {
          const b = e.currentTarget as HTMLButtonElement;
          b.disabled = true;
          showError(err);
          try {
            await run();
            reload();
          } catch (x) {
            b.disabled = false;
            showError(err, (x as Error).message);
          }
        },
      },
      label,
    );
  const remove = (question: string, run: () => Promise<unknown>) =>
    confirming("Remove", question, async () => {
      await run();
      reload();
    });
  const library = (id?: string) => (id === undefined ? "" : (d.library.get(id) ?? ""));
  const title = (f: File) => h("b", f.title ?? base(f.path));

  const dismiss = (rv: Review) =>
    act(rv.reason === "conflicting_identity" ? "Keep match" : "Dismiss", () =>
      ok(
        api.PUT("/identity-reviews/{identityReviewId}", {
          params: { path: { identityReviewId: rv.id } },
          body: { dismissed: true },
        }),
      ),
    );
  const matches = (rv: Review) => {
    const itemId = rv.itemId;
    if (
      itemId === undefined ||
      !MATCHABLE.has(rv.reason) ||
      (rv.itemKind !== "movie" && rv.itemKind !== "show")
    )
      return [];
    const cands = rv.evidence.filter((e) => e.role === "candidate" && e.provider === "tmdb");
    const folder = rv.reason === "conflicting_identity" && cands.length === 1;
    return cands.map((e) =>
      act(
        folder ? "Use the folder's ID" : named(e) || `TMDB ${e.externalId}`,
        () =>
          ok(
            api.PUT("/items/{itemId}/metadata", {
              params: { path: { itemId } },
              body: {
                provider: "tmdb",
                providerId: Number(e.externalId),
                status: "matched",
                title: e.title,
              },
            }),
          ),
        folder,
      ),
    );
  };
  const found = (rv: Review) => {
    const lines = rv.evidence.length ? rv.evidence.map(said) : [BARE[rv.reason] ?? ""];
    return h("div", lines[0], ...lines.slice(1).map((l) => h("div.sub", l)));
  };

  const groups: [number, string][] = [
    [d.reviews.length, "identity"],
    [d.unstreamable.length, "playback"],
    [d.missing.length, "missing"],
    [d.tasks.length + d.converted.length, "failed"],
  ];
  const line = groups
    .filter(([n]) => n)
    .map(([n, what]) => `${n} ${what}`)
    .join(" · ");
  const cards = [
    d.reviews.length &&
      counted(
        "Identity",
        d.reviews.length,
        table(
          ["Title", "Library", "Problem", "What was found", ""],
          d.reviews.map((rv) => [
            h(
              "div",
              h("b", rv.title ?? base(rv.path)),
              h("div.sub", rv.path ?? base(rv.evidence.find((e) => e.path)?.path)),
            ),
            library(rv.libraryId),
            tag(...REASON[rv.reason]),
            found(rv),
            h("span", ...matches(rv), dismiss(rv)),
          ]),
          "",
        ),
      ),
    d.unstreamable.length &&
      counted(
        "Cannot be streamed",
        d.unstreamable.length,
        table(
          ["File", "Library", "Problem", "What it means", ""],
          d.unstreamable.map((f) => [
            h("div", title(f), h("div", h("span.path", f.path))),
            library(f.libraryId),
            f.probeFailedAt ? tag("Unreadable", "err") : tag("No duration", "warn"),
            f.probeFailedAt
              ? h(
                  "div",
                  "ffprobe could not read the file.",
                  h("div.sub", { title: f.probeError ?? "" }, reason(f.probeError ?? "")),
                )
              : h(
                  "div",
                  "Plays only on devices that can play the file as it is.",
                  h(
                    "div.sub",
                    "The file does not say how long it is, as an unfinished recording does not.",
                  ),
                ),
            act("Read again", () =>
              ok(
                api.POST("/media-files/{mediaFileId}/probe", {
                  params: { path: { mediaFileId: f.id } },
                }),
              ),
            ),
          ]),
          "",
        ),
      ),
    d.missing.length &&
      counted(
        "Missing",
        d.missing.length,
        table(
          ["What", "Library", "Missing since", "Where it was", ""],
          d.missing.map((f) => {
            const scan = f.libraryId === undefined ? undefined : d.scans.get(f.libraryId);
            return [
              title(f),
              library(f.libraryId),
              sub(local(f.missingAt)),
              h("span.path", f.path),
              h(
                "span",
                scan ? act("Scan now", () => runNow(api, scan)) : null,
                remove(
                  `Remove the record of ${base(f.path)}? Its title goes with its last file.`,
                  () =>
                    ok(
                      api.DELETE("/media-files/{mediaFileId}", {
                        params: { path: { mediaFileId: f.id } },
                      }),
                    ),
                ),
              ),
            ];
          }),
          "",
        ),
      ),
    d.tasks.length + d.converted.length &&
      counted(
        "Failed",
        d.tasks.length + d.converted.length,
        table(
          ["What", "When", "Error", ""],
          [
            ...d.tasks.map((t) => [
              h("div", h("b", t.name), " ", sub("· task")),
              sub(local(t.lastRun!.finishedAt ?? t.lastRun!.startedAt)),
              h(
                "div",
                t.lastRun!.error ?? "",
                t.lastRun!.summary ? h("div.sub", t.lastRun!.summary) : null,
              ),
              act("Run now", () => runNow(api, t)),
            ]),
            ...d.converted.map(({ c, title }) => [
              h("div", h("b", "Download conversion"), " ", sub(`· ${title} · ${target(c)}`)),
              sub(local(c.finishedAt ?? c.createdAt)),
              c.error ?? "",
              remove("Remove this conversion and the downloads waiting on it?", () =>
                ok(
                  api.DELETE("/conversions/{conversionId}", {
                    params: { path: { conversionId: c.id } },
                  }),
                ),
              ),
            ]),
          ],
          "",
        ),
      ),
  ].filter((c): c is HTMLElement => !!c);
  root.replaceChildren(
    header("Needs attention", line),
    err,
    ...(cards.length ? cards : [h("p.sub", "Nothing needs attention.")]),
  );
}
