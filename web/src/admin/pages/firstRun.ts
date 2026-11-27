// Three steps: admin account, server name, first library. The API takes the
// admin and server name in one call, so step 2 is where the request is sent.
import type { Api } from "../../api/client";
import { createApi, ok, setToken, ApiError, MIN_PASSWORD } from "../api";
import { errorLine, field, h, input, showError } from "../dom";

type Deps = {
  api: Api;
  fetch?: (r: Request) => Promise<Response>;
  done: () => void;
  brand: () => HTMLElement;
};

export function firstRun(root: HTMLElement, d: Deps) {
  const admin = { name: "", email: "", password: "" };
  let token = "";
  const stepper = (n: number) =>
    h(
      "ol.steps",
      { "aria-label": "Setup steps" },
      ...["Create admin", "Name server", "Add a library"].map((t, i) =>
        h("li", { "aria-current": i + 1 === n ? "step" : undefined }, h("i", String(i + 1)), t),
      ),
    );
  const show = (n: number, card: HTMLElement) =>
    root.replaceChildren(h("div.solo", d.brand(), stepper(n), card));

  const step1 = () => {
    const name = input("text", { required: true, autocomplete: "name" });
    const email = input("email", { required: true, autocomplete: "email" });
    const pw = input("password", { required: true, autocomplete: "new-password" });
    const pw2 = input("password", { required: true, autocomplete: "new-password" });
    const hint = h("span.sub.hint");
    const go = h(
      "button.ib.p",
      { type: "submit", disabled: true },
      "Create admin account",
    ) as HTMLButtonElement;
    const check = () => {
      hint.textContent =
        pw.value && pw.value.length < MIN_PASSWORD
          ? `Use at least ${MIN_PASSWORD} characters.`
          : pw2.value && pw.value !== pw2.value
            ? "Passwords do not match."
            : "";
      go.disabled = !(
        name.value.trim() &&
        email.validity.valid &&
        email.value &&
        pw.value.length >= MIN_PASSWORD &&
        pw.value === pw2.value
      );
    };
    const form = h(
      "form.card",
      {
        onsubmit: (e: Event) => {
          e.preventDefault();
          Object.assign(admin, { name: name.value.trim(), email: email.value, password: pw.value });
          step2();
        },
      },
      h("h1", "Create the admin account"),
      h(
        "p.sub",
        "This is the only admin. It manages libraries and the server, and invites the adults who will use it.",
      ),
      field("Name", name),
      field("Email", email),
      field("Password", pw),
      field("Confirm password", pw2),
      hint,
      go,
    );
    form.addEventListener("input", check);
    show(1, form);
  };

  const step2 = () => {
    const server = input("text", { value: "Rabbit Ears", required: true });
    const err = errorLine();
    const form = h(
      "form.card",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            const r = await ok(
              d.api.POST("/setup", {
                body: {
                  ...admin,
                  serverName: server.value.trim(),
                  deviceName: "Admin web",
                  platform: "web",
                },
              }),
            );
            token = r.token;
            setToken(token);
            step3();
          } catch (x) {
            showError(
              err,
              x instanceof ApiError && x.status === 409
                ? "An admin already exists. Reload to sign in."
                : (x as Error).message,
            );
          }
        },
      },
      h("h1", "Name your server"),
      h("p.sub", "Shown beside the logo in every app, and where people sign in."),
      field("Server name", server),
      err,
      h("button.ib.p", { type: "submit" }, "Continue"),
    );
    show(2, form);
  };

  const step3 = () => {
    const api = createApi({ token, fetch: d.fetch });
    const kind = h(
      "select.in",
      h("option", { value: "movies" }, "Movies"),
      h("option", { value: "tv" }, "TV"),
    ) as HTMLSelectElement;
    const name = input("text", { required: true, value: "Movies" });
    const path = input("text", { required: true, placeholder: "/Volumes/Media/Movies" });
    const err = errorLine();
    // Made before its folder was refused; the next try adds only the folder.
    let libraryId: string | undefined;
    kind.addEventListener("change", () => (name.value = kind.value === "tv" ? "TV" : "Movies"));
    const form = h(
      "form.card",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            libraryId ??= (
              await ok(
                api.POST("/libraries", {
                  body: {
                    name: name.value.trim(),
                    kind: kind.value as "movies" | "tv",
                    scanHour: 3,
                    watchFiles: true,
                  },
                }),
              )
            ).id;
            await ok(
              api.POST("/libraries/{libraryId}/folders", {
                params: { path: { libraryId } },
                body: { path: path.value.trim() },
              }),
            );
            d.done();
          } catch (x) {
            showError(err, (x as Error).message);
          }
        },
      },
      h("h1", "Add a library"),
      h("p.sub", "A folder of movies or TV shows. Scanning starts right away."),
      field("Kind", kind),
      field("Name", name),
      field("Folder path", path),
      err,
      h(
        "div.row",
        h("button.ib.p", { type: "submit" }, "Add library"),
        h("button.ib", { type: "button", onclick: () => d.done() }, "Skip for now"),
      ),
    );
    show(3, form);
  };
  step1();
}
