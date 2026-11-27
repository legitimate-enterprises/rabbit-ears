// Single sign-on on the viewer's and the admin's sign-in pages: the provider
// buttons, and the result the server's callback leaves in the fragment.
import { API_BASE, type Api, type Schemas } from "../api/client";
import { ok, setToken } from "./api";
import { h } from "./dom";

// The page a sign-on began on, such as /link?code=, kept for the trip to the
// provider: the server's callback returns only to / or /admin/.
const FROM = "rabbit-ears.sso.from";

const REASONS: Record<string, string> = {
  expired: "That sign-in expired or was started in another browser. Try again.",
  provider_error: "The sign-in provider could not sign you in. Try again, or use your password.",
  not_allowed: "This account is not allowed to sign in here.",
  no_account:
    "No account here is linked to that sign-in. Ask an admin to add you, or use your password.",
};

/**
 * Takes an `#sso=` or `#sso_error=` fragment off the address. A code is
 * exchanged for this device's token, then done runs with the result as after
 * a password sign-in; otherwise it resolves to the sentence to show, if any.
 */
export async function ssoReturn(
  api: Api,
  deviceName: string,
  done: (r: Schemas["SignInResult"]) => void,
) {
  const f = new URLSearchParams(location.hash.slice(1));
  const code = f.get("sso");
  const reason = f.get("sso_error");
  if (code === null && reason === null) return undefined;
  let from: string | null = null;
  try {
    from = sessionStorage.getItem(FROM);
    sessionStorage.removeItem(FROM);
  } catch {
    /* storage blocked: the sign-on stays where the server sent it */
  }
  history.replaceState(null, "", from ?? location.pathname + location.search);
  if (code === null) return REASONS[reason!] ?? "Single sign-on failed. Try again.";
  try {
    const r = await ok(
      api.POST("/auth/sso/exchange", { body: { code, deviceName, platform: "web" } }),
    );
    setToken(r.token);
    done(r);
    return undefined;
  } catch {
    return REASONS.expired;
  }
}

/** One "Sign in with" link per enabled provider; none when the list is empty or fails. */
export async function ssoButtons(api: Api, returnTo: "viewer" | "admin", spec: string) {
  const list = await ok(api.GET("/auth/sso-providers")).catch(() => []);
  return list.map((p) =>
    h(
      spec,
      {
        href: `${API_BASE}/auth/sso/${p.id}/start?returnTo=${returnTo}`,
        onclick: () => {
          try {
            sessionStorage.setItem(FROM, location.pathname + location.search);
          } catch {
            /* storage blocked */
          }
        },
      },
      `Sign in with ${p.name}`,
    ),
  );
}
