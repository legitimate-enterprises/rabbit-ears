// Component tests for Music and playlists: the real pages, engine and
// generated client against a mocked API.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { music, resetMusic } from "./audio";
import { at, calls, mockFetch, none, root, until } from "./testSupport";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported = () => true;
  },
}));

const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label)!;
const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id,
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  updatedAt: ts,
});
const me = (active: number) => ({
  device: { id: 1 },
  account: acct(1, "Heidi"),
  activeProfileId: active,
  household: { id: 1, name: "Heidi", adultId: 1, createdAt: ts },
  profiles: [acct(1, "Heidi"), acct(2, "Jax", "kid")],
});
const page = (items: unknown[]) => ({ items });
const album = (id: number, title: string, extra = {}) => ({
  id,
  libraryId: 3,
  artistId: 50,
  artistName: "Radiohead",
  title,
  year: 2007,
  songCount: 4,
  durationMs: 1_000_000,
  lossless: true,
  genres: [{ id: 1, name: "Rock" }],
  addedAt: ts,
  ...extra,
});
const song = (id: number, title: string) => ({
  id,
  libraryId: 3,
  albumId: 100,
  albumTitle: "In Rainbows",
  albumArtistId: 50,
  artistId: 50,
  artistName: "Radiohead",
  title,
  trackNumber: id,
  durationMs: 240_000,
  codec: "flac",
  lossless: true,
});
const SONGS = [song(1, "15 Step"), song(2, "Bodysnatchers"), song(3, "Nude"), song(4, "Reckoner")];
let session = 0;
const stream = () => ({
  sessionId: ++session,
  entry: {
    position: 0,
    verdict: "direct_play",
    videoAction: "copy",
    audioAction: "copy",
    subtitleAction: "none",
    bad: false,
  },
  streamUrl: `/api/v1/streams/k${session}/0/file`,
  streamKind: "file",
  positionMs: 0,
  negotiated: {
    verdict: "direct_play",
    width: 0,
    height: 0,
    bitrate: 900000,
    audioCodec: "flac",
    lossless: true,
  },
  audio: {
    source: {
      codec: "flac",
      lossless: true,
      bitrate: 900000,
      sampleRateHz: 44100,
      channels: 2,
      sizeBytes: 27_000_000,
    },
    delivered: { verdict: "direct_play", codec: "flac", lossless: true },
  },
  rungs: [],
  sidecars: [],
});
const base = (active = 1) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(active),
  "GET /profiles/1/watch-state": page([]),
  "GET /profiles/1/show-progress": page([]),
  "GET /profiles/2/show-progress": page([]),
  "GET /genres": page([]),
  "GET /items": page([]),
  "GET /music/albums": (_: unknown, u: URL) =>
    page(
      u.searchParams.get("artistId")
        ? [album(100, "In Rainbows"), album(101, "Kid A", { year: 2000 })]
        : [
            album(100, "In Rainbows"),
            album(102, "Blue Train", {
              artistName: "John Coltrane",
              year: 1958,
              lossless: false,
              genres: [{ id: 2, name: "Jazz" }],
              addedAt: "2026-02-01T00:00:00Z",
            }),
          ],
    ),
  "GET /music/albums/100": album(100, "In Rainbows"),
  "GET /music/songs": page(SONGS),
  "GET /music/artists": page([
    {
      id: 50,
      libraryId: 3,
      name: "Radiohead",
      sortName: "Radiohead",
      albumCount: 9,
      songCount: 112,
      genres: [],
      addedAt: ts,
    },
  ]),
  "POST /playback/sessions": () => stream(),
  "PUT /playback/sessions/1/position": none,
  "DELETE /playback/sessions/1": none,
  "DELETE /playback/sessions/2": none,
});

beforeEach(async () => {
  window.onhashchange = null;
  // The last test's engine ends its session before this test counts calls.
  resetMusic();
  await new Promise((r) => setTimeout(r, 0));
  calls.length = 0;
  session = 0;
  document.body.replaceChildren();
  localStorage.clear();
  setToken("tok");
  history.replaceState(null, "", "/");
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  HTMLMediaElement.prototype.play = async () => {};
  HTMLMediaElement.prototype.pause = () => {};
  Object.defineProperty(navigator, "mediaCapabilities", {
    configurable: true,
    value: { decodingInfo: async () => ({ supported: false }) },
  });
});

