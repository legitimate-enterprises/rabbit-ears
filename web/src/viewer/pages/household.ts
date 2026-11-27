import { ok, paged } from "../../admin/api";
import { dialog, errorLine, field, h, input, showError } from "../../admin/dom";
import type { Schemas } from "../../api/client";
import type { Ctx } from "../data";
import { avatar, brand, formEnd, icon } from "../ui";
import { invite } from "./who";

type Account = Schemas["Account"];
type Offer = Schemas["ProfileOffer"];

const KIND = { kid: "Kid", adult: "Adult" } as const;
const shared = (p: Account) => (p.households ?? 1) > 1;

/**
 * Members of the device household with what the active adult may do to each:
 * any adult edits and removes kids; each adult sets their own PIN; only the
 * owner resends codes and removes anyone else. An adult only visiting manages
 * nothing and may only leave. The server enforces the same rule.
 */
export async function household(ctx: Ctx): Promise<HTMLElement[]> {
  const top = h("header.top", brand(false));
  if (ctx.profile.kind !== "adult")
    return [top, h("div.whopage", h("p.sub", "Only adults manage the household."))];
  const ownerId = ctx.me.household?.adultId;
  const isOwner = ctx.profile.id === ownerId;
  const visiting = !!ctx.profile.present;
  const [{ items: members }, offers] = await Promise.all([
    ok(ctx.api.GET("/profiles")),
    visiting ? undefined : ok(ctx.api.GET("/household/offers")),
  ]);
  const page = h("div.whopage");
  const err = errorLine();
  // Membership changed, so Who's watching needs /me again.
  const doneBtn = () => h("button.btn.p", { type: "button", onclick: ctx.reload }, "Done");
  const showCode = (name: string, jc: Schemas["JoinCode"]) =>
    page.replaceChildren(invite(name, jc, doneBtn()));
  const run = (p: Promise<unknown>) => p.catch((e: Error) => showError(err, e.message));
  const path = (p: Account) => ({ params: { path: { profileId: p.id } } });

  // A confirm-style dialog around a form; submit runs act, errors stay in the dialog.
  const ask = (
    title: string,
    text: string,
    go: string,
    extra: Node[],
    act: () => Promise<unknown>,
  ) => {
    const e = errorLine();
    const d = dialog(
      title,
      h(
        "form",
        {
          onsubmit: (ev: Event) => {
            ev.preventDefault();
            act().then(d.close, (x: Error) => showError(e, x.message));
          },
        },
        h("p.sub", text),
        ...extra,
        e,
        formEnd(() => d.close(), go),
      ),
    );
    return d;
  };
  // A profile another household holds too only leaves this one.
  const remove = (p: Account) =>
    ask(
      shared(p) ? `Remove ${p.name} from this household?` : `Remove ${p.name}?`,
      shared(p)
        ? `${p.name} stays in their other household${p.households! > 2 ? "s" : ""}. Nothing of theirs is deleted.`
        : p.kind === "kid"
          ? "Their account and watch history are deleted."
          : "Their account and watch history are deleted, and their devices are signed out.",
      "Remove",
      [],
      async () => (await ok(ctx.api.DELETE("/profiles/{profileId}", path(p))), ctx.reload()),
    );
  // The server signs out a device of the leaving adult's own; any other goes back to Who's watching.
  const leave = () =>
    ask(
      "Leave this household?",
      "You stay in your own household. Nothing of yours is deleted.",
      "Leave",
      [],
      async () => {
        await ok(ctx.api.DELETE("/profiles/{profileId}", path(ctx.profile)));
        location.hash = "#/who";
        ctx.reload();
      },
    );
  const toAdult = (p: Account) => {
    const email = input("email", { required: true, autocomplete: "off" });
    ask(
      `Change ${p.name} to an adult?`,
      "They join with a code like any adult you add, and keep their watch history.",
      "Change to adult",
      [field("Email", email)],
      async () => {
        const r = await ok(
          ctx.api.POST("/profiles/{profileId}/make-adult", {
            ...path(p),
            body: { email: email.value, expiresInDays: 7 },
          }),
        );
        showCode(p.name, r.joinCode);
      },
    );
  };
  const setPin = (p: Account) => {
    const pin = input("password", {
      required: true,
      inputMode: "numeric",
      maxLength: 4,
      pattern: "[0-9]{4}",
      autocomplete: "off",
    });
    ask(
      p.hasPin ? "Change your PIN" : "Set a PIN",
      "Four digits, asked for when anyone switches to you.",
      "Save",
      [field("PIN", pin)],
      async () => (
        await ok(
          ctx.api.PUT("/profiles/{profileId}/pin", { ...path(p), body: { pin: pin.value } }),
        ),
        ctx.reload()
      ),
    );
  };
  const clearPin = (p: Account) =>
    run(ok(ctx.api.DELETE("/profiles/{profileId}/pin", path(p))).then(ctx.reload));
  const resend = (p: Account) =>
    run(
      ok(ctx.api.POST("/profiles/{profileId}/join-code", path(p))).then((r) =>
        showCode(p.name, r.joinCode),
      ),
    );
  // The offer sheet: p goes to one of the adults an admin connected the active adult with.
  const offer = async (p: Account) => {
    const adults = await paged((offset) =>
      ok(ctx.api.GET("/household/linked-adults", { params: { query: { limit: 200, offset } } })),
    );
    let to: string | undefined;
    const d = ask(
      `Share ${p.name}`,
      `Offer ${p.name} to an adult in another household. Once they accept, ${p.name} appears in Who's watching there with the same watch history, playlists and limits, and the adults there can change ${p.name}'s settings as you can.`,
      "Send offer",
      [
        h("h2", "Offer to"),
        h(
          "div.shares",
          ...adults.map((a) =>
            h(
              "label.share",
              avatar(a, 36),
              h("b", a.name),
              h("input", {
                type: "radio",
                name: "to",
                onchange: () => ((to = a.id), (send.disabled = false)),
              }),
            ),
          ),
        ),
        h("p.sub", "Only adults the server's admin has connected you with appear here."),
      ],
      async () => {
        await ok(
          ctx.api.POST("/household/offers", { body: { profileId: p.id, toProfileId: to! } }),
        );
        ctx.reload();
      },
    );
    const send = d.box.querySelector<HTMLButtonElement>("button[type=submit]")!;
    send.disabled = true;
  };

  const act = (label: string, on: () => unknown) =>
    h("button.btn.s", { type: "button", onclick: on }, label);
  const share = (p: Account) =>
    !p.pending && !p.revokedAt && act("Share with another household…", () => run(offer(p)));
  const removeAct = (p: Account) =>
    act(shared(p) ? "Remove from this household…" : "Remove", () => remove(p));
  const actions = (p: Account) => {
    if (visiting) return [];
    if (p.present) return [act("Remove from this household…", () => remove(p))];
    const owned = isOwner && p.id !== ownerId;
    if (p.kind === "kid")
      return [
        h("a.btn.s", { href: `#/kid/${p.id}` }, "Edit"),
        share(p),
        owned && act("Change to adult", () => toAdult(p)),
        removeAct(p),
      ];
    if (p.id === ctx.profile.id)
      return [
        act(p.hasPin ? "Change PIN" : "Set PIN", () => setPin(p)),
        p.hasPin && act("Remove PIN", () => clearPin(p)),
        share(p),
      ];
    if (!owned) return [share(p)];
    return [p.pending && act("Resend code", () => resend(p)), share(p), removeAct(p)];
  };
  const label = (p: Account) =>
    [
      p.id === ownerId
        ? "Owner"
        : p.pending
          ? "Awaiting sign-in"
          : p.kind === "kid"
            ? "Kid"
            : "Adult",
      p.pending &&
        (p.inviteExpiresAt
          ? `Code expires ${new Date(p.inviteExpiresAt).toLocaleDateString()}`
          : "Code expired"),
      p.kind === "kid" &&
        (p.filterMode === "allow_list"
          ? "Only titles you choose"
          : `Movies up to ${p.movieCeiling}, TV up to ${p.tvCeiling}`),
      p.hasPin && "PIN",
      p.revokedAt && "Revoked",
    ]
      .filter(Boolean)
      .join(" · ");
  const sharing = (p: Account) =>
    p.present
      ? "Visiting from another household"
      : shared(p)
        ? `In ${p.households} households`
        : "";
  const row = (
    p: Pick<Account, "id" | "name" | "color" | "avatarUrl">,
    sub: (Node | string)[],
    buttons: (HTMLElement | false | undefined)[],
  ) =>
    h(
      "li",
      avatar(p, 40),
      h("span", h("b", p.name), h("span.sub", ...sub)),
      h("span.grow"),
      ...buttons,
    );
  const offerPath = (o: Offer) => ({ params: { path: { offerId: o.id } } });
  const answer = (p: Promise<{ response: Response }>) => run(ok(p).then(ctx.reload));
  const offerRow = (o: Offer, text: string, buttons: HTMLElement[]) =>
    row({ id: o.profileId, name: o.profileName }, [`${KIND[o.profileKind]} · ${text}`], buttons);
  const offerRows = [
    ...(offers?.incoming ?? []).map((o) =>
      offerRow(o, `${o.fromName} offers to share ${o.profileName} with your household`, [
        act("Decline", () => answer(ctx.api.DELETE("/household/offers/{offerId}", offerPath(o)))),
        h(
          "button.btn.p",
          {
            type: "button",
            onclick: () => answer(ctx.api.POST("/household/offers/{offerId}/accept", offerPath(o))),
          },
          "Accept",
        ),
      ]),
    ),
    ...(offers?.outgoing ?? []).map((o) =>
      offerRow(o, `Offered to ${o.toName}, waiting`, [
        act("Withdraw", () => answer(ctx.api.DELETE("/household/offers/{offerId}", offerPath(o)))),
      ]),
    ),
  ];

  page.append(
    h(
      "div.card.kid.household",
      h("h1", "Household"),
      err,
      ...(offerRows.length
        ? [h("h2", "Offers"), h("ul.members", ...offerRows), h("h2", "Profiles")]
        : []),
      h(
        "ul.members",
        ...members.map((p) => {
          const s = sharing(p);
          return row(p, [label(p), ...(s ? [" · ", icon("users", 13), s] : [])], actions(p));
        }),
      ),
      members.some(shared)
        ? h(
            "p.sub",
            "A shared profile is the same in every household it is in: one watch history, one set of playlists and limits. Adults in each of its households can change a kid's settings.",
          )
        : null,
      h(
        "div.end",
        h("a.btn.s", { href: "#/" }, "Done"),
        visiting
          ? h("button.btn.s", { type: "button", onclick: leave }, "Leave this household…")
          : null,
        !visiting && isOwner ? h("a.btn.s", { href: "#/add-adult" }, "Add adult") : null,
        visiting ? null : h("a.btn.p", { href: "#/add-kid" }, "Add kid"),
      ),
    ),
  );
  return [top, page];
}
