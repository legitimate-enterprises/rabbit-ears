import { ApiError, ok, paged } from "../../admin/api";
import { dialog, errorLine, field, h, input, showError } from "../../admin/dom";
import type { Schemas } from "../../api/client";
import type { Ctx } from "../data";
import { toast } from "../handoff";
import { avatar, formEnd, menu, opt } from "../ui";

type Account = Schemas["Account"];

const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: "medium" });
/** p, with a 404 taken as done: the request or friendship is already gone. */
const gone = (p: Promise<unknown>) =>
  p.catch((e) => {
    if (!(e instanceof ApiError && e.status === 404)) throw e;
  });

/**
 * The active adult's friends and friend requests, read again on opening, on a return to the
 * window and after every action. A sent request shows only what the sender wrote and when,
 * the same whoever holds the email.
 */
export async function friends(ctx: Ctx): Promise<HTMLElement[]> {
  const card = h("div.card.kid.household");
  let sentOpen = false;
  let reads = 0;
  const draw = async () => {
    const mine = ++reads;
    const [reqs, list] = await Promise.all([
      ok(ctx.api.GET("/friend-requests")),
      paged((offset) => ok(ctx.api.GET("/friends", { params: { query: { limit: 200, offset } } }))),
    ]);
    // A later read, as on focus during an action, has drawn or will draw newer lists.
    if (mine !== reads) return;
    const row = (
      p: Pick<Account, "id" | "name" | "color" | "avatarUrl">,
      sub: string,
      ...buttons: HTMLElement[]
    ) =>
      h(
        "li",
        avatar(p, 40),
        h("span", h("b", p.name), sub ? h("span.sub", sub) : null),
        h("span.grow"),
        ...buttons,
      );
    const act = (label: string, cls: string, on: () => Promise<unknown>) =>
      h(
        `button.btn.${cls}`,
        {
          type: "button",
          onclick: () => void on().catch((e: Error) => (toast(e.message), again())),
        },
        label,
      );
    const requests = reqs.incoming.map((q) => {
      const path = { params: { path: { requestId: q.id } } };
      const li: HTMLElement = row(
        { id: q.fromProfileId, name: q.fromName, color: q.fromColor },
        "Wants to be friends",
        act("Decline", "s", async () => {
          li.remove();
          await gone(ok(ctx.api.DELETE("/friend-requests/{requestId}", path)));
          again();
        }),
        act("Accept", "p", async () => {
          try {
            await ok(ctx.api.POST("/friend-requests/{requestId}/accept", path));
            again();
          } catch (e) {
            if (!(e instanceof ApiError) || ![404, 409].includes(e.status)) throw e;
            if (e.status === 409) toast("This person can no longer be added.");
            li.remove();
            if (!requestList.children.length) requestSection.remove();
          }
        }),
      );
      return li;
    });
    const requestList = h("ul.members", ...requests);
    const requestSection = h(
      "section",
      { "aria-label": "Requests" },
      h("h2", "Requests"),
      requestList,
    );
    const sent = h(
      "details",
      { open: sentOpen, ontoggle: () => (sentOpen = sent.open) },
      h("summary", `Sent (${reqs.outgoing.length})`),
      h(
        "ul.members",
        ...reqs.outgoing.map((s) =>
          h(
            "li",
            h(
              "span",
              h("b", s.email),
              h("span.sub", `Sent ${day(s.createdAt)} · Expires ${day(s.expiresAt)}`),
            ),
            h("span.grow"),
            act("Withdraw", "s", async () => {
              await gone(
                ok(
                  ctx.api.DELETE("/friend-requests/{requestId}", {
                    params: { path: { requestId: s.id } },
                  }),
                ),
              );
              again();
            }),
          ),
        ),
      ),
    ) as HTMLDetailsElement;
    card.replaceChildren(
      h("h1", "Friends"),
      ...(requests.length ? [requestSection] : []),
      h(
        "section",
        { "aria-label": "Friends" },
        h("h2", "Friends"),
        list.length
          ? h(
              "ul.members",
              ...list.map((f) =>
                row(
                  f,
                  "",
                  menu(
                    h("button.btn.s", { type: "button", "aria-label": `More for ${f.name}` }, "…"),
                    "More",
                    [opt("Remove Friend", () => remove(f))],
                  ),
                ),
              ),
            )
          : h(
              "p.sub",
              "No friends yet. Add someone by their email to share playlists and watch together.",
            ),
      ),
      ...(reqs.outgoing.length ? [sent] : []),
      h(
        "div.end",
        h("a.btn.s", { href: "#/" }, "Done"),
        h("button.btn.p", { type: "button", onclick: add }, "Add Friend"),
      ),
    );
  };
  const again = () => void draw().catch((e: Error) => toast(e.message));
  const remove = (f: Account) => {
    const e = errorLine();
    const d = dialog(
      `Remove ${f.name}?`,
      h(
        "form",
        {
          onsubmit: (ev: Event) => {
            ev.preventDefault();
            gone(
              ok(ctx.api.DELETE("/friends/{profileId}", { params: { path: { profileId: f.id } } })),
            )
              .then(() => (d.close(), again()))
              .catch((x: Error) => showError(e, x.message));
          },
        },
        h(
          "p.sub",
          "They won't be told. Playlists you've shared stay shared until you stop sharing them.",
        ),
        e,
        formEnd(() => d.close(), "Remove"),
      ),
    );
  };
  const add = () => {
    const e = errorLine();
    const email = input("email", {
      required: true,
      autocomplete: "off",
      placeholder: "name@example.com",
    });
    const end = formEnd(() => d.close(), "Send Request");
    const send = end.querySelector<HTMLButtonElement>("button[type=submit]")!;
    send.disabled = true;
    email.oninput = () => (send.disabled = !email.value.trim() || !email.checkValidity());
    const d = dialog(
      "Add Friend",
      h(
        "form",
        {
          onsubmit: (ev: Event) => {
            ev.preventDefault();
            ok(ctx.api.POST("/friend-requests", { body: { email: email.value.trim() } })).then(
              () => {
                d.close();
                toast("If they're on this server, they'll get your friend request.");
                again();
              },
              (x: Error) =>
                showError(
                  e,
                  x instanceof ApiError && x.status === 400
                    ? "Enter a valid email address."
                    : x instanceof ApiError && x.status === 429
                      ? "You've sent a lot of requests today. Try again later."
                      : x.message,
                ),
            );
          },
        },
        field("Email", email),
        h(
          "p.sub",
          "Friends can share playlists and watch together with you. They don't join your household.",
        ),
        e,
        end,
      ),
    );
    email.focus();
  };
  await draw();
  // A request answered or sent elsewhere shows on return; an open dialog is left alone.
  const back = () => !document.querySelector(".scrim") && again();
  window.addEventListener("focus", back);
  window.addEventListener("hashchange", () => window.removeEventListener("focus", back), {
    once: true,
  });
  return [card];
}
