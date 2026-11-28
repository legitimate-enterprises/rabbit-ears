import { ApiError, MIN_PASSWORD, ok } from "../../admin/api";
import { errorLine, h, input, showError } from "../../admin/dom";
import { MOVIE_RATINGS, TV_RATINGS, type Schemas } from "../../api/client";
import { title, type Ctx } from "../data";
import { avatar, brand, icon, swatches } from "../ui";
import { resetMusic } from "../audio";
import { profileTabs, remember, remembered } from "../device";
import { deleteAccountButton } from "./deleteAccount";

async function unlock(ctx: Ctx, p: Schemas["Account"], pin?: string, to = "#/") {
  await ok(
    ctx.api.POST("/profiles/{profileId}/unlock", {
      params: { path: { profileId: p.id } },
      body: pin ? { pin } : {},
    }),
  );
  profileTabs.postMessage("switched");
  // Rebooting re-reads /me, so every page answers as the chosen profile.
  location.hash = to;
  ctx.reload();
}

/**
 * Locks the device until a profile is picked, so every page and the admin UI
 * answer device_locked until then, and stops this tab's music; a video stops
 * as its player is left.
 */
export async function who(ctx: Ctx, query: URLSearchParams): Promise<HTMLElement[]> {
  resetMusic(ctx);
  // Unsent, the picker still shows: the device stays as it was, unlocked.
  await ok(ctx.api.POST("/me/lock")).then(
    // A tab booted onto a device already locked tells no one, or tabs would wake one another forever.
    () => ctx.me.locked || profileTabs.postMessage("locked"),
    (e) => {
      if (e instanceof ApiError && e.status === 401) throw e;
    },
  );
  const then = query.get("then")?.startsWith("#/") ? query.get("then")! : "#/";
  const pick = (p: Schemas["Account"], to: string) =>
    p.hasPin ? pinDialog(ctx, p, to) : void unlock(ctx, p, undefined, to).catch(() => {});
  const tile = (p: Schemas["Account"]) =>
    h(
      "a.who",
      { href: "#", onclick: (e: Event) => (e.preventDefault(), pick(p, then)) },
      h(
        "span.big",
        avatar(p, 120),
        p.hasPin
          ? h("span.pin", { role: "img", "aria-label": "PIN required" }, icon("lock", 16))
          : null,
      ),
      p.name,
    );
  const ownerId = ctx.me.household?.adultId;
  const owner = ctx.me.profiles.find((p) => p.id === ownerId) ?? ctx.me.account;
  // Invited adults appear once they have joined.
  const ready = ctx.me.profiles.filter((p) => !p.pending && !p.revokedAt);
  const remembered = ready.find((p) => p.id === query.get("pick"));
  if (remembered) pick(remembered, then);
  return [
    h("header.top", brand(false, false)),
    h(
      "div.whopage",
      h("h1.big", "Who's watching?"),
      h("div.profiles", ...ready.map(tile)),
      h(
        "p.sub",
        `This browser is in ${owner.name}'s household. `,
        h(
          "a",
          { href: "#", onclick: (e: Event) => (e.preventDefault(), ctx.signOut()) },
          "Not you? Sign in as someone else",
        ),
      ),
    ),
  ];
}

function pinDialog(ctx: Ctx, p: Schemas["Account"], to?: string) {
  const field = input("password", {
    inputMode: "numeric",
    maxLength: 4,
    autocomplete: "off",
    "aria-label": `${p.name}'s PIN`,
    className: "hidden",
  });
  const dots = h(
    "div.dots",
    { role: "img", "aria-label": "PIN, 0 of 4 digits entered" },
    ...[0, 1, 2, 3].map(() => h("i")),
  );
  const err = errorLine();
  const scrim = h("div.scrim");
  const render = () => {
    const n = field.value.length;
    dots.setAttribute("aria-label", `PIN, ${n} of 4 digits entered`);
    [...dots.children].forEach((d, i) => d.classList.toggle("on", i < n));
  };
  field.addEventListener("input", async () => {
    field.value = field.value.replace(/\D/g, "");
    render();
    if (field.value.length < 4) return;
    try {
      await unlock(ctx, p, field.value, to);
    } catch (e) {
      field.value = "";
      render();
      dots.classList.add("shake");
      showError(
        err,
        e instanceof ApiError && e.code === "pin_locked"
          ? "Too many tries. Try again in 15 minutes."
          : e instanceof ApiError && e.status === 403
            ? "Wrong PIN."
            : (e as Error).message,
      );
    }
  });
  const box = h(
    "div.dialog.pinbox",
    { role: "dialog", "aria-modal": "true", "aria-labelledby": "pt" },
    avatar(p, 72),
    h("h2", { id: "pt" }, `Enter ${p.name}'s PIN`),
    dots,
    field,
    err,
    h("p.sub", "Forgot PIN? An adult in your household can reset it"),
    h("button.btn.s.wide", { type: "button", onclick: () => scrim.remove() }, "Cancel"),
  );
  scrim.append(box);
  scrim.addEventListener("keydown", (e) => e.key === "Escape" && scrim.remove());
  scrim.addEventListener("click", () => field.focus());
  document.body.append(scrim);
  field.focus();
}

