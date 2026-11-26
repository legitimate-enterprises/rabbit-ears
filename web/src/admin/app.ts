import { ApiError, createApi, getToken, setToken, type Ctx } from "./api";
import { ok } from "./api";
import { h, sub, tag } from "./dom";
import { logo } from "../viewer/ui";
import { firstRun } from "./pages/firstRun";
import { signIn } from "./pages/signIn";
import { dashboard } from "./pages/dashboard";
import { attention } from "./pages/attention";
import { libraries } from "./pages/libraries";
import { stations } from "./pages/stations";
import { occasions } from "./pages/occasions";
import { users } from "./pages/users";
import { sso } from "./pages/sso";
import { tasks } from "./pages/tasks";
import { logs } from "./pages/logs";
import { settings } from "./pages/settings";

type Page = (ctx: Ctx) => Promise<HTMLElement[]>;
export const routes: [string, string, Page][] = [
  ["dashboard", "Dashboard", dashboard],
  ["attention", "Needs attention", attention],
  ["libraries", "Libraries", libraries],
  ["stations", "Stations", stations],
  ["occasions", "Occasions", occasions],
  ["users", "Users & profiles", users],
  ["sso", "Single sign-on", sso],
  ["tasks", "Tasks", tasks],
  ["logs", "Logs", logs],
  ["settings", "Settings", settings],
];

export interface BootOptions {
  fetch?: (req: Request) => Promise<Response>;
  /** Goes to a viewer URL: home for non-admins, Who's watching for a locked device. */
  leave?: (to: string) => void;
}

export const brand = (name = "Rabbit Ears") =>
  h(
    "a.brand",
    { href: "#/dashboard" },
    logo(),
    h("span.name", { title: name }, name),
    h("span.tag.admin", "Admin"),
  );

// The shell's polling, which outlives its page until a new boot, such as after sign-out.
const timers: number[] = [];
const stop = () => timers.splice(0).forEach(clearInterval);

export async function boot(root: HTMLElement, opts: BootOptions = {}): Promise<void> {
  stop();
  window.onhashchange = null;
  const token = getToken();
  const api = createApi({ token, fetch: opts.fetch });
  const again = () => boot(root, opts);
  const leave = opts.leave ?? ((to: string) => location.assign(to));
  const health = await ok(api.GET("/health"));
  if (health.setupRequired) return firstRun(root, { api, fetch: opts.fetch, done: again, brand });
  if (!token) return signIn(root, { api, done: again, brand });
  let me;
  try {
    me = await ok(api.GET("/me"));
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      setToken();
      return again();
    }
    throw e;
  }
  // Only an admin acting as themselves may use this UI, as the server requires.
  if (!me.account.isAdmin || me.activeProfileId !== me.account.id) return leave("/");
  mountShell(
    root,
    {
      api,
      me,
      health,
      path: [],
      every: () => {},
      recount: () => {},
      renamed: () => {},
      signedOut: () => (setToken(), void again()),
    },
    () => leave("/#/who"),
    () => leave("/"),
  );
}

function mountShell(root: HTMLElement, base: Ctx, locked: () => void, switched: () => void) {
  const main = h("main");
  const nav = h("nav", { "aria-label": "Admin" });
  const mark = brand(base.health.serverName);
  const renamed = (name: string) => {
    const el = mark.querySelector<HTMLElement>(".name")!;
    el.textContent = el.title = name;
  };
  root.replaceChildren(
    h(
      "div.layout",
      h(
        "aside.side",
        mark,
        nav,
        h("div.foot", h("div", h("span.dot"), "Online"), sub(`Rabbit Ears ${base.health.version}`)),
        h(
          "button.ib",
          {
            onclick: async () => {
              await base.api.POST("/auth/sign-out");
              base.signedOut();
            },
          },
          "Sign out",
        ),
      ),
      main,
    ),
  );
  // Needs attention is in the nav only while something does; the count is read on each poll.
  let current = "";
  let attention = 0;
  const drawNav = () =>
    nav.replaceChildren(
      ...routes
        .filter(([s]) => s !== "attention" || attention > 0)
        .map(([s, t]) =>
          h(
            "a",
            { href: `#/${s}`, "aria-current": s === current ? "page" : undefined },
            t,
            s === "attention" ? tag(String(attention), "warn") : null,
          ),
        ),
    );
  const recount = () =>
    void ok(base.api.GET("/attention"))
      .then((c) => {
        attention = c.identity + c.unstreamable + c.missing + c.failed;
        drawNav();
      })
      // A device signed out, locked or switched off its admin since is told on every page by this
      // poll; else the next one retries.
      .catch((e) => {
        if (e instanceof ApiError && e.status === 401) base.signedOut();
        else if (e instanceof ApiError && e.code === "device_locked") locked();
        else if (e instanceof ApiError && e.code === "forbidden") switched();
      });
  // Bumped by every navigation; a page still loading under an older value neither shows nor polls.
  let gen = 0;
  const render = async () => {
    const mine = ++gen;
    stop();
    // Dialogs sit on the body, outside the page that opened them.
    document.querySelectorAll(".scrim").forEach((e) => e.remove());
    const [key, ...rest] = (location.hash.replace(/^#\//, "") || "dashboard").split("/");
    const route = routes.find((r) => r[0] === key);
    const [slug, title, page] = route ?? routes[0];
    current = slug;
    drawNav();
    recount();
    timers.push(window.setInterval(() => document.hidden || recount(), 5000));
    document.title = `${title} - Rabbit Ears Admin`;
    main.replaceChildren(h("div.skel"), h("div.skel"));
    const ctx: Ctx = {
      ...base,
      path: route ? rest : [],
      recount,
      renamed,
      every: (fn, ms) => void (mine === gen && timers.push(window.setInterval(fn, ms))),
    };
    const load = async () => {
      try {
        const kids = await page(ctx);
        if (mine === gen) main.replaceChildren(...kids);
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return base.signedOut();
        // The viewer's Who's watching locked this browser's device; a pick there unlocks it.
        if (e instanceof ApiError && e.code === "device_locked") return locked();
        if (mine !== gen) return;
        main.replaceChildren(
          h("p.err", { role: "alert" }, e instanceof Error ? e.message : "Failed to load"),
          h("button.ib", { onclick: load }, "Retry"),
        );
      }
    };
    await load();
  };
  window.onhashchange = render;
  void render();
}