describe("music section", () => {
  it("shows Music to adults with albums by default, newest first, sorted and filtered", async () => {
    await at("#/music");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".grid .poster"));
    expect(r.querySelector("nav [aria-current=page]")!.textContent).toBe("Music");
    expect(text(r.querySelectorAll(".vt a"))).toEqual([
      "Albums",
      "Artists",
      "Songs",
      "Playlists",
      "Stations",
    ]);
    expect(r.querySelector(".vt [aria-current=page]")!.getAttribute("href")).toBe("#/music");
    expect(r.querySelector(".chip")).toBeNull();
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("2 albums");
    expect(text(r.querySelectorAll(".grid b"))).toEqual(["Blue Train", "In Rainbows"]);
    expect(r.querySelector(".grid .sub")!.textContent).toBe("John Coltrane");
    const [sort, g] = r.querySelectorAll<HTMLSelectElement>("select");
    expect(text(g.options)).toEqual(["Any", "Jazz", "Rock"]);
    g.value = "Rock";
    g.dispatchEvent(new Event("change"));
    expect(text(r.querySelectorAll(".grid b"))).toEqual(["In Rainbows"]);
    g.value = "Any";
    g.dispatchEvent(new Event("change"));
    sort.value = "Title";
    sort.dispatchEvent(new Event("change"));
    expect(text(r.querySelectorAll(".grid b"))).toEqual(["Blue Train", "In Rainbows"]);
    sort.value = "Year";
    sort.dispatchEvent(new Event("change"));
    expect(text(r.querySelectorAll(".grid b"))).toEqual(["In Rainbows", "Blue Train"]);
  });

  it("switches to Artists and Songs with the view tabs", async () => {
    await at("#/music");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".grid .poster"));
    button(r, "Artists").click();
    await until(() => r.querySelector(".grid .poster b")?.textContent === "Radiohead");
    expect(r.querySelector(".grid .poster")!.getAttribute("href")).toBe("#/artist/50");
    expect(r.querySelector(".grid .sub")!.textContent).toBe("9 albums");
    button(r, "Songs").click();
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector("h1 + .sub")!.textContent).toBe("4 songs");
    expect(r.querySelector(".tracks .sub")!.textContent).toBe("Radiohead · In Rainbows");
  });

  it("gives kids Music with its own player, and no Playlists view", async () => {
    await at("#/music");
    const r = root();
    await boot(r, { fetch: mockFetch({ ...base(2), "GET /profiles/2/watch-state": page([]) }) });
    await until(() => r.querySelector("[aria-label='Albums'] .poster"));
    expect(text(r.querySelectorAll("header nav a"))).toEqual([
      "Home",
      "TV",
      "Movies",
      "Music",
      "YouTube",
      "Stations",
    ]);
    expect(text(r.querySelectorAll("nav.vt a"))).toEqual([
      "Albums",
      "Artists",
      "Songs",
      "Stations",
    ]);
    expect(music()).toBeDefined();
  });
});

