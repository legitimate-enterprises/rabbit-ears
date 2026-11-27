import type Hls from "hls.js";
import { ApiError, ok } from "../../admin/api";
import { h } from "../../admin/dom";
import type { Schemas } from "../../api/client";
import { browserEnv, browserTarget, HLS_MIME, watchGroups, type Env } from "../capabilities";
import { epCode, setAutoplay, showOf, title, upNext, type Ctx, type Item } from "../data";
import { named } from "../editions";
import { deviceSnapshot, hlsLibrarySupported, keepVolume } from "../device";
import { clock } from "../format";
import { Playback, srtToVtt, type Stream } from "../playback";
import { chapterAt, scrub } from "../preview";
import {
  initialPick,
  levelIndex,
  networkQuality,
  pillText,
  qualityMenu,
  rungTelemetry,
  type Level,
  type Pick,
} from "../quality";
import { ib, icon, refresh } from "../ui";
import { caption, progress, untilMs, type Station } from "../stations";
import { playRows } from "./playlists";
import { findTogether, otherName, Sync, type Together } from "../together";
import { askHandoff, elsewhere, movedTo, moveHere, toast } from "../handoff";
import { onLive } from "../live";

interface Opt {
  name: string;
  detail?: string;
  verdict?: string;
  green?: boolean;
  checked: boolean;
  blocked?: boolean;
  rule?: boolean;
  on: () => void;
}

const PREVIEW_WIDTH = 260;

/** A station tuned in to: its session has started, on its live stream. */
export interface Live {
  station: Station;
  pb: Playback;
}

/** The player's screen when its session could not start: why, and a way Home. */
export const cannotPlay = (e: unknown) => {
  const msg =
    e instanceof ApiError && e.status === 409
      ? "No way to play this on this device."
      : (e as Error).message;
  return [
    h(
      "div.player",
      h("div.fail", h("p.err", { role: "alert" }, msg), h("a.btn.s", { href: "#/" }, "Home")),
    ),
  ];
};

