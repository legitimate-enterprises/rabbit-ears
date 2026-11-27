import { API_BASE, type Schemas } from "../../api/client";
import type { Ctx } from "../api";
import { ApiError, ok } from "../api";
import { card, confirming, copy, formEnd, header, saveSwitch, table } from "../common";
import { dialog, errorLine, field, h, input, showError, sub, tag } from "../dom";

type Provider = Schemas["SsoProvider"];

/** The form both Add and Edit send; PUT replaces, so an unchanged provider goes back whole, without the secret. */
const inputOf = (p: Provider): Schemas["SsoProviderInput"] => ({
  name: p.name,
  issuer: p.issuer,
  clientId: p.clientId,
  requiredGroup: p.requiredGroup,
  linksByEmail: p.linksByEmail,
  enabled: p.enabled,
});

/** Add or edit a provider. The secret is only ever sent: left blank, the saved one stays. */
function editDialog(ctx: Ctx, p: Provider | undefined, reload: () => void) {
  const name = input("text", { required: true, maxLength: 100, value: p?.name ?? "" });
  const issuer = input("url", { required: true, maxLength: 2048, value: p?.issuer ?? "" });
  const clientId = input("text", { required: true, maxLength: 512, value: p?.clientId ?? "" });
  const secret = input("password", { autocomplete: "off", maxLength: 1024 });
  const group = input("text", { maxLength: 256, value: p?.requiredGroup ?? "" });
  const links = input("checkbox", { checked: p?.linksByEmail ?? false });
  const enabled = input("checkbox", { checked: p?.enabled ?? true });
  const err = errorLine();
  const { box, close } = dialog(p ? `Edit ${p.name}` : "Add provider");
  box.append(
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          issuer.removeAttribute("aria-invalid");
          const body: Schemas["SsoProviderInput"] = {
            name: name.value.trim(),
            issuer: issuer.value.trim(),
            clientId: clientId.value.trim(),
            ...(secret.value ? { clientSecret: secret.value } : {}),
            ...(group.value.trim() ? { requiredGroup: group.value.trim() } : {}),
            linksByEmail: links.checked,
            enabled: enabled.checked,
          };
          try {
            await ok(
              p
                ? ctx.api.PUT("/admin/sso-providers/{providerId}", {
                    params: { path: { providerId: p.id } },
                    body,
                  })
                : ctx.api.POST("/admin/sso-providers", { body }),
            );
            close();
            reload();
          } catch (x) {
            const fe =
              x instanceof ApiError ? x.fields.find((f) => f.field === "issuer") : undefined;
            if (fe) issuer.setAttribute("aria-invalid", "true");
            showError(err, fe?.message ?? (x as Error).message);
          }
        },
      },
      field("Name", name, "The button reads Sign in with this name."),
      field("Issuer URL", issuer, "Checked with the provider when you save."),
      field("Client ID", clientId),
      field(
        "Client secret",
        secret,
        p?.clientSecretSet
          ? "A secret is saved. Leave blank to keep it."
          : "Blank for a public client.",
      ),
      field("Required group", group, "Optional. Only members of this group may sign in."),
      h("label.row", links, "Accounts from this provider are mine to control"),
      sub(
        "A first sign-in then links to the account with the same email, so turn this on only for a provider whose accounts you manage.",
      ),
      h("label.row", enabled, "Enabled"),
      err,
      formEnd(close, p ? "Save" : "Add provider"),
    ),
  );
}

export async function sso(ctx: Ctx): Promise<HTMLElement[]> {
  const root = h("div.stack");
  await fill(ctx, root);
  return [root];
}

async function fill(ctx: Ctx, root: HTMLElement) {
  const { api } = ctx;
  const reload = () => void fill(ctx, root);
  const list = (await ok(api.GET("/admin/sso-providers", { params: { query: { limit: 200 } } })))
    .items;
  const err = errorLine();
  const callback = `${location.origin}${API_BASE}/auth/sso/callback`;
  const copyBtn = h("button.ib", { type: "button" }, "Copy");
  copyBtn.onclick = () => {
    void copy(callback);
    copyBtn.textContent = "Copied";
    setTimeout(() => (copyBtn.textContent = "Copy"), 2000);
  };
  const row = (p: Provider) => {
    const sw = saveSwitch(`Enabled: ${p.name}`, p.enabled, err, async (enabled) =>
      Object.assign(
        p,
        await ok(
          api.PUT("/admin/sso-providers/{providerId}", {
            params: { path: { providerId: p.id } },
            body: { ...inputOf(p), enabled },
          }),
        ),
      ),
    );
    return [
      h("b", p.name),
      sub(p.issuer),
      p.requiredGroup ?? sub("None"),
      p.linksByEmail ? tag("By email", "warn") : sub("Linked accounts only"),
      sw,
      h(
        "span",
        h("button.ib", { type: "button", onclick: () => editDialog(ctx, p, reload) }, "Edit"),
        confirming("Delete", `Delete ${p.name}? Its accounts keep their passwords.`, async () => {
          await ok(
            api.DELETE("/admin/sso-providers/{providerId}", {
              params: { path: { providerId: p.id } },
            }),
          );
          reload();
        }),
      ),
    ];
  };
  root.replaceChildren(
    header(
      "Single sign-on",
      `${list.length} provider${list.length === 1 ? "" : "s"}`,
      h("button.ib.p", { onclick: () => editDialog(ctx, undefined, reload) }, "Add provider"),
    ),
    h(
      "p.sub",
      "Adults sign in with an OpenID Connect provider beside their password. Single sign-on never creates an account: add the adult under Users & profiles first.",
    ),
    err,
    card(
      "Callback URL",
      sub("Register this redirect URI with each provider."),
      h(
        "div.row",
        input("text", { readOnly: true, value: callback, "aria-label": "Callback URL" }),
        copyBtn,
      ),
    ),
    card(
      "Providers",
      table(
        ["Provider", "Issuer", "Required group", "Linking", "Enabled", ""],
        list.map(row),
        "No providers yet.",
      ),
    ),
  );
}
