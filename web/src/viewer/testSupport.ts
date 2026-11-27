// Shared by the viewer's component tests: the real pages and generated client
// against a mocked API. Each test file gets its own copy of this module's state.
import { expect, vi } from "vitest";
import { API_BASE } from "../api/client";

export type Handler = (body: unknown, url: URL) => unknown;

/** Every request a mockFetch answered, oldest first; tests empty it as they begin. */
export const calls: { key: string; body: unknown; keepalive: boolean; query: string }[] = [];

/**
 * A fetch that answers "METHOD /path" from routes, by value or by a Handler of the request's
 * JSON body; a route it lacks is a 404.
 */
export function mockFetch(routes: Record<string, Handler | unknown>) {
  return async (req: Request) => {
    const url = new URL(req.url, "http://x");
    const key = `${req.method} ${url.pathname.replace(API_BASE, "")}`;
    const body =
      req.method === "GET" || req.method === "DELETE"
        ? undefined
        : await req.json().catch(() => undefined);
    calls.push({ key, body, keepalive: req.keepalive, query: url.search });
    const r = routes[key];
    if (r === undefined) return json({ code: "not_found", message: `no mock ${key}` }, 404);
    const out = await (typeof r === "function" ? (r as Handler)(body, url) : r);
    return out instanceof Response ? out : json(out, key.startsWith("POST") ? 201 : 200);
  };
}

export const json = (b: unknown, status = 200) =>
  new Response(JSON.stringify(b), { status, headers: { "content-type": "application/json" } });
export const none = () => new Response(null, { status: 204 });
export const until = async (f: () => unknown, timeout = 1000) => {
  await vi.waitFor(() => expect(f()).toBeTruthy(), { timeout });
};
export const at = async (hash: string) => {
  location.hash = hash;
  // jsdom fires hashchange asynchronously; let it pass before a boot listens.
  await new Promise((r) => setTimeout(r, 0));
};
export const root = () => document.body.appendChild(document.createElement("div"));
