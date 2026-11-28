// Unit tests for what the viewer derives from a station: captions, artwork,
// tuning in and its off-air states, and the editor's input.
import { describe, expect, it } from "vitest";
import { createApi } from "../api/client";
import {
  artFor,
  caption,
  fieldOf,
  formOf,
  inputOf,
  localDate,
  onNowFraction,
  onNowName,
  programTitle,
  stationKey,
  tuneIn,
  untilMs,
} from "./stations";
import { uid } from "../api/uid";

const ts = "2026-01-01T00:00:00Z";
// Local wall-clock instants, so the expectations hold in any time zone.
const local = (d: number, h: number, m = 0) => new Date(2026, 10, d, h, m).toISOString();
// Sunday 15 November 2026, 3:20 PM local.
const NOW = new Date(2026, 10, 15, 15, 20).getTime();

const movie = (id: number, name: string, startsAt: string, extra = {}) => ({
  itemId: uid(id),
  editionId: uid(id * 10),
  kind: "movie" as const,
  startsAt,
  endsAt: startsAt,
  durationMs: 6_000_000,
  item: {
    id: uid(id),
    posterUrl: `/api/v2/artwork/w500/${uid(id)}.jpg`,
    backdropUrl: `/api/v2/artwork/w1280/${uid(id)}.jpg`,
    libraryId: uid(1),
    kind: "movie" as const,
    parsedTitle: name,
    addedAt: ts,
    createdAt: ts,
    modifiedAt: ts,
  },
  ...extra,
});
const episode = (id: number, show: string, s: number, e: number, startsAt: string) => ({
  itemId: uid(id),
  editionId: uid(id * 10),
  kind: "episode" as const,
  startsAt,
  endsAt: startsAt,
  durationMs: 1_800_000,
  item: {
    id: uid(id),
    posterUrl: `/api/v2/artwork/w500/${uid(id)}.jpg`,
    libraryId: uid(1),
    kind: "episode" as const,
    parsedTitle: "Pilot",
    seasonNumber: s,
    episodeNumber: e,
    addedAt: ts,
    createdAt: ts,
    modifiedAt: ts,
  },
  show: {
    id: uid(3),
    libraryId: uid(1),
    kind: "show" as const,
    parsedTitle: show,
    addedAt: ts,
    createdAt: ts,
    modifiedAt: ts,
  },
});
const song = (id: number, title: string, startsAt: string) => ({
  itemId: uid(id),
  editionId: uid(id * 10),
  kind: "song" as const,
  startsAt,
  endsAt: startsAt,
  durationMs: 200_000,
  song: {
    id: uid(id),
    posterUrl: `/api/v2/artwork/w500/${uid(5)}.jpg`,
    title,
    artistId: uid(4),
    artistName: "The Hollow Pines",
    libraryId: uid(3),
    albumArtistId: uid(4),
    lossless: true,
    albumId: uid(5),
    albumTitle: "Low Country",
    durationMs: 200_000,
    addedAt: ts,
  },
});

describe("programTitle", () => {
  it("names a movie, an episode by show and code, a song, and an edition when named", () => {
    expect(programTitle(movie(1, "Paper Boats", ts))).toBe("Paper Boats");
    expect(programTitle(movie(1, "Paper Boats", ts, { editionName: "Extended" }))).toBe(
      "Paper Boats · Extended",
    );
    expect(programTitle(episode(2, "Pocket Dragons", 2, 5, ts))).toBe("Pocket Dragons · S2 E5");
    expect(programTitle(song(3, "Slow Tide", ts))).toBe("Slow Tide");
  });
});