describe("music playback", () => {
  it("plays an album from a track; the mini player and Now playing follow it", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector("h1")!.textContent).toBe("In Rainbows");
    expect(r.querySelector(".meta .tag")!.textContent).toBe("Lossless");
    expect(text(r.querySelectorAll("section[aria-label='More by Radiohead'] b"))).toEqual([
      "Kid A",
    ]);
    r.querySelectorAll<HTMLElement>(".tracks .tr")[1].click();
    await until(() => !r.querySelector<HTMLElement>(".mini")?.hidden && music()?.pb);
    const start = calls.find((c) => c.key === "POST /playback/sessions")!.body as {
      itemId: number;
      positionMs: number;
    };
    expect(start).toMatchObject({ itemId: 2, positionMs: 0, profileId: 1 });
    expect(music()!.audio.getAttribute("src")).toBe("/api/v1/streams/k1/0/file");
    expect(r.querySelector(".mini b")!.textContent).toBe("Bodysnatchers");
    expect(r.querySelector(".mini .sub")!.textContent).toBe("Radiohead · In Rainbows");
    expect(r.querySelector("li[aria-current=true] b")!.textContent).toBe("Bodysnatchers");

    r.querySelector<HTMLElement>(".mini a.np")!.click();
    await until(() => r.querySelector(".np-page .np-info"));
    expect(r.querySelector(".mini")).toBeTruthy();
    expect(r.querySelector(".back")!.getAttribute("href")).toBe("#/album/100");
    // Title, artist and album each have their own slot; the stream is not narrated.
    const slots = r.querySelectorAll(".np-text > *");
    expect(text(slots)).toEqual(["Bodysnatchers", "Radiohead", "In Rainbows"]);
    expect(slots[1].getAttribute("href")).toBe("#/artist/50");
    expect(slots[2].getAttribute("href")).toBe("#/album/100");
    expect(r.querySelector(".np-page .status")).toBeNull();
    expect(r.querySelector(".np-page")!.textContent).not.toContain("Direct play");
    const up = () => text(r.querySelectorAll("section[aria-label='Up next'] .tr b"));
    expect(up()).toEqual(["NudeRadiohead", "ReckonerRadiohead"]);
    expect(r.querySelector(".qhead .sub")!.textContent).toBe(
      "Playing from In Rainbows · 2 songs · 8m",
    );
    expect(r.querySelector(".qhead + ol + p")!.textContent).toBe(
      "Drag a song to reorder. The queue ends after Reckoner.",
    );
    r.querySelector(".qrow .tr")!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, bubbles: true }),
    );
    expect(up()).toEqual(["ReckonerRadiohead", "NudeRadiohead"]);
    expect(document.activeElement!.textContent).toContain("Nude");
    r.querySelector<HTMLElement>("[aria-label='Remove Reckoner']")!.click();
    expect(up()).toEqual(["NudeRadiohead"]);
    button(r, "Clear").click();
    expect(r.querySelector("section[aria-label='Up next']")!.textContent).toContain(
      "Nothing up next.",
    );
  });

  it("flips the art to the file details and back, and forgets the choice on leaving", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    r.querySelector<HTMLElement>(".mini a.np")!.click();
    await until(() => r.querySelector(".np-info"));
    const info = () => r.querySelector<HTMLElement>(".np-info")!;
    const panel = () => r.querySelector<HTMLElement>(".np-details");
    expect(panel()).toBeNull();
    expect(r.querySelector(".np-art .art")).toBeTruthy();
    expect(info().getAttribute("aria-pressed")).toBe("false");

    info().click();
    expect(r.querySelector(".np-art .art")).toBeNull();
    expect(info().getAttribute("aria-pressed")).toBe("true");
    expect(text(panel()!.querySelectorAll("h2"))).toEqual(["Source", "Delivered"]);
    expect(text(panel()!.querySelectorAll("dt"))).toEqual([
      "Format",
      "Bitrate",
      "Sample rate",
      "Channels",
      "Size",
      "Playback",
      "Codec",
    ]);
    expect(text(panel()!.querySelectorAll("dd"))).toEqual([
      "LosslessFLAC",
      "900 kb/s",
      "44.1 kHz",
      "Stereo",
      "27.0 MB",
      "Direct play",
      "FLAC",
    ]);
    expect(panel()!.querySelector(".ok")!.textContent).toBe("Direct play");

    // The art itself is a target too.
    panel()!.click();
    expect(panel()).toBeNull();
    expect(r.querySelector(".np-art .art")).toBeTruthy();

    info().click();
    await at("#/album/100");
    await until(() => r.querySelector(".tracks"));
    r.querySelector<HTMLElement>(".mini a.np")!.click();
    await until(() => r.querySelector(".np-info"));
    expect(panel()).toBeNull();
  });

  it("offers no details when the stream carries none", async () => {
    await at("#/album/100");
    const r = root();
    const noAudio = () => ({ ...stream(), audio: undefined });
    await boot(r, { fetch: mockFetch({ ...base(), "POST /playback/sessions": noAudio }) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    r.querySelector<HTMLElement>(".mini a.np")!.click();
    await until(() => r.querySelector(".np-text"));
    expect(r.querySelector(".np-art .art")).toBeTruthy();
    expect(r.querySelector(".np-info")).toBeNull();
  });

  it("ends a finished song's session at its end and starts the next one", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    const m = music()!;
    Object.defineProperty(m.audio, "currentTime", { configurable: true, value: 240 });
    m.audio.dispatchEvent(new Event("ended"));
    await until(() => calls.filter((c) => c.key === "POST /playback/sessions").length === 2);
    const end = calls.find((c) => c.key === "DELETE /playback/sessions/1")!;
    expect(end.query).toBe("?positionMs=240000");
    expect(
      (calls.filter((c) => c.key === "POST /playback/sessions")[1].body as { itemId: number })
        .itemId,
    ).toBe(2);
  });

  it("starts a song again at its position when the server has ended its session", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    const m = music()!;
    Object.defineProperty(m.audio, "currentTime", { configurable: true, value: 42 });
    m.audio.dispatchEvent(new Event("error"));
    await until(() => m.audio.getAttribute("src") === "/api/v1/streams/k2/0/file");
    expect(calls.filter((c) => c.key === "POST /playback/sessions")[1].body).toMatchObject({
      itemId: 1,
      positionMs: 42_000,
    });
    expect(r.querySelector(".mini .sub")!.textContent).toBe("Radiohead · In Rainbows");
  });

  it("ends the song's session at its position when the page goes away", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    Object.defineProperty(music()!.audio, "currentTime", { configurable: true, value: 42 });
    window.dispatchEvent(new Event("pagehide"));
    await until(() => calls.some((c) => c.key === "DELETE /playback/sessions/1"));
    expect(calls.find((c) => c.key === "DELETE /playback/sessions/1")!.query).toBe(
      "?positionMs=42000",
    );
  });

  it("Stop ends the song, empties the queue and hides the mini player", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    const mini = r.querySelector<HTMLElement>(".mini")!;
    expect(
      [...mini.querySelectorAll(".mctl button")].map((b) => b.getAttribute("aria-label")),
    ).toEqual(["Previous", "Play", "Next", "Stop"]);
    mini.querySelector<HTMLElement>("[aria-label=Stop]")!.click();
    await until(() => calls.some((c) => c.key === "DELETE /playback/sessions/1"));
    expect(mini.hidden).toBe(true);
    expect(music()!.q.current).toBeUndefined();
    expect(music()!.audio.getAttribute("src")).toBeNull();
  });

  it("hides the mini player when the last song ends", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    r.querySelectorAll<HTMLElement>(".tracks .tr")[3].click();
    await until(() => music()?.pb);
    music()!.audio.dispatchEvent(new Event("ended"));
    await until(() => r.querySelector<HTMLElement>(".mini")!.hidden);
  });

  it("remembers the music volume on this device", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    const vol = r.querySelector<HTMLInputElement>(".mini input[aria-label=Volume]")!;
    vol.value = "0.4";
    vol.dispatchEvent(new Event("input"));
    expect(music()!.audio.volume).toBe(0.4);
    await until(() => localStorage.getItem("rabbit-ears.volume") === "0.4");
    resetMusic();
    document.body.replaceChildren();
    const again = root();
    await boot(again, { fetch: mockFetch(base()) });
    expect(music()!.audio.volume).toBe(0.4);
  });

  it("pauses music for the video player and keeps the mini player off it", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, { fetch: mockFetch(base()) });
    await until(() => r.querySelector(".tracks"));
    button(r, "Play").click();
    await until(() => music()?.pb);
    const paused = vi.spyOn(music()!.audio, "pause");
    await at("#/play/30");
    expect(paused).toHaveBeenCalled();
    expect(r.querySelector(".mini")).toBeNull();
  });
});

