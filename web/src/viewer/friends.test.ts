// The Friends page against a mocked API: what it shows, what each action sends,
// and that nothing it shows tells the sender who holds an email.
import { beforeEach, describe, expect, it } from "vitest";
import { setToken } from "../admin/api";
import { boot } from "./app";
import { type Handler, at, calls, json, mockFetch, none, root, until } from "./testSupport";
import { uid } from "../api/uid";

const ts = "2026-01-01T00:00:00Z";
const acct = (id: number, name: string, kind = "adult") => ({
  id: uid(id),
  kind,
  name,
  isAdmin: false,
  hasPin: false,
  pinLocked: false,
  pending: false,
  filterMode: "ratings",
  createdAt: ts,
  modifiedAt: ts,
});
const me = (active = 1) => ({
  device: { id: uid(1) },
  account: acct(1, "Helga"),
  activeProfileId: uid(active),
  household: { id: uid(1), name: "Helga", adultId: uid(1), createdAt: ts },
  profiles: [acct(1, "Helga"), acct(2, "Jason", "kid")],
});
const incoming = {
  id: uid(80),
  fromProfileId: uid(30),
  fromName: "Nora",
  fromColor: "#2E6B5B",
  createdAt: ts,
  expiresAt: "2026-01-31T00:00:00Z",
};
const sent = (id: number, email: string) => ({
  id: uid(id),
  email,
  createdAt: ts,
  expiresAt: "2026-01-31T00:00:00Z",
});
const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent === label);
const routes = (extra: Record<string, Handler | unknown> = {}) => ({
  "GET /health": { status: "ok", version: "1.0.0", setupRequired: false },
  "GET /me": me(),
  "GET /friend-requests": {
    incoming: [incoming],
    outgoing: [sent(90, "bea@example.com"), sent(91, "nobody@example.com")],
  },
  "GET /friends": { items: [acct(31, "Marcus")] },
  [`POST /friend-requests/${uid(80)}/accept`]: none,
  [`DELETE /friend-requests/${uid(80)}`]: none,
  [`DELETE /friend-requests/${uid(90)}`]: none,
  [`DELETE /friends/${uid(31)}`]: none,
  ...extra,
});
const reads = () => calls.filter((c) => c.key === "GET /friend-requests").length;

beforeEach(async () => {
  window.onhashchange = null;
  calls.length = 0;
  document.body.replaceChildren();
  localStorage.clear();
  setToken("tok");
  history.replaceState(null, "", "/");
  location.hash = "";
  await new Promise((r) => setTimeout(r, 0));
});