describe("caption", () => {
  it("on air reads what is on and what is next with its time", () => {
    expect(
      caption(
        {
          kind: "movies",
          onAir: {
            status: "on_air",
            now: movie(1, "The Lantern Keeper", local(15, 14)),
            positionMs: 1,
            next: movie(2, "Salt and Ember", local(15, 16)),
            upNext: [movie(2, "Salt and Ember", local(15, 16))],
          },
        },
        NOW,
      ),
    ).toEqual(["On now: The Lantern Keeper", "Next: Salt and Ember at 4:00 PM"]);
  });

  it("between programs says when what is next starts", () => {
    expect(
      caption(
        {
          kind: "tv",
          onAir: {
            status: "between_programs",
            next: episode(2, "Moon Pie", 1, 2, local(15, 15, 30)),
            upNext: [episode(2, "Moon Pie", 1, 2, local(15, 15, 30))],
          },
        },
        NOW,
      ),
    ).toEqual(["Up next at 3:30 PM: Moon Pie · S1 E2"]);
  });

  it("gives a music station's next song no time", () => {
    expect(
      caption(
        {
          kind: "music",
          onAir: {
            status: "on_air",
            now: song(1, "Paper Lanterns", local(15, 15, 18)),
            positionMs: 1,
            next: song(2, "Slow Tide", local(15, 15, 22)),
            upNext: [song(2, "Slow Tide", local(15, 15, 22))],
          },
        },
        NOW,
      ),
    ).toEqual(["On now: Paper Lanterns", "Next: Slow Tide"]);
  });

  it("off schedule says when it is back: today, or by weekday", () => {
    const off = (at: string) =>
      caption(
        {
          kind: "movies",
          onAir: { status: "off_schedule", next: movie(2, "X", at), upNext: [movie(2, "X", at)] },
        },
        NOW,
      );
    expect(off(local(15, 21))).toEqual(["Back today at 9:00 PM"]);
    expect(off(local(20, 22))).toEqual(["Back Friday at 10:00 PM"]);
  });

  it("says nothing is scheduled when no next program is", () => {
    expect(caption({ kind: "movies", onAir: { status: "off_schedule", upNext: [] } }, NOW)).toEqual(
      ["Nothing scheduled"],
    );
  });
});

describe("Home's On now tile", () => {
  it("names the station with its kind", () => {
    expect(onNowName({ kind: "movies", name: "Marvel" })).toBe("Movies: Marvel");
    expect(onNowName({ kind: "tv", name: "Cartoons" })).toBe("TV: Cartoons");
    expect(onNowName({ kind: "music", name: "Kitchen Radio" })).toBe("Music: Kitchen Radio");
  });

  it("fills as far as the live position is through the program, else not at all", () => {
    const on = (positionMs: number | undefined, durationMs = 6_000_000) => ({
      status: "on_air" as const,
      now: { ...movie(1, "A", ts), durationMs },
      positionMs,
      upNext: [],
    });
    expect(onNowFraction(on(1_500_000))).toBe(0.25);
    expect(onNowFraction(on(9_000_000))).toBe(1);
    expect(onNowFraction(on(undefined))).toBeUndefined();
    expect(onNowFraction(on(1_000, 0))).toBeUndefined();
    expect(onNowFraction({ status: "between_programs", upNext: [] })).toBeUndefined();
  });
});

describe("artFor", () => {
  it("is the art of what is on, else what is next, else none", () => {
    const next = movie(2, "Next", ts);
    expect(artFor({ status: "on_air", now: movie(1, "On", ts), next, upNext: [next] })).toBe(
      `art:/api/v2/artwork/w1280/${uid(1)}.jpg`,
    );
    expect(artFor({ status: "between_programs", next, upNext: [next] })).toBe(
      `art:/api/v2/artwork/w1280/${uid(2)}.jpg`,
    );
    expect(
      artFor({
        status: "between_programs",
        next: episode(7, "S", 1, 1, ts),
        upNext: [episode(7, "S", 1, 1, ts)],
      }),
    ).toBe(`art:/api/v2/artwork/w500/${uid(7)}.jpg`);
    expect(artFor({ status: "on_air", now: song(1, "Song", ts), upNext: [] })).toBe(
      `art:/api/v2/artwork/w500/${uid(5)}.jpg`,
    );
    expect(artFor({ status: "off_schedule", upNext: [] })).toBeUndefined();
  });
  it("gives way to the station's own image", () => {
    const onAir = { status: "on_air" as const, now: movie(1, "On", ts), upNext: [] };
    expect(stationKey({ onAir, imageUrl: `/api/v2/stations/${uid(3)}/image?v=4` })).toBe(
      `image:/api/v2/stations/${uid(3)}/image?v=4`,
    );
    expect(stationKey({ onAir })).toBe(`art:/api/v2/artwork/w1280/${uid(1)}.jpg`);
  });
});

describe("untilMs and localDate", () => {
  it("counts down to an instant, never below a second, and nothing for no instant", () => {
    expect(untilMs(new Date(NOW + 5000).toISOString(), NOW)).toBe(5000);
    expect(untilMs(new Date(NOW - 5000).toISOString(), NOW)).toBe(1000);
    expect(untilMs(undefined, NOW)).toBeUndefined();
  });
  it("names the local day", () => {
    expect(localDate(NOW)).toBe("2026-11-15");
  });
});

