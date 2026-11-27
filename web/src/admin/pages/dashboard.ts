import type { Ctx } from "../api";
import { ApiError, ok, paged } from "../api";
import { bar, card, gauge, header, table } from "../common";
import { h, sub, tag } from "../dom";
import { ago, bytes, libraryStats, share } from "../format";
import { runNow, scanTasks } from "../scan";
import type { Schemas } from "../../api/client";
import { clock, VERDICT } from "../../viewer/format";

const ENCODER: Record<Schemas["Session"]["encoder"], string> = {
  videotoolbox: "VideoToolbox",
  software: "Software encoder",
  none: "",
};

/** What a page's polls have already resolved, so a poll asks only about what is new. */
const known = () => ({
  titles: new Map<string, string>(),
  files: new Map<string, string | undefined>(),
  who: new Map<string, string>(),
  dev: new Map<string, string>(),
});
type Known = ReturnType<typeof known>;

// /items finds movies, shows, seasons and episodes only; a song or a YouTube video has its own endpoint.
export async function playingTitle(api: Ctx["api"], id?: string, seen?: Known["titles"]) {
  if (id === undefined) return undefined;
  const hit = seen?.get(id);
  if (hit) return hit;
  const lookups = [
    async () => {
      const i = await ok(api.GET("/items/{itemId}", { params: { path: { itemId: id } } }));
      return i.metadata?.title ?? i.parsedTitle;
    },
    async () =>
      (await ok(api.GET("/music/songs/{songId}", { params: { path: { songId: id } } }))).title,
    async () =>
      (await ok(api.GET("/youtube/videos/{videoId}", { params: { path: { videoId: id } } }))).title,
  ];
  for (const look of lookups)
    try {
      const name = await look();
      seen?.set(id, name);
      return name;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404)) return undefined;
    }
}

async function nowPlaying(ctx: Ctx, onStop: () => void, k: Known) {
  const { api } = ctx;
  const q = (offset: number) => ({ params: { query: { limit: 200, offset } } });
  const sessions = await ok(api.GET("/sessions", { params: { query: { active: true } } }));
  // A session of an account or a device not seen yet is the cue to read both lists again.
  if (sessions.items.some((s) => !k.who.has(s.accountId) || !k.dev.has(s.deviceId))) {
    const [accounts, devices] = await Promise.all([
      paged((o) => ok(api.GET("/accounts", q(o)))),
      paged((o) => ok(api.GET("/devices", q(o)))),
    ]);
    k.who = new Map(accounts.map((a) => [a.id, a.name]));
    k.dev = new Map(devices.map((d) => [d.id, d.name]));
  }
  const { who, dev } = k;
  const rows = await Promise.all(
    sessions.items.map(async (s) => {
      const name = await playingTitle(api, s.itemId, k.titles);
      const verdict = s.plan[s.currentEntry]?.verdict ?? "direct_play";
      const label = VERDICT[verdict];
      const kind = verdict === "direct_play" ? "ok" : "warn";
      return [
        who.get(s.accountId) ?? "Unknown",
        name ?? `Item ${s.itemId}`,
        dev.get(s.deviceId) ?? "Unknown",
        h(
          "span",
          tag(label, kind),
          " ",
          sub([ENCODER[s.encoder], s.transcodeReason].filter(Boolean).join(" · ")),
        ),
        sub(clock(s.positionMs / 1000)),
        h(
          "button.ib",
          {
            onclick: async () => {
              await ok(
                api.DELETE("/sessions/{sessionId}", { params: { path: { sessionId: s.id } } }),
              );
              onStop();
            },
          },
          "Stop",
        ),
      ];
    }),
  );
  return card(
    "Now playing",
    tag(String(rows.length)),
    table(["Who", "Playing", "Device", "Stream", "Position", ""], rows, "Nothing playing."),
  );
}

// A conversion's target: the converted picture's height, a song's audio bitrate, or how a copied picture is carried.
export const target = (c: Schemas["Conversion"]) =>
  c.height
    ? `${c.height}p`
    : c.container === "m4a"
      ? `${Math.round(c.bitrate / 1000)} kbps`
      : VERDICT[c.verdict];

async function conversionRows(api: Ctx["api"], k: Known) {
  const conversions = await ok(
    api.GET("/conversions", { params: { query: { active: true, limit: 200 } } }),
  );
  return Promise.all(
    conversions.items.map(async (c) => {
      let itemId = k.files.get(c.mediaFileId);
      if (!k.files.has(c.mediaFileId)) {
        const f = await ok(
          api.GET("/media-files/{mediaFileId}", {
            params: { path: { mediaFileId: c.mediaFileId } },
          }),
        );
        k.files.set(c.mediaFileId, (itemId = f.itemId));
      }
      const name =
        (await playingTitle(api, itemId, k.titles)) ??
        (itemId === undefined ? "Untitled" : `Item ${itemId}`);
      const detail = ["Download", target(c), ...(c.status === "queued" ? ["Queued"] : [])];
      return h("div", h("div", name, " ", sub(`· ${detail.join(" · ")}`)), bar(c.progress));
    }),
  );
}

