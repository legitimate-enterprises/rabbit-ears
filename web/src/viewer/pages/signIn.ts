import type { Api, Schemas } from "../../api/client";
import { ApiError, MIN_PASSWORD, ok, setToken, tooMany } from "../../admin/api";
import { errorLine, field, h, input, showError } from "../../admin/dom";
import { ssoButtons, ssoReturn } from "../../admin/sso";
import { icon, serverName } from "../ui";

type D = { api: Api; done: () => void; brand: () => HTMLElement };
const deviceName = () =>
  `${/Mac|iPhone|iPad|Win|Linux|Android/.exec(navigator.userAgent)?.[0] ?? "Web"} browser`;
const frame = (d: D, card: HTMLElement) => h("div.solo", h("header.top", d.brand()), card);
const submitButton = (label: string) => h("button.btn.p.wide", { type: "submit" }, label);

export function signIn(root: HTMLElement, d: D & { invite: (code: string) => void }) {
  const email = input("email", { autocomplete: "username", required: true });
  const password = input("password", { autocomplete: "current-password", required: true });
  const err = errorLine();
  const form = h(
    "form.card",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        try {
          const r = await ok(
            d.api.POST("/auth/sign-in", {
              body: {
                email: email.value,
                password: password.value,
                deviceName: deviceName(),
                platform: "web",
              },
            }),
          );
          setToken(r.token);
          whichHousehold(root, d, r);
        } catch (x) {
          showError(
            err,
            x instanceof ApiError && x.status === 401
              ? "Email or password is wrong."
              : x instanceof ApiError && x.status === 429
                ? tooMany(x)
                : (x as Error).message,
          );
        }
      },
    },
    h("h1", `Sign in to ${serverName()}`),
    field("Email", email),
    field("Password", password),
    err,
    submitButton("Sign in"),
    h(
      "a",
      { href: "#", onclick: (e: Event) => (e.preventDefault(), codeStep(root, d)) },
      "Have an invite code?",
    ),
  );
  root.replaceChildren(frame(d, form));
  void ssoReturn(d.api, deviceName(), (r) => whichHousehold(root, d, r)).then(
    (m) => m && showError(err, m),
  );
  void ssoButtons(d.api, "viewer", "a.btn.s.wide").then((b) =>
    form.querySelector("button[type=submit]")!.after(...b),
  );
}

/**
 * After a sign-in of an adult in several households, asks which one this browser is in, their
 * own first and picked; only another one is a move. Then sign-in carries on as for one household.
 */
function whichHousehold(root: HTMLElement, d: D, r: Schemas["SignInResult"]) {
  const [own, ...others] = r.households ?? [];
  if (!others.length) return d.done();
  let picked = own.id;
  const err = errorLine();
  const row = (hh: Schemas["HouseholdChoice"], tag: string) =>
    h(
      "label.share",
      h("span.avatar.house", icon("house", 18)),
      h("b", hh.name, h("span.sub", tag)),
      h("input", {
        type: "radio",
        name: "household",
        checked: hh === own,
        onchange: () => (picked = hh.id),
      }),
    );
  const form = h(
    "form.card",
    {
      onsubmit: async (e: Event) => {
        e.preventDefault();
        try {
          // The api was made before the sign-in, so it carries no token of its own.
          if (picked !== own.id)
            await ok(
              d.api.PUT("/me/household", {
                body: { householdId: picked },
                headers: { Authorization: `Bearer ${r.token}` },
              }),
            );
          d.done();
        } catch (x) {
          showError(err, (x as Error).message);
        }
      },
    },
    h("h1", "Which household is this browser in?"),
    h(
      "p.sub",
      "You are in more than one. Who's watching shows the profiles of the household you pick.",
    ),
    h("div.shares", row(own, "Yours"), ...others.map((hh) => row(hh, "Visiting"))),
    err,
    submitButton("Continue"),
    h("p.sub", "Asked once, after signing in. Sign out to change it."),
  );
  root.replaceChildren(frame(d, form));
}

/** Typed text as XXXX-XXXX: uppercase, dash inserted after the fourth character. */
export const dashed = (typed: string) => {
  const v = typed
    .replace(/[^A-Za-z0-9]/g, "")
    .toUpperCase()
    .slice(0, 8);
  return v.length > 4 ? `${v.slice(0, 4)}-${v.slice(4)}` : v;
};

/** Manual entry of an XXXX-XXXX code. */
function codeStep(root: HTMLElement, d: D & { invite: (code: string) => void }) {
  const code = input("text", { autocomplete: "off", maxLength: 9, required: true });
  code.addEventListener("input", () => (code.value = dashed(code.value)));
  root.replaceChildren(
    frame(
      d,
      h(
        "form.card",
        {
          onsubmit: (e: Event) => (
            e.preventDefault(),
            code.value.length === 9 && d.invite(code.value)
          ),
        },
        h("h1", "Enter your invite code"),
        field("Invite code", code),
        submitButton("Continue"),
      ),
    ),
  );
}

export async function accept(root: HTMLElement, d: D & { code: string }) {
  let preview;
  try {
    preview = await ok(d.api.GET("/join/{code}", { params: { path: { code: d.code } } }));
  } catch (x) {
    const limited = x instanceof ApiError && x.status === 429;
    const back = h("a", { href: "/" }, "Sign in");
    return void root.replaceChildren(
      frame(
        d,
        h(
          "div.card",
          h("h1", limited ? tooMany(x) : "This invite is no longer valid."),
          limited ? null : h("p.sub", "Ask whoever invited you for a new one."),
          back,
        ),
      ),
    );
  }
  const name = input("text", { value: preview.name, required: true, maxLength: 100 });
  const pw = input("password", { autocomplete: "new-password", required: true });
  const again = input("password", { autocomplete: "new-password", required: true });
  const pwErr = h("span.err", { hidden: true });
  const err = errorLine();
  const who = preview.invitedBy ?? "Someone";
  root.replaceChildren(
    frame(
      d,
      h(
        "form.card",
        {
          onsubmit: async (e: Event) => {
            e.preventDefault();
            pwErr.hidden = true;
            if (pw.value.length < MIN_PASSWORD)
              return (
                (pwErr.textContent = `Use at least ${MIN_PASSWORD} characters.`),
                void (pwErr.hidden = false)
              );
            if (pw.value !== again.value)
              return (
                (pwErr.textContent = "The passwords do not match."),
                void (pwErr.hidden = false)
              );
            try {
              const r = await ok(
                d.api.POST("/join/{code}", {
                  params: { path: { code: d.code } },
                  body: {
                    name: name.value,
                    password: pw.value,
                    deviceName: deviceName(),
                    platform: "web",
                  },
                }),
              );
              setToken(r.token);
              whichHousehold(root, d, r);
            } catch (x) {
              showError(err, (x as Error).message);
            }
          },
        },
        h("h1", `${who} invited you to ${serverName()}`),
        h("p.sub", "Create your account to join."),
        field("Name", name),
        field("Password", pw),
        field("Confirm password", again),
        pwErr,
        err,
        submitButton("Create account"),
        h("p.sub", "This device will show your household. You can add kids after you sign in."),
      ),
    ),
  );
}
