import { describe, expect, it } from "vitest";
import { Queue, type Song } from "./queue";
import { uid } from "../api/uid";

const song = (id: number, extra: Partial<Song> = {}): Song => ({
  id: uid(id),
  libraryId: uid(1),
  albumId: uid(100),
  albumTitle: "In Rainbows",
  albumArtistId: uid(50),
  artistId: uid(50),
  artistName: "Radiohead",
  title: `S${id}`,
  lossless: true,
  durationMs: 60000,
  ...extra,
});
const ids = (q: Queue) => q.upNext.map((s) => s.id);

describe("queue", () => {
  it("plays from the chosen song and lists the rest as up next", () => {
    const q = new Queue();
    q.load([song(1), song(2), song(3)], 1, "In Rainbows", `#/album/${uid(100)}`);
    expect(q.current?.id).toBe(uid(2));
    expect(ids(q)).toEqual([uid(3)]);
    expect(q.next()?.id).toBe(uid(3));
    expect(q.next()).toBeUndefined();
    expect(q.current).toBeUndefined();
  });

  it("starts over at the end when repeating, and goes back with prev", () => {
    const q = new Queue();
    q.load([song(1), song(2)], 1, "x", "#/");
    q.repeat = true;
    expect(q.next()?.id).toBe(uid(1));
    expect(q.prev()?.id).toBe(uid(1));
    q.index = 1;
    expect(q.prev()?.id).toBe(uid(1));
  });

  it("removes, reorders and clears only what is up next", () => {
    const q = new Queue();
    q.load([song(1), song(2), song(3), song(4), song(5)], 0, "x", "#/");
    q.remove(2);
    expect(ids(q)).toEqual([uid(2), uid(4), uid(5)]);
    q.move(3, 1);
    expect(ids(q)).toEqual([uid(5), uid(2), uid(4)]);
    q.move(1, 3);
    expect(ids(q)).toEqual([uid(2), uid(4), uid(5)]);
    // The playing song never moves or goes.
    q.remove(0);
    q.move(0, 2);
    expect(q.current?.id).toBe(uid(1));
    q.clear();
    expect(ids(q)).toEqual([]);
    expect(q.current?.id).toBe(uid(1));
  });

  it("jumps to a song up next", () => {
    const q = new Queue();
    q.load([song(1), song(2), song(3)], 0, "x", "#/");
    expect(q.jump(2)?.id).toBe(uid(3));
    expect(ids(q)).toEqual([]);
  });

  it("shuffles only what is up next, keeping every song", () => {
    const q = new Queue();
    q.load([song(1), song(2), song(3), song(4)], 0, "x", "#/");
    q.setShuffle(true, () => 0);
    expect(q.current?.id).toBe(uid(1));
    expect(ids(q).sort()).toEqual([uid(2), uid(3), uid(4)]);
    expect(ids(q)).not.toEqual([uid(2), uid(3), uid(4)]);
    expect(q.shuffle).toBe(true);
  });

  it("loads shuffled with the picked song first and every other song up next", () => {
    const q = new Queue();
    q.shuffle = true;
    q.load([song(1), song(2), song(3), song(4)], 2, "x", "#/");
    expect(q.current?.id).toBe(uid(3));
    expect(ids(q).sort()).toEqual([uid(1), uid(2), uid(4)]);
  });

  it("counts what is up next and its length", () => {
    const q = new Queue();
    q.load(
      [song(1), song(2, { durationMs: 90000 }), song(3, { durationMs: undefined })],
      0,
      "x",
      "#/",
    );
    expect(q.upNextMs()).toBe(90000);
  });
});
