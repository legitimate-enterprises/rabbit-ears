// Glue between the generated client and the pages: unwrap results into
// values or ApiError, and keep the device token.
import {
  API_BASE,
  createApi as create,
  type Api,
  type ApiOptions,
  type Schemas,
} from "../api/client";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly fields: { field: string; message: string }[] = [],
    readonly code?: string,
    /** Seconds from a Retry-After header. */
    readonly retryAfter?: number,
    /** With off_air, when the station's next program starts. */
    readonly resumesAt?: string,
  ) {
    super(message);
  }
}

/** The sign-in message for a 429, rounded up to whole minutes. */
export const tooMany = (x: ApiError) => {
  const n = Math.max(1, Math.ceil((x.retryAfter ?? 60) / 60));
  return `Too many attempts. Try again in ${n} minute${n === 1 ? "" : "s"}.`;
};

export async function ok<T>(
  p: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await p;
  if (response.ok) return data as T;
  const e = error as Partial<Schemas["Error"]> | undefined;
  const fields = e?.fields ?? [];
  const detail = fields.map((f) => `${f.field}: ${f.message}`).join("; ");
  throw new ApiError(
    detail || e?.message || response.statusText || "Request failed",
    response.status,
    fields,
    e?.code,
    Number(response.headers.get("Retry-After")) || undefined,
    e?.resumesAt,
  );
}

export async function paged<T>(
  get: (offset: number) => Promise<{ items: T[]; nextOffset?: number }>,
): Promise<T[]> {
  const out: T[] = [];
  for (let offset: number | undefined = 0; offset !== undefined;) {
    const p = await get(offset);
    out.push(...p.items);
    offset = p.nextOffset;
  }
  return out;
}

/** The shortest password the server accepts. */
export const MIN_PASSWORD = 12;

const KEY = "rabbit-ears.admin.token";
export const getToken = () => {
  try {
    return localStorage.getItem(KEY) ?? undefined;
  } catch {
    return undefined;
  }
};
export const setToken = (t?: string) => {
  try {
    if (t) localStorage.setItem(KEY, t);
    else localStorage.removeItem(KEY);
  } catch {
    /* storage blocked: the session lasts until reload */
  }
};

export type Ctx = {
  api: Api;
  me: Schemas["Me"];
  health: Schemas["Health"];
  /** The route's segments after its slug: ["new"] for #/occasions/new. */
  path: string[];
  /** Runs fn now and every ms until the page is left. */
  every(fn: () => void, ms: number): void;
  /** Called by the shell when a request fails with 401. */
  signedOut(): void;
  /** Reads the Needs attention count in the nav again. */
  recount(): void;
  /** Shows the server's new name in the sidebar. */
  renamed(name: string): void;
};

// Absolute so the same code runs under jsdom, where relative URLs do not parse.
export const createApi = (o: ApiOptions = {}): Api =>
  create({ baseUrl: `${location.origin}${API_BASE}`, ...o });