describe("tuneIn", () => {
  const json = (b: unknown, status: number) =>
    new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });
  const station = (onAir: unknown) => ({
    id: uid(4),
    name: "The Big Screen",
    kind: "movies",
    ownerId: uid(1),
    householdId: uid(1),
    playlistId: uid(9),
    order: "playlist",
    visibility: "household",
    days: [],
    times: [],
    onAir,
    createdAt: ts,
    modifiedAt: ts,
  });
  const stream = {
    sessionId: uid(7),
    entry: { position: 0, verdict: "direct_play", videoAction: "copy", audioAction: "copy" },
    streamUrl: "/s",
    streamKind: "file",
    positionMs: 1234,
    negotiated: { verdict: "direct_play" },
    rungs: [],
    sidecars: [],
  };
  const api = (playback: () => Response, onAir: unknown = {}, date?: string) => {
    const sent: { key: string; body?: unknown }[] = [];
    const fetch = async (req: Request) => {
      const key = `${req.method} ${new URL(req.url).pathname}`;
      sent.push({ key, body: req.method === "POST" ? await req.json() : undefined });
      if (key === `POST /api/v2/stations/${uid(4)}/playback`) return playback();
      if (key === `GET /api/v2/stations/${uid(4)}`) {
        const r = json(station(onAir), 200);
        if (date) r.headers.set("Date", date);
        return r;
      }
      return json({ code: "not_found", message: key }, 404);
    };
    return { api: createApi({ baseUrl: "http://x/api/v2", fetch }), sent };
  };
  const snapshot = async () => ({ decoder: {} }) as never;

  it("on air starts a session on the station's live stream", async () => {
    const now = movie(1, "On", ts);
    const { api: a, sent } = api(() =>
      json({ stream, onAir: { status: "on_air", now, upNext: [] } }, 201),
    );
    const t = await tuneIn(a, uid(4), snapshot);
    expect(t.state).toBe("on_air");
    if (t.state !== "on_air") return;
    expect(t.pb.stream.sessionId).toBe(uid(7));
    expect(t.onAir.now?.itemId).toBe(uid(1));
    expect(sent[0].body).toEqual({ snapshot: { decoder: {} }, live: true });
  });

  it("tuned in to an ident, it plays with no program on", async () => {
    const next = movie(2, "Next", local(15, 16));
    const { api: a } = api(() =>
      json({ stream, onAir: { status: "between_programs", next, upNext: [next] } }, 201),
    );
    const t = await tuneIn(a, uid(4), snapshot);
    expect(t.state).toBe("on_air");
    expect(t.onAir.now).toBeUndefined();
    expect(t.onAir.next?.itemId).toBe(uid(2));
  });

  it("off air between programs shows what is next and asks again at resumesAt", async () => {
    const next = movie(2, "Next", local(15, 16));
    const { api: a } = api(
      () => json({ code: "off_air", message: "off", resumesAt: next.startsAt }, 409),
      { status: "between_programs", next, upNext: [next] },
    );
    const t = await tuneIn(a, uid(4), snapshot);
    expect(t).toMatchObject({ state: "between_programs", resumesAt: next.startsAt });
    expect(t.onAir.next?.itemId).toBe(uid(2));
  });

  it("off air outside the schedule is off schedule", async () => {
    const { api: a } = api(
      () => json({ code: "off_air", message: "off", resumesAt: local(20, 22) }, 409),
      {
        status: "off_schedule",
        next: movie(2, "Next", local(20, 22)),
        upNext: [movie(2, "Next", local(20, 22))],
      },
    );
    expect((await tuneIn(a, uid(4), snapshot)).state).toBe("off_schedule");
  });

  it("off air with nothing scheduled asks again never", async () => {
    const { api: a } = api(() => json({ code: "off_air", message: "off" }, 409), {
      status: "off_schedule",
      upNext: [],
    });
    const t = await tuneIn(a, uid(4), snapshot);
    expect(t.state).toBe("off_schedule");
    expect(t.state !== "on_air" && t.resumesAt).toBeUndefined();
  });

  it("an answer that just went on air still waits for the next call", async () => {
    const now = movie(2, "Next", local(15, 16));
    const { api: a } = api(
      () => json({ code: "off_air", message: "off", resumesAt: now.startsAt }, 409),
      { status: "on_air", now, positionMs: 0, upNext: [] },
    );
    const t = await tuneIn(a, uid(4), snapshot);
    expect(t.state).toBe("between_programs");
    expect(t.onAir).toEqual({ status: "between_programs", next: now, upNext: [now] });
  });

  // A browser clock 30 seconds ahead of the server's.
  const server = () => Date.now() - 30_000;
  const iso = (ms: number) => new Date(ms).toISOString();

  it("times the program on air by the server's clock", async () => {
    const at = server();
    const now = movie(1, "On", iso(at - 60_000), { endsAt: iso(at + 5000) });
    const { api: a } = api(() =>
      json({ stream, onAir: { status: "on_air", now, positionMs: 60_000, upNext: [] } }, 201),
    );
    await tuneIn(a, uid(4), snapshot);
    expect(untilMs(now.endsAt)).toBeGreaterThan(4500);
  });

  it("times off air by the Date of the answer that carried the schedule", async () => {
    const at = server();
    const next = movie(2, "Next", iso(at + 5000));
    const { api: a } = api(
      () => json({ code: "off_air", message: "off", resumesAt: next.startsAt }, 409),
      { status: "between_programs", next, upNext: [next] },
      new Date(at).toUTCString(),
    );
    await tuneIn(a, uid(4), snapshot);
    // Date has whole seconds, so the wait is up to a second long, never short.
    expect(untilMs(next.startsAt)).toBeGreaterThan(4500);
    expect(untilMs(next.startsAt)).toBeLessThanOrEqual(6000);
  });

  it("passes any other refusal on", async () => {
    const { api: a } = api(() => json({ code: "conflict", message: "nothing plays" }, 409));
    await expect(tuneIn(a, uid(4), snapshot)).rejects.toMatchObject({
      status: 409,
      code: "conflict",
    });
  });
});

