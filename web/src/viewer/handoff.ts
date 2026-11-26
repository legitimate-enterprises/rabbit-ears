// Handoff: a title still playing on another device of the profile is asked
// about before it plays here, and a player whose session another device
// took says where it went.
import { ok } from "../admin/api";
import { dialog, h } from "../admin/dom";
import type { Api, Schemas } from "../api/client";
import { clock } from "./format";

export type Session = Schemas["PlaybackSession"];
export type Handoff = "move" | "together";

/** The profile's session on itemId on another device, if one is playing now. */
export const elsewhere = (api: Api, itemId: number): Promise<Session | undefined> =>
  ok(api.GET("/playback/sessions", { params: { query: { itemId } } })).then(
    (p) => p.items.find((s) => !s.thisDevice),
    // A server that cannot say plays here without asking.
    () => undefined,
  );

/** Asks what to do about s; undefined for Cancel. */
export const askHandoff = (s: Session) =>
  new Promise<Handoff | undefined>((done) => {
    const pick = (v?: Handoff) => (d.close(), done(v));
    const choice = (label: string, v?: Handoff) =>
      h("button.btn.s.wide", { type: "button", onclick: () => pick(v) }, label);
    const d = dialog(
      `Playing on ${s.deviceName} at ${clock(s.positionMs / 1000)}`,
      h(
        "div.choices",
        choice("Move here", "move"),
        choice("Watch together", "together"),
        choice("Cancel"),
      ),
    );
    d.box.parentElement!.addEventListener("keydown", (e) => e.key === "Escape" && done(undefined));
  });

/** Ends s where it plays; the answer holds the position to start here at. */
export const moveHere = (api: Api, s: Session) =>
  ok(api.POST("/playback/sessions/{sessionId}/move", { params: { path: { sessionId: s.id } } }));

export function toast(text: string) {
  const t = h("div.toast", { role: "status" }, text);
  document.body.append(t);
  setTimeout(() => t.remove(), 4000);
}

/** The device that took the session with Move here; undefined when none did. */
export const movedTo = (api: Api, sessionId: number) =>
  ok(api.GET("/playback/sessions/{sessionId}", { params: { path: { sessionId } } })).then(
    (s) => s.movedToDeviceName,
    () => undefined,
  );
