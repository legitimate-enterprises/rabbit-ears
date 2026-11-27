// The music queue. It lives in the client: the server sees one playback
// session per song and nothing of the order.
import type { Schemas } from "../api/client";

export type Song = Schemas["MusicSong"];

export class Queue {
  songs: Song[] = [];
  /** The playing song's place in songs; past the end when the queue ran out. */
  index = 0;
  /** What the queue plays from, as "In Rainbows", and where that lives. */
  from = "";
  fromHref = "#/music";
  playlistId?: string;
  shuffle = false;
  repeat = false;

  get current(): Song | undefined {
    return this.songs[this.index];
  }
  get upNext(): Song[] {
    return this.songs.slice(this.index + 1);
  }
  upNextMs() {
    return this.upNext.reduce((t, s) => t + (s.durationMs ?? 0), 0);
  }

  load(songs: Song[], start: number, from: string, fromHref: string, playlistId?: string) {
    Object.assign(this, { songs: [...songs], index: start, from, fromHref, playlistId });
    if (!this.shuffle) return;
    // The picked song leads, so every other song is up next.
    this.songs.unshift(...this.songs.splice(start, 1));
    this.index = 0;
    this.setShuffle(true);
  }

  next(): Song | undefined {
    this.index = this.index + 1 >= this.songs.length && this.repeat ? 0 : this.index + 1;
    return this.current;
  }
  prev(): Song | undefined {
    this.index = Math.max(0, this.index - 1);
    return this.current;
  }
  jump(i: number): Song | undefined {
    if (i > this.index && i < this.songs.length) this.index = i;
    return this.current;
  }

  /** Indexes are into songs; only songs after the playing one move or go. */
  remove(i: number) {
    if (i > this.index) this.songs.splice(i, 1);
  }
  move(from: number, to: number) {
    const n = this.songs.length;
    if (from <= this.index || to <= this.index || from >= n || to >= n) return;
    this.songs.splice(to, 0, ...this.songs.splice(from, 1));
  }
  clear() {
    this.songs.length = Math.min(this.songs.length, this.index + 1);
  }

  /** Turning shuffle on shuffles what is up next; turning it off keeps that order. */
  setShuffle(on: boolean, random = Math.random) {
    this.shuffle = on;
    if (!on) return;
    const rest = this.upNext;
    for (let i = rest.length - 1; i > 0; i--) {
      const j = Math.floor(random() * (i + 1));
      [rest[i], rest[j]] = [rest[j], rest[i]];
    }
    this.songs.splice(this.index + 1, rest.length, ...rest);
  }
}
