import { ApiError, ok } from "../../admin/api";
import { dialog, errorLine, field, h, input, showError } from "../../admin/dom";
import type { Schemas } from "../../api/client";
import type { Ctx } from "../data";

/** "Mia", "Mia and Leo", "Mia, Leo and Ada". */
const joined = (names: string[]) =>
  names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`;

/** What the confirmation says deleting the account takes with it. */
export function deletionText(d: Schemas["AccountDeletion"]): string {
  if (d.outcome === "refused")
    return "You are the server's only admin, so your account stays until you make another account an admin in the admin console.";
  const what =
    "This deletes your account for good, with your watch history, your playlists and the stations that play them, and your stations, and signs out every device signed in as you. It cannot be undone.";
  if (d.outcome === "transfer")
    return `${what} The household stays, with its kids, and ${d.newOwnerName} becomes its owner.`;
  if (d.outcome === "household")
    return `${what} You are the household's only adult, so the household goes too${
      d.kidNames.length ? `, with ${joined(d.kidNames)} and everything of theirs` : ""
    }.`;
  return `${what} The household stays as it is.`;
}

/** The trigger for Delete account, with the adult's own account settings. */
export const deleteAccountButton = (ctx: Ctx) =>
  h(
    "button.btn.s.danger",
    { type: "button", onclick: () => void confirmDeletion(ctx) },
    "Delete account…",
  );

async function confirmDeletion(ctx: Ctx) {
  const plan = await ok(ctx.api.GET("/me/deletion"));
  const text = h("p.sub", deletionText(plan));
  if (plan.outcome === "refused") {
    const d = dialog(
      "Delete your account?",
      text,
      h("div.end", h("button.btn.p", { type: "button", onclick: () => d.close() }, "OK")),
    );
    return;
  }
  const pw = input("password", { required: true, autocomplete: "current-password" });
  const err = errorLine();
  const d = dialog(
    "Delete your account?",
    h(
      "form",
      {
        onsubmit: async (e: Event) => {
          e.preventDefault();
          try {
            await ok(ctx.api.POST("/me/deletion", { body: { password: pw.value } }));
            d.close();
            ctx.signOut();
          } catch (x) {
            showError(err, deletionError(x as Error));
          }
        },
      },
      text,
      field("Password", pw),
      err,
      h(
        "div.end",
        h("button.btn.s", { type: "button", onclick: () => d.close() }, "Cancel"),
        h("button.btn.p.danger", { type: "submit" }, "Delete account"),
      ),
    ),
  );
}

function deletionError(x: Error) {
  if (!(x instanceof ApiError)) return x.message;
  if (x.status === 403) return "The password is wrong.";
  if (x.status === 429) return "Too many tries. Wait a few minutes and try again.";
  return x.message;
}
