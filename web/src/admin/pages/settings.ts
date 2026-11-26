import type { Ctx } from "../api";
import { ApiError, MIN_PASSWORD, ok } from "../api";
import { card, confirming, header, setting } from "../common";
import { errorLine, field, h, input, showError, sub, tag } from "../dom";
import { bytes } from "../format";
import type { Schemas } from "../../api/client";

function serverCard(ctx: Ctx, s: Schemas["Settings"]) {
  const name = input("text", {
    required: true,
    maxLength: 100,
    value: s.serverName,
    "aria-label": "Server name",
  });
  const status = sub("");
  name.onchange = async () => {
    const serverName = name.value.trim();
    if (!serverName) return void (status.textContent = "A name is required.");
    try {
      Object.assign(s, await ok(ctx.api.PUT("/settings", { body: { serverName } })));
      ctx.renamed(s.serverName);
      status.textContent = "Saved";
    } catch (x) {
      status.textContent = (x as Error).message;
    }
  };
  return card(
    "Server",
    setting(
      "Server name",
      "Shown beside the logo in every app, and where people sign in",
      status,
      name,
    ),
  );
}

function profilesCard(ctx: Ctx, s: Schemas["Settings"]) {
  const status = sub("");
  const sw = h("input", {
    type: "checkbox",
    role: "switch",
    "aria-label": "Gravatar",
    checked: !!s.gravatar,
    onchange: async () => {
      try {
        Object.assign(
          s,
          await ok(
            ctx.api.PUT("/settings", {
              body: { serverName: s.serverName, gravatar: sw.checked },
            }),
          ),
        );
        status.textContent = "Saved";
      } catch (x) {
        sw.checked = !sw.checked;
        status.textContent = (x as Error).message;
      }
    },
  }) as HTMLInputElement;
  return card(
    "Profiles",
    setting(
      "Gravatar",
      "Adults with a Gravatar show it as their picture. The server fetches and caches each picture; clients never contact gravatar.com.",
      status,
      sw,
    ),
  );
}

type Draw = (sourcesNote?: string, keyNote?: string) => void;

const pick = (label: string, value: string, options: [string, string][]) =>
  h(
    "select.in",
    { "aria-label": label },
    ...options.map(([v, text]) => h("option", { value: v, selected: v === value }, text)),
  ) as HTMLSelectElement;

const READ_AHEADS: Record<number, string> = {
  30: "30 seconds",
  60: "1 minute",
  120: "2 minutes",
  300: "5 minutes",
  600: "10 minutes",
};

/** A menu row for one numeric setting, saved as it changes. */
function menuRow(
  ctx: Ctx,
  s: Schemas["Settings"],
  key: "readAheadSeconds" | "maxTranscodes" | "transcodeCacheCapBytes",
  label: string,
  caption: string,
  values: number[],
  text: (n: number) => string,
) {
  const status = sub("");
  const now = s[key];
  // A value saved through the API that the menu lacks shows as it is.
  const menu = pick(
    label,
    String(now),
    (values.includes(now) ? values : [now, ...values]).map((n) => [String(n), text(n)]),
  );
  menu.onchange = async () => {
    try {
      Object.assign(
        s,
        await ok(
          ctx.api.PUT("/settings", {
            body: {
              serverName: s.serverName,
              [key]: Number(menu.value),
            },
          }),
        ),
      );
      status.textContent = "Saved";
    } catch (x) {
      menu.value = String(s[key]);
      status.textContent = (x as Error).message;
    }
  };
  return setting(label, caption, status, menu);
}

function transcodingCard(ctx: Ctx, s: Schemas["Settings"]) {
  return card(
    "Transcoding",
    menuRow(
      ctx,
      s,
      "maxTranscodes",
      "Simultaneous transcodes",
      "Video streams converted at once. Streams beyond this wait.",
      [1, 2, 3, 4, 5, 6, 7, 8],
      String,
    ),
    menuRow(
      ctx,
      s,
      "transcodeCacheCapBytes",
      "Cache limit",
      `Converted video kept on disk. Past this, the oldest idle streams are deleted. ${bytes(s.storage.transcodeCacheBytes)} used now.`,
      [5e9, 10e9, 20e9, 50e9, 100e9],
      bytes,
    ),
    menuRow(
      ctx,
      s,
      "readAheadSeconds",
      "Read-ahead",
      "How far ahead of the viewer a stream is converted and players buffer. More rides out a poor connection; it takes more disk, and more work after each seek.",
      Object.keys(READ_AHEADS).map(Number),
      (n) => READ_AHEADS[n] ?? `${n} seconds`,
    ),
  );
}

