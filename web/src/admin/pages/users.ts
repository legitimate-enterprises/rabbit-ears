import type { Ctx } from "../api";
import { ok, paged } from "../api";
import { card, confirming, copy, formEnd, header, saveSwitch, table } from "../common";
import { dialog, errorLine, field, h, input, showError, sub, tag } from "../dom";
import { age, local } from "../format";
import { MOVIE_RATINGS, TV_RATINGS, type Schemas } from "../../api/client";

type Account = Schemas["Account"];
const AVATARS = ["#3c4170", "#2e6b8a", "#5a3a5a", "#5b6b3f", "#6b3a2e", "#2b5a4a"];
const avatar = (a: Account) =>
  h(
    "span.avatar",
    { style: `background:${AVATARS[a.id % AVATARS.length]}`, "aria-hidden": "true" },
    a.name.slice(0, 1).toUpperCase(),
  );

function joinDialog(name: string, jc: Schemas["JoinCode"]) {
  const url = new URL(jc.link, location.origin).href;
  const link = input("text", { readOnly: true, value: url, "aria-label": "Join link" });
  const btn = h("button.ib.p", { type: "button" }, "Copy link");
  btn.onclick = () => {
    void copy(url);
    btn.textContent = "Copied";
    setTimeout(() => (btn.textContent = "Copy link"), 2000);
  };
  const { box, close } = dialog(`${name}'s account is ready`);
  box.append(
    h("p.sub", "Send them this link any way you like. They pick a password when they open it."),
    h("div.row", link, btn),
    h("div.row", sub("Or read out the code"), h("b.code", jc.code)),
    h(
      "p.sub",
      `Expires ${local(jc.expiresAt)}. Until they sign in, the account appears under Awaiting sign-in, where you can copy the link again or revoke it.`,
    ),
    h("div.end", h("button.ib.p", { type: "button", onclick: close }, "Done")),
  );
}

const ALPHABET = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ";
/** Three groups of four, easy to read out: 60 bits. */
function temporaryPassword() {
  const b = crypto.getRandomValues(new Uint8Array(12));
  return [...b]
    .map((x, i) => (i && i % 4 === 0 ? "-" : "") + ALPHABET[x % ALPHABET.length])
    .join("");
}

function tempDialog(name: string, password: string) {
  const { box, close } = dialog(`${name}'s temporary password`);
  box.append(
    h("p.sub", "Give it to them. They sign in with it, then choose their own under Password."),
    h("div.row", h("b.code", password)),
    h("div.end", h("button.ib.p", { type: "button", onclick: close }, "Done")),
  );
}

async function resetPassword(ctx: Ctx, a: Account) {
  const password = temporaryPassword();
  await ok(
    ctx.api.PUT("/accounts/{accountId}/password", {
      params: { path: { accountId: a.id } },
      body: { password },
    }),
  );
  tempDialog(a.name, password);
}

const select = (current: string | undefined, ...opts: [string, string][]) =>
  h(
    "select.in",
    ...opts.map(([v, t]) => h("option", { value: v, selected: v === (current ?? "") }, t)),
  ) as HTMLSelectElement;
const ratings = (current: string | undefined, all: string[]) =>
  select(current, ["", "Not set"], ...all.map((r): [string, string] => [r, r]));

async function identities(ctx: Ctx, a: Account, slot: HTMLElement) {
  const accountId = a.id;
  const [ids, providers] = await Promise.all([
    ok(ctx.api.GET("/accounts/{accountId}/identities", { params: { path: { accountId } } })),
    ok(ctx.api.GET("/admin/sso-providers", { params: { query: { limit: 200 } } })),
  ]).catch(() => [undefined, undefined]);
  if (!ids?.items.length) return void slot.replaceChildren();
  const name = new Map(providers!.items.map((p) => [p.id, p.name]));
  slot.replaceChildren(
    h("h3.sub", "Single sign-on"),
    ...ids.items.map((i) => {
      const via = name.get(i.providerId) ?? "a provider";
      return h(
        "div.row",
        h("b", via),
        sub(i.email ?? i.subject),
        confirming("Unlink", `Unlink ${a.name} from ${via}?`, async () => {
          await ok(
            ctx.api.DELETE("/accounts/{accountId}/identities/{identityId}", {
              params: { path: { accountId, identityId: i.id } },
            }),
          );
          await identities(ctx, a, slot);
        }),
      );
    }),
  );
}

