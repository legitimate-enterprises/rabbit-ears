import type { Ctx } from "../api";
import { ok } from "../api";
import { card, header } from "../common";
import { h, tag } from "../dom";
import { local } from "../format";
import type { Schemas } from "../../api/client";

type Entry = Schemas["LogEntry"];

const LEVELS: [string, string][] = [
  ["", "All"],
  ["info", "Info"],
  ["warn", "Warning"],
  ["error", "Error"],
];
const SOURCES: [string, string][] = [
  ["", "All"],
  ["scan", "Scanner"],
  ["tmdb", "Metadata"],
  ["playback", "Playback"],
  ["transcode", "Transcode"],
  ["task", "Tasks"],
  ["auth", "Auth"],
  ["http", "HTTP"],
  ["server", "Server"],
];
const WINDOWS: [string, string, number][] = [
  ["today", "Today", 0],
  ["hour", "Last hour", 3600e3],
  ["week", "Last 7 days", 7 * 86400e3],
  ["month", "Last 30 days", 30 * 86400e3],
];
const LEVEL_TAG: Record<string, [string, string]> = {
  debug: ["Debug", ""],
  info: ["Info", ""],
  warn: ["Warning", "warn"],
  error: ["Error", "err"],
};
const PAGE = 100;

const select = (label: string, options: [string, string][], onchange: () => void) =>
  h(
    "select.in",
    { "aria-label": label, onchange },
    ...options.map(([v, t]) => h("option", { value: v }, t)),
  ) as HTMLSelectElement;

export async function logs(ctx: Ctx): Promise<HTMLElement[]> {
  const { api } = ctx;
  const level = select("Level", LEVELS, () => void reload());
  const source = select("Source", SOURCES, () => void reload());
  const time = select(
    "Time",
    WINDOWS.map(([v, t]) => [v, t]),
    () => void reload(),
  );
  const q = h("input.in", {
    type: "search",
    placeholder: "Search messages",
    "aria-label": "Search messages",
    oninput: () => {
      clearTimeout(typing);
      typing = window.setTimeout(() => void reload(), 300);
    },
  }) as HTMLInputElement;
  let typing = 0;

  const query = () => {
    const w = WINDOWS.find(([v]) => v === time.value)!;
    const since = w[2] ? new Date(Date.now() - w[2]) : new Date(new Date().setHours(0, 0, 0, 0)); // start of the local day
    return {
      ...(level.value && { level: level.value as Schemas["LogLevel"] }),
      ...(source.value && { source: source.value }),
      since: since.toISOString(),
      ...(q.value.trim() && { q: q.value.trim() }),
    };
  };

  let entries: Entry[] = [];
  let next: number | undefined;
  const open = new Set<number>();
  const body = h("div");
  const caption = h("span.sub");
  const older = h("button.ib", { onclick: () => void fetchPage(true) }, "Load older");

  const row = (e: Entry) => {
    const [label, kind] = LEVEL_TAG[e.level] ?? [e.level, ""];
    const detail = !!(e.ffmpegCommand || e.transcodeReason);
    const expanded = open.has(e.id);
    const toggle = h(
      "button.ib",
      {
        "aria-expanded": String(expanded),
        onclick: () => {
          if (open.has(e.id)) open.delete(e.id);
          else open.add(e.id);
          render();
        },
      },
      expanded ? "Hide details" : "Show details",
    );
    return [
      h(
        "tr",
        h(
          "td.mono",
          time.value === "today" || time.value === "hour"
            ? new Date(e.at).toLocaleTimeString()
            : local(e.at),
        ),
        h("td", tag(label, kind)),
        h("td", e.source),
        h("td", e.message, detail ? " " : null, detail ? toggle : null),
      ),
      ...(detail && expanded
        ? [
            h(
              "tr.detail",
              h(
                "td",
                { colSpan: 4 },
                e.ffmpegCommand ? h("pre", e.ffmpegCommand) : null,
                e.transcodeReason ? h("p.sub", e.transcodeReason) : null,
              ),
            ),
          ]
        : []),
    ];
  };

  const render = () => {
    caption.textContent = `${entries.length}${next !== undefined ? "+" : ""} ${entries.length === 1 ? "entry" : "entries"}`;
    body.replaceChildren(
      entries.length
        ? h(
            "table",
            h("thead", h("tr", ...["Time", "Level", "Source", "Message"].map((c) => h("th", c)))),
            h("tbody", ...entries.flatMap(row)),
          )
        : h("p.sub", "No entries match."),
      ...(next !== undefined ? [older] : []),
    );
  };

  // Only the newest request may draw: an older answer can land after a newer one.
  let latest = 0;
  async function fetchPage(more: boolean) {
    const mine = ++latest;
    const offset = more ? next : 0;
    const page = await ok(
      api.GET("/logs", { params: { query: { ...query(), limit: PAGE, offset } } }),
    );
    if (mine !== latest) return;
    entries = more ? [...entries, ...page.items] : page.items;
    next = page.nextOffset;
    render();
  }
  const reload = () => fetchPage(false).catch(() => {});

  const download = async () => {
    const text = await ok(
      api.GET("/logs/download", { params: { query: query() }, parseAs: "text" }),
    );
    const a = h("a", {
      href: URL.createObjectURL(new Blob([text], { type: "text/plain" })),
      download: "rabbit-ears-logs.txt",
    });
    a.click();
    URL.revokeObjectURL(a.getAttribute("href")!);
  };

  await fetchPage(false);
  // Live tail: only the first page refreshes, and only while it is what is shown.
  ctx.every(() => {
    if (time.value === "today" && !q.value && entries.length <= PAGE) void reload();
  }, 5000);

  return [
    header("Logs", "", h("button.ib", { onclick: () => void download() }, "Download")),
    card("Log entries", h("div.filters", level, source, time, q, caption), body),
  ];
}