describe("editor input", () => {
  const st = {
    id: uid(4),
    name: "Creature Double",
    kind: "movies" as const,
    ownerId: uid(1),
    householdId: uid(1),
    playlistId: uid(9),
    order: "random" as const,
    visibility: "private" as const,
    timing: "half_hour" as const,
    days: ["friday", "saturday"] as ("friday" | "saturday")[],
    times: [{ from: "22:00", to: "02:00" }],
    onAir: { status: "off_schedule" as const, upNext: [] },
    createdAt: ts,
    modifiedAt: ts,
  };

  it("a new station of the kind given starts private, continuous, in order, at the kind's timing", () => {
    const f = formOf(undefined, "tv");
    expect(f).toEqual({
      name: "",
      kind: "tv",
      order: "playlist",
      visibility: "private",
      timing: "half_hour",
      chosen: false,
      days: [],
      times: [],
    });
    expect(inputOf({ ...f, name: " Westerns " })).toEqual({
      name: "Westerns",
      kind: "tv",
      order: "playlist",
      visibility: "private",
      timing: "half_hour",
      days: [],
      times: [],
    });
  });

  it("round-trips chosen days and times", () => {
    const f = formOf(st);
    expect(f.chosen).toBe(true);
    expect(inputOf(f)).toEqual({
      name: "Creature Double",
      kind: "movies",
      order: "random",
      visibility: "private",
      timing: "half_hour",
      days: ["friday", "saturday"],
      times: [{ from: "22:00", to: "02:00" }],
    });
  });

  it("continuous sends no days or times, whatever was chosen before", () => {
    expect(inputOf({ ...formOf(st), chosen: false })).toMatchObject({ days: [], times: [] });
  });

  it("drops a time range left blank", () => {
    const f = {
      ...formOf(st),
      times: [
        { from: "", to: "" },
        { from: "18:00", to: "20:00" },
      ],
    };
    expect(inputOf(f).times).toEqual([{ from: "18:00", to: "20:00" }]);
  });

  it("never sends a server station's visibility, which only an admin changes", () => {
    const f = formOf({ ...st, visibility: "server" });
    expect(f.visibility).toBe("server");
    expect(inputOf(f)).not.toHaveProperty("visibility");
  });

  it("maps a 400's field to the form field it belongs to", () => {
    expect(fieldOf("name")).toBe("name");
    expect(fieldOf("kind")).toBe("kind");
    expect(fieldOf("times[0].to")).toBe("times");
    expect(fieldOf("/times/1/from")).toBe("times");
    expect(fieldOf("days.2")).toBe("days");
  });
});
