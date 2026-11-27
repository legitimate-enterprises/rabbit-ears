import { expect, test, type APIRequestContext } from "@playwright/test";
import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { api, fixtures, seed, signIn } from "./support";

type Headers = { Authorization: string };

/** Runs a library's scan now and waits for that run to finish. */
async function scan(request: APIRequestContext, headers: Headers, libraryId: string) {
  let before: string | undefined;
  await expect(async () => {
    const tasks = (await (await request.get(`${api}/tasks`, { headers })).json()).items;
    const task = tasks.find((t: { kind: string }) => t.kind === `scan:${libraryId}`);
    expect(task).toBeTruthy();
    before = task.lastRun?.finishedAt;
    const put = await request.put(`${api}/tasks/${task.id}`, {
      headers,
      data: { name: task.name, kind: task.kind, nextRunAt: new Date().toISOString() },
    });
    expect(put.ok()).toBeTruthy();
  }).toPass({ timeout: 40_000, intervals: [1000] });
  await expect(async () => {
    const tasks = (await (await request.get(`${api}/tasks`, { headers })).json()).items;
    const run = tasks.find((t: { kind: string }) => t.kind === `scan:${libraryId}`).lastRun;
    expect(run?.finishedAt).toBeTruthy();
    expect(run.finishedAt).not.toBe(before);
  }).toPass({ timeout: 60_000, intervals: [1000] });
}

test("an unreadable file and a missing one need attention until they are dealt with", async ({
  page,
  request,
}) => {
  const headers = await seed(request);
  const dir = mkdtempSync(join(tmpdir(), "rabbit-ears-attention-"));
  const gone = join(dir, "Gone Movie (1999).mp4");
  copyFileSync(join(fixtures, "h264-high41.mp4"), gone);
  writeFileSync(join(dir, "Broken (2001).mkv"), Buffer.alloc(64 << 10, 7));
  const made = await request.post(`${api}/libraries`, {
    headers,
    data: { name: "Attic", kind: "movies" },
  });
  expect(made.ok()).toBeTruthy();
  const attic = await made.json();
  try {
    const folder = await request.post(`${api}/libraries/${attic.id}/folders`, {
      headers,
      data: { path: dir },
    });
    expect(folder.ok()).toBeTruthy();
    await scan(request, headers, attic.id);
    rmSync(gone);
    await scan(request, headers, attic.id);

    await signIn(page);
    await page.goto("/admin/");
    await page.getByRole("link", { name: /Needs attention/ }).click();
    await expect(page.getByRole("heading", { name: "Needs attention" })).toBeVisible();

    const unreadable = page.getByRole("row", { name: /Broken \(2001\)\.mkv/ });
    await expect(unreadable.getByText("Unreadable")).toBeVisible();
    await expect(unreadable.getByText("ffprobe could not read the file.")).toBeVisible();

    const missing = page.getByRole("row", { name: /Gone Movie/ });
    await expect(missing.getByText("Attic")).toBeVisible();
    await missing.getByRole("button", { name: "Remove" }).click();
    await missing.getByRole("button", { name: "Remove" }).click();
    await expect(missing).toBeHidden();
    const left = await (
      await request.get(`${api}/media-files?problem=missing`, { headers })
    ).json();
    expect(left.items).toEqual([]);
  } finally {
    await request.delete(`${api}/libraries/${attic.id}`, { headers });
    rmSync(dir, { recursive: true, force: true });
  }
});