/**
 * Side sheet to edit any account through the admin accounts API, which
 * reaches every household. Kids keep no email and are never admins.
 */
function editSheet(ctx: Ctx, a: Account, isOwner: boolean, reload: () => void) {
  const kid = a.kind === "kid";
  const name = input("text", { required: true, maxLength: 100, value: a.name });
  const email = input("email", { required: true, value: a.email ?? "" });
  const admin = input("checkbox", { checked: a.isAdmin });
  const birthday = input("date", { required: true, value: a.birthday ?? "" });
  const mode = select(a.filterMode, ["ratings", "Ratings"], ["allow_list", "Allow list"]);
  const movie = ratings(a.movieCeiling, MOVIE_RATINGS);
  const tv = ratings(a.tvCeiling, TV_RATINGS);
  // The clients suggest ceilings from a new birthday; so does this sheet.
  birthday.onchange = async () => {
    if (!birthday.value) return;
    const s = await ok(
      ctx.api.GET("/ratings/suggestion", { params: { query: { birthday: birthday.value } } }),
    ).catch(() => undefined);
    if (s) [movie.value, tv.value] = [s.movieCeiling, s.tvCeiling];
  };
  const err = errorLine();
  const ssoSlot = h("div");
  if (!kid) void identities(ctx, a, ssoSlot);
  const { box, close } = dialog(`Edit ${a.name}`);
  box.classList.add("sheet");
  const body = (): Schemas["AccountInput"] =>
    kid
      ? {
          kind: "kid",
          name: name.value.trim(),
          birthday: birthday.value || undefined,
          isAdmin: false,
          filterMode: mode.value as Schemas["FilterMode"],
          movieCeiling: (movie.value || undefined) as Schemas["MovieRating"] | undefined,
          tvCeiling: (tv.value || undefined) as Schemas["TvRating"] | undefined,
        }
      : {
          kind: "adult",
          name: name.value.trim(),
          email: email.value.trim(),
          isAdmin: admin.checked,
          birthday: a.birthday,
          filterMode: a.filterMode,
        };
  box.append(
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            await ok(
              ctx.api.PUT("/accounts/{accountId}", {
                params: { path: { accountId: a.id } },
                body: body(),
              }),
            );
            close();
            reload();
          } catch (x) {
            showError(err, (x as Error).message);
          }
        },
      },
      field("Name", name),
      ...(kid
        ? [
            field("Birthday", birthday),
            field("Filter", mode, "Ratings hide anything above the ceilings below."),
            field("Movies up to", movie),
            field("TV up to", tv),
          ]
        : [
            field("Email", email, "They sign in with it."),
            h("label.row", admin, "Admin: can use this admin UI"),
          ]),
      err,
      formEnd(close, "Save"),
    ),
    ...(kid ? [] : [ssoSlot]),
    ...(!kid && !a.pending
      ? [
          h(
            "div.row",
            sub("Forgot their password?"),
            confirming(
              "Reset password",
              `Give ${a.name} a temporary password? Their current one stops working and their devices are signed out.`,
              () => resetPassword(ctx, a),
            ),
          ),
        ]
      : []),
    ...(isOwner
      ? []
      : [
          h(
            "div.row",
            confirming(
              "Remove",
              kid
                ? `Remove ${a.name} and their watch history?`
                : `Remove ${a.name}? Their devices are signed out and their watch history is deleted.`,
              async () => {
                await ok(
                  ctx.api.DELETE("/accounts/{accountId}", {
                    params: { path: { accountId: a.id } },
                  }),
                );
                close();
                reload();
              },
            ),
          ),
        ]),
  );
}