function sourcesRow(ctx: Ctx, s: Schemas["Settings"], draw: Draw, note: string) {
  const m = s.metadata;
  const status = sub(note);
  const source = pick("Metadata source", m.source ?? "local", [
    ["local", "Local"],
    ["remote", "Remote"],
  ]);
  const mode = pick("Metadata mode", m.mode ?? "only", [
    ["only", "Only"],
    ["preferred", "Preferred"],
  ]);
  source.onchange = mode.onchange = async () => {
    try {
      Object.assign(
        s,
        await ok(
          ctx.api.PUT("/settings", {
            body: {
              serverName: s.serverName,
              metadataSource: source.value as Schemas["MetadataSource"],
              metadataMode: mode.value as Schemas["MetadataMode"],
            },
          }),
        ),
      );
      draw("Saved");
    } catch (x) {
      status.textContent = (x as Error).message;
    }
  };
  const remote = m.source === "remote" || m.mode === "preferred";
  return h(
    "div",
    setting(
      "Source",
      "Local reads each file's tags and pictures, else a frame of the video, and music's covers and YouTube's sidecar files. Remote fetches from TMDB, MusicBrainz, the Cover Art Archive and YouTube. Only uses one; Preferred fills what it lacks from the other.",
      status,
      h("div.row", source, mode),
    ),
    remote && !m.tmdbConfigured
      ? h(
          "p.sub",
          "No TMDB key is set, so movies and TV have only local data. Music and YouTube need no key.",
        )
      : null,
  );
}

/** The TMDB row: the key is only ever sent, never shown; the server says whether one is set. */
function tmdbRow(ctx: Ctx, s: Schemas["Settings"], draw: Draw, note: string) {
  const on = s.metadata.tmdbConfigured;
  const localOnly =
    (s.metadata.source ?? "local") === "local" && (s.metadata.mode ?? "only") === "only";
  const err = errorLine();
  err.id = `e-${Math.random().toString(36).slice(2, 8)}`;
  const key = input("password", {
    "aria-label": "TMDB API key",
    "aria-describedby": err.id,
    autocomplete: "off",
    maxLength: 256,
    placeholder: on ? "New key" : "API key",
  });
  const save = async (tmdbKey: string) => {
    try {
      Object.assign(
        s,
        await ok(
          ctx.api.PUT("/settings", {
            body: { serverName: s.serverName, tmdbKey },
          }),
        ),
      );
      draw("", tmdbKey ? "Saved" : "Removed");
    } catch (x) {
      const fe = x instanceof ApiError ? x.fields.find((f) => f.field === "tmdbKey") : undefined;
      if (fe) key.setAttribute("aria-invalid", "true");
      showError(err, fe?.message ?? (x as Error).message);
    }
  };
  return setting(
    "TMDB",
    on
      ? "Movies and TV: titles, ratings, cast and artwork"
      : "Paste an API key from your TMDB account settings.",
    note ? sub(note) : null,
    on ? tag("Key set", "ok") : tag("Not configured", "err"),
    h(
      "form",
      {
        onsubmit: (e: Event) => {
          e.preventDefault();
          key.removeAttribute("aria-invalid");
          const v = key.value.trim();
          if (!v) return showError(err, "Enter a key.");
          showError(err);
          void save(v);
        },
      },
      h(
        "div.row",
        key,
        h("button.ib.p", { type: "submit" }, "Save"),
        on ? h("button.ib", { type: "button", onclick: () => save("") }, "Remove key") : null,
      ),
      err,
      localOnly ? h("p.sub", "Not used while metadata is Local only.") : null,
    ),
  );
}

/** Both metadata rows, drawn again after either saves, since each row's note reads the other's setting. */
function metadataRows(ctx: Ctx, s: Schemas["Settings"]) {
  const box = h("div");
  const draw: Draw = (sourcesNote = "", keyNote = "") =>
    box.replaceChildren(sourcesRow(ctx, s, draw, sourcesNote), tmdbRow(ctx, s, draw, keyNote));
  draw();
  return box;
}

