import type { Ctx } from "../api";
import { ok } from "../api";
import { card, confirming, formEnd, header } from "../common";
import { dialog, errorLine, field, h, input, showError, sub, tag } from "../dom";
import { ago, hour12, libraryStats } from "../format";
import { runNow, scanTasks } from "../scan";
import type { Schemas } from "../../api/client";

const KIND: Record<string, string> = {
  movies: "Movies",
  tv: "Shows",
  music: "Music",
  youtube: "Channels",
};
const RATINGS: Record<string, string> = {
  movies: "MPAA ratings",
  tv: "TV Parental Guidelines ratings",
};

function libraryDialog(ctx: Ctx, existing: Schemas["Library"] | undefined, reload: () => void) {
  const kind = h(
    "select.in",
    h("option", { value: "movies" }, "Movies"),
    h("option", { value: "tv" }, "TV"),
    h("option", { value: "music" }, "Music"),
    h("option", { value: "youtube" }, "YouTube"),
  ) as HTMLSelectElement;
  const name = input("text", { required: true, value: existing?.name ?? "" });
  const path = input("text", { placeholder: "/Volumes/Media/Movies" });
  const hourSel = h(
    "select.in",
    ...Array.from({ length: 24 }, (_, i) =>
      h("option", { value: String(i), selected: i === (existing?.scanHour ?? 3) }, hour12(i)),
    ),
  ) as HTMLSelectElement;
  const watch = input("checkbox", { checked: existing?.watchFiles ?? true });
  const err = errorLine();
  const folders = (existing?.folders ?? []).map((f) => ({
    f,
    path: input("text", { required: true, value: f.path }),
  }));
  // Made before its folder was refused; the next try adds only the folder.
  let libraryId: number | undefined;
  const { box, close } = dialog(existing ? "Edit library" : "Add library");
  const form = h(
    "form",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        try {
          const body = {
            name: name.value.trim(),
            kind: (existing?.kind ?? kind.value) as Schemas["LibraryKind"],
            scanHour: Number(hourSel.value),
            watchFiles: watch.checked,
          };
          if (existing) {
            libraryId = existing.id;
            await ok(
              ctx.api.PUT("/libraries/{libraryId}", {
                params: { path: { libraryId } },
                body,
              }),
            );
            for (const { f, path } of folders)
              if (path.value.trim() !== f.path)
                await ok(
                  ctx.api.PUT("/libraries/{libraryId}/folders/{folderId}", {
                    params: { path: { libraryId, folderId: f.id } },
                    body: { path: path.value.trim() },
                  }),
                );
          } else libraryId ??= (await ok(ctx.api.POST("/libraries", { body }))).id;
          // Last, so a try after a refusal never adds the folder twice.
          if (path.value.trim())
            await ok(
              ctx.api.POST("/libraries/{libraryId}/folders", {
                params: { path: { libraryId } },
                body: { path: path.value.trim() },
              }),
            );
          close();
          reload();
        } catch (x) {
          if (libraryId) reload();
          showError(err, (x as Error).message);
        }
      },
    },
    ...(existing ? [] : [field("Kind", kind)]),
    field("Name", name),
    ...folders.map((row) => {
      const el = h(
        "div.row.folder",
        field("Folder path", row.path),
        confirming(
          "Remove",
          `Remove ${row.f.path} from ${existing!.name}? Its titles stay; the scan that follows marks its files missing.`,
          async () => {
            await ok(
              ctx.api.DELETE("/libraries/{libraryId}/folders/{folderId}", {
                params: { path: { libraryId: existing!.id, folderId: row.f.id } },
              }),
            );
            folders.splice(folders.indexOf(row), 1);
            el.remove();
            reload();
          },
        ),
      );
      return el;
    }),
    field(existing ? "Add folder" : "First folder", path),
    field("Nightly scan at", hourSel),
    h("label.row", watch, "Scan again when files change"),
    err,
    formEnd(close, existing ? "Save" : "Add library"),
  );
  box.append(form);
  if (existing)
    box.append(
      h(
        "div.row",
        confirming(
          "Delete library",
          `Delete ${existing.name}? Its titles go, with their watch state and their playlist, collection and station entries. The files in its folders stay on disk.`,
          async () => {
            await ok(
              ctx.api.DELETE("/libraries/{libraryId}", {
                params: { path: { libraryId: existing.id } },
              }),
            );
            close();
            reload();
          },
        ),
      ),
    );
}

export async function libraries(ctx: Ctx): Promise<HTMLElement[]> {
  const root = h("div.stack");
  await fill(ctx, root);
  return [root];
}

async function fill(ctx: Ctx, root: HTMLElement) {
  const { api } = ctx;
  const [libs, scans] = await Promise.all([ok(api.GET("/libraries")), scanTasks(api)]);
  const reload = () => void fill(ctx, root);
  const scan = (l: Schemas["Library"], err: HTMLElement) => async (e: Event) => {
    const t = scans.get(l.id);
    const b = e.currentTarget as HTMLButtonElement;
    if (!t) return;
    b.disabled = true;
    showError(err);
    try {
      await runNow(api, t);
      b.textContent = "Queued";
    } catch (x) {
      b.disabled = false;
      showError(err, (x as Error).message);
    }
  };
  const cards = libs.items.map((l) => {
    const err = errorLine();
    return h(
      "section.card.libcard",
      { "aria-label": `${l.name} library` },
      h(
        "h2",
        l.name,
        tag(KIND[l.kind] ?? l.kind),
        sub(`scanned ${ago(l.lastScannedAt)}`),
        h(
          "span",
          { style: "margin-left:auto;display:flex;gap:8px" },
          err,
          h("button.ib", { onclick: scan(l, err), disabled: !scans.has(l.id) }, "Scan now"),
          h("button.ib", { onclick: () => libraryDialog(ctx, l, reload) }, "Edit"),
        ),
      ),
      h(
        "div.libgrid",
        h(
          "div",
          h("h3", "Folders"),
          h(
            "ul",
            ...l.folders.map((f) => h("li", h("span.path", f.path))),
            l.folders.length ? null : h("li", sub("None")),
          ),
        ),
        h(
          "dl.kv",
          h("dt", "Scan"),
          h(
            "dd",
            `Nightly at ${hour12(l.scanHour)}${l.watchFiles ? ", and when files change" : ""}`,
          ),
          h("dt", "Contents"),
          h("dd", libraryStats(l.kind, l.stats)),
          ...(l.kind === "youtube"
            ? []
            : [
                h("dt", "Metadata"),
                h("dd", l.kind === "music" ? "MusicBrainz, Cover Art Archive" : "TMDB"),
              ]),
          ...(RATINGS[l.kind] ? [h("dt", "Kids filter"), h("dd", RATINGS[l.kind])] : []),
        ),
      ),
    );
  });
  const page = [
    header(
      "Libraries",
      `${libs.items.length} ${libs.items.length === 1 ? "library" : "libraries"}`,
      h(
        "button.ib",
        {
          onclick: () =>
            libs.items.forEach((l) => void (scans.get(l.id) && runNow(api, scans.get(l.id)!))),
        },
        "Scan all",
      ),
      h("button.ib.p", { onclick: () => libraryDialog(ctx, undefined, reload) }, "Add library"),
    ),
    ...(cards.length
      ? cards
      : [
          card(
            "No libraries yet.",
            h("p.sub", "Add a folder of movies or TV shows to get started."),
          ),
        ]),
  ];
  root.replaceChildren(...page);
}