export const addKid = (ctx: Ctx) => kidForm(ctx);

export async function editKid(ctx: Ctx, profileId: string): Promise<HTMLElement[]> {
  return kidForm(
    ctx,
    await ok(ctx.api.GET("/profiles/{profileId}", { params: { path: { profileId } } })),
  );
}

/** Add a kid, or edit one when given; either returns to the household page. */
function kidForm(ctx: Ctx, kid?: Schemas["Account"]): HTMLElement[] {
  const back = "#/household";
  const name = input("text", { required: true, maxLength: 40, id: "kn", value: kid?.name ?? "" });
  const birthday = input("date", {
    required: true,
    max: new Date().toISOString().slice(0, 10),
    id: "kb",
    value: kid?.birthday ?? "",
  });
  const help = h("span.sub", "");
  let color = kid?.color;
  let mode: Schemas["FilterMode"] = kid?.filterMode ?? "ratings";
  const movie = h(
    "select.in",
    { id: "mc" },
    ...MOVIE_RATINGS.map((r) => h("option", r)),
  ) as HTMLSelectElement;
  const tv = h(
    "select.in",
    { id: "tc" },
    ...TV_RATINGS.map((r) => h("option", r)),
  ) as HTMLSelectElement;
  if (kid?.movieCeiling) movie.value = kid.movieCeiling;
  if (kid?.tvCeiling) tv.value = kid.tvCeiling;
  const ratings = h("div.row", h("label", "Movies up to", movie), h("label", "TV up to", tv));
  ratings.hidden = mode !== "ratings";
  // Allow-list changes apply at once; a new kid gets one after it is saved.
  const allowed = kid ? allowList(ctx, kid.id) : h("div");
  allowed.hidden = mode !== "allow_list";
  const needPin = h("input", {
    type: "checkbox",
    role: "switch",
    "aria-label": "Require a PIN",
    checked: !!kid?.hasPin,
  }) as HTMLInputElement;
  const pin = input("password", {
    inputMode: "numeric",
    maxLength: 4,
    pattern: "[0-9]{4}",
    "aria-label": "PIN",
    hidden: !kid?.hasPin,
    autocomplete: "off",
    placeholder: kid?.hasPin ? "Leave blank to keep the PIN" : "",
  });
  needPin.onchange = () => {
    pin.hidden = !needPin.checked;
    // A hidden field cannot show why it blocks the submit.
    if (!needPin.checked) pin.value = "";
    pin.required = needPin.checked && !kid?.hasPin;
  };
  // A kid added before a later step failed; the next try saves that kid again.
  let saved = kid;
  const err = errorLine();
  const submit = h("button.btn.p", { type: "submit" }, kid ? "Save" : "Add kid");
  const radio = (v: Schemas["FilterMode"], title: string, text: string, extra?: HTMLElement) => {
    const r = h("input", {
      type: "radio",
      name: "filter",
      checked: v === mode,
      onchange: () => (
        (mode = v),
        (ratings.hidden = v !== "ratings"),
        (allowed.hidden = v !== "allow_list")
      ),
    }) as HTMLInputElement;
    return h("label.opt", r, h("span", h("b", title), h("span.sub", text), extra));
  };
  if (!kid)
    name.addEventListener(
      "input",
      () => (submit.textContent = name.value ? `Add ${name.value}` : "Add kid"),
    );
  birthday.addEventListener("change", async () => {
    if (!birthday.value) return;
    try {
      const s = await ok(
        ctx.api.GET("/ratings/suggestion", { params: { query: { birthday: birthday.value } } }),
      );
      movie.value = s.movieCeiling;
      tv.value = s.tvCeiling;
      help.textContent = `${name.value || "They"} ${name.value ? "is" : "are"} ${s.age}. Suggested: movies up to ${s.movieCeiling}, TV up to ${s.tvCeiling}.`;
    } catch (e) {
      help.textContent = (e as Error).message;
    }
  });
  const form = h(
    "form.card.kid",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        try {
          const body = {
            name: name.value,
            birthday: birthday.value,
            filterMode: mode,
            ...(color ? { color } : {}),
            ...(mode === "ratings"
              ? {
                  movieCeiling: movie.value as Schemas["MovieRating"],
                  tvCeiling: tv.value as Schemas["TvRating"],
                }
              : {}),
          };
          saved = await ok(
            saved
              ? ctx.api.PUT("/profiles/{profileId}", {
                  params: { path: { profileId: saved.id } },
                  body,
                })
              : ctx.api.POST("/profiles", { body }),
          );
          const path = { profileId: saved.id };
          if (needPin.checked && (pin.value || !kid?.hasPin))
            await ok(
              ctx.api.PUT("/profiles/{profileId}/pin", {
                params: { path },
                body: { pin: pin.value },
              }),
            );
          else if (!needPin.checked && kid?.hasPin)
            await ok(ctx.api.DELETE("/profiles/{profileId}/pin", { params: { path } }));
          // Who's watching and the household page read the household from /me.
          location.hash = back;
          ctx.reload();
        } catch (x) {
          showError(err, (x as Error).message);
        }
      },
    },
    h("h1", kid ? `Edit ${kid.name}` : "Add a kid"),
    kid ? null : h("p.sub", "They will appear next to you on every device in your household."),
    h("div.field", h("label", { htmlFor: "kn" }, "Name"), name),
    h("div.field", h("label", { htmlFor: "kb" }, "Birthday"), birthday, help),
    h(
      "div.field",
      h("span.lbl", "Color"),
      swatches(color, (hex) => (color = hex)),
    ),
    h(
      "fieldset",
      h("legend", "Filter"),
      radio(
        "ratings",
        "Ratings",
        "Pick the highest rating for movies and TV. Suggested from the birthday.",
        ratings,
      ),
      radio("allow_list", "Only titles I choose", "Nothing shows until you add it."),
    ),
    allowed,
    h(
      "div.row",
      h(
        "span",
        h("b", "Require a PIN"),
        h("br"),
        h("span.sub", "Only you can set or change it. Leave off for a young kid."),
      ),
      needPin,
    ),
    pin,
    h(
      "p.sub",
      "Explicit songs are hidden from kids. Watch history is kept separately for each kid.",
    ),
    err,
    h("div.end", h("a.btn.s", { href: back }, "Cancel"), submit),
  );
  return [h("header.top", brand(false)), h("div.whopage", form)];
}

