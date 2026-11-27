import { ApiError, createApi, getToken, ok, setToken } from "../admin/api";
import { h } from "../admin/dom";
import { avatar, brand, menu, nameServer, opt, placeholder } from "./ui";
import { watchArt } from "./artwork";
import type { Ctx, Me } from "./data";
import { profileTabs, remembered } from "./device";
import { signIn, accept } from "./pages/signIn";
import { who, addKid, addAdult, password, editKid, preferences } from "./pages/who";
import { household } from "./pages/household";
import { search, personPage } from "./pages/search";
import { home } from "./pages/home";
import { grid } from "./pages/grid";
import { movie, show, season, episode, video } from "./pages/details";
import { player } from "./pages/player";
import { link } from "./pages/link";
import { music, artist, album, nowPlaying } from "./pages/music";
import { playlist } from "./pages/playlists";
import { collection } from "./pages/collections";
import { occasion } from "./pages/occasion";
import { editStation, newStation, stationPage, stations, tune } from "./pages/stations";
import { channel, youtube } from "./pages/youtube";
import { mini, music as musicEngine, resetMusic } from "./audio";
import { command, helpSheet, SECTIONS } from "./keys";
import { follow, onLive, type LiveEvent } from "./live";
import { loadSections, rememberedSections, SECTION_KINDS } from "./sections";

export interface BootOptions {
  fetch?: (req: Request) => Promise<Response>;
}
export type Page = (ctx: Ctx, args: string[], query: URLSearchParams) => Promise<HTMLElement[]>;

const routes: Record<string, Page> = {
  home,
  tv: (c, _a, q) => grid(c, "tv", q),
  movies: (c, _a, q) => grid(c, "movies", q),
  movie,
  collection,
  occasion,
  station: stationPage,
  stations,
  "station-new": adult(newStation),
  "station-edit": adult(editStation),
  tune,
  preferences: adult(preferences),
  household,
  show,
  season,
  episode,
  search,
  person: personPage,
  music,
  artist,
  album,
  "now-playing": nowPlaying,
  playlist,
  youtube,
  channel,
  video,
};