function runningTasks(tasks: Schemas["Task"][], converting: HTMLElement[]) {
  const running = tasks
    .filter((t) => t.lastRun?.status === "running")
    .map((t) => h("div", h("div", t.name), bar(t.lastRun!.progress)));
  return card(
    "Running tasks",
    ...(running.length || converting.length
      ? [...running, ...converting]
      : [h("p.sub", "Nothing running.")]),
  );
}

function gauges(s: Schemas["ServerStats"]) {
  const pct = Math.round(s.cpuPercent);
  return [
    gauge("CPU", String(pct), "%", pct / 100),
    gauge(
      "Memory",
      ...share(s.memoryUsedBytes, s.memoryTotalBytes),
      s.memoryUsedBytes / s.memoryTotalBytes,
    ),
    gauge(
      "Storage",
      ...share(s.storageUsedBytes, s.storageTotalBytes),
      s.storageUsedBytes / s.storageTotalBytes,
      `${bytes(s.storageTotalBytes - s.storageUsedBytes)} free`,
    ),
    gauge(
      "Transcodes",
      String(s.activeTranscodes),
      ` of ${s.maxTranscodes}`,
      s.activeTranscodes / (s.maxTranscodes || 1),
      "Video transcodes running",
    ),
  ];
}

export async function dashboard(ctx: Ctx): Promise<HTMLElement[]> {
  const { api } = ctx;
  const [stats, libs, tasks, scans] = await Promise.all([
    ok(api.GET("/stats")),
    ok(api.GET("/libraries")),
    ok(api.GET("/tasks", { params: { query: { limit: 200 } } })),
    scanTasks(api),
  ]);
  const scanBtn = (id: string) => {
    const t = scans.get(id);
    return h(
      "button.ib",
      {
        disabled: !t,
        onclick: async (e: Event) => {
          const b = e.currentTarget as HTMLButtonElement;
          b.disabled = true;
          await runNow(api, t!);
          b.textContent = "Queued";
        },
      },
      "Scan",
    );
  };
  const k = known();
  let playing = h("div");
  const refresh = async () => {
    try {
      // Read the card after the wait: an overlapping refresh may have replaced it.
      const next = await nowPlaying(ctx, refresh, k);
      playing.replaceWith(next);
      playing = next;
    } catch {
      /* next poll retries */
    }
  };
  playing = await nowPlaying(ctx, refresh, k);
  // The dashboard opens without the conversions when they fail to load; the poll asks again.
  let running = runningTasks(tasks.items, await conversionRows(api, k).catch(() => []));
  const meters = h("div.gauges", ...gauges(stats));
  // A slow poll is not overtaken by the next one.
  let polling = false;
  ctx.every(() => {
    if (document.hidden || polling) return;
    polling = true;
    void Promise.all([
      refresh(),
      ok(api.GET("/stats"))
        .then((s) => meters.replaceChildren(...gauges(s)))
        .catch(() => {}), // next poll retries
      Promise.all([
        ok(api.GET("/tasks", { params: { query: { limit: 200 } } })),
        conversionRows(api, k),
      ])
        .then(([t, c]) => {
          const next = runningTasks(t.items, c);
          running.replaceWith(next);
          running = next;
        })
        .catch(() => {}), // next poll retries
    ]).finally(() => (polling = false));
  }, 5000);
  const recent = tasks.items
    .flatMap((t) => (t.lastRun ? [{ t, r: t.lastRun }] : []))
    .sort((a, b) => Date.parse(b.r.startedAt) - Date.parse(a.r.startedAt))
    .slice(0, 8);
  return [
    header(
      "Dashboard",
      new Date().toLocaleString([], { dateStyle: "full", timeStyle: "short" }),
      h(
        "button.ib",
        {
          onclick: () =>
            libs.items.forEach((l) => scans.get(l.id) && void runNow(api, scans.get(l.id)!)),
        },
        "Scan all libraries",
      ),
      h("a.ib", { href: "/" }, "Open client"),
    ),
    playing,
    meters,
    h(
      "div.cols",
      card(
        "Libraries",
        table(
          ["Library", "Contents", "Last scan", ""],
          libs.items.map((l) => [
            l.name,
            sub(libraryStats(l.kind, l.stats)),
            sub(ago(l.lastScannedAt)),
            scanBtn(l.id),
          ]),
          "No libraries yet.",
        ),
      ),
      card(
        "Recent activity",
        ...(recent.length
          ? recent.map(({ t, r }) =>
              h(
                "div",
                sub(ago(r.startedAt)),
                " ",
                h("b", t.name),
                ` ${r.status}`,
                r.summary ? h("div.sub", r.summary) : null,
              ),
            )
          : [h("p.sub", "No activity yet.")]),
      ),
    ),
    running,
  ];
}