/** The titles a kid on an allow list may see, with a title search to add more. */
function allowList(ctx: Ctx, profileId: string): HTMLElement {
  const list = h("ul.members");
  const found = h("ul.members");
  const err = errorLine();
  const row = (label: string, action: string, on: () => Promise<unknown>) => {
    const b = h("button.btn.s", { type: "button" }, action);
    b.onclick = () => on().catch((e: Error) => showError(err, e.message));
    return h("li", h("b", label), b);
  };
  const path = (itemId: string) => ({ params: { path: { profileId, itemId } } });
  const none = h("li.sub", "No titles yet.");
  const shown = new Map<string, HTMLElement>();
  // A change edits the rows in place; asking for every title again would be a request each.
  const put = (itemId: string, label: string) => {
    const li: HTMLElement = row(label, "Remove", async () => {
      await ok(ctx.api.DELETE("/profiles/{profileId}/allowed-items/{itemId}", path(itemId)));
      li.remove();
      if (!list.children.length) list.append(none);
    });
    shown.get(itemId)?.remove();
    shown.set(itemId, li);
    none.remove();
    list.append(li);
  };
  const load = async () => {
    const { itemIds } = await ok(
      ctx.api.GET("/profiles/{profileId}/allowed-items", { params: { path: { profileId } } }),
    );
    // A YouTube channel is allowed too, and /items answers only movies and shows.
    const titles = await Promise.all(
      itemIds.map((itemId) =>
        ok(ctx.api.GET("/items/{itemId}", { params: { path: { itemId } } })).then(title, () =>
          ok(
            ctx.api.GET("/youtube/channels/{channelId}", {
              params: { path: { channelId: itemId } },
            }),
          ).then(
            (c) => c.title,
            () => "Untitled",
          ),
        ),
      ),
    );
    list.replaceChildren(none);
    itemIds.forEach((id, n) => put(id, titles[n]));
  };
  const find = input("search", {
    "aria-label": "Find a title to add",
    placeholder: "Find a title",
  });
  let gen = 0;
  find.addEventListener("input", async () => {
    const mine = ++gen;
    const q = find.value.trim();
    // A kind each: episode titles are all "SxxExx" and would crowd the page.
    const hits = q
      ? (
          await Promise.all(
            (["movie", "show"] as const).map((kind) =>
              ok(ctx.api.GET("/items", { params: { query: { q, kind, limit: 20 } } })),
            ),
          )
        ).flatMap((p) => p.items)
      : [];
    if (mine !== gen) return;
    found.replaceChildren(
      ...hits.map((i) =>
        row(title(i), "Add", async () => {
          await ok(ctx.api.PUT("/profiles/{profileId}/allowed-items/{itemId}", path(i.id)));
          found.replaceChildren();
          find.value = "";
          put(i.id, title(i));
        }),
      ),
    );
  });
  void load().catch((e: Error) => showError(err, e.message));
  return h("div.allow", h("b", "Allowed titles"), list, find, found, err);
}