export async function player(
  ctx: Ctx,
  itemId: string,
  query: URLSearchParams,
  live?: Live,
): Promise<HTMLElement[]> {
  let left = false;
  window.addEventListener("hashchange", () => (left = true), { once: true });
  // What the station is doing, kept current by follow.
  let onAir = live?.pb.onAir;
  // A YouTube video is not a catalog item, so its own endpoint names it.
  let item: Item | undefined;
  let clip: Schemas["YouTubeVideo"] | undefined;
  if (!live)
    try {
      item = await ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId } } }));
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 404) throw e;
      clip = await ok(
        ctx.api.GET("/youtube/videos/{videoId}", { params: { path: { videoId: itemId } } }),
      );
    }
  const me = ctx.profile.id;
  const together = () =>
    live ? undefined : findTogether(ctx.api, itemId, me).catch(() => undefined);
  let tg = await together();
  // A title still playing on another device of the profile is asked about first.
  let movedAt: number | undefined;
  const there = live || tg || query.has("pos") ? undefined : await elsewhere(ctx.api, itemId);
  if (there) {
    const pick = await askHandoff(there);
    if (left) return [];
    if (!pick) {
      if (history.length > 1) history.back();
      else ctx.go("#/");
      return [];
    }
    if (pick === "move") movedAt = (await moveHere(ctx.api, there)).positionMs;
    else {
      await ok(ctx.api.POST("/watch-together", { body: { itemId, fromSessionId: there.id } }));
      tg = await together();
    }
  }
  // A station's stream runs from file to file, so it has no one file's tracks or chapters.
  const files = live
    ? []
    : ((await ok(ctx.api.GET("/media-files", { params: { query: { itemId } } })))
        .items as Schemas["MediaFile"][]);
  // The file in play, which need not be the item's first: subtitles, chapters and length are its own.
  const file = () => files.find((f) => f.id === pb.stream.mediaFileId);
  const show = item?.kind === "episode" && !live ? await showOf(ctx, item) : undefined;
  const playlistId = query.get("playlist") || undefined;
  const n = Number(query.get("n"));
  const [cur] = playlistId ? await playRows(ctx, playlistId, n).catch(() => []) : [];
  const row = cur?.itemId === itemId ? cur : undefined;
  // The server decides what follows; a station or a Watch together has nothing next.
  const up =
    live || tg ? undefined : await upNext(ctx, itemId, playlistId, n).catch(() => undefined);
  const next = up && {
    href:
      up.index === undefined
        ? `#/play/${up.itemId}`
        : `#/play/${up.itemId}?playlist=${playlistId}&n=${up.index}`,
    title: up.title,
  };

  const video = h("video", { playsInline: true, preload: "auto" }) as HTMLVideoElement;
  const audioTrackId = query.get("audio") ?? undefined;
  let env!: Env;
  const pb: Playback =
    live?.pb ??
    new Playback(ctx.api, {
      profileId: ctx.profile.id,
      itemId,
      // A Watch together always plays the default edition.
      editionId: tg ? undefined : (row?.editionId ?? (query.get("edition") || undefined)),
      positionMs: query.has("pos")
        ? Number(query.get("pos"))
        : tg?.status === "joined"
          ? tg.positionMs
          : movedAt,
      audioTrackId,
      playlistId,
      snapshot: () => deviceSnapshot(pb.choice),
    });
  try {
    if (!live) await pb.start();
    env = browserEnv(await hlsLibrarySupported());
    // Leaving while the session started skips the teardown below, which
    // would hold a transcode slot and play unseen.
    if (left) {
      void pb.end().catch(() => {});
      return [];
    }
  } catch (e) {
    return cannotPlay(e);
  }

  let hls: Hls | undefined;
  let stalls = 0;
  let adjusting = false;
  let picked: Pick = initialPick(pb.stream);
  // The variant hls.js is on, which in Auto the player chooses itself.
  let level: Level | undefined;
  // The Auto row's "Now …" line, kept current with level.
  let autoSub: HTMLElement | undefined;
  let subId: string | undefined;
  let subOff = false;
  let ended = false;
  let done = false;
  let sync: Sync | undefined;
  let leftTimer: ReturnType<typeof setTimeout> | undefined;
  let upTimer: ReturnType<typeof setInterval> | undefined;
  // Bumped by every attach and by teardown; work begun under an older value is dropped.
  let gen = 0;
  // Where the stream was attached to start, until the element has moved there.
  let startMs: number | undefined;
  // Every document and window listener, removed together on teardown.
  const off = new AbortController();
  const listen = { signal: off.signal };

  // Text of the quality chip comes from what the server negotiated.
  const pill = h("button.qb", {
    type: "button",
    "aria-haspopup": "menu",
    "aria-expanded": "false",
    onclick: () => toggle("quality"),
  });
  const status = h("span.adjusting", { role: "status", hidden: true }, "Adjusting…");
  const paintPill = (s: Stream) => {
    const p = pillText(s, level);
    pill.replaceChildren(
      h("span.tag", p.res),
      ...(p.hdr ? [h("span.tag", p.hdr)] : []),
      h(`b${p.ok ? ".ok" : ".warn"}`, p.label),
      h("span.sub", p.bitrate),
      icon("chev", 14),
    );
    pill.setAttribute("aria-label", p.aria);
    if (autoSub) autoSub.textContent = qualityMenu(s, picked, level).entries[0].detail;
  };

  const dropTracks = () =>
    video.querySelectorAll("track").forEach((x) => {
      if (x.src.startsWith("blob:")) URL.revokeObjectURL(x.src);
      x.remove();
    });
  const clearSource = () => {
    hls?.destroy();
    hls = undefined;
    video.removeAttribute("src");
    dropTracks();
  };

  async function attach(s: Stream, atMs: number) {
    const mine = ++gen;
    clearSource();
    level = undefined;
    // A live stream starts at its live edge, which the player finds.
    const start = s.live ? -1 : atMs / 1000;
    startMs = atMs > 0 && !s.live ? atMs : undefined;
    const wantHls = s.streamKind === "hls";
    // hls.js wherever MSE exists: Chrome's own HLS desyncs multi-track fMP4
    // after seeks. Safari keeps its player, which AirPlay needs.
    const native =
      video.canPlayType(HLS_MIME) !== "" &&
      ("WebKitPlaybackTargetAvailabilityEvent" in window || !(await hlsLibrarySupported()));
    if (mine !== gen) return;
    if (wantHls && !native) {
      const { default: Hls } = await import("hls.js");
      if (mine !== gen) return;
      // The server runs one encoder per session and a request for another
      // rung takes it over. So no bandwidth probe on the lowest rung before
      // the real start, and loading waits until the start level is chosen.
      // The forward buffer is the server's goal by length alone: hls.js's
      // size cap would stop a high bitrate short of it and run a low one
      // past it. The browser's own quota still bounds what is held.
      const goal = s.bufferGoalSeconds;
      const x = new Hls({
        startPosition: start,
        autoStartLoad: false,
        testBandwidth: false,
        ...(goal ? { maxBufferLength: goal, maxBufferSize: 0 } : {}),
      });
      hls = x;
      x.on(Hls.Events.ERROR, (_e, d) => d.fatal && void fail(`hls: ${d.type} ${d.details}`));
      // Both modes start on the negotiated rung; a fixed pick also stays there.
      x.on(Hls.Events.MANIFEST_PARSED, (_e, d) => {
        const i = levelIndex(d.levels, s);
        x.startLevel = i;
        if (!s.auto && i >= 0) x.loadLevel = i;
        x.startLoad(start);
      });
      x.on(Hls.Events.LEVEL_SWITCHED, (_e, d) => {
        level = x.levels[d.level];
        paintPill(pb.stream);
      });
      x.loadSource(s.streamUrl);
      x.attachMedia(video);
    } else {
      video.src = s.streamUrl;
      if (!s.live)
        video.addEventListener("loadedmetadata", () => (video.currentTime = start), {
          once: true,
        });
    }
    paintPill(s);
    await sidecars(s);
    if (mine !== gen) return;
    if (sync?.joined) sync.follow();
    // A joined one waits for Sync, which starts it on the server's clock.
    else if (sync || tg?.status !== "joined") void video.play().catch(() => {});
  }

  /** Replaces the subtitle tracks; a sidecar that fails to load is left out. */
  async function sidecars(s: Stream) {
    const mine = gen;
    const tracks: [HTMLTrackElement, Stream["sidecars"][number]][] = [];
    for (const sc of s.sidecars) {
      let src = sc.url;
      if (sc.format === "srt") {
        const text = await fetch(sc.url)
          .then((r) => (r.ok ? r.text() : undefined))
          .catch(() => undefined);
        if (text === undefined) continue;
        src = URL.createObjectURL(new Blob([srtToVtt(text)], { type: "text/vtt" }));
      }
      const tr = h("track", {
        kind: "subtitles",
        src,
        srclang: sc.language ?? "",
        label: sc.title || sc.language || "Subtitles",
        "data-id": sc.trackId,
        "data-forced": sc.forced,
      }) as HTMLTrackElement;
      tracks.push([tr, sc]);
    }
    if (mine !== gen || done) {
      tracks.forEach(([tr]) => tr.src.startsWith("blob:") && URL.revokeObjectURL(tr.src));
      return;
    }
    dropTracks();
    for (const [tr, sc] of tracks) {
      video.append(tr);
      tr.track.mode =
        !subOff && (sc.trackId === subId || (subId === undefined && sc.forced))
          ? "showing"
          : "hidden";
    }
  }

  async function failWith<T>(p: () => Promise<T>): Promise<T | undefined> {
    try {
      return await p();
    } catch (e) {
      msg.textContent = (e as Error).message;
      msg.hidden = false;
    }
  }

  // Applies a server answer: restart switches stream at its position, continue leaves playback alone.
  async function apply(u: { action: string; stream: Stream } | undefined) {
    if (!u) return;
    if (u.action === "restart") await attach(u.stream, u.stream.positionMs);
    else {
      paintPill(u.stream);
      await sidecars(u.stream);
    }
  }
  const adjust = async (fn: () => Promise<{ action: string; stream: Stream } | undefined>) => {
    adjusting = true;
    status.hidden = false;
    const wasPlaying = !video.paused;
    video.pause();
    await failWith(async () => apply(await fn()));
    adjusting = false;
    status.hidden = true;
    if (wasPlaying) void video.play().catch(() => {});
  };
  // The element reads 0 until the stream has loaded and moved to its start, and a
  // report of that 0 would erase the resume point.
  const here = () => startMs ?? video.currentTime * 1000;
  for (const e of ["timeupdate", "seeked"])
    video.addEventListener(e, () => video.currentTime > 0 && (startMs = undefined));

  async function fail(reason: string) {
    if (adjusting || done) return;
    adjusting = true;
    status.hidden = false;
    const u = await pb.failed(here(), reason).catch((e: Error) => e);
    if (u && !(u instanceof Error)) await apply(u);
    // A station with nothing left to fall back to is asked again, which says why not.
    else if (live) refresh();
    else {
      msg.textContent = u?.message ?? "No way to play this on this device.";
      msg.hidden = false;
    }
    adjusting = false;
    status.hidden = true;
  }

  // Menus
  const msg = h("p.err.pmsg", { role: "alert", hidden: true });
  const menus: Record<string, HTMLElement> = {};
  let open: string | undefined;
  let subOpts: Opt[] = [];
  const toggle = (name?: string) => {
    const was = open;
    open = open === name ? undefined : name;
    for (const [k, m] of Object.entries(menus)) m.hidden = k !== open;
    pill.setAttribute("aria-expanded", String(open === "quality"));
    subBtn.setAttribute("aria-expanded", String(open === "subs"));
    if (open) (menus[open].querySelector("[aria-checked=true]") as HTMLElement | null)?.focus();
    else if (was) (was === "quality" ? pill : subBtn).focus();
  };
  const step = (by: number) => {
    const opts = [...menus[open!].querySelectorAll<HTMLElement>(".opt")];
    const i = opts.indexOf(document.activeElement as HTMLElement);
    opts[(i + by + opts.length) % opts.length]?.focus();
  };
  const build = (label: string, cls: string, caption: string | undefined, opts: Opt[]) =>
    h(
      `div.menu.${cls}`,
      { role: "menu", "aria-label": label, hidden: true },
      caption ? h("div.cap", caption) : null,
      ...opts.flatMap((o) => [
        h(
          "button.opt",
          {
            type: "button",
            role: "menuitemradio",
            "aria-checked": String(o.checked),
            "aria-disabled": o.blocked ? "true" : undefined,
            onclick: () => !o.blocked && (toggle(), o.on()),
          },
          h("span.chk", o.checked ? icon("check", 14) : null),
          h("span.grow", h("b", o.name), o.detail ? h("span.sub", o.detail) : null),
          o.verdict ? h(`span.verdict${o.green ? ".ok" : ""}`, o.verdict) : null,
        ),
        o.rule ? h("div.rule", { role: "separator" }) : null,
      ]),
    );
  const buildMenus = () => {
    const s = pb.stream;
    const m = qualityMenu(s, picked, level);
    const rungs: Opt[] = m.entries.map((e) => ({
      ...e,
      on: () => {
        picked = e.pick;
        void adjust(async () =>
          pb.quality((await deviceSnapshot(networkQuality(e.pick, s.rungs))).network, here()),
        ).then(buildMenus);
      },
    }));
    const subs: Opt[] = [
      { name: "Off", checked: subOff, on: () => pick(undefined, true) },
      {
        name: "Forced only",
        detail: "Shows the forced track when another language is spoken",
        checked: !subOff && subId === undefined,
        on: () => pick(undefined, false),
      },
      ...(file()?.tracks ?? [])
        .filter((t) => t.kind === "subtitle" && !t.isForced)
        .map((tr) => ({
          name: [
            tr.title || tr.language || "Subtitles",
            tr.subtitleIsText === false ? "(image)" : "",
          ]
            .filter(Boolean)
            .join(" "),
          checked: !subOff && subId === tr.id,
          on: () => pick(tr.id, false),
        })),
    ];
    const q = build("Playback quality", "quality", m.caption, rungs);
    const sm = build("Subtitles", "subs", undefined, subs);
    subOpts = subs;
    menus.quality?.replaceWith(q);
    menus.subs?.replaceWith(sm);
    menus.quality = q;
    menus.subs = sm;
    autoSub = m.entries[0]?.pick === "auto" ? q.querySelector<HTMLElement>(".sub")! : undefined;
    q.hidden = sm.hidden = true;
    open = undefined;
  };
  const nextSub = () => subOpts[(subOpts.findIndex((o) => o.checked) + 1) % subOpts.length]?.on();
  const pick = (id: string | undefined, off: boolean) => {
    subId = id;
    subOff = off;
    if (off)
      video
        .querySelectorAll("track")
        .forEach((x) => ((x as HTMLTrackElement).track.mode = "hidden"));
    // The server replaces the whole selection, so the audio track goes with every pick.
    void adjust(() => pb.tracks({ audioTrackId, subtitleTrackId: id }, here())).then(buildMenus);
  };

  // Controls
  // A station's bar shows the program's progress, with no knob to move it.
  const seek = live
    ? h(
        "div.seek.live",
        { role: "progressbar", "aria-label": "Program progress", "aria-valuemin": "0" },
        h("div.fill"),
      )
    : h(
        "div.seek",
        {
          role: "slider",
          tabIndex: 0,
          "aria-label": "Seek",
          "aria-valuemin": "0",
          "aria-valuemax": "0",
          "aria-valuenow": "0",
        },
        h("div.fill"),
        h("div.thumb"),
      );
  const pvImg = h("img", { alt: "" }) as HTMLImageElement;
  const pvTime = h("span");
  const pvChapter = h("span.sub");
  const pv = h("div.pv", { hidden: true }, pvImg, h("div.pvcap", pvTime, pvChapter));
  seek.append(pv);
  const elapsed = h("span.tm", "0:00");
  const remaining = h("span.tm", "-0:00");
  const play = h(
    "button.pb.big",
    // The video starts paused; its play event turns this into Pause.
    { type: "button", "aria-label": "Play", onclick: () => toggleplay() },
    icon("play", 32),
  );
  const mute = ib("Mute", "vol", () => (video.muted = !video.muted), 20);
  const vol = h("input.volume", {
    type: "range",
    min: "0",
    max: "1",
    step: "0.05",
    value: "1",
    "aria-label": "Volume",
    oninput: () => ((video.volume = Number(vol.value)), (video.muted = false)),
  }) as HTMLInputElement;
  keepVolume(video);
  vol.value = String(video.volume);
  const subBtn = ib("Subtitles", "cc", () => toggle("subs"), 20);
  subBtn.setAttribute("aria-haspopup", "menu");
  subBtn.setAttribute("aria-expanded", "false");
  const dur = () =>
    Number.isFinite(video.duration) ? video.duration : (file()?.durationMs ?? 0) / 1000;
  // A station never pauses, but a click or Space starts one the browser would not autoplay.
  const toggleplay = () => (video.paused ? void video.play() : live || video.pause());
  const skip = (s: number) =>
    (video.currentTime = Math.min(dur(), Math.max(0, video.currentTime + s)));
  const goBack = () => (history.length > 1 ? history.back() : ctx.go("#/"));
  const fullscreen = () =>
    document.fullscreenElement ? void document.exitFullscreen() : void root.requestFullscreen?.();

  const upnext = h("div.upnext", { hidden: true });
  const chip = h("span.pill.sync.small", { hidden: true });
  const pnote = h("div.pnote", { role: "status", hidden: true });
  let chipFor = "";
  let peer = tg ? otherName(tg, me) : "";
  const paintTogether = (wt: Together) => {
    const joined = wt.status === "joined";
    const name = otherName(wt, me);
    if (name) peer = name;
    chip.hidden = !joined;
    if (joined && chipFor !== name) {
      chipFor = name;
      chip.replaceChildren(
        icon("users", 16),
        `In sync with ${name}`,
        h("button.leave", { type: "button", onclick: () => void leave() }, "Leave"),
      );
    }
    const by = wt.pausedById !== me ? wt.pausedByName : undefined;
    const said = joined && !wt.playing && by ? `${by} paused` : "";
    if (wt.status === "ended" && chipFor) {
      chipFor = "";
      pnote.replaceChildren(`${peer} left`);
      pnote.hidden = false;
      leftTimer = setTimeout(() => (pnote.hidden = true), 4000);
    } else if (joined) {
      pnote.replaceChildren(...(said ? [icon("pause", 14), said] : []));
      pnote.hidden = !said;
    }
  };
  const leave = async () => {
    chip.hidden = true;
    chipFor = "";
    await sync?.leave().catch(() => {});
  };
  const subline = h(
    "span.sub",
    live
      ? caption({ kind: live.station.kind, onAir: onAir! })[0]
      : clip
        ? clip.channelTitle
        : item?.kind === "episode"
          ? named(`${epCode(item)} - ${title(item)}`, row?.editionName)
          : "",
  );
  const root = h(
    "div.player",
    video,
    h(
      "div.overlay",
      h(
        "div.ph",
        ib("Back", "back", goBack),
        h(
          "span",
          h(
            "b.t",
            live
              ? live.station.name
              : clip
                ? clip.title
                : show
                  ? title(show)
                  : named(title(item!), row?.editionName),
          ),
          subline,
        ),
        h("span.grow"),
        chip,
        ib(
          "Picture in picture",
          "pip",
          () =>
            void (document.pictureInPictureElement
              ? document.exitPictureInPicture()
              : video.requestPictureInPicture?.()),
          20,
        ),
      ),
      live
        ? h("div.center")
        : h(
            "div.center",
            ib("Back 10 seconds", "back10", () => skip(-10), 40),
            play,
            ib("Forward 10 seconds", "fwd10", () => skip(10), 40),
          ),
      pnote,
      h(
        "div.pf",
        h("div.r1", elapsed, seek, remaining),
        h(
          "div.r2",
          mute,
          vol,
          pill,
          h("span.grow"),
          status,
          live ? null : subBtn,
          next ? ib("Next", "next", () => ctx.go(next.href), 20) : null,
          ib("Full screen", "full", fullscreen, 20),
          (menus.quality = h("div")),
          (menus.subs = h("div")),
        ),
        msg,
      ),
    ),
    upnext,
  );
  buildMenus();

  // Video events
  const paint = () => {
    // A station's bar is the program on, by the schedule: its stream has no length.
    const d = live ? (onAir?.now?.durationMs ?? 0) / 1000 : dur();
    const c = live ? progress(onAir?.now) * d : video.currentTime;
    seek.setAttribute("aria-valuemax", String(Math.round(d)));
    seek.setAttribute("aria-valuenow", String(Math.round(c)));
    seek.setAttribute("aria-valuetext", `${clock(c)} of ${clock(d)}`);
    (seek.querySelector(".fill") as HTMLElement).style.width = d ? `${(c / d) * 100}%` : "0";
    const thumb = seek.querySelector<HTMLElement>(".thumb");
    if (thumb) thumb.style.left = d ? `${(c / d) * 100}%` : "0";
    elapsed.textContent = clock(c);
    remaining.textContent = `-${clock(d - c)}`;
  };
  video.addEventListener("timeupdate", paint);
  video.addEventListener("loadedmetadata", paint);
  video.addEventListener(
    "play",
    () => (play.replaceChildren(icon("pause", 32)), play.setAttribute("aria-label", "Pause")),
  );
  video.addEventListener("pause", () => {
    play.replaceChildren(icon("play", 32));
    play.setAttribute("aria-label", "Play");
    if (!adjusting && !done && !video.ended) void pb.position(here(), true).catch(() => {});
  });
  video.addEventListener("volumechange", () => {
    mute.setAttribute("aria-label", video.muted ? "Unmute" : "Mute");
    vol.value = String(video.muted ? 0 : video.volume);
  });
  video.addEventListener("waiting", () => {
    if (video.paused || video.readyState >= 3) return;
    stalls++;
    report();
  });
  video.addEventListener("error", () => void fail(video.error?.message || "media error"));
  video.addEventListener("click", () => toggleplay());
  // Only the viewer's own play, pause or seek is the other side's business.
  for (const e of ["play", "pause", "seeked"])
    video.addEventListener(e, () => !adjusting && !done && !video.ended && sync?.local());
  video.addEventListener("ended", () => {
    ended = true;
    // The program is over: leaving the route ends the session, and tuning in again starts the next.
    // A live stream's player runs a little ahead of the schedule, so it asks once the program is
    // over by the schedule: sooner, the answer is that program's last seconds again.
    if (live) {
      const wait = pb.stream.live ? (untilMs(onAir?.now?.endsAt) ?? 0) : 0;
      return void setTimeout(() => !done && refresh(), wait);
    }
    if (!next) return goBack();
    let n = 10;
    const go = () => ctx.go(next.href);
    const label = h("span", "");
    const tick = () =>
      (label.textContent = `Up next: ${next.title}` + (auto.checked ? ` in ${n}s` : ""));
    // Autoplay counts down to the next item; without it the card waits for Play now.
    const count = () => {
      clearInterval(upTimer);
      n = 10;
      tick();
      if (auto.checked)
        upTimer = setInterval(() => (--n <= 0 ? (clearInterval(upTimer), go()) : tick()), 1000);
    };
    const auto = h("input", {
      type: "checkbox",
      checked: ctx.profile.autoplay !== false,
      onchange: () => {
        count();
        void setAutoplay(ctx, auto.checked).catch(() => {});
      },
    }) as HTMLInputElement;
    upnext.replaceChildren(
      label,
      h("button.btn.p", { onclick: () => (clearInterval(upTimer), go()) }, "Play now"),
      h("button.btn.s", { onclick: () => (clearInterval(upTimer), goBack()) }, "Cancel"),
      h("label.auto", auto, "Autoplay"),
    );
    upnext.hidden = false;
    count();
  });
  // A rewind or replay after the end cancels the countdown, and the position is the viewer's own.
  for (const e of ["seeking", "play"])
    video.addEventListener(e, () => {
      // A nudge that leaves the element at its end is not a rewind.
      if (video.ended) return;
      ended = false;
      clearInterval(upTimer);
      upnext.hidden = true;
    });
  const seekTo = (ev: PointerEvent) => {
    const r = seek.getBoundingClientRect();
    video.currentTime = Math.min(1, Math.max(0, (ev.clientX - r.left) / r.width)) * dur();
  };
  // A stream without previews shows none. One frame loads at a time and the
  // latest wanted follows, so a sweep across the bar costs a handful of requests.
  let dragging = false;
  let frameWanted = "";
  let frameShown = "";
  let frameLoading = false;
  const loadFrame = () => {
    if (frameLoading || frameWanted === frameShown) return;
    frameLoading = true;
    frameShown = frameWanted;
    pvImg.src = frameWanted;
  };
  const frameDone = () => ((frameLoading = false), loadFrame());
  pvImg.addEventListener("load", frameDone);
  pvImg.addEventListener("error", frameDone);
  const preview = (x: number) => {
    const p = pb.stream.previews;
    if (!p) return;
    const v = scrub(p, x, seek.getBoundingClientRect(), dur() * 1000, PREVIEW_WIDTH);
    frameWanted = v.url;
    loadFrame();
    pvImg.style.aspectRatio = `${p.width} / ${p.height}`;
    pv.style.left = `${v.left}px`;
    pvTime.textContent = clock(v.ms / 1000);
    pvChapter.textContent = chapterAt(file()?.chapters ?? [], v.ms) ?? "";
    pv.hidden = false;
  };
  if (!live) {
    seek.addEventListener("pointermove", (ev) => preview(ev.clientX));
    seek.addEventListener("pointerleave", () => dragging || (pv.hidden = true));
    seek.addEventListener("pointerdown", (ev) => {
      seekTo(ev);
      dragging = true;
      const move = (e: PointerEvent) => (seekTo(e), preview(e.clientX));
      window.addEventListener("pointermove", move, listen);
      window.addEventListener(
        "pointerup",
        () => {
          dragging = false;
          window.removeEventListener("pointermove", move);
          pv.hidden = !seek.matches(":hover");
        },
        { once: true, ...listen },
      );
    });
    seek.addEventListener("keydown", (e) =>
      e.key === "ArrowRight" ? skip(10) : e.key === "ArrowLeft" ? skip(-10) : undefined,
    );
  }

  // Auto-hide, keyboard. FADE_MS matches .player.idle's transitions in
  // style.css. Without motion the class comes when the fade would have ended.
  const IDLE_MS = 2000;
  const FADE_MS = 1000;
  let idle: ReturnType<typeof setTimeout>;
  const wake = () => {
    root.classList.remove("idle");
    clearTimeout(idle);
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    idle = setTimeout(
      () => !open && !video.paused && root.classList.add("idle"),
      IDLE_MS + (still ? FADE_MS : 0),
    );
  };
  root.addEventListener("pointermove", wake);
  // A pause that comes from elsewhere, as from the other viewer, shows its note.
  video.addEventListener("pause", wake);
  const keys = (e: KeyboardEvent) => {
    wake();
    if ((e.target as HTMLElement).closest?.("[role=slider], input, select")) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    if (live && (k === "ArrowLeft" || k === "ArrowRight")) return;
    if (open && (k === "ArrowDown" || k === "ArrowUp")) step(k === "ArrowDown" ? 1 : -1);
    else if (k === " " && !(e.target as HTMLElement).closest?.("button")) toggleplay();
    else if (k === "ArrowLeft") skip(e.shiftKey ? -30 : -10);
    else if (k === "ArrowRight") skip(e.shiftKey ? 30 : 10);
    else if (k === "ArrowUp") video.volume = Math.min(1, video.volume + 0.1);
    else if (k === "ArrowDown") video.volume = Math.max(0, video.volume - 0.1);
    else if (k === "m") video.muted = !video.muted;
    else if (k === "f") fullscreen();
    else if (k === "c" && !live) nextSub();
    else if (k === "Escape") {
      if (open) toggle();
      else goBack();
    } else return;
    e.preventDefault();
  };
  document.addEventListener("keydown", keys, listen);

  // Reporting. Telemetry carries the position because a restart answer
  // resumes at the session's position.
  function report() {
    if (adjusting || done) return;
    void pb
      .throughput({
        positionMs: Math.round(here()),
        measuredBitrate: Math.max(0, Math.round(hls?.bandwidthEstimate ?? 0)),
        bufferMs: Math.round(
          Math.max(
            0,
            (video.buffered.length ? video.buffered.end(video.buffered.length - 1) : 0) -
              video.currentTime,
          ) * 1000,
        ),
        stallCount: stalls,
        ...rungTelemetry(pb.stream, hls?.levels, hls?.currentLevel ?? -1),
      })
      .then((u) => (u.action === "restart" && !done ? adjust(async () => u) : undefined))
      .catch(() => {});
  }
  if (tg) {
    sync = new Sync(
      ctx.api,
      tg,
      {
        paused: () => video.paused,
        ms: here,
        // A start the other side asked for has no gesture here; muted, the browser lets it play.
        play: () =>
          void video
            .play()
            .catch(() => ((video.muted = true), video.play()))
            .catch(() => {}),
        pause: () => video.pause(),
        seek: (ms) => (video.currentTime = ms / 1000),
      },
      paintTogether,
    );
    paintTogether(tg);
    void sync.start();
  }
  const offLive = onLive((events) => {
    for (const e of events) {
      if (done || live || e.sessionId !== pb.stream.sessionId) continue;
      // Another device took this session with Move here: the player closes and says where it went.
      if (e.kind === "playback" && e.change === "moved") {
        void movedTo(ctx.api, e.sessionId).then((to) => to && toast(`Moved to ${to}`));
        goBack();
      }
      // Another device asked this one to watch together: it joins, plays for both and starts over in sync.
      if (e.kind === "watch_together" && e.change === "opened" && e.watchTogetherId) {
        const path = { params: { path: { watchTogetherId: e.watchTogetherId } } };
        void ok(ctx.api.POST("/watch-together/{watchTogetherId}/join", path))
          .then(() =>
            ok(
              ctx.api.PUT("/watch-together/{watchTogetherId}/state", {
                ...path,
                body: { playing: true, positionMs: Math.round(here()) },
              }),
            ),
          )
          .then(refresh, () => {});
      }
    }
  });
  const timers = [
    setInterval(
      () => !video.paused && !adjusting && void pb.position(here()).catch(() => {}),
      5000,
    ),
    setInterval(report, 10000),
  ];
  const unwatch = watchGroups(
    env,
    () => pb.choice,
    (c) => void adjust(() => pb.groups(c, here())),
    browserTarget(),
  );

  // A station's stream plays on from program to ident to program: when what is on ends, by the
  // schedule, the station says what is on then.
  let followTimer: ReturnType<typeof setTimeout> | undefined;
  const follow = () => {
    if (!live || done) return;
    followTimer = setTimeout(
      async () => {
        const s = await ok(
          ctx.api.GET("/stations/{stationId}", {
            params: { path: { stationId: live.station.id } },
          }),
        ).catch(() => undefined);
        if (done) return;
        if (s) {
          onAir = s.onAir;
          subline.textContent = caption(s)[0];
          paint();
        }
        follow();
      },
      untilMs(onAir?.now?.endsAt ?? onAir?.next?.startsAt) ?? 60_000,
    );
  };
  if (live) paint();
  follow();
  const teardown = () => {
    if (done) return;
    done = true;
    gen++;
    off.abort();
    offLive();
    clearTimeout(followTimer);
    timers.forEach(clearInterval);
    clearInterval(upTimer);
    clearTimeout(idle);
    clearTimeout(leftTimer);
    sync?.stop();
    unwatch();
    if (document.fullscreenElement) void document.exitFullscreen();
    const pos = ended ? dur() * 1000 : here();
    video.pause();
    clearSource();
    // keepalive lets the request outlive the page.
    void pb.end(pos, true).catch(() => {});
  };
  window.addEventListener("hashchange", teardown, listen);
  window.addEventListener("pagehide", teardown, listen);
  document.addEventListener(
    "visibilitychange",
    () =>
      document.visibilityState === "hidden" &&
      !adjusting &&
      void pb.position(here(), video.paused, true).catch(() => {}),
    listen,
  );

  queueMicrotask(() => void attach(pb.stream, pb.stream.positionMs).then(wake));
  return [root];
}
