// Scanning is a task per library (kind "scan:<id>"). Run now is a persisted
// request the scheduler picks up within seconds, even while tasks are paused.
import type { Api, Schemas } from "../api/client";
import { ok } from "./api";

export async function runNow(api: Api, task: Schemas["Task"]) {
  await ok(api.POST("/tasks/{taskId}/runs", { params: { path: { taskId: task.id } } }));
}

export async function scanTasks(api: Api): Promise<Map<string, Schemas["Task"]>> {
  const page = await ok(api.GET("/tasks", { params: { query: { limit: 200 } } }));
  const out = new Map<string, Schemas["Task"]>();
  for (const t of page.items) if (t.kind.startsWith("scan:")) out.set(t.kind.slice(5), t);
  return out;
}
