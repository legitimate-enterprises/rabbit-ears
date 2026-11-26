import type { Api } from "../../api/client";
import { ApiError, ok, setToken, tooMany } from "../api";
import { errorLine, field, h, input, showError } from "../dom";
import { ssoButtons, ssoReturn } from "../sso";

export function signIn(
  root: HTMLElement,
  d: { api: Api; done: () => void; brand: () => HTMLElement },
) {
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
                deviceName: "Admin web",
                platform: "web",
              },
            }),
          );
          setToken(r.token);
          d.done();
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
    h("h1", "Sign in"),
    field("Email", email),
    field("Password", password),
    err,
    h("button.ib.p", { type: "submit" }, "Sign in"),
  );
  root.replaceChildren(h("div.solo", d.brand(), form));
  void ssoReturn(d.api, "Admin web", d.done).then((m) => m && showError(err, m));
  void ssoButtons(d.api, "admin", "a.ib").then((b) => form.append(...b));
}