export async function password(ctx: Ctx): Promise<HTMLElement[]> {
  const current = input("password", { required: true, autocomplete: "current-password", id: "pc" });
  const next = input("password", { required: true, autocomplete: "new-password", id: "pn" });
  const again = input("password", { required: true, autocomplete: "new-password", id: "pa" });
  const err = errorLine();
  const done = h("p.sub", { role: "status" });
  const form = h(
    "form.card.kid",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        done.textContent = "";
        // Same rules as choosing one on a join link.
        if (next.value.length < MIN_PASSWORD)
          return showError(err, `Use at least ${MIN_PASSWORD} characters.`);
        if (next.value !== again.value) return showError(err, "The new passwords do not match.");
        try {
          await ok(
            ctx.api.PUT("/me/password", {
              body: { currentPassword: current.value, newPassword: next.value },
            }),
          );
          err.hidden = true;
          (form as HTMLFormElement).reset();
          done.textContent = "Password changed.";
        } catch (x) {
          showError(
            err,
            x instanceof ApiError && x.status === 403
              ? "The current password is wrong."
              : (x as Error).message,
          );
        }
      },
    },
    h("h1", "Change password"),
    h("p.sub", `For ${ctx.profile.name}. Your other devices are signed out.`),
    h("div.field", h("label", { htmlFor: "pc" }, "Current password"), current),
    h("div.field", h("label", { htmlFor: "pn" }, "New password"), next),
    h("div.field", h("label", { htmlFor: "pa" }, "Confirm new password"), again),
    err,
    done,
    h(
      "div.end",
      h("a.btn.s", { href: "#/" }, "Home"),
      h("button.btn.p", { type: "submit" }, "Save"),
    ),
  );
  return [h("header.top", brand(false)), h("div.whopage", form)];
}