function addAdult(ctx: Ctx, reload: () => void) {
  const name = input("text", { required: true });
  const email = input("email", { required: true });
  const err = errorLine();
  const { box, close } = dialog("Add adult");
  box.append(
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            const acct = await ok(
              ctx.api.POST("/accounts", {
                body: {
                  kind: "adult",
                  name: name.value.trim(),
                  email: email.value.trim(),
                  isAdmin: false,
                  revoked: false,
                },
              }),
            );
            const jc = await ok(
              ctx.api.POST("/join-codes", { body: { accountId: acct.id, expiresInDays: 7 } }),
            );
            close();
            joinDialog(acct.name, jc);
            reload();
          } catch (x) {
            showError(err, (x as Error).message);
          }
        },
      },
      field("Name", name),
      field("Email", email),
      err,
      formEnd(close, "Create account"),
    ),
  );
}

export async function users(ctx: Ctx): Promise<HTMLElement[]> {
  const root = h("div.stack");
  await fill(ctx, root);
  return [root];
}

async function fill(ctx: Ctx, root: HTMLElement) {
  const { api } = ctx;
  const reload = () => void fill(ctx, root);
  const q = (offset: number) => ({ params: { query: { limit: 200, offset } } });
  const [hhs, accts, devs, links] = await Promise.all([
    paged((o) => ok(api.GET("/households", q(o)))),
    paged((o) => ok(api.GET("/accounts", q(o)))),
    paged((o) => ok(api.GET("/devices", q(o)))),
    paged((o) => ok(api.GET("/adult-links", q(o)))),
  ]);
  const byId = new Map(accts.map((a) => [a.id, a]));
  const live = devs.filter((d) => !d.revokedAt);
  const revoke = async (d: Schemas["Device"]) =>
    ok(
      api.PUT("/devices/{deviceId}", {
        params: { path: { deviceId: d.id } },
        body: { name: d.name, revoked: true },
      }),
    );
  // This browser's own device goes last: once it is revoked the rest would answer 401.
  const signOut = async (ds: Schemas["Device"][]) => {
    await Promise.all(ds.filter((d) => d.id !== ctx.me.device.id).map(revoke));
    const own = ds.find((d) => d.id === ctx.me.device.id);
    if (!own) return reload();
    await revoke(own);
    ctx.signedOut();
  };
  const adultName = (hh: Schemas["Household"]) => byId.get(hh.adultId)?.name ?? hh.name;
  const households = [...hhs].sort(
    (a, b) =>
      Number(b.id === ctx.me.household?.id) - Number(a.id === ctx.me.household?.id) ||
      adultName(a).localeCompare(adultName(b)),
  );
  const named = (id?: number) => {
    const hh = hhs.find((x) => x.id === id);
    return hh ? `${adultName(hh)}'s household` : "";
  };
  const joined = accts.filter((a) => !a.pending && !a.revokedAt);

  const cards = households.map((hh) => {
    const members = accts.filter((a) => inHouseholds(a).includes(hh.id) && !a.pending);
    const hdevs = live.filter((d) => d.householdId === hh.id);
    const visiting = (a: Account) => a.kind === "adult" && a.householdId !== hh.id;
    return h(
      "section.card",
      { "aria-label": `Household of ${adultName(hh)}` },
      h(
        "h2",
        `${adultName(hh)}'s household`,
        tag(`${members.length} profiles - ${hdevs.length} devices`),
        h(
          "span",
          { style: "margin-left:auto" },
          h(
            "button.ib",
            {
              type: "button",
              onclick: () =>
                addExisting(
                  ctx,
                  hh,
                  named(hh.id),
                  joined.filter((a) => !members.includes(a)),
                  reload,
                ),
            },
            "Add existing profile…",
          ),
          " ",
          confirming(
            "Sign out everywhere",
            `Sign out every device in ${adultName(hh)}'s household?`,
            () => signOut(hdevs),
          ),
        ),
      ),
      table(
        ["Profile", "Role", "Also in", "Age", "Movies up to", "TV up to", "PIN", ""],
        members.map((a) => [
          h("span", avatar(a), h("b", a.name)),
          h(
            "span",
            tag(a.id === hh.adultId ? "Owner" : a.kind === "adult" ? "Adult" : "Kid"),
            visiting(a) ? tag("Visiting") : null,
            a.isAdmin ? tag("Admin", "warn") : null,
          ),
          inHouseholds(a)
            .filter((id) => id !== hh.id)
            .map(named)
            .join(", ") || sub("-"),
          a.birthday ? `${age(a.birthday)} - ${a.birthday}` : "-",
          a.kind === "adult"
            ? sub("All")
            : a.filterMode === "allow_list"
              ? "Allow list"
              : (a.movieCeiling ?? "-"),
          a.kind === "adult"
            ? sub("All")
            : a.filterMode === "allow_list"
              ? "Allow list"
              : (a.tvCeiling ?? "-"),
          a.hasPin ? "Set" : sub("None"),
          h(
            "span",
            // An adult visiting is edited in their own household, whose Remove deletes them.
            visiting(a)
              ? null
              : h(
                  "button.ib",
                  { onclick: () => editSheet(ctx, a, a.id === hh.adultId, reload) },
                  "Edit",
                ),
            inHouseholds(a).length > 1 && a.id !== hh.adultId
              ? confirming(
                  "Remove from household",
                  `Remove ${a.name} from ${named(hh.id)}?`,
                  async () => {
                    await ok(
                      api.DELETE("/households/{householdId}/members/{accountId}", {
                        params: { path: { householdId: hh.id, accountId: a.id } },
                      }),
                    );
                    reload();
                  },
                )
              : null,
          ),
        ]),
        "No profiles.",
      ),
      h("h3.sub", "Devices in this household"),
      table(
        ["Device", "Platform", "Last seen", ""],
        hdevs.map((d) => [
          h("span", h("b", d.name), d.id === ctx.me.device.id ? tag("This browser", "ok") : null),
          d.platform,
          sub(local(d.lastSeenAt)),
          confirming("Revoke", `Revoke ${d.name}? It must sign in again.`, () => signOut([d])),
        ]),
        "No devices.",
      ),
    );
  });

  const pending = accts.filter((a) => a.pending && !a.revokedAt);
  const pendingRows = await Promise.all(
    pending.map(async (a) => {
      const codes = await ok(api.GET("/join-codes", { params: { query: { accountId: a.id } } }));
      const active = codes.items.find(
        (c) => !c.usedAt && !c.revokedAt && Date.parse(c.expiresAt) > Date.now(),
      );
      return [
        a.name,
        sub(a.email ?? ""),
        tag("Adult"),
        sub(local(a.createdAt)),
        sub(active ? local(active.expiresAt) : "Expired"),
        h(
          "span",
          h(
            "button.ib",
            {
              onclick: async () => {
                const jc =
                  active ??
                  (await ok(
                    api.POST("/join-codes", { body: { accountId: a.id, expiresInDays: 7 } }),
                  ));
                joinDialog(a.name, jc);
              },
            },
            "Copy link",
          ),
          confirming("Revoke", `Revoke ${a.name}'s invitation?`, async () => {
            if (active)
              await ok(
                api.DELETE("/join-codes/{joinCodeId}", {
                  params: { path: { joinCodeId: active.id } },
                }),
              );
            await ok(
              api.DELETE("/accounts/{accountId}", { params: { path: { accountId: a.id } } }),
            );
            reload();
          }),
        ),
      ];
    }),
  );

  root.replaceChildren(
    header(
      "Users & profiles",
      `${households.length} households - ${members(accts)} profiles - ${live.length} devices`,
      h("button.ib.p", { onclick: () => addAdult(ctx, reload) }, "Add adult"),
    ),
    h(
      "p.sub",
      "Each household has an owner. The owner adds other adults from the client; any adult there adds kids and chooses each kid's filter. A device shows the household of the adult who signed in on it.",
    ),
    ...(cards.length ? cards : [h("p.sub", "No households yet. Add an adult to get started.")]),
    connected(ctx, links, byId, named, joined, reload),
    ...(pendingRows.length
      ? [
          card(
            "Awaiting sign-in",
            tag(String(pendingRows.length)),
            table(["Name", "Email", "Role", "Created", "Expires", ""], pendingRows, ""),
          ),
        ]
      : []),
  );
}