describe("playlists", () => {
  const pl = (id: number, name: string, kind = "music") => ({
    id,
    profileId: 1,
    access: "owner",
    name,
    kind,
    itemCount: 2,
    durationMs: 480_000,
    artItemId: 1,
    createdAt: ts,
    updatedAt: ts,
  });
  const entry = (id: number, s: ReturnType<typeof song>, position: number) => ({
    id,
    playlistId: 9,
    position,
    itemId: s.id,
    kind: "song",
    itemCount: 1,
    durationMs: s.durationMs,
    song: s,
    addedAt: ts,
  });

  it("lists music playlists under the Playlists tab and creates one", async () => {
    await at("#/music?view=playlists");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/playlists": page([pl(9, "Road trip")]),
        "POST /profiles/1/playlists": pl(10, "Late night"),
        "GET /profiles/1/playlists/10": { ...pl(10, "Late night"), itemCount: 0 },
        "GET /profiles/1/playlists/10/entries": page([]),
        "GET /profiles/1/playlists/10/play-order": page([]),
      }),
    });
    await until(() => r.querySelector(".grid .poster"));
    expect(calls.find((c) => c.key === "GET /profiles/1/playlists")!.query).toContain("kind=music");
    expect(r.querySelector(".grid .sub")!.textContent).toBe("2 songs");
    button(r, "New playlist").click();
    const dlg = document.querySelector("[role=dialog]")!;
    dlg.querySelector("input")!.value = "Late night";
    dlg.querySelector("form")!.requestSubmit();
    await until(() => r.querySelector("h1")?.textContent === "Late night");
    expect(calls.find((c) => c.key === "POST /profiles/1/playlists")!.body).toEqual({
      name: "Late night",
      kind: "music",
    });
    expect(r.textContent).toContain("Nothing here yet.");
  });

  it("plays, reorders, removes, renames and deletes on the playlist page", async () => {
    let entries = [entry(1, SONGS[0], 0), entry(2, SONGS[2], 1)];
    await at("#/playlist/9");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/playlists/9": pl(9, "Road trip"),
        "GET /profiles/1/playlists/9/entries": () => page(entries),
        "GET /profiles/1/playlists/9/play-order": () =>
          page(
            entries.map((e) => ({ entryId: e.id, itemId: e.itemId, kind: "song", song: e.song })),
          ),
        "PUT /profiles/1/playlists/9/entries/2": () => (
          (entries = [entries[1], entries[0]]),
          entries[0]
        ),
        "DELETE /profiles/1/playlists/9/entries/1": () => (
          (entries = entries.filter((e) => e.id !== 1)),
          none()
        ),
        "PUT /profiles/1/playlists/9": pl(9, "Drive"),
        "DELETE /profiles/1/playlists/9": none,
        "GET /profiles/1/playlists": page([]),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    expect(r.querySelector(".meta")!.textContent).toBe("2 songs8m");
    const titles = () =>
      text(r.querySelectorAll(".tracks .tr b")).map((t) => t!.split("Radiohead")[0]);
    expect(titles()).toEqual(["15 Step", "Nude"]);

    button(r, "Play").click();
    await until(() => music()?.pb);
    expect(calls.find((c) => c.key === "POST /playback/sessions")!.body).toMatchObject({
      itemId: 1,
      playlistId: 9,
    });
    expect(music()!.q.upNext.map((s) => s.id)).toEqual([3]);

    r.querySelectorAll(".qrow .tr")[1].dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", altKey: true, bubbles: true }),
    );
    await until(() => titles()[0] === "Nude");
    expect(calls.find((c) => c.key === "PUT /profiles/1/playlists/9/entries/2")!.body).toEqual({
      itemId: 3,
      position: 0,
    });

    r.querySelector<HTMLElement>("[aria-label='Remove 15 Step']")!.click();
    await until(() => titles().length === 1);

    button(r, "Rename").click();
    let dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.querySelector("input")!.value).toBe("Road trip");
    dlg.querySelector("input")!.value = "Drive";
    dlg.querySelector("form")!.requestSubmit();
    await until(() => calls.some((c) => c.key === "PUT /profiles/1/playlists/9"));
    expect(calls.find((c) => c.key === "PUT /profiles/1/playlists/9")!.body).toEqual({
      name: "Drive",
      kind: "music",
    });

    await until(() => !document.querySelector("[role=dialog]"));
    button(r, "Delete").click();
    dlg = document.querySelector("[role=dialog]")!;
    button(dlg, "Delete").click();
    await until(() => location.hash === "#/music?view=playlists");
    expect(calls.some((c) => c.key === "DELETE /profiles/1/playlists/9")).toBe(true);
  });

  it("adds a movie to a movies playlist from its details; kids get no button", async () => {
    const movie = {
      id: 30,
      libraryId: 1,
      kind: "movie",
      parsedTitle: "Dune",
      addedAt: ts,
      updatedAt: ts,
    };
    const routes = {
      ...base(),
      "GET /items/30": movie,
      "GET /media-files": page([]),
      "GET /items/30/credits": { cast: [], crew: [] },
      "GET /profiles/1/playlists": page([pl(5, "Weekend", "movies")]),
      "POST /profiles/1/playlists/5/entries": { ...entry(1, SONGS[0], 0), itemId: 30 },
    };
    await at("#/movie/30");
    const r = root();
    await boot(r, { fetch: mockFetch(routes) });
    await until(() => button(r, "Add to playlist"));
    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    expect(calls.find((c) => c.key === "GET /profiles/1/playlists")!.query).toContain(
      "kind=movies",
    );
    button(document.querySelector("[role=dialog]")!, "Weekend").click();
    await until(() => button(r, "Added to Weekend"));
    expect(calls.find((c) => c.key === "POST /profiles/1/playlists/5/entries")!.body).toEqual({
      itemId: 30,
    });
    expect(document.querySelector("[role=dialog]")).toBeNull();

    document.body.replaceChildren();
    const kid = root();
    await boot(kid, {
      fetch: mockFetch({ ...routes, "GET /me": me(2), "GET /profiles/2/watch-state": page([]) }),
    });
    await until(() => kid.querySelector("h1"));
    expect(button(kid, "Add to playlist")).toBeUndefined();
  });

  const media = (id: number, kind: string, extra = {}) => ({
    id,
    libraryId: 1,
    kind,
    parsedTitle: `T${id}`,
    addedAt: ts,
    updatedAt: ts,
    ...extra,
  });
  const show = media(10, "show");
  const season = media(11, "season", { parentId: 10, seasonNumber: 1 });
  const ep = media(21, "episode", { parentId: 11, seasonNumber: 1, episodeNumber: 1 });
  const TREE = {
    "GET /items": (_: unknown, u: URL) => {
      const k = u.searchParams.get("kind");
      return page(k === "show" ? [show] : k === "season" ? [season] : k === "episode" ? [ep] : []);
    },
    "GET /items/10": show,
    "GET /items/11": season,
    "GET /items/21": ep,
  };

  it.each([
    ["#/show/10", "tv", "Weekend"],
    ["#/season/11", "tv", "Weekend"],
    ["#/episode/21", "tv", "Weekend"],
    ["#/artist/50", "music", "Road trip"],
    ["#/album/100", "music", "Road trip"],
  ])("offers Add to playlist on %s from %s playlists only", async (hash, kind, name) => {
    await at(hash);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...TREE,
        "GET /items/10/credits": { cast: [], crew: [] },
        "GET /media-files": page([]),
        "GET /music/artists/50": {
          id: 50,
          libraryId: 3,
          name: "Radiohead",
          sortName: "Radiohead",
          albumCount: 9,
          songCount: 112,
          genres: [],
          addedAt: ts,
        },
        "GET /profiles/1/playlists": page([pl(5, name, kind)]),
      }),
    });
    await until(() => button(r, "Add to playlist"));
    button(r, "Add to playlist").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    expect(calls.find((c) => c.key === "GET /profiles/1/playlists")!.query).toContain(
      `kind=${kind}`,
    );
  });

  it.each([
    ["movies", "movie", "#/movies"],
    ["tv", "episode", "#/tv"],
  ])(
    "lists %s playlists under the Playlists tab and makes one of that type",
    async (kind, noun, hash) => {
      await at(`${hash}?view=playlists`);
      const r = root();
      await boot(r, {
        fetch: mockFetch({
          ...base(),
          ...TREE,
          "GET /profiles/1/playlists": page([pl(9, "Weekend", kind)]),
          "POST /profiles/1/playlists": pl(10, "Late", kind),
          "GET /profiles/1/playlists/10": { ...pl(10, "Late", kind), itemCount: 0 },
          "GET /profiles/1/playlists/10/entries": page([]),
          "GET /profiles/1/playlists/10/play-order": page([]),
        }),
      });
      await until(() => r.querySelector(".grid .poster"));
      expect(calls.find((c) => c.key === "GET /profiles/1/playlists")!.query).toContain(
        `kind=${kind}`,
      );
      expect(r.querySelector(".vt [aria-current=page]")!.textContent).toBe("Playlists");
      expect(r.querySelector(".grid .sub")!.textContent).toBe(`2 ${noun}s`);
      button(r, "New playlist").click();
      const dlg = document.querySelector("[role=dialog]")!;
      dlg.querySelector("input")!.value = "Late";
      dlg.querySelector("form")!.requestSubmit();
      await until(() => r.querySelector("h1")?.textContent === "Late");
      expect(calls.find((c) => c.key === "POST /profiles/1/playlists")!.body).toEqual({
        name: "Late",
        kind,
      });
    },
  );

  it.each([
    ["#/season/11", "tv"],
    ["#/episode/21", "tv"],
    ["#/artist/50", "music"],
    ["#/album/100", "music"],
    ["#/now-playing", "music"],
  ])("offers Add to station on %s from the adult's own %s stations", async (hash, kind) => {
    await at(hash === "#/now-playing" ? "#/" : hash);
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...TREE,
        "GET /media-files": page([]),
        "GET /music/artists/50": {
          id: 50,
          libraryId: 3,
          name: "Radiohead",
          sortName: "Radiohead",
          albumCount: 9,
          songCount: 112,
          genres: [],
          addedAt: ts,
        },
        "GET /stations": page([
          { id: 4, name: "Mine", kind, ownerId: 1, playlistId: 40 },
          { id: 5, name: "Theirs", kind, ownerId: 3, playlistId: 50 },
        ]),
      }),
    });
    if (hash === "#/now-playing") {
      await until(() => music());
      await music()!.play(SONGS, 0, "In Rainbows", "#/album/100");
      await at("#/now-playing");
    }
    await until(() => button(r, "Add to station"));
    button(r, "Add to station").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    expect(calls.findLast((c) => c.key === "GET /stations")!.query).toContain(`kind=${kind}`);
    expect(text(document.querySelectorAll("[role=dialog] .choices button"))).toEqual(["Mine"]);
  });

  it("asks Reschedule now? once something goes into a station, and Later leaves the schedule", async () => {
    await at("#/album/100");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        ...TREE,
        "GET /media-files": page([]),
        "GET /stations": page([{ id: 4, name: "Mine", kind: "music", ownerId: 1, playlistId: 40 }]),
        "POST /profiles/1/playlists/40/entries": { id: 1 },
      }),
    });
    await until(() => button(r, "Add to station"));
    button(r, "Add to station").click();
    await until(() => document.querySelector("[role=dialog] .choices button"));
    button(document.querySelector("[role=dialog] .choices")!, "Mine").click();
    await until(
      () => document.querySelector("[role=dialog] h1")?.textContent === "Reschedule now?",
    );
    button(document.querySelector("[role=dialog]")!, "Later").click();
    await until(() => !document.querySelector("[role=dialog]"));
    expect(calls.some((c) => c.key === "POST /stations/4/reschedule")).toBe(false);
  });

  it("shows a container entry as one row and plays the play order from its first row", async () => {
    const tv = { ...pl(9, "Binge", "tv"), itemCount: 13 };
    await at("#/playlist/9");
    const r = root();
    await boot(r, {
      fetch: mockFetch({
        ...base(),
        "GET /profiles/1/playlists/9": tv,
        "GET /profiles/1/playlists/9/entries": page([
          {
            id: 1,
            playlistId: 9,
            position: 0,
            itemId: 11,
            kind: "season",
            itemCount: 12,
            durationMs: 3_600_000,
            item: media(11, "season", { parentId: 10, seasonNumber: 1 }),
            show: media(10, "show", { parsedTitle: "Slow Burn" }),
            addedAt: ts,
          },
          {
            id: 2,
            playlistId: 9,
            position: 1,
            itemId: 30,
            kind: "episode",
            itemCount: 1,
            durationMs: 1_800_000,
            item: media(30, "episode", { seasonNumber: 2, episodeNumber: 4 }),
            show: media(31, "show"),
            addedAt: ts,
          },
        ]),
        "GET /profiles/1/playlists/9/play-order": page([
          { entryId: 1, itemId: 21, kind: "episode" },
          { entryId: 2, itemId: 30, kind: "episode" },
        ]),
      }),
    });
    await until(() => r.querySelector(".tracks"));
    expect(text(r.querySelectorAll(".tracks .tr b"))[0]).toBe("Slow Burn · Season 112 episodes");
    expect(r.querySelector(".meta")!.textContent).toBe("13 episodes8m");
    expect(r.querySelectorAll(".qrow").length).toBe(2);
    button(r, "Play").click();
    await until(() => location.hash === "#/play/21?playlist=9&n=0");
  });
});