/** The routes that show what each kind of event changes; the player follows its own. */
const WATCHED = ["home", "movie", "show", "season", "episode", "video", "tv", "movies"];
const SHOWS: Partial<Record<LiveEvent["kind"], string[]>> = {
  watch_state: [...WATCHED, "youtube", "channel", "collection"],
  playback: [...WATCHED, "youtube", "channel", "collection"],
  station: ["home", "stations", "station", "tv", "movies", "music"],
  watch_together: ["movie", "episode", "video"],
  playlist: ["playlist", "tv", "movies", "music"],
  library: ["home", "tv", "movies"],
  household: ["household"],
};
const routeKey = () => location.hash.replace(/^#\/?/, "").split(/[?/]/)[0] || "home";
/** The pages drawn without the top bar: the player and the forms. */
const BARE = [
  "who",
  "add-kid",
  "add-adult",
  "password",
  "play",
  "tune",
  "household",
  "kid",
  "station-new",
  "station-edit",
];
/** The forms only an adult may open; a kid who types one lands on Home. */
const ADULT_FORMS = ["add-kid", "add-adult", "password", "kid"];
/** Ends the last boot's following. */
let unfollow = () => {};
/** The sections of the top navigation the server has libraries for; all until it has answered. */
let shown = rememberedSections();
/** Counts boots; a boot overtaken by a later one leaves the screen to it. */
let boots = 0;

/** A route kids may not open lands on Home. */
function adult(p: Page): Page {
  return (c, a, q) => (c.profile.kind === "kid" ? home(c) : p(c, a, q));
}

export async function boot(root: HTMLElement, opts: BootOptions = {}, start = true): Promise<void> {
  root.dataset.surface = "viewer";
  const mine = ++boots;
  unfollow();
  resetMusic();
  // The page already mounted stops rendering while this boot loads.
  window.onhashchange = null;
  // Pages end their work on a hashchange, which a boot that replaces them does not send.
  window.dispatchEvent(new HashChangeEvent("hashchange"));
  const token = getToken();
  const api = createApi({ token, fetch: opts.fetch });
  // Only the page load's own boot is a start; signing in, a pick and a reload boot again.
  const again = () => boot(root, opts, false);
  // A lock or a pick in another tab changes who this one answers as; Who's watching gives way to the pick.
  profileTabs.onmessage = () => {
    const then = new URLSearchParams(location.hash.split("?")[1] ?? "").get("then");
    if (/^#\/who\b/.test(location.hash))
      history.replaceState(null, "", then?.startsWith("#/") ? then : "#/");
    void again();
  };
  // Asked when needed: a single sign-on returns to / and is then put back on /link.
  const isLink = () => /^\/link\/?$/.test(location.pathname);
  const join = /^\/join\/([A-Za-z0-9-]+)\/?$/.exec(location.pathname)?.[1];
  const health = await ok(api.GET("/health"));
  if (mine !== boots) return;
  nameServer(health.serverName);
  if (join)
    return accept(root, {
      api,
      code: join,
      done: () => (history.replaceState(null, "", "/"), (location.hash = "#/who"), again()),
      brand,
    });
  if (health.setupRequired)
    return void root.replaceChildren(
      h(
        "main.solo",
        brand(),
        h("p", "This server is not set up yet. "),
        h("a", { href: "/admin/" }, "Open the admin console"),
      ),
    );
  if (!token)
    return signIn(root, {
      api,
      // Signing in for /link returns to it; the signed-in adult is already the active profile.
      done: () => (isLink() || (location.hash = "#/who"), again()),
      brand,
      invite: (code) => (history.pushState(null, "", `/join/${code}`), again()),
    });
  let me;
  try {
    me = await ok(api.GET("/me"));
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return (setToken(), again());
    throw e;
  }
  if (mine !== boots) return;
  const atWho = /^#\/who\b/.test(location.hash);
  if (start && !atWho && !isLink()) resume(me);
  else if (me.locked && !atWho) toWho();
  // A locked device picks a profile first; the pick boots back to /link.
  if (isLink() && !me.locked)
    return link(root, {
      api,
      profile: me.profiles.find((p) => p.id === me.activeProfileId) ?? me.account,
      brand,
      code: new URLSearchParams(location.search).get("code") ?? undefined,
      signedOut: () => (setToken(), again()),
    });
  const ctx: Ctx = {
    api,
    me,
    health,
    profile: me.profiles.find((p) => p.id === me.activeProfileId) ?? me.account,
    go: (hash) => (location.hash = hash),
    signOut: () => void api.POST("/auth/sign-out").finally(() => (setToken(), again())),
    switchProfile: () => (location.hash = "#/who"),
    reload: () => void again(),
    cache: new Map(),
  };
  mount(root, ctx);
}

/**
 * Puts Who's watching in place of the current page, which a pick returns to;
 * given a profile, Who's watching picks it at once.
 */
const toWho = (pick?: string) =>
  history.replaceState(
    null,
    "",
    `#/who?then=${encodeURIComponent(location.hash || "#/")}${pick ? `&pick=${pick}` : ""}`,
  );

/** At start, stays the device's active profile when its adult chose Remember profile on this browser. */
function resume(me: Me) {
  const r = remembered(me.account.id);
  const p = me.profiles.find((x) => x.id === me.activeProfileId && !x.pending && !x.revokedAt);
  if (!r.profile || !p) return toWho();
  // A locked device is reached only by unlocking p, and a PIN not skipped only by entering it.
  if (me.locked || (p.hasPin && !r.skipPin)) toWho(p.id);
}

function mount(root: HTMLElement, ctx: Ctx) {
  // The body, not root: dialogs are appended to it.
  watchArt(document.body, ctx.api, placeholder);
  resetMusic(ctx);
  const main = h("main");
  let gen = 0;
  // Set for the one render a live update asks for, which redraws in place.
  let quiet = false;
  let due: ReturnType<typeof setTimeout> | undefined;
  /** Whether redrawing now would take something from under the viewer's hands. */
  const busy = () =>
    !!document.querySelector(".scrim, .selecting, .keysheet, .menu.drop:not([hidden])") ||
    (main.contains(document.activeElement) &&
      /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement!.tagName));
  const quietly = () => {
    if (busy()) return void (due = setTimeout(quietly, 2000));
    // The viewer may have left for a page that is not redrawn, such as the player.
    if (!Object.values(SHOWS).some((keys) => keys.includes(routeKey()))) return;
    quiet = true;
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  };
  // A locked device is told nothing; the pick that unlocks it boots again.
  const stop = ctx.me.locked ? () => {} : follow(ctx.api, undefined, ctx.signOut);
  const off = onLive((events) => {
    // A reload of everything shown may follow a scan whose event was missed.
    if (!events.length || events.some((e) => e.kind === "library")) ctx.cache.clear();
    if (events.some((e) => e.kind === "library")) void refresh();
    if (!events.length || events.some((e) => e.kind === "household")) void preferences();
    const key = routeKey();
    const shown = events.length
      ? events.some((e) => SHOWS[e.kind]?.includes(key))
      : Object.values(SHOWS).some((keys) => keys.includes(key));
    if (!shown) return;
    // A burst of events is one redraw.
    clearTimeout(due);
    due = setTimeout(quietly, 250);
  });
  shown = rememberedSections();
  // Libraries change with a sign-in, a profile, a scan and a return to the tab; the bar and the page follow.
  const born = boots;
  const refresh = async () => {
    const next = await loadSections(ctx);
    if (born !== boots || [...next].join() === [...shown].join()) return;
    // The redraw would close an open menu or dialog; it asks again once that may have closed.
    if (busy()) return void setTimeout(refresh, 2000);
    shown = next;
    // A page without the bar has nothing to redraw, and a hashchange would end what the player plays.
    if (!BARE.includes(routeKey())) window.dispatchEvent(new HashChangeEvent("hashchange"));
  };
  // A preference saved on another device takes effect here as the local toggle does: by booting again.
  const preferences = async () => {
    const next = await ctx.api.GET("/me").catch(() => undefined);
    const mine = next?.data?.profiles.find((p) => p.id === next.data.activeProfileId);
    if (born === boots && mine && !!mine.hideKidsContent !== !!ctx.profile.hideKidsContent)
      ctx.reload();
  };
  const back = () => document.visibilityState === "visible" && void refresh();
  document.addEventListener("visibilitychange", back);
  void refresh();
  unfollow = () => (
    stop(),
    off(),
    clearTimeout(due),
    document.removeEventListener("visibilitychange", back)
  );
  const render = async () => {
    const mine = ++gen;
    const inPlace = quiet;
    quiet = false;
    const [name, ...args] = location.hash.replace(/^#\/?/, "").split("?")[0].split("/");
    const query = new URLSearchParams(location.hash.split("?")[1] ?? "");
    const key = name || "home";
    // A section the server has no library for is Home.
    if (SECTION_KINDS.includes(key) && !shown.has(key)) return location.replace("#/");
    if (ADULT_FORMS.includes(key) && ctx.profile.kind === "kid") return location.replace("#/");
    if (inPlace) {
      try {
        const kids = await (routes[key] ?? home)(ctx, args, query);
        if (mine === gen) swap(main, kids);
      } catch (e) {
        // What is shown stays; only a device signed out leaves.
        if (e instanceof ApiError && e.status === 401) ctx.signOut();
      }
      return;
    }
    document.querySelectorAll(".scrim").forEach((e) => e.remove());
    const bare = BARE.includes(key);
    const bar = root.querySelector<HTMLElement>("header.top");
    // Typing searches on: the header holding the focused field stays in place, so it keeps focus.
    if (key === "search" && bar?.contains(document.activeElement)) {
      for (const c of [...root.children]) if (c !== bar) c.remove();
      bar.querySelectorAll("a.nav[aria-current]").forEach((a) => a.removeAttribute("aria-current"));
      root.append(main);
    } else {
      root.replaceChildren(...(bare ? [main] : [topbar(ctx, key, query.get("q") ?? ""), main]));
      if (key === "search") root.querySelector<HTMLInputElement>("input[type=search]")?.focus();
    }
    // Music plays on under every page but the full-screen video player.
    const m = musicEngine();
    if (m && (key === "play" || key === "tune")) m.pause();
    else if (m && !bare) root.append(mini(m));
    main.className = bare ? "" : "page";
    main.replaceChildren(h("p.sub", "Loading"));
    try {
      let kids: HTMLElement[];
      if (key === "who") kids = await who(ctx, query);
      else if (key === "add-kid") kids = await addKid(ctx);
      else if (key === "add-adult") kids = await addAdult(ctx);
      else if (key === "password") kids = await password(ctx);
      else if (key === "kid") kids = await editKid(ctx, args[0]);
      else if (key === "play") kids = await player(ctx, args[0], query);
      else kids = await (routes[key] ?? home)(ctx, args, query);
      if (mine === gen) main.replaceChildren(...kids);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return ctx.signOut();
      // Another tab of this browser showed Who's watching, which locks the device they share.
      if (e instanceof ApiError && e.code === "device_locked" && key !== "who") {
        if (mine !== gen) return;
        toWho();
        return render();
      }
      if (mine === gen) main.replaceChildren(h("p.err", { role: "alert" }, (e as Error).message));
    }
  };
  window.onhashchange = render;
  window.onkeydown = (e) => shortcut(e, root);
  void render();
}

/** Puts kids in place of main's with the art already painted and every scroll position kept. */
function swap(main: HTMLElement, kids: HTMLElement[]) {
  const painted = new Map<string, string>();
  for (const e of main.querySelectorAll<HTMLElement>("[data-art]"))
    if (e.style.backgroundImage) painted.set(e.dataset.art!, e.style.cssText);
  const strips = [...main.querySelectorAll(".strip")].map((s) => s.scrollLeft);
  const y = window.scrollY;
  for (const k of kids)
    for (const e of [k, ...k.querySelectorAll<HTMLElement>("[data-art]")]) {
      const was = e.dataset.art && painted.get(e.dataset.art);
      if (was && !e.style.backgroundImage) e.style.cssText = was;
    }
  main.replaceChildren(...kids);
  main.querySelectorAll(".strip").forEach((s, i) => (s.scrollLeft = strips[i] ?? 0));
  window.scrollTo(0, y);
}

/** Answers the app-wide shortcuts; a page that took the key first keeps it. */
function shortcut(e: KeyboardEvent, root: HTMLElement) {
  const c = e.defaultPrevented ? undefined : command(e);
  if (!c) return;
  e.preventDefault();
  if (c === "back") history.back();
  else if (c === "forward") history.forward();
  else if (c === "help") {
    if (!document.querySelector(".keysheet")) root.append(helpSheet());
  } else if (c === "search") {
    const field = root.querySelector<HTMLInputElement>("input[type=search]");
    if (field) field.select();
    // A page without the top bar, as the player, leaves for Search, whose bar takes the focus.
    else location.hash = "#/search";
  } else {
    const [section, to] = SECTIONS.find(([s]) => s === c)!;
    if (!SECTION_KINDS.includes(section) || shown.has(section)) location.hash = to;
  }
}

function topbar(ctx: Ctx, current: string, q: string) {
  const field = h("input.in", {
    type: "search",
    "aria-label": "Search",
    placeholder: "Search",
    value: q,
  }) as HTMLInputElement;
  const find = () => {
    const q = field.value.trim();
    if (!q) return;
    const to = `#/search?q=${encodeURIComponent(q)}`;
    // Searches typed on replace one another in history.
    if (location.hash.startsWith("#/search")) location.replace(to);
    else ctx.go(to);
  };
  let typing: ReturnType<typeof setTimeout> | undefined;
  field.oninput = () => (clearTimeout(typing), (typing = setTimeout(find, 300)));
  const link = (to: string, label: string) => {
    const on =
      current === to ||
      (to === "tv" && ["show", "season", "episode"].includes(current)) ||
      (to === "movies" && ["movie", "collection"].includes(current)) ||
      (to === "music" && ["artist", "album", "now-playing"].includes(current)) ||
      (to === "youtube" && ["channel", "video"].includes(current)) ||
      (to === "stations" && current === "station");
    return h(
      "a.nav",
      { href: `#/${to === "home" ? "" : to}`, "aria-current": on ? "page" : undefined },
      label,
    );
  };
  return h(
    "header.top",
    brand(),
    h(
      "nav",
      { "aria-label": "Library" },
      link("home", "Home"),
      shown.has("tv") && link("tv", "TV"),
      shown.has("movies") && link("movies", "Movies"),
      shown.has("music") && link("music", "Music"),
      shown.has("youtube") && link("youtube", "YouTube"),
      link("stations", "Stations"),
    ),
    h("span.grow"),
    h(
      "form.search",
      {
        role: "search",
        onsubmit: (e: Event) => {
          e.preventDefault();
          clearTimeout(typing);
          if (field.value.trim()) ctx.go(`#/search?q=${encodeURIComponent(field.value.trim())}`);
        },
      },
      field,
    ),
    profileMenu(ctx),
  );
}

function profileMenu(ctx: Ctx) {
  const adult = ctx.profile.kind === "adult";
  return menu(
    h(
      "button.nav.profile",
      { type: "button" },
      avatar(ctx.profile, 28),
      h("span", ctx.profile.name),
    ),
    "Profile",
    [
      opt("Switch profile", ctx.switchProfile),
      adult && opt("Preferences", "#/preferences"),
      adult && opt("Manage household", "#/household"),
      adult && ctx.me.account.isAdmin && opt("Admin", "/admin/"),
      adult && opt("Password", "#/password"),
      opt("Sign out", ctx.signOut),
    ],
  );
}
