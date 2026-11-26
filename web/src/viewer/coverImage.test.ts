// The custom image's routes, choices and requests, against a mocked API.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createApi } from "../admin/api";
import type { Schemas } from "../api/client";
import { artChoices, imageButtons, imageDialog, imageKey, imageRoute } from "./coverImage";

type Entry = Schemas["PlaylistEntry"];

const entry = (e: Partial<Entry>): Entry =>
  ({ id: 1, playlistId: 9, position: 0, addedAt: "", itemCount: 1, durationMs: 0, ...e }) as Entry;

describe("imageRoute", () => {
  it("reads the playlist and station routes an imageUrl names", () => {
    expect(imageRoute("/api/v1/profiles/3/playlists/7/image?v=12")).toEqual({
      playlist: { profileId: 3, playlistId: 7 },
      v: 12,
    });
    expect(imageRoute("/api/v1/stations/5/image?v=2")).toEqual({ station: { stationId: 5 }, v: 2 });
  });
  it("fetches no other address", () => {
    for (const u of [
      "https://example.com/a.jpg",
      "/api/v1/items/1/artwork/poster",
      "/api/v1/stations/5/image",
    ])
      expect(imageRoute(u)).toBeUndefined();
  });
  it("keys art by the address, so a new image is a new key", () => {
    expect(imageKey("/api/v1/stations/5/image?v=2")).not.toBe(
      imageKey("/api/v1/stations/5/image?v=3"),
    );
  });
});

describe("artChoices", () => {
  it("offers each title once, a backdrop only for movies and TV, nothing for a collection", () => {
    const movie = { id: 4, kind: "movie", parsedTitle: "Heat" } as Schemas["Item"];
    const got = artChoices([
      entry({ itemId: 4, kind: "movie", item: movie }),
      entry({ id: 2, itemId: 4, kind: "movie", item: movie }),
      entry({ id: 3, collectionId: 8, kind: "collection" }),
      entry({ id: 5, itemId: 6, kind: "album", album: { title: "Blue" } as Schemas["MusicAlbum"] }),
    ]);
    expect(got).toEqual([
      { itemId: 4, kind: "poster", label: "Heat, poster" },
      { itemId: 4, kind: "backdrop", label: "Heat, backdrop" },
      { itemId: 6, kind: "poster", label: "Blue, poster" },
    ]);
  });
});

describe("Set image", () => {
  const sent: { method: string; path: string; type: string | null; body: string }[] = [];
  const api = createApi({
    token: "t",
    fetch: async (req) => {
      const path = new URL(req.url, "http://x").pathname.replace("/api/v1", "");
      sent.push({
        method: req.method,
        path,
        type: req.headers.get("Content-Type"),
        body: await req.text(),
      });
      if (req.method === "GET")
        return Response.json({
          items: [
            entry({
              itemId: 4,
              kind: "movie",
              item: { id: 4, kind: "movie", parsedTitle: "Heat" } as Schemas["Item"],
            }),
          ],
        });
      return req.method === "DELETE"
        ? new Response(null, { status: 204 })
        : Response.json({ id: 7 });
    },
  });
  const playlist = { profileId: 3, playlistId: 7 };
  afterEach(() => {
    sent.length = 0;
    document.body.replaceChildren();
  });

  it("uploads the file as is, as application/octet-stream", async () => {
    const done = vi.fn();
    imageDialog(api, { playlist }, playlist, done);
    const file = document.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(file, "files", {
      value: [new File(["bytes"], "a.png", { type: "image/png" })],
    });
    file.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(done).toHaveBeenCalled());
    expect(sent.find((s) => s.method === "PUT")).toEqual({
      method: "PUT",
      path: "/profiles/3/playlists/7/image",
      type: "application/octet-stream",
      body: "bytes",
    });
    expect(document.querySelector(".dialog")).toBeNull();
  });

  it("picks a title's artwork from the station's playlist", async () => {
    const done = vi.fn();
    imageDialog(api, { station: { stationId: 5 } }, playlist, done);
    await vi.waitFor(() => expect(document.querySelectorAll(".pick").length).toBe(2));
    expect(sent[0].path).toBe("/profiles/3/playlists/7/entries");
    document.querySelector<HTMLButtonElement>('.pick[aria-label="Heat, backdrop"]')!.click();
    await vi.waitFor(() => expect(done).toHaveBeenCalled());
    const put = sent.find((s) => s.method === "PUT")!;
    expect([put.path, JSON.parse(put.body)]).toEqual([
      "/stations/5/image",
      { itemId: 4, kind: "backdrop" },
    ]);
  });

  it("sends a pasted link for the server to fetch", async () => {
    imageDialog(api, { playlist }, playlist, () => {});
    const link = document.querySelector<HTMLInputElement>('input[type="url"]')!;
    link.value = " https://example.com/a.jpg ";
    link.form!.requestSubmit();
    await vi.waitFor(() => expect(sent.some((s) => s.method === "PUT")).toBe(true));
    expect(JSON.parse(sent.find((s) => s.method === "PUT")!.body)).toEqual({
      url: "https://example.com/a.jpg",
    });
  });

  it("offers Remove image only while one is set", async () => {
    expect(
      imageButtons(api, { playlist }, playlist, undefined, () => {}).filter(Boolean),
    ).toHaveLength(1);
    const done = vi.fn();
    const [, remove] = imageButtons(
      api,
      { playlist },
      playlist,
      "/api/v1/profiles/3/playlists/7/image?v=1",
      done,
    );
    remove!.click();
    await vi.waitFor(() => expect(done).toHaveBeenCalled());
    expect(sent).toEqual([
      { method: "DELETE", path: "/profiles/3/playlists/7/image", type: null, body: "" },
    ]);
  });
});
