import type { Api, Schemas } from "../../api/client";
import { ApiError, ok } from "../../admin/api";
import { errorLine, field, h, input, showError } from "../../admin/dom";
import { ago } from "../format";
import { avatar, icon } from "../ui";
import { dashed } from "./signIn";

const CODE = /^[0-9A-Z]{4}-[0-9A-Z]{4}$/;

interface D {
  api: Api;
  profile: Schemas["Account"];
  brand: () => HTMLElement;
  /** The code from ?code=, looked up on arrival. */
  code?: string;
  /** The device token stopped working. */
  signedOut: () => void;
}

/** An adult enters the code a TV shows, sees which device asked, and approves or denies it. */
export function link(root: HTMLElement, d: D) {
  const card = (title: string, ...kids: (Node | null)[]) =>
    root.replaceChildren(
      h(
        "div.solo",
        h("header.top", d.brand(), h("span.grow"), avatar(d.profile, 32), d.profile.name),
        h("div.card", h("h1", title), ...kids),
      ),
    );
  const needsAdult = () =>
    card(
      "Link a TV",
      h(
        "p.sub",
        "Only the adult who signed in on this device can link a TV. Switch to their profile first.",
      ),
      h("a.btn.s", { href: "/#/who" }, "Switch profile"),
    );
  if (d.profile.kind !== "adult") return needsAdult();

  // The buttons that held focus are gone, so focus moves to the outcome.
  const finish = (title: string, text: string) => {
    card(title, h("p.sub", text), h("a.btn.p", { href: "/" }, "Done"));
    const h1 = root.querySelector("h1")!;
    h1.tabIndex = -1;
    h1.focus();
  };
  const problem = (x: unknown) => {
    if (!(x instanceof ApiError)) return (x as Error).message;
    if (x.status === 404)
      return "That code is unknown, has expired or was already used. Check the code on the TV or ask it for a new one.";
    if (x.status === 429) return "Too many tries. Wait a few minutes and try again.";
    return x.message;
  };
  // A 403 means a kid or another adult's profile is active, and a 401 is a dead device token.
  const failed = (x: unknown) => {
    if (x instanceof ApiError && x.status === 401) return d.signedOut();
    if (x instanceof ApiError && x.status === 403) return needsAdult();
    showError(err, problem(x));
  };

  const code = input("text", {
    autocomplete: "off",
    maxLength: 9,
    value: dashed(d.code ?? ""),
    "aria-label": "Code",
    autocapitalize: "characters",
  });
  code.classList.add("code");
  const err = errorLine();
  const found = h("div.found", { "aria-live": "polite" });
  const look = async (c: string) => {
    showError(err);
    found.replaceChildren();
    try {
      const p = await ok(d.api.GET("/link/{code}", { params: { path: { code: c } } }));
      // A slower answer for an earlier code must not offer to approve that code.
      if (code.value !== c) return;
      const decide = async (yes: boolean) => {
        const buttons = found.querySelectorAll("button");
        buttons.forEach((b) => (b.disabled = true));
        const path = { params: { path: { code: c } } };
        try {
          if (yes) {
            await ok(d.api.POST("/link/{code}/approve", path));
            finish("TV linked", `${p.deviceName} will sign in to ${d.profile.name}'s household.`);
          } else {
            await ok(d.api.POST("/link/{code}/deny", path));
            finish("Not linked", `${p.deviceName} was not signed in.`);
          }
        } catch (x) {
          buttons.forEach((b) => (b.disabled = false));
          failed(x);
        }
      };
      found.replaceChildren(
        h(
          "div.device",
          icon("tv", 28),
          h("div", h("b", p.deviceName), h("span.sub", `Asked to sign in ${ago(p.createdAt)}`)),
        ),
        h(
          "div.pair",
          h("button.btn.p", { type: "button", onclick: () => decide(true) }, "Link this TV"),
          h("button.btn.s", { type: "button", onclick: () => decide(false) }, "Cancel"),
        ),
        h("p.sub", `The TV will show ${d.profile.name}'s household. PINs still apply.`),
      );
    } catch (x) {
      if (code.value === c) failed(x);
    }
  };
  code.addEventListener("input", () => {
    code.value = dashed(code.value);
    found.replaceChildren();
    showError(err);
    if (CODE.test(code.value)) void look(code.value);
  });

  card("Link a TV", h("p.sub", "Enter the code shown on the TV."), field("Code", code), err, found);
  if (CODE.test(code.value)) void look(code.value);
}