const members = (a: Account[]) => a.filter((x) => !x.pending).length;

/** Every household a profile is in, its own first. */
const inHouseholds = (a: Account) => a.householdIds ?? (a.householdId ? [a.householdId] : []);

/** Puts one of the joined profiles not yet in hh into it, as an accepted offer does. */
function addExisting(
  ctx: Ctx,
  hh: Schemas["Household"],
  name: string,
  candidates: Account[],
  reload: () => void,
) {
  const pick = select(
    undefined,
    ...candidates.map((a): [string, string] => [String(a.id), a.name]),
  );
  pick.required = true;
  const err = errorLine();
  const { box, close } = dialog(`Add an existing profile to ${name}`);
  box.append(
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            await ok(
              ctx.api.PUT("/households/{householdId}/members/{accountId}", {
                params: { path: { householdId: hh.id, accountId: Number(pick.value) } },
              }),
            );
            close();
            reload();
          } catch (x) {
            showError(err, (x as Error).message);
          }
        },
      },
      field("Profile", pick),
      err,
      formEnd(close, "Add"),
    ),
  );
}

/** The pairs of adults of different households who see each other, and Connect adults. */
function connected(
  ctx: Ctx,
  links: Schemas["AdultLink"][],
  byId: Map<number, Account>,
  named: (householdId?: number) => string,
  joined: Account[],
  reload: () => void,
) {
  const err = errorLine();
  const who = (id: number) => {
    const a = byId.get(id);
    return a ? h("span", avatar(a), h("b", a.name), sub(named(a.householdId))) : sub("-");
  };
  const path = (l: Schemas["AdultLink"]) => ({
    params: { path: { accountId: l.accountId, otherAccountId: l.otherAccountId } },
  });
  const rows = links.map((l) => {
    const pair = `${byId.get(l.accountId)?.name} and ${byId.get(l.otherAccountId)?.name}`;
    const sw = saveSwitch(`${pair} see each other on TVs`, l.tv, err, (tv) =>
      ok(ctx.api.PUT("/adult-links/{accountId}/{otherAccountId}", { ...path(l), body: { tv } })),
    );
    return [
      who(l.accountId),
      who(l.otherAccountId),
      sw,
      confirming("Disconnect", `Disconnect ${pair}?`, async () => {
        await ok(ctx.api.DELETE("/adult-links/{accountId}/{otherAccountId}", path(l)));
        reload();
      }),
    ];
  });
  return h(
    "section.card",
    { "aria-label": "Connected adults" },
    h(
      "h2",
      "Connected adults",
      h(
        "span",
        { style: "margin-left:auto" },
        h(
          "button.ib.p",
          {
            type: "button",
            onclick: () =>
              connect(
                ctx,
                joined.filter((a) => a.kind === "adult"),
                reload,
              ),
          },
          "Connect adults",
        ),
      ),
    ),
    sub(
      "Adults in different households never see each other unless you connect them here. Connected adults can share playlists with each other, watch together, and offer to share a profile into each other's household. They see each other on phones, tablets, computers and the web; turn on TVs to include TVs.",
    ),
    err,
    table(["Adult", "Adult", "On TVs", ""], rows, "No adults connected yet."),
  );
}

function connect(ctx: Ctx, adults: Account[], reload: () => void) {
  const opts = adults.map((a): [string, string] => [String(a.id), a.name]);
  const one = select(opts[0]?.[0], ...opts);
  const other = select(opts[1]?.[0], ...opts);
  const err = errorLine();
  const { box, close } = dialog("Connect adults");
  box.append(
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          const [a, b] = [one, other].map((s) => adults.find((x) => String(x.id) === s.value));
          if (!a || !b || a.householdId === b.householdId)
            return showError(err, "Pick two adults of different households.");
          try {
            await ok(
              ctx.api.POST("/adult-links", {
                body: { accountId: a.id, otherAccountId: b.id, tv: false },
              }),
            );
            close();
            reload();
          } catch (x) {
            showError(err, (x as Error).message);
          }
        },
      },
      field("Adult", one),
      field("Other adult", other),
      err,
      formEnd(close, "Connect"),
    ),
  );
}
