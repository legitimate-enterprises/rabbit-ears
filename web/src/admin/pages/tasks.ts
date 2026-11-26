import type { Ctx } from "../api";
import { ok } from "../api";
import { bar, card, header, table } from "../common";
import { h, sub, tag } from "../dom";
import { every, local } from "../format";
import { runNow } from "../scan";

export async function tasks(ctx: Ctx): Promise<HTMLElement[]> {
  const root = h("div.stack");
  await fill(ctx, root);
  ctx.every(() => {
    if (!document.hidden) void fill(ctx, root).catch(() => {});
  }, 5000);
  return [root];
}

// Only the newest request may draw: an older answer can land after a newer one.
let latest = 0;

async function fill(ctx: Ctx, root: HTMLElement) {
  const mine = ++latest;
  const { api } = ctx;
  const [{ items: list }, settings] = await Promise.all([
    ok(api.GET("/tasks", { params: { query: { limit: 200 } } })),
    ok(api.GET("/settings")),
  ]);
  const paused = settings.tasksPaused;
  const togglePause = async () => {
    await ok(
      api.PUT("/settings", { body: { serverName: settings.serverName, tasksPaused: !paused } }),
    );
    void fill(ctx, root);
  };
  const runs = (
    await Promise.all(
      list.map(async (t) =>
        (
          await ok(
            api.GET("/tasks/{taskId}/runs", {
              params: { path: { taskId: t.id }, query: { limit: 5 } },
            }),
          )
        ).items.map((r) => ({ t, r })),
      ),
    )
  )
    .flat()
    .sort((a, b) => Date.parse(b.r.startedAt) - Date.parse(a.r.startedAt))
    .slice(0, 20);
  const running = list.filter((t) => t.lastRun?.status === "running");
  const failed = list.filter((t) => t.lastRun?.status === "failed");
  const go = (t: (typeof list)[number]) => async () => {
    await runNow(api, t);
    void fill(ctx, root);
  };
  const status = (t: (typeof list)[number]) =>
    !t.lastRun
      ? sub("Never")
      : t.lastRun.status === "running"
        ? tag("Running", "warn")
        : t.lastRun.status === "succeeded"
          ? tag("OK", "ok")
          : tag("Failed", "err");
  if (mine !== latest) return;
  root.replaceChildren(
    header(
      "Tasks",
      `${running.length} running - ${list.length} scheduled - ${failed.length} failed${paused ? " - Paused" : ""}`,
      h("button.ib", { onclick: togglePause }, paused ? "Resume all" : "Pause all"),
    ),
    ...(paused
      ? [h("p.sub", "Scheduled and file-change runs wait until you resume. Run now still runs.")]
      : []),
    card(
      "Running",
      tag(String(running.length)),
      ...(running.length
        ? running.map((t) => h("div", h("div", h("b", t.name)), bar(t.lastRun!.progress)))
        : [h("p.sub", "Nothing running.")]),
    ),
    card(
      "Scheduled",
      table(
        ["Task", "Schedule", "Last run", "Next run", ""],
        list.map((t) => [
          h("div", h("b", t.name), t.lastRun?.error ? h("div.sub", t.lastRun.error) : null),
          sub(every(t.intervalSeconds)),
          h(
            "div",
            h("span", status(t), " ", sub(local(t.lastRun?.startedAt))),
            t.lastRun?.summary ? h("div.sub", t.lastRun.summary) : null,
          ),
          sub(t.runRequestedAt ? "Queued" : t.enabled ? local(t.nextRunAt) : "Disabled"),
          h(
            "button.ib",
            { disabled: t.lastRun?.status === "running", onclick: go(t) },
            t.lastRun?.status === "failed" ? "Retry" : "Run now",
          ),
        ]),
        "No tasks.",
      ),
    ),
    card(
      "History",
      ...(runs.length
        ? runs.map(({ t, r }) =>
            h(
              "div",
              sub(local(r.startedAt)),
              " ",
              h("b", t.name),
              ` - ${r.status}${r.error ? ` - ${r.error}` : ""}`,
              r.summary ? h("div.sub", r.summary) : null,
            ),
          )
        : [h("p.sub", "No runs yet.")]),
    ),
  );
}
