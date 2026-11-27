// Typed client for the server's REST API. The types are generated from
// server/api/openapi.yaml into schema.gen.ts by `npm run generate`.
import createClient from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export type { components, paths };
export type Schemas = components["schemas"];

/** A kid's ceilings, lowest first. */
export const MOVIE_RATINGS: Schemas["MovieRating"][] = ["G", "PG", "PG-13", "R", "NC-17"];
export const TV_RATINGS: Schemas["TvRating"][] = [
  "TV-Y",
  "TV-Y7",
  "TV-G",
  "TV-PG",
  "TV-14",
  "TV-MA",
];

export interface ApiOptions {
  /** Device bearer token; omit for public operations such as sign in. */
  token?: string;
  baseUrl?: string;
  fetch?: (req: Request) => Promise<Response>;
}

/** The path every REST route, stream and artwork URL lives under. */
export const API_BASE = "/api/v2";

export function createApi({ token, baseUrl = API_BASE, fetch }: ApiOptions = {}) {
  return createClient<paths>({
    baseUrl,
    fetch,
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
}

export type Api = ReturnType<typeof createApi>;