/** The active adult's color, Hide kids' content and Autoplay; each change saves at once. */
export async function preferences(ctx: Ctx): Promise<HTMLElement[]> {
  const err = errorLine();
  const save = async (body: Schemas["ProfilePreferences"]) => {
    try {
      await ok(
        ctx.api.PUT("/profiles/{profileId}/preferences", {
          params: { path: { profileId: ctx.profile.id } },
          body,
        }),
      );
      // The header, and every listing that hides kids' content, answer from /me again.
      ctx.reload();
    } catch (x) {
      showError(err, (x as Error).message);
    }
  };
  const hide = h("input", {
    type: "checkbox",
    role: "switch",
    "aria-label": "Hide kids' content",
    checked: !!ctx.profile.hideKidsContent,
    onchange: () => void save({ hideKidsContent: hide.checked }),
  }) as HTMLInputElement;
  const auto = h("input", {
    type: "checkbox",
    role: "switch",
    "aria-label": "Autoplay",
    checked: ctx.profile.autoplay !== false,
    onchange: () => void save({ autoplay: auto.checked }),
  }) as HTMLInputElement;
  const account = ctx.me.account.id;
  const r = remembered(account);
  const toggle = (label: string, on: boolean) =>
    h("input", {
      type: "checkbox",
      role: "switch",
      "aria-label": label,
      checked: on,
      onchange: () => {
        remember(account, { profile: keep.checked, skipPin: skip.checked });
        skipRow.hidden = !keep.checked;
      },
    }) as HTMLInputElement;
  const keep = toggle("Remember profile", r.profile);
  const skip = toggle("Skip PIN", r.skipPin);
  const row = (title: string, text: string, el: HTMLElement) =>
    h("div.row", h("span", h("b", title), h("br"), h("span.sub", text)), el);
  const skipRow = row("Skip PIN", "Opens without asking for that profile's PIN.", skip);
  skipRow.hidden = !r.profile;
  return [
    h(
      "div.card.kid",
      h("h1", "Preferences"),
      h("p.sub", `For ${ctx.profile.name}, on every device.`),
      h(
        "div.field",
        h("span.lbl", "Color"),
        swatches(ctx.profile.color, (color) => void save({ color })),
      ),
      h(
        "div.row",
        h(
          "span",
          h("b", "Hide kids' content"),
          h("br"),
          h("span.sub", "Leaves movies, shows and channels for kids out of what you browse."),
        ),
        hide,
      ),
      row("Autoplay", "Plays what's up next 10 seconds after a title ends.", auto),
      h("p.sub", `On this browser, for ${ctx.me.account.name}'s sign-in.`),
      row(
        "Remember profile",
        "Opens as the last profile used here instead of at Who's watching.",
        keep,
      ),
      skipRow,
      row(
        "Delete account",
        "Removes your account and what is yours from this server.",
        deleteAccountButton(ctx),
      ),
      err,
    ),
  ];
}

export async function addAdult(ctx: Ctx): Promise<HTMLElement[]> {
  const name = input("text", { required: true, id: "an", maxLength: 100 });
  const email = input("email", { required: true, id: "ae", autocomplete: "off" });
  const err = errorLine();
  const page = h("div.whopage");
  const form = h(
    "form.card.kid",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        try {
          const { joinCode } = await ok(
            ctx.api.POST("/household/adults", {
              body: { name: name.value, email: email.value, expiresInDays: 7 },
            }),
          );
          page.replaceChildren(invite(name.value, joinCode));
        } catch (x) {
          showError(
            err,
            x instanceof ApiError && x.status === 409
              ? "That email already has an account."
              : (x as Error).message,
          );
        }
      },
    },
    h("h1", "Add an adult"),
    h(
      "p.sub",
      "They get their own sign-in to this household and can look after the kids. Only you can add or remove adults.",
    ),
    h("div.field", h("label", { htmlFor: "an" }, "Name"), name),
    h("div.field", h("label", { htmlFor: "ae" }, "Email"), email),
    err,
    h(
      "div.end",
      h("a.btn.s", { href: "#/household" }, "Cancel"),
      h("button.btn.p", { type: "submit" }, "Add"),
    ),
  );
  page.append(form);
  return [h("header.top", brand(false)), page];
}

export function invite(
  name: string,
  jc: Schemas["JoinCode"],
  done: HTMLElement = h("a.btn.p", { href: "#/household" }, "Done"),
) {
  const url = new URL(jc.link, location.origin).href;
  return h(
    "div.card.kid",
    h("h1", `Invite ${name}`),
    h("p.sub", "Give them this link or code. They pick a password when they open it."),
    input("text", { readOnly: true, value: url, "aria-label": "Join link" }),
    h("p", h("b.code", jc.code)),
    h("p.sub", `Expires ${new Date(jc.expiresAt).toLocaleDateString()}.`),
    h("div.end", done),
  );
}