function passwordForm(ctx: Ctx, done: () => void, cancel: () => void) {
  const current = input("password", { required: true, autocomplete: "current-password" });
  const next = input("password", { required: true, autocomplete: "new-password" });
  const again = input("password", { required: true, autocomplete: "new-password" });
  const err = errorLine();
  return h(
    "form.stack.narrow",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        if (next.value.length < MIN_PASSWORD)
          return showError(err, `Use at least ${MIN_PASSWORD} characters.`);
        if (next.value !== again.value) return showError(err, "The passwords do not match.");
        try {
          await ok(
            ctx.api.PUT("/me/password", {
              body: { currentPassword: current.value, newPassword: next.value },
            }),
          );
          done();
        } catch (x) {
          showError(err, (x as Error).message);
        }
      },
    },
    field("Current password", current),
    field("New password", next, `At least ${MIN_PASSWORD} characters`),
    field("Confirm new password", again),
    err,
    h(
      "div.row",
      h("button.ib.p", { type: "submit" }, "Change password"),
      h("button.ib", { type: "button", onclick: cancel }, "Cancel"),
    ),
  );
}

function adminCard(ctx: Ctx) {
  const slot = h("div");
  const idle = (note = `Signed in as ${ctx.me.account.email ?? ctx.me.account.name}`) =>
    slot.replaceChildren(
      setting(
        "Password",
        note,
        h(
          "button.ib",
          {
            onclick: () =>
              slot.replaceChildren(
                passwordForm(
                  ctx,
                  () => idle("Password changed. Your other devices are signed out."),
                  () => idle(),
                ),
              ),
          },
          "Change password",
        ),
      ),
    );
  idle();
  return card("Admin account", slot);
}

/** Facts the server reads at start-up or detects; none can change here. */
function aboutCard(ctx: Ctx, s: Schemas["Settings"]) {
  const { storage: st, hardware: hw } = s;
  const fromEnv = (name: string, value: string) => [h("span.path", name), sub(value)];
  return card(
    "About this server",
    h(
      "p.sub",
      "Nothing here changes from this page. A row that names a variable is read once, when the server starts: set it in the ",
      h("span.path", "env"),
      " file in the server's folder, then restart.",
    ),
    setting("Version", null, sub(`Rabbit Ears ${ctx.health.version}`)),
    setting("Port", null, ...fromEnv("RABBIT_EARS_PORT", String(s.port))),
    setting("Database", null, ...fromEnv("RABBIT_EARS_DB", s.databasePath)),
    setting(
      "Data folder",
      "Caches, artwork and thumbnails",
      ...fromEnv("RABBIT_EARS_DATA", s.dataDir),
    ),
    setting(
      "Disk",
      "The volume holding the data folder",
      sub(`${bytes(st.diskFreeBytes)} free of ${bytes(st.diskTotalBytes)}`),
    ),
    setting("Processor", null, sub(`${hw.cpuModel} · ${hw.cores} cores`)),
    setting("Memory", null, sub(bytes(hw.memoryBytes))),
    setting(
      "Video encoder",
      "Detected when the server starts",
      hw.videoToolbox ? tag("Apple VideoToolbox", "ok") : tag("Software only", "warn"),
    ),
    setting(
      "Subtitles",
      "A subtitle shows on its own only when the file marks it forced; viewers choose any other in the player. Text subtitles are sent beside the video; bitmap ones are converted, or burned in as a last resort.",
    ),
  );
}

export async function settings(ctx: Ctx): Promise<HTMLElement[]> {
  const { api } = ctx;
  const [s, attribution] = await Promise.all([
    ok(api.GET("/settings")),
    ok(api.GET("/attribution")),
  ]);
  const danger = card(
    "Danger zone",
    setting(
      "Clear transcode cache",
      "Deletes converted segments and extracted subtitles. Streams playing now keep theirs; everything else is converted again when played.",
      confirming("Clear cache", "Clear the transcode cache?", async () => {
        await ok(api.DELETE("/transcode-cache"));
        return "Cleared";
      }),
    ),
    setting(
      "Refresh all metadata",
      "Fetches titles, ratings, cast and artwork from TMDB again for every item, 200 items per hourly Refresh metadata run, while metadata uses remote data.",
      confirming("Refresh all", "Refresh metadata for every item?", async () => {
        await ok(api.POST("/metadata/refresh"));
        return "Queued";
      }),
    ),
  );
  danger.classList.add("danger");
  return [
    header("Settings", "Changes save as you make them"),
    serverCard(ctx, s),
    profilesCard(ctx, s),
    transcodingCard(ctx, s),
    card(
      "Metadata sources",
      metadataRows(ctx, s),
      h(
        "p.sub",
        attribution.text,
        " ",
        /^https?:\/\//i.test(attribution.url)
          ? h(
              "a",
              { href: attribution.url, target: "_blank", rel: "noreferrer" },
              attribution.provider,
            )
          : attribution.provider,
      ),
    ),
    adminCard(ctx),
    danger,
    aboutCard(ctx, s),
  ];
}