describe("friends", () => {
  it("is in an adult's profile menu, and a kid has neither the item nor the page", async () => {
    await at("#/");
    const adult = root();
    await boot(adult, { fetch: mockFetch(routes({ "GET /items": { items: [] } })) });
    await until(() => adult.querySelector("[role=menu][aria-label=Profile]"));
    const menu = adult.querySelector("[role=menu][aria-label=Profile]")!;
    expect(text(menu.querySelectorAll("[role=menuitem]"))).toContain("Friends");
    expect(button(menu, "Friends")!.getAttribute("href")).toBe("#/friends");

    document.body.replaceChildren();
    window.onhashchange = null;
    calls.length = 0;
    await at("#/friends");
    const kid = root();
    await boot(kid, {
      fetch: mockFetch(routes({ "GET /me": me(2), "GET /items": { items: [] } })),
    });
    await until(() => kid.querySelector("[role=menu][aria-label=Profile]"));
    expect(text(kid.querySelectorAll("[role=menuitem]"))).not.toContain("Friends");
    expect(calls.map((c) => c.key).filter((k) => k.includes("/friend"))).toEqual([]);
  });

  it("shows requests by name, friends, and sent requests collapsed and all alike", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => r.querySelector("h1")?.textContent === "Friends" && r.querySelector("li"));
    expect(text(r.querySelectorAll("h2"))).toEqual(["Requests", "Friends"]);
    const req = r.querySelector("section[aria-label=Requests] li")!;
    expect(req.querySelector("b")!.textContent).toBe("Nora");
    expect(req.querySelector(".sub")!.textContent).toBe("Wants to be friends");
    expect(req.textContent).not.toContain("@");
    expect(text(r.querySelectorAll("section[aria-label=Friends] li b"))).toEqual(["Marcus"]);
    const details = r.querySelector("details")!;
    expect(details.open).toBe(false);
    expect(details.querySelector("summary")!.textContent).toBe("Sent (2)");
    const [a, b] = details.querySelectorAll("li");
    // Nothing but the address tells two sent requests apart.
    expect(a.innerHTML.replace("bea@example.com", "x")).toBe(
      b.innerHTML.replace("nobody@example.com", "x"),
    );
    expect(a.querySelector(".sub")!.textContent).toMatch(/^Sent .+ · Expires .+$/);
  });

  it("leaves out Requests and Sent when there are none, and says how to add a friend", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "GET /friend-requests": { incoming: [], outgoing: [] },
          "GET /friends": { items: [] },
        }),
      ),
    });
    await until(() => r.querySelector("h2"));
    expect(text(r.querySelectorAll("h2"))).toEqual(["Friends"]);
    expect(r.querySelector("details")).toBeNull();
    expect(r.querySelector("section[aria-label=Friends] .sub")!.textContent).toBe(
      "No friends yet. Add someone by their email to share playlists and watch together.",
    );
  });

  it("accepts a request and reads the lists again", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => button(r, "Accept"));
    const before = reads();
    button(r, "Accept")!.click();
    await until(() => reads() > before);
    expect(calls.some((c) => c.key === `POST /friend-requests/${uid(80)}/accept`)).toBe(true);
  });

  it("says a sender can no longer be added on 409 and drops the row", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          [`POST /friend-requests/${uid(80)}/accept`]: () =>
            json({ code: "conflict", message: "gone" }, 409),
        }),
      ),
    });
    await until(() => button(r, "Accept"));
    button(r, "Accept")!.click();
    await until(() => document.querySelector(".toast"));
    expect(document.querySelector(".toast")!.textContent).toBe(
      "This person can no longer be added.",
    );
    expect(r.querySelector("section[aria-label=Requests]")).toBeNull();
  });

  it("declines at once, without asking", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => button(r, "Decline"));
    button(r, "Decline")!.click();
    expect(r.querySelector("section[aria-label=Requests] li")).toBeNull();
    expect(document.querySelector(".scrim")).toBeNull();
    await until(() => calls.some((c) => c.key === `DELETE /friend-requests/${uid(80)}`));
  });

  it("withdraws a sent request without asking", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => button(r, "Withdraw"));
    button(r, "Withdraw")!.click();
    await until(() => calls.some((c) => c.key === `DELETE /friend-requests/${uid(90)}`));
  });

  it("removes a friend after the confirmation", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => r.querySelector("[aria-label='More for Marcus']"));
    r.querySelector<HTMLElement>("[aria-label='More for Marcus']")!.click();
    button(r, "Remove Friend")!.click();
    const d = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(d.querySelector("h1")!.textContent).toBe("Remove Marcus?");
    expect(d.querySelector("p")!.textContent).toBe(
      "They won't be told. Playlists you've shared stay shared until you stop sharing them.",
    );
    expect(text(d.querySelectorAll("button"))).toEqual(["Cancel", "Remove"]);
    d.querySelector("form")!.requestSubmit();
    await until(() => calls.some((c) => c.key === `DELETE /friends/${uid(31)}`));
    await until(() => !document.querySelector(".scrim"));
  });

  it("sends a request with the one answer for every 202, and maps 400 and 429", async () => {
    let status = 202;
    await at("#/friends");
    const r = root();
    await boot(r, {
      fetch: mockFetch(
        routes({
          "POST /friend-requests": () =>
            status === 202
              ? json(sent(92, "x@example.com"), 202)
              : json({ code: "x", message: "server words" }, status),
        }),
      ),
    });
    await until(() => button(r, "Add Friend"));
    const open = () => {
      button(r, "Add Friend")!.click();
      const d = document.querySelector<HTMLElement>("[role=dialog]")!;
      const email = d.querySelector<HTMLInputElement>("input[type=email]")!;
      const send = button(d, "Send Request") as HTMLButtonElement;
      return { d, email, send };
    };
    const type = (e: HTMLInputElement, v: string) => {
      e.value = v;
      e.dispatchEvent(new Event("input"));
    };
    const { d, email, send } = open();
    expect(d.querySelector("h1")!.textContent).toBe("Add Friend");
    expect(email.placeholder).toBe("name@example.com");
    expect(d.textContent).toContain(
      "Friends can share playlists and watch together with you. They don't join your household.",
    );
    expect(send.disabled).toBe(true);
    type(email, "not an email");
    expect(send.disabled).toBe(true);
    type(email, " bea@example.com ");
    expect(send.disabled).toBe(false);

    status = 400;
    d.querySelector("form")!.requestSubmit();
    await until(() => d.querySelector(".err:not([hidden])"));
    expect(d.querySelector(".err")!.textContent).toBe("Enter a valid email address.");
    status = 429;
    d.querySelector("form")!.requestSubmit();
    await until(() => d.querySelector(".err")!.textContent!.startsWith("You've"));
    expect(d.querySelector(".err")!.textContent).toBe(
      "You've sent a lot of requests today. Try again later.",
    );
    status = 202;
    d.querySelector("form")!.requestSubmit();
    await until(() => document.querySelector(".toast"));
    expect(document.querySelector(".toast")!.textContent).toBe(
      "If they're on this server, they'll get your friend request.",
    );
    expect(document.querySelector("[role=dialog]")).toBeNull();
    const posted = calls.filter((c) => c.key === "POST /friend-requests").map((c) => c.body);
    expect(posted.at(-1)).toEqual({ email: "bea@example.com" });
  });

  it("reads the lists again when the window regains focus", async () => {
    await at("#/friends");
    const r = root();
    await boot(r, { fetch: mockFetch(routes()) });
    await until(() => r.querySelector("li"));
    const before = reads();
    window.dispatchEvent(new Event("focus"));
    await until(() => reads() > before);
    calls.length = 0;
    await at("#/");
    window.dispatchEvent(new Event("focus"));
    await new Promise((r) => setTimeout(r, 20));
    expect(reads()).toBe(0);
  });
});
